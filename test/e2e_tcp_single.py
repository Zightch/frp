#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import os
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
SUPPORTED_SCENARIOS = ("happy_path", "bad_token", "disabled_group", "disabled_tunnel", "local_unavailable")
BAD_TOKEN_ERROR_CODE = 1101
BAD_TOKEN_ERROR_TEXT = "challenge response mismatch"
DISABLED_GROUP_ERROR_CODE = 1103
DISABLED_GROUP_ERROR_TEXT = "proxy group is disabled"
NEGATIVE_STABILITY_WINDOW_SECONDS = 2.0


@dataclass(frozen=True)
class Ports:
    control: int
    management: int
    remote: int
    echo: int


@dataclass(frozen=True)
class TokenMaterial:
    token_id: str
    token_secret: str
    token_hash: str
    frpc_token: str


@dataclass(frozen=True)
class RuntimePaths:
    temp_root: Path
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


@dataclass
class RunObservations:
    external_attempt: ExternalClientAttempt | None = None


class ThreadedEchoServer(ThreadingMixIn, TCPServer):
    allow_reuse_address = True
    daemon_threads = True


class EchoRequestHandler(BaseRequestHandler):
    def handle(self) -> None:
        while True:
            try:
                payload = self.request.recv(65535)
            except OSError:
                return
            if not payload:
                return
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
    echo_handle: EchoServerHandle | None = None
    ports: Ports | None = None
    observations = RunObservations()

    temp_root = Path(tempfile.mkdtemp(prefix="frp-e2e-"))
    paths = RuntimePaths(
        temp_root=temp_root,
        db_path=temp_root / "frps.sqlite",
        frps_config_path=temp_root / "frps.json",
        frps_log_path=temp_root / "frps.log",
        frpc_log_path=temp_root / "frpc.log",
    )

    try:
        print(f"[info] scenario={args.scenario}")
        print(f"[stage] {stage}")
        ensure_go_available(args)

        stage = "build or resolve binaries"
        print(f"[stage] {stage}")
        binaries = build_or_resolve_binaries(args, repo_root, temp_root)

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
            command=[str(binaries["frps"]), "--config", str(paths.frps_config_path)],
            cwd=repo_root / "frps",
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

        if args.scenario == "happy_path":
            stage = "start echo server"
            print(f"[stage] {stage}")
            echo_handle = run_echo_server("127.0.0.1", ports.echo)

        stage = "start frpc"
        print(f"[stage] {stage}")
        frpc_process = start_process(
            name=f"frpc[{args.scenario}]",
            command=[
                str(binaries["frpc"]),
                "--server",
                f"127.0.0.1:{ports.control}",
                "--token",
                token_value_for_scenario(args.scenario, token),
            ],
            cwd=repo_root / "frpc",
            log_path=paths.frpc_log_path,
            env={"FRPC_LOG_LEVEL": args.frpc_log_level},
        )
        processes.append(frpc_process)

        if args.scenario == "happy_path":
            stage = "wait for remote tcp listener"
            print(f"[stage] {stage}")
            wait_log_contains(paths.frps_log_path, "tcp tunnel listener ready", timeout, processes)

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
        cleanup(processes, echo_handle, paths.temp_root, args.keep_temp)


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
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(
            f"go build failed for {package} in {module_dir}\nstdout:\n{result.stdout}\nstderr:\n{result.stderr}",
        )


def allocate_ports() -> Ports:
    reserved: list[socket.socket] = []
    numbers: list[int] = []
    try:
        for _ in range(4):
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
    required_tables = {"schema_migrations", "proxy_groups", "tunnels"}

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
    config = {
        "control_listen_addr": f"127.0.0.1:{ports.control}",
        "management_listen_addr": f"127.0.0.1:{ports.management}",
        "read_header_timeout": "5s",
        "shutdown_timeout": "10s",
        "database": {
            "type": "sqlite",
            "path": str(db_path),
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
                token_id,
                token_hash,
                enabled,
                max_clients,
                rate_limit,
                client_access_mode,
                tunnel_access_mode,
                created_at,
                updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                "e2e-group",
                token.token_id,
                token.token_hash,
                group_enabled,
                1,
                0,
                "disabled",
                "disabled",
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
                rate_limit,
                capture_enabled,
                created_at,
                updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
                0,
                0,
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
    wait_log_contains(paths.frpc_log_path, "login succeeded", timeout_seconds, processes)
    wait_log_contains(paths.frpc_log_path, "config applied", timeout_seconds, processes)

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
    wait_log_contains(paths.frpc_log_path, "login succeeded", timeout_seconds, processes)
    wait_log_contains(paths.frpc_log_path, "config applied", timeout_seconds, processes)
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


def run_external_client(host: str, port: int, payload: bytes, timeout_seconds: float) -> bytes:
    response = bytearray()
    with socket.create_connection((host, port), timeout=timeout_seconds) as conn:
        conn.settimeout(timeout_seconds)
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
    parts = [
        f"group_enabled={group_enabled}",
        f"tunnel_enabled={tunnel_enabled}",
        f"frps_login_failed={'frpc control login failed' in frps_log}",
        f"frps_login_succeeded={'frpc control login succeeded' in frps_log}",
        f"frps_config_ack={'config acknowledged' in frps_log}",
        f"frps_listener_ready={'tcp tunnel listener ready' in frps_log}",
        f"frps_stream_open_rejected={'stream open rejected' in frps_log}",
        f"frpc_login_succeeded={'login succeeded' in frpc_log}",
        f"frpc_config_applied={'config applied' in frpc_log}",
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
    echo_handle: EchoServerHandle | None,
    temp_root: Path,
    keep_temp: bool,
) -> None:
    if echo_handle is not None:
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
        f"echo_port={ports.echo}"
    )


def timestamp_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f")


if __name__ == "__main__":
    sys.exit(main())
