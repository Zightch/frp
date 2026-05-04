#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import hashlib
import http.cookiejar
import json
import os
import queue
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from socketserver import BaseRequestHandler, ThreadingMixIn, TCPServer
from typing import IO


DEFAULT_TIMEOUT_SECONDS = 30.0
SCHEMA_TIMEOUT_SECONDS = 15.0
SUPPORTED_SCENARIOS = (
    "happy_path",
    "bad_token",
    "disabled_group",
    "disabled_tunnel",
    "local_unavailable",
    "hot_reload",
    "rate_limit_independent",
    "rate_limit_shared",
    "rate_limit_reload",
)
BAD_TOKEN_ERROR_CODE = 1101
BAD_TOKEN_ERROR_TEXT = "challenge response mismatch"
DISABLED_GROUP_ERROR_CODE = 1103
DISABLED_GROUP_ERROR_TEXT = "proxy group is disabled"
NEGATIVE_STABILITY_WINDOW_SECONDS = 2.0
MANAGEMENT_SECRET = "frp-tcp-e2e-management-secret"
TCP_RATE_LIMIT_PAYLOAD_BYTES = 512 * 1024
TCP_RATE_LIMIT_FAST_MEGABITS = 8
TCP_RATE_LIMIT_SLOW_MEGABITS = 1
TCP_RATE_LIMIT_INDEPENDENT_MIN_SECONDS = 2.0
TCP_RATE_LIMIT_INDEPENDENT_MAX_SECONDS = 5.5
TCP_RATE_LIMIT_SHARED_MIN_SECONDS = 5.0
TCP_RATE_LIMIT_SHARED_MAX_SECONDS = 9.0
TCP_RATE_LIMIT_RELOAD_FAST_MAX_SECONDS = 1.5
TCP_RATE_LIMIT_RELOAD_SLOW_MIN_SECONDS = 2.2


@dataclass(frozen=True)
class Ports:
    control: int
    management: int
    remote: int
    echo: int
    reloaded_remote: int
    reloaded_echo: int


@dataclass(frozen=True)
class TokenMaterial:
    token_id: str
    token_secret: str
    token_hash: str
    frpc_token: str


@dataclass(frozen=True)
class RuntimePaths:
    temp_root: Path
    workspace_dir: Path
    data_dir: Path
    webui_dir: Path
    webui_dist_dir: Path
    db_path: Path
    frps_config_path: Path
    frps_log_path: Path
    frpc_log_path: Path


@dataclass
class ManagedProcess:
    name: str
    command: list[str]
    cwd: Path
    log_path: Path
    log_handle: IO[str]
    popen: subprocess.Popen[str]


@dataclass
class EchoServerHandle:
    server: "ThreadedEchoServer"
    thread: threading.Thread


@dataclass(frozen=True)
class ExternalClientAttempt:
    send_error: str | None
    response: bytes
    recv_error: str | None
    connection_closed: bool
    timed_out: bool


@dataclass(frozen=True)
class TimedRoundTrip:
    response: bytes
    elapsed_seconds: float


@dataclass(frozen=True)
class HTTPResult:
    status: int
    body: bytes
    headers: dict[str, str]


@dataclass
class RunObservations:
    external_attempt: ExternalClientAttempt | None = None


class ThreadedEchoServer(ThreadingMixIn, TCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, server_address: tuple[str, int], handler_class: type[BaseRequestHandler]) -> None:
        super().__init__(server_address, handler_class)
        self._payload_lock = threading.Lock()
        self._received_payloads: list[bytes] = []

    def record_payload(self, payload: bytes) -> None:
        with self._payload_lock:
            self._received_payloads.append(payload)

    def snapshot(self) -> list[bytes]:
        with self._payload_lock:
            return list(self._received_payloads)


class EchoRequestHandler(BaseRequestHandler):
    def handle(self) -> None:
        while True:
            try:
                payload = self.request.recv(65535)
            except OSError:
                return
            if not payload:
                return
            server = self.server
            if isinstance(server, ThreadedEchoServer):
                server.record_payload(payload)
            try:
                self.request.sendall(payload)
            except OSError:
                return


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run a minimal TCP single-port e2e test against local frps/frpc.",
    )
    parser.add_argument(
        "--frps-bin",
        help="Existing frps binary path. If omitted, the script builds one into a temp directory.",
    )
    parser.add_argument(
        "--frpc-bin",
        help="Existing frpc binary path. If omitted, the script builds one into a temp directory.",
    )
    parser.add_argument(
        "--webui-dist",
        help="Existing built webui dist directory. If omitted, reuse frps/webui/dist or build it.",
    )
    parser.add_argument(
        "--payload",
        default="frp-e2e-payload",
        help="Payload sent by the external client and expected back from the echo server.",
    )
    parser.add_argument(
        "--scenario",
        default="happy_path",
        choices=SUPPORTED_SCENARIOS,
        help="Test scenario to run. Default: happy_path.",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_SECONDS,
        help=f"Timeout in seconds for readiness checks and the TCP round-trip. Default: {DEFAULT_TIMEOUT_SECONDS}.",
    )
    parser.add_argument(
        "--keep-temp",
        action="store_true",
        help="Keep the temporary working directory for inspection after the run.",
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

    timeout = max(args.timeout, 1.0)
    stage = "prepare workspace"
    processes: list[ManagedProcess] = []
    echo_handles: list[EchoServerHandle] = []
    initial_echo_handle: EchoServerHandle | None = None
    ports: Ports | None = None
    observations = RunObservations()

    temp_root = Path(tempfile.mkdtemp(prefix="frp-e2e-"))
    workspace_dir = temp_root / "workspace"
    data_dir = workspace_dir / "data"
    webui_dir = workspace_dir / "webui"
    paths = RuntimePaths(
        temp_root=temp_root,
        workspace_dir=workspace_dir,
        data_dir=data_dir,
        webui_dir=webui_dir,
        webui_dist_dir=webui_dir / "dist",
        db_path=data_dir / "frps.db",
        frps_config_path=data_dir / "config.json",
        frps_log_path=temp_root / "frps.log",
        frpc_log_path=temp_root / "frpc.log",
    )

    try:
        print(f"[info] scenario={args.scenario}")
        print(f"[stage] {stage}")
        ensure_go_available(args)
        prepare_workspace(paths)

        stage = "build or resolve binaries"
        print(f"[stage] {stage}")
        binaries = build_or_resolve_binaries(args, repo_root, temp_root)

        stage = "build or resolve webui dist"
        print(f"[stage] {stage}")
        copy_webui_dist(resolve_webui_dist(args, repo_root), paths.webui_dist_dir)

        stage = "allocate ports and token"
        print(f"[stage] {stage}")
        ports = allocate_ports()
        token = build_token_material()

        stage = "create sqlite file and frps config"
        print(f"[stage] {stage}")
        init_sqlite_db(paths.db_path)
        write_frps_config(paths.frps_config_path, paths.db_path, ports, args.frps_log_level)

        stage = "start frps"
        print(f"[stage] {stage}")
        frps_process = start_process(
            name="frps",
            command=[str(binaries["frps"])],
            cwd=paths.workspace_dir,
            log_path=paths.frps_log_path,
        )
        processes.append(frps_process)

        stage = "wait for frps management api"
        print(f"[stage] {stage}")
        wait_http_ready(
            url=f"http://127.0.0.1:{ports.management}/readyz",
            timeout_seconds=timeout,
            processes=processes,
        )

        stage = "wait for frps control listener and schema bootstrap"
        print(f"[stage] {stage}")
        wait_log_contains(paths.frps_log_path, "frpc control listener ready", timeout, processes)
        wait_sqlite_schema(paths.db_path, SCHEMA_TIMEOUT_SECONDS)

        stage = "seed runtime data"
        print(f"[stage] {stage}")
        seed_runtime_data(paths.db_path, token, ports, args.scenario)

        if args.scenario in (
            "happy_path",
            "hot_reload",
            "rate_limit_independent",
            "rate_limit_shared",
            "rate_limit_reload",
        ):
            stage = "start echo server"
            print(f"[stage] {stage}")
            initial_echo_handle = run_echo_server("127.0.0.1", ports.echo)
            echo_handles.append(initial_echo_handle)

        stage = "start frpc"
        print(f"[stage] {stage}")
        frpc_process = start_process(
            name=f"frpc[{args.scenario}]",
            command=[
                str(binaries["frpc"]),
                "--server",
                f"127.0.0.1:{ports.control}",
                "--key",
                token_value_for_scenario(args.scenario, token),
            ],
            cwd=repo_root / "frpc",
            log_path=paths.frpc_log_path,
            env={"FRPC_LOG_LEVEL": args.frpc_log_level},
        )
        processes.append(frpc_process)

        if args.scenario in (
            "happy_path",
            "hot_reload",
            "rate_limit_independent",
            "rate_limit_shared",
            "rate_limit_reload",
        ):
            stage = "wait for remote tcp listener"
            print(f"[stage] {stage}")
            wait_log_contains(paths.frps_log_path, "tcp tunnel listener ready", timeout, processes)

        if args.scenario == "happy_path":
            stage = "run external client echo round-trip"
            print(f"[stage] {stage}")
            payload = args.payload.encode("utf-8")
            response = run_external_client("127.0.0.1", ports.remote, payload, timeout)
            if response != payload:
                raise RuntimeError(
                    f"echo mismatch: sent {payload!r}, received {response!r}",
                )

            print("[ok] tcp single-port happy_path succeeded")
        elif args.scenario == "bad_token":
            stage = "validate bad token rejection"
            print(f"[stage] {stage}")
            validate_bad_token_scenario(paths, ports, timeout, processes)
            print("[ok] tcp single-port bad_token rejected invalid token as expected")
        elif args.scenario == "disabled_group":
            stage = "validate disabled group rejection"
            print(f"[stage] {stage}")
            validate_disabled_group_scenario(paths, ports, timeout, processes)
            print("[ok] tcp single-port disabled_group rejected disabled proxy group as expected")
        elif args.scenario == "disabled_tunnel":
            stage = "validate disabled tunnel has no remote listener"
            print(f"[stage] {stage}")
            validate_disabled_tunnel_scenario(paths, ports, timeout, processes)
            print("[ok] tcp single-port disabled_tunnel kept remote listener closed as expected")
        elif args.scenario == "local_unavailable":
            stage = "validate local target dial failure"
            print(f"[stage] {stage}")
            validate_local_unavailable_scenario(
                paths,
                ports,
                args.payload.encode("utf-8"),
                timeout,
                processes,
                observations,
            )
            print("[ok] tcp single-port local_unavailable rejected missing local target as expected")
        elif args.scenario == "hot_reload":
            if initial_echo_handle is None:
                raise RuntimeError("hot_reload requires the initial tcp echo server")

            stage = "start reload echo server"
            print(f"[stage] {stage}")
            reload_echo_handle = run_echo_server("127.0.0.1", ports.reloaded_echo)
            echo_handles.append(reload_echo_handle)

            stage = "validate online hot reload"
            print(f"[stage] {stage}")
            validate_hot_reload_scenario(
                paths,
                ports,
                initial_echo_handle,
                reload_echo_handle,
                args.payload.encode("utf-8"),
                timeout,
                processes,
            )
            print("[ok] tcp single-port hot_reload replaced old runtime and applied new target")
        elif args.scenario == "rate_limit_independent":
            if initial_echo_handle is None:
                raise RuntimeError("rate_limit_independent requires the initial tcp echo server")

            stage = "start second echo server for independent rate limit"
            print(f"[stage] {stage}")
            secondary_echo_handle = run_echo_server("127.0.0.1", ports.reloaded_echo)
            echo_handles.append(secondary_echo_handle)

            stage = "validate independent tcp rate limit policy"
            print(f"[stage] {stage}")
            validate_rate_limit_independent_scenario(
                paths,
                ports,
                args.payload.encode("utf-8"),
                timeout,
                processes,
            )
            print("[ok] tcp single-port rate_limit_independent applied one policy to multiple tunnels without shared competition")
        elif args.scenario == "rate_limit_shared":
            if initial_echo_handle is None:
                raise RuntimeError("rate_limit_shared requires the initial tcp echo server")

            stage = "start second echo server for shared rate limit"
            print(f"[stage] {stage}")
            secondary_echo_handle = run_echo_server("127.0.0.1", ports.reloaded_echo)
            echo_handles.append(secondary_echo_handle)

            stage = "validate shared tcp rate limit policy"
            print(f"[stage] {stage}")
            validate_rate_limit_shared_scenario(
                paths,
                ports,
                args.payload.encode("utf-8"),
                timeout,
                processes,
            )
            print("[ok] tcp single-port rate_limit_shared enforced one shared bucket across multiple tunnels")
        elif args.scenario == "rate_limit_reload":
            if initial_echo_handle is None:
                raise RuntimeError("rate_limit_reload requires the initial tcp echo server")

            stage = "validate tcp rate limit reload"
            print(f"[stage] {stage}")
            validate_rate_limit_reload_scenario(
                paths,
                ports,
                args.payload.encode("utf-8"),
                timeout,
                processes,
            )
            print("[ok] tcp single-port rate_limit_reload applied the updated rate policy online")
        else:
            raise RuntimeError(f"unsupported scenario: {args.scenario}")

        print(f"[info] temp_root={paths.temp_root}")
        print(f"[info] db_path={paths.db_path}")
        print(f"[info] {format_ports(ports)}")
        return 0
    except Exception as exc:
        print(f"[FAIL] stage={stage}: {exc}", file=sys.stderr)
        print(f"[FAIL] scenario={args.scenario}", file=sys.stderr)
        print(f"[FAIL] expected={scenario_expectation_summary(args.scenario)}", file=sys.stderr)
        print(
            f"[FAIL] actual={scenario_actual_summary(args.scenario, paths, processes, observations)}",
            file=sys.stderr,
        )
        print(f"[FAIL] temp_root={paths.temp_root}", file=sys.stderr)
        print(f"[FAIL] db_path={paths.db_path}", file=sys.stderr)
        if ports is not None:
            print(f"[FAIL] {format_ports(ports)}", file=sys.stderr)
        dump_process_logs(processes)
        return 1
    finally:
        cleanup(processes, echo_handles, paths.temp_root, args.keep_temp)


def ensure_go_available(args: argparse.Namespace) -> None:
    if args.frps_bin and args.frpc_bin:
        return

    if shutil.which("go") is None:
        raise RuntimeError("go executable not found in PATH; pass --frps-bin and --frpc-bin to skip building")


def build_or_resolve_binaries(args: argparse.Namespace, repo_root: Path, temp_root: Path) -> dict[str, Path]:
    suffix = ".exe" if os.name == "nt" else ""
    binaries: dict[str, Path] = {}

    if args.frps_bin:
        binaries["frps"] = Path(args.frps_bin).resolve()
    else:
        output_path = temp_root / f"frps{suffix}"
        build_go_binary(repo_root / "frps", "./cmd/frps", output_path)
        binaries["frps"] = output_path

    if args.frpc_bin:
        binaries["frpc"] = Path(args.frpc_bin).resolve()
    else:
        output_path = temp_root / f"frpc{suffix}"
        build_go_binary(repo_root / "frpc", "./cmd/frpc", output_path)
        binaries["frpc"] = output_path

    for name, path in binaries.items():
        if not path.exists():
            raise RuntimeError(f"{name} binary does not exist: {path}")

    return binaries


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
            f"go build failed for {package} in {module_dir}\nstdout:\n{result.stdout}\nstderr:\n{result.stderr}",
        )


def prepare_workspace(paths: RuntimePaths) -> None:
    paths.data_dir.mkdir(parents=True, exist_ok=True)
    paths.webui_dir.mkdir(parents=True, exist_ok=True)


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
    reserved: list[socket.socket] = []
    numbers: list[int] = []
    try:
        for _ in range(6):
            probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            probe.bind(("127.0.0.1", 0))
            probe.listen(1)
            reserved.append(probe)
            numbers.append(probe.getsockname()[1])
    finally:
        for probe in reserved:
            probe.close()

    return Ports(
        control=numbers[0],
        management=numbers[1],
        remote=numbers[2],
        echo=numbers[3],
        reloaded_remote=numbers[4],
        reloaded_echo=numbers[5],
    )


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


def token_value_for_scenario(scenario: str, token: TokenMaterial) -> str:
    if scenario == "bad_token":
        return token.token_id + mutate_hex_secret(token.token_secret)
    if scenario in SUPPORTED_SCENARIOS:
        return token.frpc_token
    raise ValueError(f"unsupported scenario: {scenario}")


def mutate_hex_secret(secret: str) -> str:
    if not secret:
        raise ValueError("token secret is empty")

    first = "0" if secret[0] != "0" else "1"
    return first + secret[1:]


def init_sqlite_db(db_path: Path) -> None:
    db_path.parent.mkdir(parents=True, exist_ok=True)
    with sqlite3.connect(db_path) as conn:
        conn.execute("PRAGMA busy_timeout = 5000")


def wait_sqlite_schema(db_path: Path, timeout_seconds: float) -> None:
    deadline = time.time() + timeout_seconds
    required_tables = {"proxy_groups", "tunnels", "rate_policies", "rate_policy_bindings"}

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


def write_frps_config(config_path: Path, db_path: Path, ports: Ports, log_level: str) -> None:
    _ = db_path
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


def seed_runtime_data(db_path: Path, token: TokenMaterial, ports: Ports, scenario: str) -> None:
    created_at = timestamp_now()
    group_enabled = 0 if scenario == "disabled_group" else 1
    tunnel_enabled = 0 if scenario == "disabled_tunnel" else 1
    with sqlite3.connect(db_path, timeout=5.0) as conn:
        conn.execute("PRAGMA busy_timeout = 5000")
        cursor = conn.execute(
            """
            INSERT INTO proxy_groups (
                name,
                client_id,
                client_secret_hash,
                effective_ip,
                enabled,
                created_at,
                updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                "e2e-group",
                token.token_id,
                token.token_hash,
                "0.0.0.0",
                group_enabled,
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
                "e2e-tcp",
                "tcp",
                "single",
                ports.remote,
                ports.remote,
                "127.0.0.1",
                ports.echo,
                ports.echo,
                tunnel_enabled,
                created_at,
                created_at,
            ),
        )
        if scenario in ("rate_limit_independent", "rate_limit_shared"):
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
                    "e2e-tcp-b",
                    "tcp",
                    "single",
                    ports.reloaded_remote,
                    ports.reloaded_remote,
                    "127.0.0.1",
                    ports.reloaded_echo,
                    ports.reloaded_echo,
                    1,
                    created_at,
                    created_at,
                ),
            )
        conn.commit()


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


def validate_bad_token_scenario(
    paths: RuntimePaths,
    ports: Ports,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    validate_auth_rejection_scenario(
        scenario="bad_token",
        paths=paths,
        ports=ports,
        timeout_seconds=timeout_seconds,
        processes=processes,
        error_code=BAD_TOKEN_ERROR_CODE,
        error_text=BAD_TOKEN_ERROR_TEXT,
    )


def validate_disabled_group_scenario(
    paths: RuntimePaths,
    ports: Ports,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    validate_auth_rejection_scenario(
        scenario="disabled_group",
        paths=paths,
        ports=ports,
        timeout_seconds=timeout_seconds,
        processes=processes,
        error_code=DISABLED_GROUP_ERROR_CODE,
        error_text=DISABLED_GROUP_ERROR_TEXT,
    )


def validate_disabled_tunnel_scenario(
    paths: RuntimePaths,
    ports: Ports,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    assert_single_tunnel_enabled(paths.db_path, expected=0)
    wait_log_contains(paths.frps_log_path, "frpc control login succeeded", timeout_seconds, processes)
    wait_log_contains(paths.frps_log_path, "config acknowledged", timeout_seconds, processes)
    wait_log_contains(paths.frpc_log_path, "登录成功", timeout_seconds, processes)
    wait_log_contains(paths.frpc_log_path, "领取配置", timeout_seconds, processes)

    deadline = time.time() + min(timeout_seconds, NEGATIVE_STABILITY_WINDOW_SECONDS)
    while time.time() < deadline:
        ensure_processes_alive(processes)
        frps_log = read_log_text(paths.frps_log_path)
        if "frpc control login failed" in frps_log:
            raise RuntimeError("disabled_tunnel unexpectedly failed frpc login")
        if "tcp tunnel listener ready" in frps_log:
            raise RuntimeError("disabled_tunnel unexpectedly started remote listener")
        if tcp_connectable("127.0.0.1", ports.remote, timeout_seconds=0.25):
            raise RuntimeError(
                f"disabled_tunnel unexpectedly exposed remote listener on 127.0.0.1:{ports.remote}",
            )
        time.sleep(0.25)


def validate_local_unavailable_scenario(
    paths: RuntimePaths,
    ports: Ports,
    payload: bytes,
    timeout_seconds: float,
    processes: list[ManagedProcess],
    observations: RunObservations,
) -> None:
    assert_single_tunnel_enabled(paths.db_path, expected=1)
    wait_log_contains(paths.frps_log_path, "frpc control login succeeded", timeout_seconds, processes)
    wait_log_contains(paths.frps_log_path, "config acknowledged", timeout_seconds, processes)
    wait_log_contains(paths.frpc_log_path, "登录成功", timeout_seconds, processes)
    wait_log_contains(paths.frpc_log_path, "领取配置", timeout_seconds, processes)
    wait_log_contains(paths.frps_log_path, "tcp tunnel listener ready", timeout_seconds, processes)

    frps_log = read_log_text(paths.frps_log_path)
    if "frpc control login failed" in frps_log:
        raise RuntimeError("local_unavailable unexpectedly failed frpc login")

    attempt = run_external_client_expect_failure("127.0.0.1", ports.remote, payload, timeout_seconds)
    observations.external_attempt = attempt
    try:
        wait_log_contains(paths.frps_log_path, "stream open rejected", timeout_seconds, processes)
    except Exception as exc:
        raise RuntimeError(
            f"local_unavailable missing stream open rejection; {describe_external_attempt(attempt)}"
        ) from exc

    if attempt.response:
        raise RuntimeError(
            f"local_unavailable unexpectedly returned payload bytes: {attempt.response!r}; {describe_external_attempt(attempt)}",
        )
    if attempt.timed_out:
        raise RuntimeError(f"local_unavailable external client timed out waiting for failure; {describe_external_attempt(attempt)}")
    if attempt.send_error is None and attempt.recv_error is None and not attempt.connection_closed:
        raise RuntimeError(
            "local_unavailable external connection stayed open without response or close; "
            f"{describe_external_attempt(attempt)}",
        )


def validate_hot_reload_scenario(
    paths: RuntimePaths,
    ports: Ports,
    initial_echo_handle: EchoServerHandle,
    reload_echo_handle: EchoServerHandle,
    payload: bytes,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    wait_log_count_at_least(paths.frps_log_path, "config acknowledged", 1, timeout_seconds, processes)
    wait_log_count_at_least(paths.frpc_log_path, "领取配置", 1, timeout_seconds, processes)
    wait_log_count_at_least(paths.frps_log_path, "tcp tunnel listener ready", 1, timeout_seconds, processes)

    initial_ack_count = count_log_occurrences(paths.frps_log_path, "config acknowledged")
    initial_apply_count = count_log_occurrences(paths.frpc_log_path, "领取配置")
    initial_listener_count = count_log_occurrences(paths.frps_log_path, "tcp tunnel listener ready")

    first_payload = payload + b"-before"
    second_payload = payload + b"-after"
    public_conn = socket.create_connection(("127.0.0.1", ports.remote), timeout=timeout_seconds)
    try:
        public_conn.settimeout(timeout_seconds)
        first_response = run_external_client_exchange(public_conn, first_payload)
        if first_response != first_payload:
            raise RuntimeError(f"hot_reload first echo mismatch: sent {first_payload!r}, received {first_response!r}")

        wait_for_echo_payload(initial_echo_handle, first_payload, min(timeout_seconds, 10.0))

        base_url = f"http://127.0.0.1:{ports.management}"
        opener = login_management_session(base_url, MANAGEMENT_SECRET)
        try:
            tunnel_id, group_id = load_single_tunnel_record(paths.db_path, "e2e-tcp")
            patch_single_tunnel_via_management(
                opener,
                base_url,
                tunnel_id,
                group_id,
                "e2e-tcp",
                "tcp",
                ports.reloaded_remote,
                ports.reloaded_echo,
            )
            assert_single_tunnel_mapping(paths.db_path, "e2e-tcp", ports.reloaded_remote, ports.reloaded_echo)
        finally:
            logout_management_session(opener, base_url)

        wait_log_count_at_least(paths.frps_log_path, "config acknowledged", initial_ack_count + 1, timeout_seconds, processes)
        wait_log_count_at_least(paths.frpc_log_path, "领取配置", initial_apply_count + 1, timeout_seconds, processes)
        wait_log_count_at_least(
            paths.frps_log_path,
            "tcp tunnel listener ready",
            initial_listener_count + 1,
            timeout_seconds,
            processes,
        )
        wait_log_contains(paths.frpc_log_path, "+0 ~1 -0", timeout_seconds, processes)

        wait_for_tcp_connection_close(public_conn, timeout_seconds)
        wait_for_tcp_port_close("127.0.0.1", ports.remote, timeout_seconds, processes)

        second_response = run_external_client("127.0.0.1", ports.reloaded_remote, second_payload, timeout_seconds)
        if second_response != second_payload:
            raise RuntimeError(f"hot_reload second echo mismatch: sent {second_payload!r}, received {second_response!r}")

        wait_for_echo_payload(reload_echo_handle, second_payload, min(timeout_seconds, 10.0))
        if second_payload in initial_echo_handle.server.snapshot():
            raise RuntimeError("hot_reload unexpectedly delivered the second payload to the old tcp target")
    finally:
        public_conn.close()


def validate_rate_limit_independent_scenario(
    paths: RuntimePaths,
    ports: Ports,
    payload_seed: bytes,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    apply_rate_policy_to_tunnels(
        paths,
        ports,
        ("e2e-tcp", "e2e-tcp-b"),
        name="e2e-tcp-independent",
        mode="independent",
        downlink_value=TCP_RATE_LIMIT_SLOW_MEGABITS,
        downlink_unit="M",
        uplink_value=TCP_RATE_LIMIT_SLOW_MEGABITS,
        uplink_unit="M",
        timeout_seconds=timeout_seconds,
        processes=processes,
    )

    payload_a = make_sized_payload(payload_seed + b"-independent-a", TCP_RATE_LIMIT_PAYLOAD_BYTES)
    payload_b = make_sized_payload(payload_seed + b"-independent-b", TCP_RATE_LIMIT_PAYLOAD_BYTES)
    results, wall_seconds = wait_for_concurrent_tcp_rate_limit_minimum(
        (
            ("tunnel_a", ports.remote, payload_a),
            ("tunnel_b", ports.reloaded_remote, payload_b),
        ),
        minimum_wall_seconds=TCP_RATE_LIMIT_INDEPENDENT_MIN_SECONDS,
        timeout_seconds=timeout_seconds,
    )
    if results["tunnel_a"].response != payload_a:
        raise RuntimeError("rate_limit_independent tunnel_a returned mismatched payload")
    if results["tunnel_b"].response != payload_b:
        raise RuntimeError("rate_limit_independent tunnel_b returned mismatched payload")
    if wall_seconds > TCP_RATE_LIMIT_INDEPENDENT_MAX_SECONDS:
        raise RuntimeError(
            "rate_limit_independent took too long, limiters may have competed unexpectedly: "
            f"wall={wall_seconds:.3f}s",
        )


def validate_rate_limit_shared_scenario(
    paths: RuntimePaths,
    ports: Ports,
    payload_seed: bytes,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    apply_rate_policy_to_tunnels(
        paths,
        ports,
        ("e2e-tcp", "e2e-tcp-b"),
        name="e2e-tcp-shared",
        mode="shared",
        downlink_value=TCP_RATE_LIMIT_SLOW_MEGABITS,
        downlink_unit="M",
        uplink_value=TCP_RATE_LIMIT_SLOW_MEGABITS,
        uplink_unit="M",
        timeout_seconds=timeout_seconds,
        processes=processes,
    )

    payload_a = make_sized_payload(payload_seed + b"-shared-a", TCP_RATE_LIMIT_PAYLOAD_BYTES)
    payload_b = make_sized_payload(payload_seed + b"-shared-b", TCP_RATE_LIMIT_PAYLOAD_BYTES)
    results, wall_seconds = wait_for_concurrent_tcp_rate_limit_minimum(
        (
            ("tunnel_a", ports.remote, payload_a),
            ("tunnel_b", ports.reloaded_remote, payload_b),
        ),
        minimum_wall_seconds=TCP_RATE_LIMIT_SHARED_MIN_SECONDS,
        timeout_seconds=timeout_seconds,
    )
    if results["tunnel_a"].response != payload_a:
        raise RuntimeError("rate_limit_shared tunnel_a returned mismatched payload")
    if results["tunnel_b"].response != payload_b:
        raise RuntimeError("rate_limit_shared tunnel_b returned mismatched payload")
    if wall_seconds > TCP_RATE_LIMIT_SHARED_MAX_SECONDS:
        raise RuntimeError(f"rate_limit_shared took too long: wall={wall_seconds:.3f}s")


def validate_rate_limit_reload_scenario(
    paths: RuntimePaths,
    ports: Ports,
    payload_seed: bytes,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    policy_id = apply_rate_policy_to_tunnels(
        paths,
        ports,
        ("e2e-tcp",),
        name="e2e-tcp-reload",
        mode="independent",
        downlink_value=TCP_RATE_LIMIT_FAST_MEGABITS,
        downlink_unit="M",
        uplink_value=TCP_RATE_LIMIT_FAST_MEGABITS,
        uplink_unit="M",
        timeout_seconds=timeout_seconds,
        processes=processes,
    )

    payload = make_sized_payload(payload_seed + b"-reload", TCP_RATE_LIMIT_PAYLOAD_BYTES)
    fast_round_trip = run_external_client_timed("127.0.0.1", ports.remote, payload, timeout_seconds)
    if fast_round_trip.response != payload:
        raise RuntimeError("rate_limit_reload fast policy returned mismatched payload")
    if fast_round_trip.elapsed_seconds > TCP_RATE_LIMIT_RELOAD_FAST_MAX_SECONDS:
        raise RuntimeError(
            "rate_limit_reload fast policy was slower than expected: "
            f"elapsed={fast_round_trip.elapsed_seconds:.3f}s",
        )

    update_rate_policy_and_wait(
        paths,
        ports,
        policy_id,
        name="e2e-tcp-reload",
        mode="independent",
        downlink_value=TCP_RATE_LIMIT_SLOW_MEGABITS,
        downlink_unit="M",
        uplink_value=TCP_RATE_LIMIT_SLOW_MEGABITS,
        uplink_unit="M",
        timeout_seconds=timeout_seconds,
        processes=processes,
    )

    slow_round_trip = wait_for_tcp_rate_limit_minimum(
        "127.0.0.1",
        ports.remote,
        payload,
        minimum_elapsed_seconds=TCP_RATE_LIMIT_RELOAD_SLOW_MIN_SECONDS,
        timeout_seconds=timeout_seconds,
    )
    if slow_round_trip.response != payload:
        raise RuntimeError("rate_limit_reload slow policy returned mismatched payload")
    if slow_round_trip.elapsed_seconds <= fast_round_trip.elapsed_seconds:
        raise RuntimeError(
            "rate_limit_reload did not get slower after policy update: "
            f"fast={fast_round_trip.elapsed_seconds:.3f}s slow={slow_round_trip.elapsed_seconds:.3f}s",
        )


def apply_rate_policy_to_tunnels(
    paths: RuntimePaths,
    ports: Ports,
    tunnel_names: tuple[str, ...],
    *,
    name: str,
    mode: str,
    downlink_value: int,
    downlink_unit: str,
    uplink_value: int,
    uplink_unit: str,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> int:
    base_url = f"http://127.0.0.1:{ports.management}"
    opener = login_management_session(base_url, MANAGEMENT_SECRET)
    initial_ack_count = count_log_occurrences(paths.frps_log_path, "config acknowledged")
    initial_apply_count = count_log_occurrences(paths.frpc_log_path, "领取配置")
    try:
        policy_id = create_rate_policy(
            opener,
            base_url,
            name=name,
            mode=mode,
            downlink_value=downlink_value,
            downlink_unit=downlink_unit,
            uplink_value=uplink_value,
            uplink_unit=uplink_unit,
        )
        tunnel_ids: list[int] = []
        for tunnel_name in tunnel_names:
            tunnel_id, _ = load_single_tunnel_record(paths.db_path, tunnel_name)
            tunnel_ids.append(tunnel_id)
        set_rate_policy_tunnels(opener, base_url, policy_id, tunnel_ids)

        expected_refreshes = 1 if tunnel_ids else 0
        wait_log_count_at_least(
            paths.frps_log_path,
            "config acknowledged",
            initial_ack_count + expected_refreshes,
            timeout_seconds,
            processes,
        )
        wait_log_count_at_least(
            paths.frpc_log_path,
            "领取配置",
            initial_apply_count + expected_refreshes,
            timeout_seconds,
            processes,
        )
        return policy_id
    finally:
        logout_management_session(opener, base_url)


def update_rate_policy_and_wait(
    paths: RuntimePaths,
    ports: Ports,
    policy_id: int,
    *,
    name: str,
    mode: str,
    downlink_value: int,
    downlink_unit: str,
    uplink_value: int,
    uplink_unit: str,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    base_url = f"http://127.0.0.1:{ports.management}"
    opener = login_management_session(base_url, MANAGEMENT_SECRET)
    initial_ack_count = count_log_occurrences(paths.frps_log_path, "config acknowledged")
    initial_apply_count = count_log_occurrences(paths.frpc_log_path, "领取配置")
    try:
        update_rate_policy(
            opener,
            base_url,
            policy_id,
            name=name,
            mode=mode,
            downlink_value=downlink_value,
            downlink_unit=downlink_unit,
            uplink_value=uplink_value,
            uplink_unit=uplink_unit,
        )

        wait_log_count_at_least(
            paths.frps_log_path,
            "config acknowledged",
            initial_ack_count + 1,
            timeout_seconds,
            processes,
        )
        wait_log_count_at_least(
            paths.frpc_log_path,
            "领取配置",
            initial_apply_count + 1,
            timeout_seconds,
            processes,
        )
    finally:
        logout_management_session(opener, base_url)


def wait_for_tcp_rate_limit_minimum(
    host: str,
    port: int,
    payload: bytes,
    minimum_elapsed_seconds: float,
    timeout_seconds: float,
) -> TimedRoundTrip:
    deadline = time.time() + timeout_seconds
    last_round_trip: TimedRoundTrip | None = None
    while time.time() < deadline:
        last_round_trip = run_external_client_timed(host, port, payload, timeout_seconds)
        if last_round_trip.response != payload:
            raise RuntimeError("tcp rate-limited round-trip returned mismatched payload")
        if last_round_trip.elapsed_seconds >= minimum_elapsed_seconds:
            return last_round_trip
        time.sleep(0.25)

    raise RuntimeError(
        "tcp rate limit did not take effect in time: "
        f"minimum={minimum_elapsed_seconds:.3f}s last={0.0 if last_round_trip is None else last_round_trip.elapsed_seconds:.3f}s",
    )


def wait_for_concurrent_tcp_rate_limit_minimum(
    cases: tuple[tuple[str, int, bytes], ...],
    *,
    minimum_wall_seconds: float,
    timeout_seconds: float,
) -> tuple[dict[str, TimedRoundTrip], float]:
    deadline = time.time() + timeout_seconds
    last_wall_seconds = 0.0
    while time.time() < deadline:
        results, wall_seconds = run_concurrent_tcp_round_trips(cases, timeout_seconds)
        last_wall_seconds = wall_seconds
        if wall_seconds >= minimum_wall_seconds:
            return results, wall_seconds
        time.sleep(0.25)

    raise RuntimeError(
        "concurrent tcp rate limit did not take effect in time: "
        f"minimum={minimum_wall_seconds:.3f}s last={last_wall_seconds:.3f}s",
    )


def run_concurrent_tcp_round_trips(
    cases: tuple[tuple[str, int, bytes], ...],
    timeout_seconds: float,
) -> tuple[dict[str, TimedRoundTrip], float]:
    outcomes: queue.Queue[tuple[str, TimedRoundTrip | None, str | None]] = queue.Queue()
    threads: list[threading.Thread] = []

    def worker(name: str, port: int, payload: bytes) -> None:
        try:
            outcomes.put((name, run_external_client_timed("127.0.0.1", port, payload, timeout_seconds), None))
        except Exception as exc:
            outcomes.put((name, None, str(exc)))

    start = time.perf_counter()
    for name, port, payload in cases:
        thread = threading.Thread(
            target=worker,
            args=(name, port, payload),
            name=f"tcp-rate-limit-{name}",
            daemon=True,
        )
        threads.append(thread)
        thread.start()

    for thread in threads:
        thread.join(timeout=timeout_seconds + 5.0)
        if thread.is_alive():
            raise TimeoutError(f"concurrent tcp worker did not finish in time: {thread.name}")

    wall_seconds = time.perf_counter() - start
    results: dict[str, TimedRoundTrip] = {}
    errors: list[str] = []
    while not outcomes.empty():
        name, result, error_text = outcomes.get_nowait()
        if error_text is not None or result is None:
            errors.append(f"{name}: {error_text}")
            continue
        results[name] = result

    if errors:
        raise RuntimeError("concurrent tcp round-trip failed: " + "; ".join(errors))
    if len(results) != len(cases):
        raise RuntimeError(f"concurrent tcp round-trip lost results: got {len(results)} want {len(cases)}")
    return results, wall_seconds


def validate_auth_rejection_scenario(
    scenario: str,
    paths: RuntimePaths,
    ports: Ports,
    timeout_seconds: float,
    processes: list[ManagedProcess],
    error_code: int,
    error_text: str,
) -> None:
    wait_log_contains(paths.frps_log_path, "frpc control login failed", timeout_seconds, processes)
    wait_log_contains(paths.frps_log_path, error_text, timeout_seconds, processes)
    wait_log_contains(
        paths.frpc_log_path,
        f"frps error {error_code}: {error_text}",
        timeout_seconds,
        processes,
    )

    deadline = time.time() + min(timeout_seconds, NEGATIVE_STABILITY_WINDOW_SECONDS)
    while time.time() < deadline:
        ensure_processes_alive(processes)
        frps_log = read_log_text(paths.frps_log_path)
        if "frpc control login succeeded" in frps_log:
            raise RuntimeError(f"{scenario} unexpectedly authenticated")
        if "tcp tunnel listener ready" in frps_log:
            raise RuntimeError(f"{scenario} unexpectedly started remote listener")
        if tcp_connectable("127.0.0.1", ports.remote, timeout_seconds=0.25):
            raise RuntimeError(f"{scenario} unexpectedly exposed remote listener on 127.0.0.1:{ports.remote}")
        time.sleep(0.25)


def assert_single_tunnel_enabled(db_path: Path, expected: int) -> None:
    with sqlite3.connect(db_path, timeout=5.0) as conn:
        row = conn.execute(
            "SELECT enabled FROM tunnels WHERE name = ?",
            ("e2e-tcp",),
        ).fetchone()
    if row is None:
        raise RuntimeError("expected e2e-tcp tunnel row to exist")
    actual = int(row[0])
    if actual != expected:
        raise RuntimeError(f"expected e2e-tcp tunnel enabled={expected}, got {actual}")


def ensure_processes_alive(processes: list[ManagedProcess]) -> None:
    for process in processes:
        return_code = process.popen.poll()
        if return_code is not None:
            raise RuntimeError(f"{process.name} exited unexpectedly with code {return_code}")


def run_echo_server(host: str, port: int) -> EchoServerHandle:
    server = ThreadedEchoServer((host, port), EchoRequestHandler)
    thread = threading.Thread(target=server.serve_forever, name="echo-server", daemon=True)
    thread.start()
    return EchoServerHandle(server=server, thread=thread)


def make_sized_payload(seed: bytes, size: int) -> bytes:
    if size <= 0:
        raise ValueError("payload size must be positive")
    pattern = seed or b"x"
    return (pattern * ((size + len(pattern) - 1) // len(pattern)))[:size]


def run_external_client(host: str, port: int, payload: bytes, timeout_seconds: float) -> bytes:
    with socket.create_connection((host, port), timeout=timeout_seconds) as conn:
        conn.settimeout(timeout_seconds)
        return run_external_client_exchange(conn, payload)


def run_external_client_timed(host: str, port: int, payload: bytes, timeout_seconds: float) -> TimedRoundTrip:
    started_at = time.perf_counter()
    response = run_external_client(host, port, payload, timeout_seconds)
    return TimedRoundTrip(response=response, elapsed_seconds=time.perf_counter() - started_at)


def run_external_client_exchange(conn: socket.socket, payload: bytes) -> bytes:
    response = bytearray()
    conn.sendall(payload)
    while len(response) < len(payload):
        chunk = conn.recv(65535)
        if not chunk:
            break
        response.extend(chunk)
    return bytes(response)


def run_external_client_expect_failure(
    host: str,
    port: int,
    payload: bytes,
    timeout_seconds: float,
) -> ExternalClientAttempt:
    response = bytearray()
    send_error: str | None = None
    recv_error: str | None = None
    connection_closed = False
    timed_out = False

    with socket.create_connection((host, port), timeout=timeout_seconds) as conn:
        conn.settimeout(min(timeout_seconds, 1.0))
        try:
            conn.sendall(payload)
        except OSError as exc:
            send_error = str(exc)
            return ExternalClientAttempt(
                send_error=send_error,
                response=bytes(response),
                recv_error=recv_error,
                connection_closed=connection_closed,
                timed_out=timed_out,
            )

        deadline = time.time() + timeout_seconds
        while time.time() < deadline:
            try:
                chunk = conn.recv(65535)
            except socket.timeout:
                continue
            except OSError as exc:
                recv_error = str(exc)
                break

            if not chunk:
                connection_closed = True
                break

            response.extend(chunk)
            if len(response) >= len(payload):
                break
        else:
            timed_out = True

    return ExternalClientAttempt(
        send_error=send_error,
        response=bytes(response),
        recv_error=recv_error,
        connection_closed=connection_closed,
        timed_out=timed_out,
    )


def tcp_connectable(host: str, port: int, timeout_seconds: float) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout_seconds):
            return True
    except OSError:
        return False


def wait_for_echo_payload(handle: EchoServerHandle, payload: bytes, timeout_seconds: float) -> None:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        if payload in handle.server.snapshot():
            return
        time.sleep(0.05)

    raise TimeoutError(f"tcp echo server did not observe payload in time: {payload!r}")


def wait_for_tcp_connection_close(conn: socket.socket, timeout_seconds: float) -> None:
    deadline = time.time() + timeout_seconds
    conn.settimeout(0.25)
    while time.time() < deadline:
        try:
            payload = conn.recv(1)
        except socket.timeout:
            continue
        except OSError:
            return

        if not payload:
            return
        raise RuntimeError(f"tcp hot_reload received unexpected payload while waiting for close: {payload!r}")

    raise TimeoutError("tcp public connection stayed open after hot reload")


def wait_for_tcp_port_close(
    host: str,
    port: int,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> None:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        ensure_processes_alive(processes)
        if not tcp_connectable(host, port, timeout_seconds=0.25):
            return
        time.sleep(0.25)

    raise TimeoutError(f"tcp public port stayed open after hot reload: {host}:{port}")


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
        if count_log_occurrences(log_path, needle) >= minimum_count:
            return
        time.sleep(0.25)

    raise TimeoutError(
        f"log count check timed out: {needle!r} expected at least {minimum_count} in {log_path}",
    )


def count_log_occurrences(log_path: Path, needle: str) -> int:
    return read_log_text(log_path).count(needle)


def login_management_session(base_url: str, secret: str) -> urllib.request.OpenerDirector:
    cookie_jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookie_jar))
    key_hash = sha256_hex(secret)

    try:
        init_result = request_json(
            opener,
            "POST",
            f"{base_url}/api/v1/auth/init",
            payload={"key_hash": key_hash},
            expected_status=201,
        )
        if init_result.get("initialized") is not True:
            raise RuntimeError(f"management auth init returned unexpected payload: {init_result!r}")
    except RuntimeError:
        perform_request(
            opener,
            "POST",
            f"{base_url}/api/v1/auth/init",
            payload={"key_hash": key_hash},
            expected_status=409,
        )

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


def logout_management_session(opener: urllib.request.OpenerDirector, base_url: str) -> None:
    try:
        request_json(
            opener,
            "POST",
            f"{base_url}/api/v1/auth/logout",
            expected_status=200,
        )
    except RuntimeError:
        return


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


def create_rate_policy(
    opener: urllib.request.OpenerDirector,
    base_url: str,
    *,
    name: str,
    mode: str,
    downlink_value: int,
    downlink_unit: str,
    uplink_value: int,
    uplink_unit: str,
) -> int:
    response = request_json(
        opener,
        "POST",
        f"{base_url}/api/v1/rate-policies",
        payload={
            "name": name,
            "mode": mode,
            "downlink": {"value": downlink_value, "unit": downlink_unit},
            "uplink": {"value": uplink_value, "unit": uplink_unit},
        },
        expected_status=201,
    )
    item = response.get("item")
    if not isinstance(item, dict) or int(item.get("id") or 0) <= 0:
        raise RuntimeError(f"unexpected create rate policy payload: {response!r}")
    return int(item["id"])


def update_rate_policy(
    opener: urllib.request.OpenerDirector,
    base_url: str,
    policy_id: int,
    *,
    name: str,
    mode: str,
    downlink_value: int,
    downlink_unit: str,
    uplink_value: int,
    uplink_unit: str,
) -> None:
    request_json(
        opener,
        "PATCH",
        f"{base_url}/api/v1/rate-policies/{policy_id}",
        payload={
            "name": name,
            "mode": mode,
            "downlink": {"value": downlink_value, "unit": downlink_unit},
            "uplink": {"value": uplink_value, "unit": uplink_unit},
        },
        expected_status=200,
    )


def set_rate_policy_tunnels(
    opener: urllib.request.OpenerDirector,
    base_url: str,
    policy_id: int,
    tunnel_ids: list[int],
) -> None:
    request_json(
        opener,
        "PUT",
        f"{base_url}/api/v1/rate-policies/{policy_id}/bindings",
        payload={"tunnel_ids": tunnel_ids},
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


def describe_external_attempt(attempt: ExternalClientAttempt) -> str:
    return (
        f"send_error={attempt.send_error!r} "
        f"response_len={len(attempt.response)} "
        f"response_preview={preview_bytes(attempt.response)} "
        f"recv_error={attempt.recv_error!r} "
        f"connection_closed={attempt.connection_closed} "
        f"timed_out={attempt.timed_out}"
    )


def scenario_expectation_summary(scenario: str) -> str:
    expectations = {
        "happy_path": "frpc login succeeds, frps exposes the remote listener, and the external client receives an exact echo of the payload",
        "bad_token": "frpc login is rejected during authentication and frps never exposes the remote listener",
        "disabled_group": "the proxy group is disabled, frpc login is rejected, and frps never exposes the remote listener",
        "disabled_tunnel": "frpc login and config apply succeed, but the disabled tunnel never exposes the remote listener",
        "local_unavailable": "frpc login and remote listener succeed, then a real stream.open is rejected because the local target is unavailable and no echo payload is returned",
        "hot_reload": "frpc completes a second config.push/config.ack cycle, the old tcp runtime is closed, and external traffic reaches the reloaded target",
        "rate_limit_independent": "two tcp tunnels bind to one independent rate policy, each tunnel is limited, and concurrent traffic does not share one bucket",
        "rate_limit_shared": "two tcp tunnels bind to one shared rate policy and concurrent traffic competes for one shared bucket",
        "rate_limit_reload": "an existing tcp rate policy is updated online and frps applies the slower limit without changing the tunnel mapping",
    }
    try:
        return expectations[scenario]
    except KeyError as exc:
        raise ValueError(f"unsupported scenario: {scenario}") from exc


def scenario_actual_summary(
    scenario: str,
    paths: RuntimePaths,
    processes: list[ManagedProcess],
    observations: RunObservations,
) -> str:
    frps_log = read_log_text(paths.frps_log_path)
    frpc_log = read_log_text(paths.frpc_log_path)
    group_enabled, tunnel_enabled = read_seed_flags(paths.db_path)
    rate_policy_count, rate_policy_binding_count = read_rate_policy_counts(paths.db_path)
    parts = [
        f"group_enabled={group_enabled}",
        f"tunnel_enabled={tunnel_enabled}",
        f"rate_policy_count={rate_policy_count}",
        f"rate_policy_binding_count={rate_policy_binding_count}",
        f"frps_login_failed={'frpc control login failed' in frps_log}",
        f"frps_login_succeeded={'frpc control login succeeded' in frps_log}",
        f"frps_config_ack={'config acknowledged' in frps_log}",
        f"frps_listener_ready={'tcp tunnel listener ready' in frps_log}",
        f"frps_stream_open_rejected={'stream open rejected' in frps_log}",
        f"frpc_login_succeeded={'登录成功' in frpc_log}",
        f"frpc_config_applied={'领取配置' in frpc_log}",
        f"processes={summarize_process_states(processes)}",
    ]

    error_text = scenario_expected_error_text(scenario)
    if error_text is not None:
        parts.append(f"expected_error_in_frps_log={error_text in frps_log}")
        parts.append(f"expected_error_in_frpc_log={error_text in frpc_log}")

    if observations.external_attempt is not None:
        parts.append(f"external_attempt=({describe_external_attempt(observations.external_attempt)})")
    elif scenario == "local_unavailable":
        parts.append("external_attempt=not_recorded")

    return "; ".join(parts)


def scenario_expected_error_text(scenario: str) -> str | None:
    if scenario == "bad_token":
        return BAD_TOKEN_ERROR_TEXT
    if scenario == "disabled_group":
        return DISABLED_GROUP_ERROR_TEXT
    return None


def read_seed_flags(db_path: Path) -> tuple[str, str]:
    if not db_path.exists():
        return ("unknown", "unknown")

    try:
        with sqlite3.connect(db_path, timeout=1.0) as conn:
            group_row = conn.execute(
                "SELECT enabled FROM proxy_groups WHERE name = ? ORDER BY id DESC LIMIT 1",
                ("e2e-group",),
            ).fetchone()
            tunnel_row = conn.execute(
                "SELECT enabled FROM tunnels WHERE name = ? ORDER BY id DESC LIMIT 1",
                ("e2e-tcp",),
            ).fetchone()
    except sqlite3.Error:
        return ("unknown", "unknown")

    group_enabled = "missing" if group_row is None else str(int(group_row[0]))
    tunnel_enabled = "missing" if tunnel_row is None else str(int(tunnel_row[0]))
    return (group_enabled, tunnel_enabled)


def read_rate_policy_counts(db_path: Path) -> tuple[str, str]:
    if not db_path.exists():
        return ("unknown", "unknown")

    try:
        with sqlite3.connect(db_path, timeout=1.0) as conn:
            policy_row = conn.execute("SELECT COUNT(1) FROM rate_policies").fetchone()
            binding_row = conn.execute("SELECT COUNT(1) FROM rate_policy_bindings").fetchone()
    except sqlite3.Error:
        return ("unknown", "unknown")

    policy_count = "unknown" if policy_row is None else str(int(policy_row[0]))
    binding_count = "unknown" if binding_row is None else str(int(binding_row[0]))
    return (policy_count, binding_count)


def summarize_process_states(processes: list[ManagedProcess]) -> str:
    if not processes:
        return "none"

    states: list[str] = []
    for process in processes:
        return_code = process.popen.poll()
        if return_code is None:
            states.append(f"{process.name}=running")
        else:
            states.append(f"{process.name}=exit({return_code})")
    return ",".join(states)


def preview_bytes(data: bytes, limit: int = 48) -> str:
    if not data:
        return "b''"

    preview = repr(data[:limit])
    if len(data) > limit:
        preview += "..."
    return preview


def cleanup(
    processes: list[ManagedProcess],
    echo_handles: list[EchoServerHandle],
    temp_root: Path,
    keep_temp: bool,
) -> None:
    for echo_handle in reversed(echo_handles):
        echo_handle.server.shutdown()
        echo_handle.server.server_close()
        echo_handle.thread.join(timeout=5.0)

    for process in reversed(processes):
        terminate_process(process)

    if not keep_temp:
        shutil.rmtree(temp_root, ignore_errors=True)


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


def read_log_text(log_path: Path) -> str:
    try:
        return log_path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return ""


def format_ports(ports: Ports) -> str:
    return (
        f"control_port={ports.control} "
        f"management_port={ports.management} "
        f"remote_port={ports.remote} "
        f"echo_port={ports.echo} "
        f"reloaded_remote_port={ports.reloaded_remote} "
        f"reloaded_echo_port={ports.reloaded_echo}"
    )


def timestamp_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f")


if __name__ == "__main__":
    sys.exit(main())
