#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import hashlib
import http.cookiejar
import json
import os
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import IO


DEFAULT_TIMEOUT_SECONDS = 30.0
SCHEMA_TIMEOUT_SECONDS = 15.0
IDLE_CLEANUP_WAIT_SECONDS = 45.0
SUPPORTED_SCENARIOS = ("happy_path", "idle_cleanup", "hot_reload")
MANAGEMENT_SECRET = "frp-udp-e2e-management-secret"


@dataclass(frozen=True)
class Ports:
    control: int
    management: int
    remote_udp: int
    local_udp: int
    reloaded_remote_udp: int
    reloaded_local_udp: int


@dataclass(frozen=True)
class TokenMaterial:
    token_id: str
    token_secret: str
    token_hash: str
    frpc_token: str


@dataclass(frozen=True)
class RuntimePaths:
    output_dir: Path
    workspace_dir: Path
    data_dir: Path
    webui_dir: Path
    webui_dist_dir: Path
    frps_bin_path: Path
    frpc_bin_path: Path
    config_path: Path
    db_path: Path
    frps_log_path: Path
    frpc_log_path: Path
    result_json_path: Path


@dataclass
class ManagedProcess:
    name: str
    command: list[str]
    cwd: Path
    log_path: Path
    log_handle: IO[str]
    popen: subprocess.Popen[str]


@dataclass(frozen=True)
class HTTPResult:
    status: int
    body: bytes
    headers: dict[str, str]


@dataclass
class UDPEchoServerHandle:
    sock: socket.socket
    thread: threading.Thread
    stop_event: threading.Event
    lock: threading.Lock = field(default_factory=threading.Lock)
    received_payloads: list[bytes] = field(default_factory=list)
    received_addrs: list[tuple[str, int]] = field(default_factory=list)
    error: str | None = None

    def snapshot(self) -> tuple[list[bytes], list[tuple[str, int]], str | None]:
        with self.lock:
            return (
                list(self.received_payloads),
                list(self.received_addrs),
                self.error,
            )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Run a minimal UDP single-port e2e test against local frps/frpc with a "
            "Python public client and a Python local UDP server."
        ),
    )
    parser.add_argument(
        "--frps-bin",
        help="Existing frps binary path. If omitted, build one into the output workspace.",
    )
    parser.add_argument(
        "--frpc-bin",
        help="Existing frpc binary path. If omitted, build one into the output workspace.",
    )
    parser.add_argument(
        "--webui-dist",
        help="Existing built webui dist directory. If omitted, reuse frps/webui/dist or build it.",
    )
    parser.add_argument(
        "--output-dir",
        help="Directory used for the isolated workspace, logs, sqlite database, and result.json.",
    )
    parser.add_argument(
        "--payload",
        default="frp-udp-e2e-payload",
        help="UDP datagram payload sent by the public client and expected back from the local UDP server.",
    )
    parser.add_argument(
        "--scenario",
        default="happy_path",
        choices=SUPPORTED_SCENARIOS,
        help="UDP e2e scenario to run. Default: happy_path.",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_SECONDS,
        help=f"Timeout in seconds for startup checks and the UDP round-trip. Default: {DEFAULT_TIMEOUT_SECONDS}.",
    )
    parser.add_argument(
        "--frps-log-level",
        default="info",
        choices=("debug", "info", "warn", "error"),
        help="frps log level written into the generated config file.",
    )
    parser.add_argument(
        "--frpc-log-level",
        default="info",
        choices=("debug", "info", "warn", "error"),
        help="FRPC_LOG_LEVEL passed to the frpc process.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    script_path = Path(__file__).resolve()
    repo_root = script_path.parent.parent
    if not (repo_root / "frps").is_dir() or not (repo_root / "frpc").is_dir():
        print(f"[FAIL] could not locate frps/frpc from script path: {script_path}", file=sys.stderr)
        return 1

    validate_args(args)
    output_dir = resolve_output_dir(args, repo_root)
    paths = build_runtime_paths(output_dir)

    stage = "prepare output workspace"
    processes: list[ManagedProcess] = []
    echo_servers: list[UDPEchoServerHandle] = []
    initial_echo_server: UDPEchoServerHandle | None = None
    ports: Ports | None = None

    try:
        print(f"[stage] {stage}")
        prepare_output_dirs(paths)

        stage = "build or resolve binaries"
        print(f"[stage] {stage}")
        build_or_copy_frps_binary(args, repo_root, paths)
        build_or_copy_frpc_binary(args, repo_root, paths)

        stage = "build or resolve webui dist"
        print(f"[stage] {stage}")
        copy_webui_dist(resolve_webui_dist(args, repo_root), paths.webui_dist_dir)

        stage = "allocate ports and token"
        print(f"[stage] {stage}")
        ports = allocate_ports()
        token = build_token_material()

        stage = "write fixed data/config.json"
        print(f"[stage] {stage}")
        write_config_file(paths.config_path, ports, args.frps_log_level)

        stage = "start frps from isolated workspace"
        print(f"[stage] {stage}")
        frps_process = start_process(
            name="frps",
            command=[str(paths.frps_bin_path)],
            cwd=paths.workspace_dir,
            log_path=paths.frps_log_path,
        )
        processes.append(frps_process)

        base_url = f"http://127.0.0.1:{ports.management}"

        stage = "wait for frps management api and sqlite schema"
        print(f"[stage] {stage}")
        wait_http_ready(f"{base_url}/readyz", args.timeout, processes)
        wait_sqlite_schema(paths.db_path, min(args.timeout, SCHEMA_TIMEOUT_SECONDS))
        wait_log_contains(paths.frps_log_path, "frpc control listener ready", args.timeout, processes)

        stage = "seed udp runtime data"
        print(f"[stage] {stage}")
        seed_runtime_data(paths.db_path, token, ports)

        stage = "start python local udp echo server"
        print(f"[stage] {stage}")
        initial_echo_server = start_udp_echo_server("127.0.0.1", ports.local_udp)
        echo_servers.append(initial_echo_server)

        stage = "start frpc"
        print(f"[stage] {stage}")
        frpc_process = start_process(
            name="frpc",
            command=[
                str(paths.frpc_bin_path),
                "--server",
                f"127.0.0.1:{ports.control}",
                "--token",
                token.frpc_token,
            ],
            cwd=paths.output_dir,
            log_path=paths.frpc_log_path,
            env={"FRPC_LOG_LEVEL": args.frpc_log_level},
        )
        processes.append(frpc_process)

        stage = "wait for udp tunnel readiness"
        print(f"[stage] {stage}")
        wait_log_contains(paths.frps_log_path, "frpc control login succeeded", args.timeout, processes)
        wait_log_contains(paths.frps_log_path, "config acknowledged", args.timeout, processes)
        wait_log_contains(paths.frps_log_path, "udp tunnel listener ready", args.timeout, processes)
        wait_log_contains(paths.frpc_log_path, "login succeeded", args.timeout, processes)
        wait_log_contains(paths.frpc_log_path, "config applied", args.timeout, processes)

        stage = f"run udp scenario {args.scenario}"
        print(f"[stage] {stage}")
        payload = args.payload.encode("utf-8")
        if initial_echo_server is None:
            raise RuntimeError("initial udp echo server was not started")

        if args.scenario == "happy_path":
            scenario_summary = run_happy_path_scenario(paths, ports, initial_echo_server, payload, args.timeout, processes)
        elif args.scenario == "idle_cleanup":
            scenario_summary = run_idle_cleanup_scenario(paths, ports, initial_echo_server, payload, args.timeout, processes)
        elif args.scenario == "hot_reload":
            stage = "start reload udp echo server"
            print(f"[stage] {stage}")
            reload_echo_server = start_udp_echo_server("127.0.0.1", ports.reloaded_local_udp)
            echo_servers.append(reload_echo_server)

            stage = f"run udp scenario {args.scenario}"
            print(f"[stage] {stage}")
            scenario_summary = run_hot_reload_scenario(
                paths,
                ports,
                initial_echo_server,
                reload_echo_server,
                payload,
                args.timeout,
                processes,
            )
        else:
            raise RuntimeError(f"unsupported scenario: {args.scenario}")

        received_payloads, received_addrs, server_error = initial_echo_server.snapshot()
        if server_error is not None:
            raise RuntimeError(f"local udp server failed: {server_error}")

        summary = {
            "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "output_dir": str(paths.output_dir),
            "workspace_dir": str(paths.workspace_dir),
            "management_url": base_url,
            "control_addr": f"127.0.0.1:{ports.control}",
            "remote_udp_addr": f"127.0.0.1:{ports.remote_udp}",
            "local_udp_addr": f"127.0.0.1:{ports.local_udp}",
            "scenario": args.scenario,
            "payload_utf8": args.payload,
            "frps_log_path": str(paths.frps_log_path),
            "frpc_log_path": str(paths.frpc_log_path),
            "db_path": str(paths.db_path),
            "local_server_received_count": len(received_payloads),
            "local_server_last_addr": None if not received_addrs else f"{received_addrs[-1][0]}:{received_addrs[-1][1]}",
            "verified_steps": [
                "frps direct startup from workspace/data/config.json",
                "sqlite seed for a single enabled udp tunnel",
                "python local udp echo server startup",
                "frpc login and config apply",
                "frps udp tunnel listener startup",
            ]
            + scenario_summary.pop("verified_steps"),
        }
        summary.update(scenario_summary)
        paths.result_json_path.write_text(json.dumps(summary, indent=2), encoding="utf-8")

        print(f"[ok] udp single-port {args.scenario} succeeded")
        print(f"[info] output_dir={paths.output_dir}")
        print(f"[info] result_json={paths.result_json_path}")
        print(f"[info] frps_log={paths.frps_log_path}")
        print(f"[info] frpc_log={paths.frpc_log_path}")
        print(f"[info] {format_ports(ports)}")
        return 0
    except Exception as exc:
        print(f"[FAIL] stage={stage}: {exc}", file=sys.stderr)
        print(f"[FAIL] output_dir={paths.output_dir}", file=sys.stderr)
        if ports is not None:
            print(f"[FAIL] {format_ports(ports)}", file=sys.stderr)
        dump_process_logs(processes)
        return 1
    finally:
        for echo_server in reversed(echo_servers):
            stop_udp_echo_server(echo_server)
        for process in reversed(processes):
            terminate_process(process)


def validate_args(args: argparse.Namespace) -> None:
    if args.timeout <= 0:
        raise ValueError("--timeout must be positive")


def resolve_output_dir(args: argparse.Namespace, repo_root: Path) -> Path:
    if args.output_dir:
        return Path(args.output_dir).resolve()
    timestamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    return (repo_root / "test" / "tmp" / f"udp-e2e-{timestamp}").resolve()


def build_runtime_paths(output_dir: Path) -> RuntimePaths:
    workspace_dir = output_dir / "workspace"
    data_dir = workspace_dir / "data"
    webui_dir = workspace_dir / "webui"
    return RuntimePaths(
        output_dir=output_dir,
        workspace_dir=workspace_dir,
        data_dir=data_dir,
        webui_dir=webui_dir,
        webui_dist_dir=webui_dir / "dist",
        frps_bin_path=workspace_dir / executable_name("frps"),
        frpc_bin_path=output_dir / executable_name("frpc"),
        config_path=data_dir / "config.json",
        db_path=data_dir / "frps.db",
        frps_log_path=output_dir / "frps.log",
        frpc_log_path=output_dir / "frpc.log",
        result_json_path=output_dir / "result.json",
    )


def executable_name(base: str) -> str:
    return f"{base}.exe" if os.name == "nt" else base


def prepare_output_dirs(paths: RuntimePaths) -> None:
    if paths.output_dir.exists():
        shutil.rmtree(paths.output_dir)
    paths.data_dir.mkdir(parents=True, exist_ok=True)
    paths.webui_dir.mkdir(parents=True, exist_ok=True)


def build_or_copy_frps_binary(args: argparse.Namespace, repo_root: Path, paths: RuntimePaths) -> None:
    if args.frps_bin:
        source = Path(args.frps_bin).resolve()
        if not source.is_file():
            raise RuntimeError(f"frps binary does not exist: {source}")
        shutil.copy2(source, paths.frps_bin_path)
        return

    ensure_go_available()
    build_go_binary(repo_root / "frps", "./cmd/frps", paths.frps_bin_path)


def build_or_copy_frpc_binary(args: argparse.Namespace, repo_root: Path, paths: RuntimePaths) -> None:
    if args.frpc_bin:
        source = Path(args.frpc_bin).resolve()
        if not source.is_file():
            raise RuntimeError(f"frpc binary does not exist: {source}")
        shutil.copy2(source, paths.frpc_bin_path)
        return

    ensure_go_available()
    build_go_binary(repo_root / "frpc", "./cmd/frpc", paths.frpc_bin_path)


def ensure_go_available() -> None:
    if shutil.which("go") is None:
        raise RuntimeError("go executable not found in PATH; pass --frps-bin and --frpc-bin to skip building")


def build_go_binary(module_dir: Path, package: str, output_path: Path) -> None:
    result = subprocess.run(
        ["go", "build", "-o", str(output_path), package],
        cwd=str(module_dir),
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(
            f"go build failed for {package} in {module_dir}\n"
            f"stdout:\n{result.stdout}\n"
            f"stderr:\n{result.stderr}"
        )


def resolve_webui_dist(args: argparse.Namespace, repo_root: Path) -> Path:
    if args.webui_dist:
        source = Path(args.webui_dist).resolve()
        if not source.is_dir():
            raise RuntimeError(f"webui dist directory does not exist: {source}")
        index_path = source / "index.html"
        if not index_path.is_file():
            raise RuntimeError(f"webui dist directory is missing index.html: {index_path}")
        return source

    dist_dir = (repo_root / "frps" / "webui" / "dist").resolve()
    if dist_dir.is_dir() and (dist_dir / "index.html").is_file():
        return dist_dir

    npm_executable = resolve_npm_executable()
    if npm_executable is None:
        raise RuntimeError("npm executable not found in PATH; pass --webui-dist to skip building")

    result = subprocess.run(
        [npm_executable, "run", "build"],
        cwd=str(repo_root / "frps" / "webui"),
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(
            "npm run build failed for frps/webui\n"
            f"stdout:\n{result.stdout}\n"
            f"stderr:\n{result.stderr}"
        )

    if not dist_dir.is_dir() or not (dist_dir / "index.html").is_file():
        raise RuntimeError(f"webui dist directory was not produced: {dist_dir}")
    return dist_dir


def resolve_npm_executable() -> str | None:
    for candidate in ("npm.cmd", "npm"):
        resolved = shutil.which(candidate)
        if resolved:
            return resolved
    return None


def copy_webui_dist(source_dist: Path, target_dist: Path) -> None:
    if target_dist.exists():
        shutil.rmtree(target_dist)
    shutil.copytree(source_dist, target_dist)


def allocate_ports() -> Ports:
    tcp_ports = reserve_port_numbers(socket.SOCK_STREAM, 2)
    udp_ports = reserve_port_numbers(socket.SOCK_DGRAM, 4)
    return Ports(
        control=tcp_ports[0],
        management=tcp_ports[1],
        remote_udp=udp_ports[0],
        local_udp=udp_ports[1],
        reloaded_remote_udp=udp_ports[2],
        reloaded_local_udp=udp_ports[3],
    )


def reserve_port_numbers(sock_type: int, count: int) -> list[int]:
    reserved: list[socket.socket] = []
    ports: list[int] = []
    try:
        for _ in range(count):
            probe = socket.socket(socket.AF_INET, sock_type)
            probe.bind(("127.0.0.1", 0))
            if sock_type == socket.SOCK_STREAM:
                probe.listen(1)
            reserved.append(probe)
            ports.append(probe.getsockname()[1])
    finally:
        for probe in reserved:
            probe.close()
    return ports


def build_token_material() -> TokenMaterial:
    token_id_bytes = os.urandom(16)
    token_secret_bytes = os.urandom(32)
    token_id = token_id_bytes.hex()
    token_secret = token_secret_bytes.hex()
    token_hash = hashlib.sha256(token_secret_bytes).hexdigest()
    return TokenMaterial(
        token_id=token_id,
        token_secret=token_secret,
        token_hash=token_hash,
        frpc_token=token_id + token_secret,
    )


def write_config_file(config_path: Path, ports: Ports, log_level: str) -> None:
    config = {
        "control_listen_addr": f"127.0.0.1:{ports.control}",
        "management_listen_addr": f"127.0.0.1:{ports.management}",
        "read_header_timeout": "5s",
        "shutdown_timeout": "10s",
        "database": {
            "type": "sqlite",
            "path": "./frps.db",
        },
        "webui": {
            "dist_dir": "../webui/dist",
        },
        "log": {
            "level": log_level,
            "format": "text",
        },
    }
    config_path.write_text(json.dumps(config, indent=2), encoding="utf-8")


def wait_sqlite_schema(db_path: Path, timeout_seconds: float) -> None:
    deadline = time.time() + timeout_seconds
    required_tables = {"proxy_groups", "tunnels"}

    while time.time() < deadline:
        try:
            with sqlite3.connect(db_path, timeout=1.0) as conn:
                rows = conn.execute(
                    "SELECT name FROM sqlite_master WHERE type = 'table'",
                ).fetchall()
        except sqlite3.Error:
            time.sleep(0.25)
            continue

        existing = {str(row[0]) for row in rows}
        if required_tables.issubset(existing):
            return
        time.sleep(0.25)

    raise TimeoutError(f"sqlite schema was not bootstrapped in time: {db_path}")


def seed_runtime_data(db_path: Path, token: TokenMaterial, ports: Ports) -> None:
    created_at = timestamp_now()
    with sqlite3.connect(db_path, timeout=5.0) as conn:
        conn.execute("PRAGMA busy_timeout = 5000")
        cursor = conn.execute(
            """
            INSERT INTO proxy_groups (
                name,
                token_id,
                token_hash,
                effective_ip,
                enabled,
                rate_limit,
                created_at,
                updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                "e2e-group",
                token.token_id,
                token.token_hash,
                "0.0.0.0",
                1,
                0,
                created_at,
                created_at,
            ),
        )
        group_id = cursor.lastrowid
        if group_id is None:
            raise RuntimeError("failed to insert proxy_groups row")

        conn.execute(
            """
            INSERT INTO tunnels (
                group_id,
                name,
                protocol,
                remote_type,
                remote_start,
                remote_end,
                local_host,
                local_start,
                local_end,
                enabled,
                created_at,
                updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                int(group_id),
                "e2e-udp",
                "udp",
                "single",
                ports.remote_udp,
                ports.remote_udp,
                "127.0.0.1",
                ports.local_udp,
                ports.local_udp,
                1,
                created_at,
                created_at,
            ),
        )
        conn.commit()


def start_udp_echo_server(host: str, port: int) -> UDPEchoServerHandle:
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind((host, port))
    sock.settimeout(0.25)

    handle = UDPEchoServerHandle(
        sock=sock,
        thread=threading.Thread(),
        stop_event=threading.Event(),
    )

    def serve() -> None:
        while not handle.stop_event.is_set():
            try:
                payload, addr = sock.recvfrom(65535)
            except socket.timeout:
                continue
            except OSError as exc:
                if handle.stop_event.is_set():
                    return
                with handle.lock:
                    handle.error = str(exc)
                return

            with handle.lock:
                handle.received_payloads.append(payload)
                handle.received_addrs.append((addr[0], addr[1]))

            try:
                sock.sendto(payload, addr)
            except OSError as exc:
                with handle.lock:
                    handle.error = str(exc)
                return

    handle.thread = threading.Thread(target=serve, name="udp-echo-server", daemon=True)
    handle.thread.start()
    return handle


def stop_udp_echo_server(handle: UDPEchoServerHandle) -> None:
    handle.stop_event.set()
    try:
        handle.sock.close()
    except OSError:
        pass
    handle.thread.join(timeout=5.0)


def start_process(
    name: str,
    command: list[str],
    cwd: Path,
    log_path: Path,
    env: dict[str, str] | None = None,
) -> ManagedProcess:
    log_path.parent.mkdir(parents=True, exist_ok=True)
    log_handle = log_path.open("w", encoding="utf-8", buffering=1)
    merged_env = os.environ.copy()
    if env:
        merged_env.update(env)

    creationflags = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0) if os.name == "nt" else 0
    try:
        popen = subprocess.Popen(
            command,
            cwd=str(cwd),
            stdout=log_handle,
            stderr=subprocess.STDOUT,
            text=True,
            env=merged_env,
            creationflags=creationflags,
        )
    except Exception:
        log_handle.close()
        raise

    return ManagedProcess(
        name=name,
        command=command,
        cwd=cwd,
        log_path=log_path,
        log_handle=log_handle,
        popen=popen,
    )


def wait_http_ready(url: str, timeout_seconds: float, processes: list[ManagedProcess]) -> None:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        ensure_processes_alive(processes)
        try:
            with urllib.request.urlopen(url, timeout=1.0) as response:
                if response.status == 200:
                    return
        except (OSError, urllib.error.URLError):
            time.sleep(0.25)
            continue
        time.sleep(0.25)

    raise TimeoutError(f"http readiness check timed out: {url}")


def wait_log_contains(
    log_path: Path,
    needle: str,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        ensure_processes_alive(processes)
        try:
            content = log_path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            time.sleep(0.25)
            continue

        if needle in content:
            return
        time.sleep(0.25)

    raise TimeoutError(f"log readiness check timed out: {needle!r} in {log_path}")


def wait_for_udp_payload(handle: UDPEchoServerHandle, payload: bytes, timeout_seconds: float) -> None:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        received_payloads, _, error_text = handle.snapshot()
        if error_text is not None:
            raise RuntimeError(f"local udp server failed: {error_text}")
        if payload in received_payloads:
            return
        time.sleep(0.05)

    raise TimeoutError(f"local udp server did not observe payload in time: {payload!r}")


def ensure_processes_alive(processes: list[ManagedProcess]) -> None:
    for process in processes:
        return_code = process.popen.poll()
        if return_code is not None:
            raise RuntimeError(f"{process.name} exited unexpectedly with code {return_code}")


def run_happy_path_scenario(
    paths: RuntimePaths,
    ports: Ports,
    echo_server: UDPEchoServerHandle,
    payload: bytes,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> dict[str, object]:
    response = run_public_udp_client("127.0.0.1", ports.remote_udp, payload, timeout_seconds)
    if response != payload:
        raise RuntimeError(f"udp echo mismatch: sent {payload!r}, received {response!r}")

    wait_for_udp_payload(echo_server, payload, min(timeout_seconds, 10.0))
    wait_log_count_at_least(paths.frps_log_path, "udp session opened", 1, timeout_seconds, processes)
    wait_log_count_at_least(paths.frpc_log_path, "udp session opened", 1, timeout_seconds, processes)

    return {
        "verified_steps": [
            "python public udp client single-datagram round-trip",
        ],
        "response_utf8": response.decode("utf-8", errors="replace"),
    }


def run_idle_cleanup_scenario(
    paths: RuntimePaths,
    ports: Ports,
    echo_server: UDPEchoServerHandle,
    payload: bytes,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> dict[str, object]:
    first_payload = payload + b"-first"
    second_payload = payload + b"-second"
    idle_wait_timeout = max(timeout_seconds, IDLE_CLEANUP_WAIT_SECONDS)

    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        sock.settimeout(timeout_seconds)
        sock.connect(("127.0.0.1", ports.remote_udp))

        first_response = run_connected_udp_exchange(sock, first_payload)
        if first_response != first_payload:
            raise RuntimeError(f"first udp echo mismatch: sent {first_payload!r}, received {first_response!r}")

        wait_for_udp_payload(echo_server, first_payload, min(timeout_seconds, 10.0))
        wait_log_count_at_least(paths.frps_log_path, "udp session opened", 1, timeout_seconds, processes)
        wait_log_count_at_least(paths.frpc_log_path, "udp session opened", 1, timeout_seconds, processes)

        wait_log_count_at_least(
            paths.frps_log_path,
            "udp session closed for idle timeout",
            1,
            idle_wait_timeout,
            processes,
        )
        wait_log_count_at_least(
            paths.frpc_log_path,
            "udp session closed",
            1,
            idle_wait_timeout,
            processes,
        )
        wait_log_contains(paths.frpc_log_path, "udp session idle timeout", idle_wait_timeout, processes)

        second_response = run_connected_udp_exchange(sock, second_payload)
        if second_response != second_payload:
            raise RuntimeError(f"second udp echo mismatch: sent {second_payload!r}, received {second_response!r}")

        wait_for_udp_payload(echo_server, second_payload, min(timeout_seconds, 10.0))
        wait_log_count_at_least(paths.frps_log_path, "udp session opened", 2, timeout_seconds, processes)
        wait_log_count_at_least(paths.frpc_log_path, "udp session opened", 2, timeout_seconds, processes)

    received_payloads, received_addrs, server_error = echo_server.snapshot()
    if server_error is not None:
        raise RuntimeError(f"local udp server failed: {server_error}")

    unique_addrs = unique_udp_addrs(received_addrs)

    return {
        "verified_steps": [
            "python public udp client round-trip before idle timeout",
            "frps idle cleanup after about 30 seconds of inactivity",
            "frpc udp.close receipt and local udp session release signal in logs",
            "same public udp client round-trip after idle cleanup-triggered reopen",
        ],
        "idle_cleanup_observed": True,
        "first_response_utf8": first_response.decode("utf-8", errors="replace"),
        "second_response_utf8": second_response.decode("utf-8", errors="replace"),
        "local_server_unique_source_addrs": [f"{host}:{port}" for host, port in unique_addrs],
        "local_server_unique_source_addr_count": len(unique_addrs),
        "local_server_received_payload_count": len(received_payloads),
    }


def run_hot_reload_scenario(
    paths: RuntimePaths,
    ports: Ports,
    echo_server: UDPEchoServerHandle,
    reload_echo_server: UDPEchoServerHandle,
    payload: bytes,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> dict[str, object]:
    wait_log_count_at_least(paths.frps_log_path, "config acknowledged", 1, timeout_seconds, processes)
    wait_log_count_at_least(paths.frpc_log_path, "config applied", 1, timeout_seconds, processes)
    wait_log_count_at_least(paths.frps_log_path, "udp tunnel listener ready", 1, timeout_seconds, processes)

    initial_ack_count = count_log_occurrences(paths.frps_log_path, "config acknowledged")
    initial_apply_count = count_log_occurrences(paths.frpc_log_path, "config applied")
    initial_listener_count = count_log_occurrences(paths.frps_log_path, "udp tunnel listener ready")

    first_payload = payload + b"-before"
    stale_payload = payload + b"-stale"
    second_payload = payload + b"-after"

    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as old_sock:
        old_sock.settimeout(timeout_seconds)
        old_sock.connect(("127.0.0.1", ports.remote_udp))

        first_response = run_connected_udp_exchange(old_sock, first_payload)
        if first_response != first_payload:
            raise RuntimeError(f"hot_reload first udp echo mismatch: sent {first_payload!r}, received {first_response!r}")

        wait_for_udp_payload(echo_server, first_payload, min(timeout_seconds, 10.0))
        received_before_stale = len(echo_server.snapshot()[0])

        base_url = f"http://127.0.0.1:{ports.management}"
        opener = login_management_session(base_url, MANAGEMENT_SECRET)
        tunnel_id, group_id = load_single_tunnel_record(paths.db_path, "e2e-udp")
        patch_single_tunnel_via_management(
            opener,
            base_url,
            tunnel_id,
            group_id,
            "e2e-udp",
            "udp",
            ports.reloaded_remote_udp,
            ports.reloaded_local_udp,
        )
        assert_single_tunnel_mapping(
            paths.db_path,
            "e2e-udp",
            ports.reloaded_remote_udp,
            ports.reloaded_local_udp,
        )

        wait_log_count_at_least(paths.frps_log_path, "config acknowledged", initial_ack_count + 1, timeout_seconds, processes)
        wait_log_count_at_least(paths.frpc_log_path, "config applied", initial_apply_count + 1, timeout_seconds, processes)
        wait_log_count_at_least(
            paths.frps_log_path,
            "udp tunnel listener ready",
            initial_listener_count + 1,
            timeout_seconds,
            processes,
        )
        wait_log_contains(paths.frpc_log_path, "replaced_tunnels=1", timeout_seconds, processes)
        wait_log_contains(paths.frpc_log_path, "udp session closed", timeout_seconds, processes)
        wait_log_contains(paths.frpc_log_path, "reload in progress", timeout_seconds, processes)

        expect_no_connected_udp_response(old_sock, stale_payload, min(timeout_seconds, 2.0))
        assert_udp_received_count_stable(echo_server, received_before_stale, 0.5)

    second_response = run_public_udp_client("127.0.0.1", ports.reloaded_remote_udp, second_payload, timeout_seconds)
    if second_response != second_payload:
        raise RuntimeError(f"hot_reload second udp echo mismatch: sent {second_payload!r}, received {second_response!r}")

    wait_for_udp_payload(reload_echo_server, second_payload, min(timeout_seconds, 10.0))

    old_received_payloads, _, old_server_error = echo_server.snapshot()
    if old_server_error is not None:
        raise RuntimeError(f"old local udp server failed during hot_reload: {old_server_error}")
    if second_payload in old_received_payloads:
        raise RuntimeError("hot_reload unexpectedly delivered the second payload to the old udp target")

    reload_received_payloads, _, reload_server_error = reload_echo_server.snapshot()
    if reload_server_error is not None:
        raise RuntimeError(f"reloaded local udp server failed during hot_reload: {reload_server_error}")

    return {
        "verified_steps": [
            "python public udp client round-trip before reload",
            "management tunnel patch triggered a second config.push/config.ack cycle",
            "frpc closed the previous udp session during reload",
            "old udp public endpoint stopped forwarding after reload",
            "new udp public endpoint forwarded to the reloaded local target",
        ],
        "first_response_utf8": first_response.decode("utf-8", errors="replace"),
        "second_response_utf8": second_response.decode("utf-8", errors="replace"),
        "old_local_server_received_payload_count": len(old_received_payloads),
        "reloaded_local_server_received_payload_count": len(reload_received_payloads),
    }


def run_public_udp_client(host: str, port: int, payload: bytes, timeout_seconds: float) -> bytes:
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
        sock.settimeout(timeout_seconds)
        sock.connect((host, port))
        return run_connected_udp_exchange(sock, payload)


def run_connected_udp_exchange(sock: socket.socket, payload: bytes) -> bytes:
    sent = sock.send(payload)
    if sent != len(payload):
        raise RuntimeError(f"short udp send: {sent}/{len(payload)}")
    return sock.recv(65535)


def expect_no_connected_udp_response(sock: socket.socket, payload: bytes, timeout_seconds: float) -> None:
    sock.settimeout(timeout_seconds)
    sent = sock.send(payload)
    if sent != len(payload):
        raise RuntimeError(f"short udp send while expecting no response: {sent}/{len(payload)}")

    try:
        response = sock.recv(65535)
    except (socket.timeout, OSError):
        return

    raise RuntimeError(f"udp hot_reload unexpectedly received stale response: {response!r}")


def wait_log_count_at_least(
    log_path: Path,
    needle: str,
    minimum_count: int,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        ensure_processes_alive(processes)
        try:
            content = log_path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            time.sleep(0.25)
            continue

        if content.count(needle) >= minimum_count:
            return
        time.sleep(0.25)

    raise TimeoutError(
        f"log count check timed out: {needle!r} expected at least {minimum_count} in {log_path}",
    )


def count_log_occurrences(log_path: Path, needle: str) -> int:
    try:
        return log_path.read_text(encoding="utf-8", errors="replace").count(needle)
    except OSError:
        return 0


def assert_udp_received_count_stable(
    handle: UDPEchoServerHandle,
    expected_count: int,
    stable_window_seconds: float,
) -> None:
    deadline = time.time() + stable_window_seconds
    while time.time() < deadline:
        received_payloads, _, error_text = handle.snapshot()
        if error_text is not None:
            raise RuntimeError(f"local udp server failed: {error_text}")
        if len(received_payloads) != expected_count:
            raise RuntimeError(
                f"old udp target unexpectedly received more payloads after reload: got {len(received_payloads)} want {expected_count}",
            )
        time.sleep(0.05)


def login_management_session(base_url: str, secret: str) -> urllib.request.OpenerDirector:
    cookie_jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookie_jar))
    key_hash = sha256_hex(secret)

    init_result = request_json(
        opener,
        "POST",
        f"{base_url}/api/v1/auth/init",
        payload={"key_hash": key_hash},
        expected_status=201,
    )
    if init_result.get("initialized") is not True:
        raise RuntimeError(f"management auth init returned unexpected payload: {init_result!r}")

    challenge = request_json(opener, "POST", f"{base_url}/api/v1/auth/challenge", expected_status=200)
    challenge_id = str(challenge.get("challenge_id") or "").strip()
    salt = str(challenge.get("salt") or "").strip()
    if not challenge_id or not salt:
        raise RuntimeError(f"invalid management auth challenge payload: {challenge!r}")

    login_result = request_json(
        opener,
        "POST",
        f"{base_url}/api/v1/auth/login",
        payload={
            "challenge_id": challenge_id,
            "proof": build_management_proof(key_hash, salt),
        },
        expected_status=200,
    )
    if login_result.get("authenticated") is not True:
        raise RuntimeError(f"management auth login returned unexpected payload: {login_result!r}")
    if not list(cookie_jar):
        raise RuntimeError("management auth login did not produce a session cookie")

    return opener


def patch_single_tunnel_via_management(
    opener: urllib.request.OpenerDirector,
    base_url: str,
    tunnel_id: int,
    group_id: int,
    tunnel_name: str,
    protocol_name: str,
    remote_port: int,
    local_port: int,
) -> None:
    request_json(
        opener,
        "PATCH",
        f"{base_url}/api/v1/tunnels/{tunnel_id}",
        payload={
            "group_id": group_id,
            "name": tunnel_name,
            "protocol": protocol_name,
            "remote_type": "single",
            "remote_start": remote_port,
            "remote_end": remote_port,
            "local_host": "127.0.0.1",
            "local_start": local_port,
            "local_end": local_port,
            "enabled": True,
        },
        expected_status=200,
    )


def load_single_tunnel_record(db_path: Path, tunnel_name: str) -> tuple[int, int]:
    with sqlite3.connect(db_path, timeout=5.0) as conn:
        row = conn.execute(
            "SELECT id, group_id FROM tunnels WHERE name = ? ORDER BY id DESC LIMIT 1",
            (tunnel_name,),
        ).fetchone()
    if row is None:
        raise RuntimeError(f"tunnel row not found for {tunnel_name!r}")
    return (int(row[0]), int(row[1]))


def assert_single_tunnel_mapping(db_path: Path, tunnel_name: str, expected_remote: int, expected_local: int) -> None:
    with sqlite3.connect(db_path, timeout=5.0) as conn:
        row = conn.execute(
            """
            SELECT remote_start, remote_end, local_start, local_end
            FROM tunnels
            WHERE name = ?
            ORDER BY id DESC
            LIMIT 1
            """,
            (tunnel_name,),
        ).fetchone()
    if row is None:
        raise RuntimeError(f"tunnel row not found for {tunnel_name!r}")

    actual = tuple(int(value) for value in row)
    expected = (expected_remote, expected_remote, expected_local, expected_local)
    if actual != expected:
        raise RuntimeError(f"{tunnel_name} mapping mismatch: got {actual!r} want {expected!r}")


def request_json(
    opener: urllib.request.OpenerDirector,
    method: str,
    url: str,
    payload: dict[str, object] | None = None,
    expected_status: int = 200,
) -> dict[str, object]:
    result = perform_request(opener, method, url, payload, expected_status)
    if not result.body:
        return {}
    decoded = json.loads(result.body.decode("utf-8"))
    if not isinstance(decoded, dict):
        raise RuntimeError(f"expected JSON object from {method} {url}, got {type(decoded).__name__}")
    return decoded


def perform_request(
    opener: urllib.request.OpenerDirector,
    method: str,
    url: str,
    payload: dict[str, object] | None,
    expected_status: int,
) -> HTTPResult:
    headers: dict[str, str] = {}
    data: bytes | None = None
    if payload is not None:
        data = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"

    request = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        response = opener.open(request, timeout=5.0)
        with response:
            body = response.read()
            status = response.status
            response_headers = {key.lower(): value for key, value in response.headers.items()}
    except urllib.error.HTTPError as exc:
        with exc:
            body = exc.read()
            status = exc.code
            response_headers = {key.lower(): value for key, value in exc.headers.items()}

    if status != expected_status:
        raise RuntimeError(
            f"unexpected status for {method} {url}: got {status} want {expected_status} body={body.decode('utf-8', errors='replace')}"
        )

    return HTTPResult(status=status, body=body, headers=response_headers)


def sha256_hex(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def build_management_proof(key_hash: str, salt: str) -> str:
    return hashlib.sha256((key_hash + salt).encode("utf-8")).hexdigest()


def unique_udp_addrs(addrs: list[tuple[str, int]]) -> list[tuple[str, int]]:
    unique: list[tuple[str, int]] = []
    seen: set[tuple[str, int]] = set()
    for addr in addrs:
        if addr in seen:
            continue
        seen.add(addr)
        unique.append(addr)
    return unique


def terminate_process(process: ManagedProcess) -> None:
    try:
        if process.popen.poll() is None and os.name == "nt":
            try:
                process.popen.send_signal(signal.CTRL_BREAK_EVENT)
                process.popen.wait(timeout=3.0)
            except (OSError, ValueError, subprocess.TimeoutExpired):
                pass

        if process.popen.poll() is None:
            process.popen.terminate()
            process.popen.wait(timeout=5.0)
    except subprocess.TimeoutExpired:
        process.popen.kill()
        try:
            process.popen.wait(timeout=5.0)
        except subprocess.TimeoutExpired:
            pass
    finally:
        process.log_handle.close()


def dump_process_logs(processes: list[ManagedProcess]) -> None:
    for process in processes:
        print(f"===== {process.name} log: {process.log_path} =====", file=sys.stderr)
        try:
            content = process.log_path.read_text(encoding="utf-8", errors="replace")
        except OSError as exc:
            print(f"<unable to read log: {exc}>", file=sys.stderr)
            continue

        if content:
            print(content, file=sys.stderr, end="" if content.endswith("\n") else "\n")
        else:
            print("<empty>", file=sys.stderr)


def format_ports(ports: Ports) -> str:
    return (
        f"control_port={ports.control} "
        f"management_port={ports.management} "
        f"remote_udp_port={ports.remote_udp} "
        f"local_udp_port={ports.local_udp} "
        f"reloaded_remote_udp_port={ports.reloaded_remote_udp} "
        f"reloaded_local_udp_port={ports.reloaded_local_udp}"
    )


def timestamp_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f")


if __name__ == "__main__":
    sys.exit(main())
