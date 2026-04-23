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
from dataclasses import dataclass
from pathlib import Path
from socketserver import BaseRequestHandler, ThreadingMixIn, TCPServer
from typing import IO


DEFAULT_TIMEOUT_SECONDS = 30.0
SCHEMA_TIMEOUT_SECONDS = 15.0
SUPPORTED_SCENARIOS = ("happy_path", "hot_reload")
MANAGEMENT_SECRET = "frp-tcp-range-e2e-management-secret"
TCP_RANGE_SIZE = 2


@dataclass(frozen=True)
class Ports:
    control: int
    management: int
    remote_single: int
    remote_range_start: int
    reloaded_remote_range_start: int
    local_single: int
    local_range_start: int
    reloaded_local_range_start: int

    @property
    def remote_range_end(self) -> int:
        return self.remote_range_start + TCP_RANGE_SIZE - 1

    @property
    def local_range_end(self) -> int:
        return self.local_range_start + TCP_RANGE_SIZE - 1

    @property
    def remote_range_ports(self) -> list[int]:
        return [self.remote_range_start + offset for offset in range(TCP_RANGE_SIZE)]

    @property
    def local_range_ports(self) -> list[int]:
        return [self.local_range_start + offset for offset in range(TCP_RANGE_SIZE)]

    @property
    def reloaded_remote_range_end(self) -> int:
        return self.reloaded_remote_range_start + TCP_RANGE_SIZE - 1

    @property
    def reloaded_local_range_end(self) -> int:
        return self.reloaded_local_range_start + TCP_RANGE_SIZE - 1

    @property
    def reloaded_remote_range_ports(self) -> list[int]:
        return [self.reloaded_remote_range_start + offset for offset in range(TCP_RANGE_SIZE)]

    @property
    def reloaded_local_range_ports(self) -> list[int]:
        return [self.reloaded_local_range_start + offset for offset in range(TCP_RANGE_SIZE)]


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
    db_path: Path
    frps_bin_path: Path
    frpc_bin_path: Path
    frps_config_path: Path
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
class RecordedTCPServerHandle:
    name: str
    response_prefix: bytes
    server: "RecordedTCPServer"
    thread: threading.Thread

    def snapshot(self) -> tuple[list[bytes], list[str]]:
        with self.server.lock:
            return list(self.server.received_payloads), list(self.server.client_addrs)


class RecordedTCPServer(ThreadingMixIn, TCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, server_address: tuple[str, int], handler_class: type[BaseRequestHandler], response_prefix: bytes):
        self.response_prefix = response_prefix
        self.lock = threading.Lock()
        self.received_payloads: list[bytes] = []
        self.client_addrs: list[str] = []
        super().__init__(server_address, handler_class)


class RecordedTCPRequestHandler(BaseRequestHandler):
    def handle(self) -> None:
        server = self.server
        if not isinstance(server, RecordedTCPServer):
            return

        try:
            payload = self.request.recv(65535)
        except OSError:
            return
        if not payload:
            return

        client_ip, client_port = self.client_address
        with server.lock:
            server.received_payloads.append(payload)
            server.client_addrs.append(f"{client_ip}:{client_port}")

        response = server.response_prefix + payload
        try:
            self.request.sendall(response)
        except OSError:
            return


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Run a minimal TCP range e2e test against local frps/frpc with Python public "
            "clients and Python local TCP servers."
        ),
    )
    parser.add_argument(
        "--frps-bin",
        help="Existing frps binary path. If omitted, build one into the output directory.",
    )
    parser.add_argument(
        "--frpc-bin",
        help="Existing frpc binary path. If omitted, build one into the output directory.",
    )
    parser.add_argument(
        "--webui-dist",
        help="Existing built webui dist directory. If omitted, reuse frps/webui/dist or build it.",
    )
    parser.add_argument(
        "--output-dir",
        help="Directory used for sqlite data, built binaries, logs, and result.json.",
    )
    parser.add_argument(
        "--payload-prefix",
        default="frp-tcp-range-e2e",
        help="Prefix used to derive the single and range verification payloads.",
    )
    parser.add_argument(
        "--scenario",
        default="happy_path",
        choices=SUPPORTED_SCENARIOS,
        help="TCP range e2e scenario to run. Default: happy_path.",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_SECONDS,
        help=f"Timeout in seconds for readiness checks and tcp round-trips. Default: {DEFAULT_TIMEOUT_SECONDS}.",
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
    local_servers: list[RecordedTCPServerHandle] = []
    ports: Ports | None = None

    try:
        print(f"[info] scenario={args.scenario}")
        print(f"[stage] {stage}")
        prepare_output_dir(paths)

        stage = "build or resolve binaries"
        print(f"[stage] {stage}")
        build_or_copy_binary(
            source_arg=args.frps_bin,
            module_dir=repo_root / "frps",
            package="./cmd/frps",
            output_path=paths.frps_bin_path,
            binary_name="frps",
        )
        build_or_copy_binary(
            source_arg=args.frpc_bin,
            module_dir=repo_root / "frpc",
            package="./cmd/frpc",
            output_path=paths.frpc_bin_path,
            binary_name="frpc",
        )

        stage = "build or resolve webui dist"
        print(f"[stage] {stage}")
        copy_webui_dist(resolve_webui_dist(args, repo_root), paths.webui_dist_dir)

        stage = "allocate ports and token"
        print(f"[stage] {stage}")
        ports = allocate_ports()
        token = build_token_material()

        stage = "write frps config and init sqlite"
        print(f"[stage] {stage}")
        init_sqlite_db(paths.db_path)
        write_frps_config(paths.frps_config_path, paths.db_path, ports, args.frps_log_level)

        stage = "start frps"
        print(f"[stage] {stage}")
        frps_process = start_process(
            name="frps",
            command=[str(paths.frps_bin_path)],
            cwd=paths.workspace_dir,
            log_path=paths.frps_log_path,
        )
        processes.append(frps_process)

        stage = "wait for frps management api and schema bootstrap"
        print(f"[stage] {stage}")
        wait_http_ready(f"http://127.0.0.1:{ports.management}/readyz", args.timeout, processes)
        wait_sqlite_schema(paths.db_path, min(args.timeout, SCHEMA_TIMEOUT_SECONDS))
        wait_log_contains(paths.frps_log_path, "frpc control listener ready", args.timeout, processes)

        stage = "seed tcp single and range tunnels"
        print(f"[stage] {stage}")
        seed_runtime_data(paths.db_path, token, ports)

        stage = "start local python tcp servers"
        print(f"[stage] {stage}")
        local_servers = [
            start_recorded_tcp_server("single", "127.0.0.1", ports.local_single, b"single:"),
            start_recorded_tcp_server("range-0", "127.0.0.1", ports.local_range_start, b"range-0:"),
            start_recorded_tcp_server("range-1", "127.0.0.1", ports.local_range_start + 1, b"range-1:"),
        ]

        stage = "start frpc"
        print(f"[stage] {stage}")
        frpc_process = start_process(
            name="frpc",
            command=[
                str(paths.frpc_bin_path),
                "--server",
                f"127.0.0.1:{ports.control}",
                "--key",
                token.frpc_token,
            ],
            cwd=repo_root / "frpc",
            log_path=paths.frpc_log_path,
            env={"FRPC_LOG_LEVEL": args.frpc_log_level},
        )
        processes.append(frpc_process)

        stage = "wait for tcp single and range listeners"
        print(f"[stage] {stage}")
        wait_log_contains(paths.frps_log_path, "frpc control login succeeded", args.timeout, processes)
        wait_log_contains(paths.frps_log_path, "config acknowledged", args.timeout, processes)
        wait_log_contains(paths.frpc_log_path, "login succeeded", args.timeout, processes)
        wait_log_contains(paths.frpc_log_path, "config applied", args.timeout, processes)
        wait_log_count_at_least(paths.frps_log_path, "tcp tunnel listener ready", 3, args.timeout, processes)

        stage = "run tcp range scenario"
        print(f"[stage] {stage}")
        verification = run_verification_round(paths, ports, local_servers, args.payload_prefix, args.timeout)
        if args.scenario == "hot_reload":
            stage = "start reload local python tcp servers"
            print(f"[stage] {stage}")
            local_servers.extend(
                [
                    start_recorded_tcp_server(
                        "reload-range-0",
                        "127.0.0.1",
                        ports.reloaded_local_range_start,
                        b"reload-range-0:",
                    ),
                    start_recorded_tcp_server(
                        "reload-range-1",
                        "127.0.0.1",
                        ports.reloaded_local_range_start + 1,
                        b"reload-range-1:",
                    ),
                ],
            )

            stage = "run tcp range hot reload verification"
            print(f"[stage] {stage}")
            hot_reload_summary = run_hot_reload_scenario(
                paths,
                ports,
                local_servers,
                args.payload_prefix,
                args.timeout,
                processes,
            )
            verification.update(hot_reload_summary)

        result = {
            "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "output_dir": str(paths.output_dir),
            "workspace_dir": str(paths.workspace_dir),
            "db_path": str(paths.db_path),
            "frps_log_path": str(paths.frps_log_path),
            "frpc_log_path": str(paths.frpc_log_path),
            "control_addr": f"127.0.0.1:{ports.control}",
            "management_url": f"http://127.0.0.1:{ports.management}",
            "scenario": args.scenario,
            "remote_single_addr": f"127.0.0.1:{ports.remote_single}",
            "remote_range_addrs": [f"127.0.0.1:{port}" for port in ports.remote_range_ports],
            "reloaded_remote_range_addrs": [f"127.0.0.1:{port}" for port in ports.reloaded_remote_range_ports],
            "local_single_addr": f"127.0.0.1:{ports.local_single}",
            "local_range_addrs": [f"127.0.0.1:{port}" for port in ports.local_range_ports],
            "reloaded_local_range_addrs": [f"127.0.0.1:{port}" for port in ports.reloaded_local_range_ports],
            "verified_steps": [
                "frps direct startup from workspace/data/config.json",
                "frps/frpc real process startup",
                "sqlite seed for one tcp single tunnel and one tcp range tunnel",
                "python local tcp servers for single and two range targets",
                "single-port tcp path still reaches the single local target",
                "same tcp range tunnel reaches two different local ports by remotePort offset",
            ]
            + verification.pop("verified_steps", []),
        }
        result.update(verification)
        paths.result_json_path.write_text(json.dumps(result, indent=2), encoding="utf-8")

        print(f"[ok] tcp range {args.scenario} succeeded")
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
        for handle in local_servers:
            stop_recorded_tcp_server(handle)
        for process in reversed(processes):
            terminate_process(process)


def validate_args(args: argparse.Namespace) -> None:
    if args.timeout <= 0:
        raise ValueError("--timeout must be positive")


def resolve_output_dir(args: argparse.Namespace, repo_root: Path) -> Path:
    if args.output_dir:
        return Path(args.output_dir).resolve()
    timestamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    return (repo_root / "test" / "tmp" / f"tcp-range-e2e-{timestamp}").resolve()


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
        db_path=data_dir / "frps.db",
        frps_bin_path=workspace_dir / executable_name("frps"),
        frpc_bin_path=output_dir / executable_name("frpc"),
        frps_config_path=data_dir / "config.json",
        frps_log_path=output_dir / "frps.log",
        frpc_log_path=output_dir / "frpc.log",
        result_json_path=output_dir / "result.json",
    )


def executable_name(base: str) -> str:
    return f"{base}.exe" if os.name == "nt" else base


def prepare_output_dir(paths: RuntimePaths) -> None:
    if paths.output_dir.exists():
        shutil.rmtree(paths.output_dir)
    paths.data_dir.mkdir(parents=True, exist_ok=True)
    paths.webui_dir.mkdir(parents=True, exist_ok=True)


def build_or_copy_binary(
    source_arg: str | None,
    module_dir: Path,
    package: str,
    output_path: Path,
    binary_name: str,
) -> None:
    if source_arg:
        source = Path(source_arg).resolve()
        if not source.is_file():
            raise RuntimeError(f"{binary_name} binary does not exist: {source}")
        shutil.copy2(source, output_path)
        return

    ensure_go_available()
    build_go_binary(module_dir, package, output_path)


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
            f"go build failed for {package} in {module_dir}\nstdout:\n{result.stdout}\nstderr:\n{result.stderr}"
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
    excluded = set(tcp_ports)

    remote_range_start, reloaded_remote_range_start = find_free_overlapping_tcp_ranges(excluded)
    excluded.update(remote_range_start + offset for offset in range(TCP_RANGE_SIZE + 1))

    remote_single = find_free_tcp_port(excluded)
    excluded.add(remote_single)

    local_range_start = find_free_tcp_port_range(TCP_RANGE_SIZE, excluded)
    excluded.update(local_range_start + offset for offset in range(TCP_RANGE_SIZE))

    local_single = find_free_tcp_port(excluded)
    excluded.add(local_single)

    reloaded_local_range_start = find_free_tcp_port_range(TCP_RANGE_SIZE, excluded)

    return Ports(
        control=tcp_ports[0],
        management=tcp_ports[1],
        remote_single=remote_single,
        remote_range_start=remote_range_start,
        reloaded_remote_range_start=reloaded_remote_range_start,
        local_single=local_single,
        local_range_start=local_range_start,
        reloaded_local_range_start=reloaded_local_range_start,
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


def find_free_tcp_port(excluded: set[int]) -> int:
    for _ in range(200):
        probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            probe.bind(("127.0.0.1", 0))
            probe.listen(1)
            port = probe.getsockname()[1]
        finally:
            probe.close()

        if port not in excluded:
            return port

    raise RuntimeError("failed to allocate a distinct tcp port")


def find_free_tcp_port_range(size: int, excluded: set[int]) -> int:
    if size <= 0:
        raise ValueError("tcp port range size must be positive")

    seed = find_free_tcp_port(excluded)
    max_start = 65535 - size + 1
    for start in range(max(seed, 1024), max_start + 1):
        if tcp_range_is_bindable(start, size, excluded):
            return start
    for start in range(1024, max(seed, 1024)):
        if tcp_range_is_bindable(start, size, excluded):
            return start

    raise RuntimeError(f"failed to allocate contiguous tcp port range of size {size}")


def tcp_range_is_bindable(start: int, size: int, excluded: set[int]) -> bool:
    listeners: list[socket.socket] = []
    try:
        for offset in range(size):
            port = start + offset
            if port in excluded:
                return False
            probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            try:
                probe.bind(("127.0.0.1", port))
                probe.listen(1)
            except OSError:
                probe.close()
                return False
            listeners.append(probe)
        return True
    finally:
        for listener in listeners:
            listener.close()


def find_free_overlapping_tcp_ranges(excluded: set[int]) -> tuple[int, int]:
    span_start = find_free_tcp_port_range(TCP_RANGE_SIZE + 1, excluded)
    return (span_start, span_start + 1)


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


def init_sqlite_db(db_path: Path) -> None:
    db_path.parent.mkdir(parents=True, exist_ok=True)
    with sqlite3.connect(db_path) as conn:
        conn.execute("PRAGMA busy_timeout = 5000")


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


def wait_sqlite_schema(db_path: Path, timeout_seconds: float) -> None:
    deadline = time.time() + timeout_seconds
    required_tables = {"proxy_groups", "tunnels"}

    while time.time() < deadline:
        try:
            with sqlite3.connect(db_path, timeout=1.0) as conn:
                rows = conn.execute("SELECT name FROM sqlite_master WHERE type = 'table'").fetchall()
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
                client_id,
                client_secret_hash,
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

        conn.executemany(
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
            [
                (
                    int(group_id),
                    "e2e-tcp-single",
                    "tcp",
                    "single",
                    ports.remote_single,
                    ports.remote_single,
                    "127.0.0.1",
                    ports.local_single,
                    ports.local_single,
                    1,
                    created_at,
                    created_at,
                ),
                (
                    int(group_id),
                    "e2e-tcp-range",
                    "tcp",
                    "range",
                    ports.remote_range_start,
                    ports.remote_range_end,
                    "127.0.0.1",
                    ports.local_range_start,
                    ports.local_range_end,
                    1,
                    created_at,
                    created_at,
                ),
            ],
        )
        conn.commit()


def start_recorded_tcp_server(name: str, host: str, port: int, response_prefix: bytes) -> RecordedTCPServerHandle:
    server = RecordedTCPServer((host, port), RecordedTCPRequestHandler, response_prefix)
    thread = threading.Thread(target=server.serve_forever, name=f"{name}-tcp-server", daemon=True)
    thread.start()
    return RecordedTCPServerHandle(
        name=name,
        response_prefix=response_prefix,
        server=server,
        thread=thread,
    )


def stop_recorded_tcp_server(handle: RecordedTCPServerHandle) -> None:
    handle.server.shutdown()
    handle.server.server_close()
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


def wait_log_contains(log_path: Path, needle: str, timeout_seconds: float, processes: list[ManagedProcess]) -> None:
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


def ensure_processes_alive(processes: list[ManagedProcess]) -> None:
    for process in processes:
        return_code = process.popen.poll()
        if return_code is not None:
            raise RuntimeError(f"{process.name} exited unexpectedly with code {return_code}")


def run_verification_round(
    paths: RuntimePaths,
    ports: Ports,
    local_servers: list[RecordedTCPServerHandle],
    payload_prefix: str,
    timeout_seconds: float,
) -> dict[str, object]:
    handles_by_name = {handle.name: handle for handle in local_servers}
    single_payload = f"{payload_prefix}-single".encode("utf-8")
    range_payloads = [
        f"{payload_prefix}-range-0".encode("utf-8"),
        f"{payload_prefix}-range-1".encode("utf-8"),
    ]

    single_response = run_external_client(
        "127.0.0.1",
        ports.remote_single,
        single_payload,
        handles_by_name["single"].response_prefix,
        timeout_seconds,
    )
    expected_single = handles_by_name["single"].response_prefix + single_payload
    if single_response != expected_single:
        raise RuntimeError(f"single tcp response mismatch: sent {single_payload!r}, received {single_response!r}")
    wait_for_server_payload(handles_by_name["single"], single_payload, timeout_seconds)

    range_responses: list[str] = []
    for index, payload in enumerate(range_payloads):
        handle = handles_by_name[f"range-{index}"]
        remote_port = ports.remote_range_start + index
        response = run_external_client("127.0.0.1", remote_port, payload, handle.response_prefix, timeout_seconds)
        expected = handle.response_prefix + payload
        if response != expected:
            raise RuntimeError(
                f"range tcp response mismatch for remote port {remote_port}: sent {payload!r}, received {response!r}"
            )
        wait_for_server_payload(handle, payload, timeout_seconds)
        range_responses.append(response.decode("utf-8", errors="replace"))

    single_received, single_clients = handles_by_name["single"].snapshot()
    range0_received, range0_clients = handles_by_name["range-0"].snapshot()
    range1_received, range1_clients = handles_by_name["range-1"].snapshot()

    ensure_exact_server_payloads(handles_by_name["single"], [single_payload])
    ensure_exact_server_payloads(handles_by_name["range-0"], [range_payloads[0]])
    ensure_exact_server_payloads(handles_by_name["range-1"], [range_payloads[1]])

    frps_log = read_log_text(paths.frps_log_path)
    for remote_port in [ports.remote_single, *ports.remote_range_ports]:
        if f"remote_port={remote_port}" not in frps_log:
            raise RuntimeError(f"frps log did not record tcp listener startup for remote_port={remote_port}")

    return {
        "verified_steps": [],
        "single_payload_utf8": single_payload.decode("utf-8", errors="replace"),
        "single_response_utf8": single_response.decode("utf-8", errors="replace"),
        "range_payloads_utf8": [payload.decode("utf-8", errors="replace") for payload in range_payloads],
        "range_responses_utf8": range_responses,
        "single_server_received_count": len(single_received),
        "range_server_received_counts": [len(range0_received), len(range1_received)],
        "single_server_client_addrs": single_clients,
        "range_server_client_addrs": [range0_clients, range1_clients],
    }


def run_external_client(
    host: str,
    port: int,
    payload: bytes,
    response_prefix: bytes,
    timeout_seconds: float,
) -> bytes:
    with socket.create_connection((host, port), timeout=timeout_seconds) as conn:
        conn.settimeout(timeout_seconds)
        return run_external_client_exchange(conn, payload, response_prefix)


def run_external_client_exchange(conn: socket.socket, payload: bytes, response_prefix: bytes) -> bytes:
    expected_length = len(response_prefix) + len(payload)
    response = bytearray()
    conn.sendall(payload)
    while len(response) < expected_length:
        chunk = conn.recv(65535)
        if not chunk:
            break
        response.extend(chunk)
    return bytes(response)


def wait_for_server_payload(handle: RecordedTCPServerHandle, payload: bytes, timeout_seconds: float) -> None:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        received_payloads, _ = handle.snapshot()
        if payload in received_payloads:
            return
        time.sleep(0.05)

    raise TimeoutError(f"local tcp server {handle.name} did not observe payload in time: {payload!r}")


def ensure_exact_server_payloads(handle: RecordedTCPServerHandle, expected_payloads: list[bytes]) -> None:
    received_payloads, _ = handle.snapshot()
    if received_payloads != expected_payloads:
        raise RuntimeError(
            f"local tcp server {handle.name} observed payloads {received_payloads!r}, expected {expected_payloads!r}"
        )


def run_hot_reload_scenario(
    paths: RuntimePaths,
    ports: Ports,
    local_servers: list[RecordedTCPServerHandle],
    payload_prefix: str,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> dict[str, object]:
    handles_by_name = {handle.name: handle for handle in local_servers}
    single_handle = handles_by_name["single"]
    old_range0_handle = handles_by_name["range-0"]
    old_range1_handle = handles_by_name["range-1"]
    reload_range0_handle = handles_by_name["reload-range-0"]
    reload_range1_handle = handles_by_name["reload-range-1"]

    wait_log_count_at_least(paths.frps_log_path, "config acknowledged", 1, timeout_seconds, processes)
    wait_log_count_at_least(paths.frpc_log_path, "config applied", 1, timeout_seconds, processes)
    wait_log_count_at_least(paths.frps_log_path, "tcp tunnel listener ready", 3, timeout_seconds, processes)

    initial_ack_count = count_log_occurrences(paths.frps_log_path, "config acknowledged")
    initial_apply_count = count_log_occurrences(paths.frpc_log_path, "config applied")
    initial_listener_count = count_log_occurrences(paths.frps_log_path, "tcp tunnel listener ready")

    removed_payload = f"{payload_prefix}-range-reload-before-removed".encode("utf-8")
    shared_payload = f"{payload_prefix}-range-reload-before-shared".encode("utf-8")
    single_after_payload = f"{payload_prefix}-single-after-reload".encode("utf-8")
    shared_after_payload = f"{payload_prefix}-range-reload-after-shared".encode("utf-8")
    added_after_payload = f"{payload_prefix}-range-reload-after-added".encode("utf-8")

    removed_connection = socket.create_connection(("127.0.0.1", ports.remote_range_start), timeout=timeout_seconds)
    shared_connection = socket.create_connection(
        ("127.0.0.1", ports.remote_range_start + 1),
        timeout=timeout_seconds,
    )
    try:
        removed_connection.settimeout(timeout_seconds)
        shared_connection.settimeout(timeout_seconds)

        removed_response = run_external_client_exchange(
            removed_connection,
            removed_payload,
            old_range0_handle.response_prefix,
        )
        expected_removed_response = old_range0_handle.response_prefix + removed_payload
        if removed_response != expected_removed_response:
            raise RuntimeError(
                "tcp range hot_reload removed-port response mismatch: "
                f"sent {removed_payload!r}, received {removed_response!r}"
            )
        wait_for_server_payload(old_range0_handle, removed_payload, timeout_seconds)

        shared_response = run_external_client_exchange(
            shared_connection,
            shared_payload,
            old_range1_handle.response_prefix,
        )
        expected_shared_response = old_range1_handle.response_prefix + shared_payload
        if shared_response != expected_shared_response:
            raise RuntimeError(
                "tcp range hot_reload shared-port response mismatch: "
                f"sent {shared_payload!r}, received {shared_response!r}"
            )
        wait_for_server_payload(old_range1_handle, shared_payload, timeout_seconds)

        old_range0_count = len(old_range0_handle.snapshot()[0])
        old_range1_count = len(old_range1_handle.snapshot()[0])

        base_url = f"http://127.0.0.1:{ports.management}"
        opener = login_management_session(base_url, MANAGEMENT_SECRET)
        tunnel_id, group_id = load_tunnel_record(paths.db_path, "e2e-tcp-range")
        patch_range_tunnel_via_management(
            opener,
            base_url,
            tunnel_id,
            group_id,
            "e2e-tcp-range",
            "tcp",
            ports.reloaded_remote_range_start,
            ports.reloaded_remote_range_end,
            ports.reloaded_local_range_start,
            ports.reloaded_local_range_end,
        )
        assert_range_tunnel_mapping(
            paths.db_path,
            "e2e-tcp-range",
            ports.reloaded_remote_range_start,
            ports.reloaded_remote_range_end,
            ports.reloaded_local_range_start,
            ports.reloaded_local_range_end,
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
            "config applied",
            initial_apply_count + 1,
            timeout_seconds,
            processes,
        )
        wait_log_count_at_least(
            paths.frps_log_path,
            "tcp tunnel listener ready",
            initial_listener_count + 3,
            timeout_seconds,
            processes,
        )
        wait_log_contains(paths.frpc_log_path, "replaced_tunnels=1", timeout_seconds, processes)

        wait_for_tcp_connection_close(removed_connection, timeout_seconds)
        wait_for_tcp_connection_close(shared_connection, timeout_seconds)
        wait_for_tcp_port_close("127.0.0.1", ports.remote_range_start, timeout_seconds, processes)

        single_after_response = run_external_client(
            "127.0.0.1",
            ports.remote_single,
            single_after_payload,
            single_handle.response_prefix,
            timeout_seconds,
        )
        expected_single_after_response = single_handle.response_prefix + single_after_payload
        if single_after_response != expected_single_after_response:
            raise RuntimeError(
                "tcp range hot_reload single-port response mismatch after reload: "
                f"sent {single_after_payload!r}, received {single_after_response!r}"
            )
        wait_for_server_payload(single_handle, single_after_payload, timeout_seconds)

        shared_after_response = run_external_client(
            "127.0.0.1",
            ports.reloaded_remote_range_start,
            shared_after_payload,
            reload_range0_handle.response_prefix,
            timeout_seconds,
        )
        expected_shared_after_response = reload_range0_handle.response_prefix + shared_after_payload
        if shared_after_response != expected_shared_after_response:
            raise RuntimeError(
                "tcp range hot_reload shared-port response mismatch after reload: "
                f"sent {shared_after_payload!r}, received {shared_after_response!r}"
            )
        wait_for_server_payload(reload_range0_handle, shared_after_payload, timeout_seconds)

        added_after_response = run_external_client(
            "127.0.0.1",
            ports.reloaded_remote_range_start + 1,
            added_after_payload,
            reload_range1_handle.response_prefix,
            timeout_seconds,
        )
        expected_added_after_response = reload_range1_handle.response_prefix + added_after_payload
        if added_after_response != expected_added_after_response:
            raise RuntimeError(
                "tcp range hot_reload added-port response mismatch after reload: "
                f"sent {added_after_payload!r}, received {added_after_response!r}"
            )
        wait_for_server_payload(reload_range1_handle, added_after_payload, timeout_seconds)

        assert_tcp_received_count_stable(old_range0_handle, old_range0_count, 0.5)
        assert_tcp_received_count_stable(old_range1_handle, old_range1_count, 0.5)
    finally:
        removed_connection.close()
        shared_connection.close()

    old_range0_payloads, _ = old_range0_handle.snapshot()
    old_range1_payloads, _ = old_range1_handle.snapshot()
    reload_range0_payloads, _ = reload_range0_handle.snapshot()
    reload_range1_payloads, _ = reload_range1_handle.snapshot()

    if shared_after_payload in old_range1_payloads:
        raise RuntimeError("tcp range hot_reload unexpectedly delivered shared-port traffic to the old range-1 target")
    if added_after_payload in old_range0_payloads or added_after_payload in old_range1_payloads:
        raise RuntimeError("tcp range hot_reload unexpectedly delivered added-port traffic to an old range target")

    return {
        "verified_steps": [
            "management tunnel patch triggered a second config.push/config.ack cycle",
            "existing tcp connections on removed and overlapping range ports were closed during reload",
            "removed old range port stopped accepting new tcp connections after reload",
            "overlapping range port was rebound to the new offset base instead of reusing the old mapping",
            "newly added range port forwarded to the reloaded local target",
            "unchanged single-port tunnel was rebuilt and still forwarded after the group reload",
        ],
        "range_hot_reload_removed_response_utf8": removed_response.decode("utf-8", errors="replace"),
        "range_hot_reload_shared_response_utf8": shared_response.decode("utf-8", errors="replace"),
        "range_hot_reload_single_after_response_utf8": single_after_response.decode("utf-8", errors="replace"),
        "range_hot_reload_shared_after_response_utf8": shared_after_response.decode("utf-8", errors="replace"),
        "range_hot_reload_added_after_response_utf8": added_after_response.decode("utf-8", errors="replace"),
        "old_range_server_received_counts_after_reload": [
            len(old_range0_payloads),
            len(old_range1_payloads),
        ],
        "reloaded_range_server_received_counts": [
            len(reload_range0_payloads),
            len(reload_range1_payloads),
        ],
    }


def tcp_connectable(host: str, port: int, timeout_seconds: float) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout_seconds):
            return True
    except OSError:
        return False


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
        raise RuntimeError(f"tcp range hot_reload received unexpected payload while waiting for close: {payload!r}")

    raise TimeoutError("tcp range public connection stayed open after hot reload")


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

    raise TimeoutError(f"tcp range public port stayed open after hot reload: {host}:{port}")


def assert_tcp_received_count_stable(
    handle: RecordedTCPServerHandle,
    expected_count: int,
    stable_window_seconds: float,
) -> None:
    deadline = time.time() + stable_window_seconds
    while time.time() < deadline:
        received_payloads, _ = handle.snapshot()
        if len(received_payloads) != expected_count:
            raise RuntimeError(
                f"old tcp target {handle.name} unexpectedly received more payloads after reload: "
                f"got {len(received_payloads)} want {expected_count}"
            )
        time.sleep(0.05)


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


def count_log_occurrences(log_path: Path, needle: str) -> int:
    return read_log_text(log_path).count(needle)


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


def patch_range_tunnel_via_management(
    opener: urllib.request.OpenerDirector,
    base_url: str,
    tunnel_id: int,
    group_id: int,
    tunnel_name: str,
    protocol_name: str,
    remote_start: int,
    remote_end: int,
    local_start: int,
    local_end: int,
) -> None:
    request_json(
        opener,
        "PATCH",
        f"{base_url}/api/v1/tunnels/{tunnel_id}",
        payload={
            "group_id": group_id,
            "name": tunnel_name,
            "protocol": protocol_name,
            "remote_type": "range",
            "remote_start": remote_start,
            "remote_end": remote_end,
            "local_host": "127.0.0.1",
            "local_start": local_start,
            "local_end": local_end,
            "enabled": True,
        },
        expected_status=200,
    )


def load_tunnel_record(db_path: Path, tunnel_name: str) -> tuple[int, int]:
    with sqlite3.connect(db_path, timeout=5.0) as conn:
        row = conn.execute(
            "SELECT id, group_id FROM tunnels WHERE name = ? ORDER BY id DESC LIMIT 1",
            (tunnel_name,),
        ).fetchone()
    if row is None:
        raise RuntimeError(f"tunnel row not found for {tunnel_name!r}")
    return (int(row[0]), int(row[1]))


def assert_range_tunnel_mapping(
    db_path: Path,
    tunnel_name: str,
    expected_remote_start: int,
    expected_remote_end: int,
    expected_local_start: int,
    expected_local_end: int,
) -> None:
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
    expected = (
        expected_remote_start,
        expected_remote_end,
        expected_local_start,
        expected_local_end,
    )
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
            f"unexpected status for {method} {url}: got {status} want {expected_status} "
            f"body={body.decode('utf-8', errors='replace')}"
        )

    return HTTPResult(status=status, body=body, headers=response_headers)


def sha256_hex(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def build_management_proof(key_hash: str, salt: str) -> str:
    return hashlib.sha256((key_hash + salt).encode("utf-8")).hexdigest()


def format_ports(ports: Ports) -> str:
    return (
        f"control_port={ports.control} "
        f"management_port={ports.management} "
        f"remote_single_port={ports.remote_single} "
        f"remote_range={ports.remote_range_start}-{ports.remote_range_end} "
        f"local_single_port={ports.local_single} "
        f"local_range={ports.local_range_start}-{ports.local_range_end}"
    )


def timestamp_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f")


if __name__ == "__main__":
    sys.exit(main())
