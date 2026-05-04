#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import math
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
from collections import Counter
from dataclasses import asdict, dataclass
from pathlib import Path
from socketserver import BaseRequestHandler, ThreadingMixIn, TCPServer
from typing import IO


DEFAULT_TIMEOUT_SECONDS = 30.0
DEFAULT_STABILITY_DURATION_SECONDS = 5.0
DEFAULT_STABILITY_CONCURRENCY = 64
DEFAULT_STABILITY_PAYLOAD_BYTES = 1024
DEFAULT_TRANSFER_CONCURRENCY = 4
DEFAULT_TRANSFER_BYTES_PER_CONNECTION = 8 * 1024 * 1024
PROCESS_SAMPLE_INTERVAL_SECONDS = 1.0
PROCESS_SAMPLE_HISTORY_LIMIT = 512
LINE_LIMIT_BYTES = 256
TRANSFER_CHUNK_BYTES = 64 * 1024


@dataclass(frozen=True)
class Ports:
    control: int
    management: int
    remote: int
    target: int


@dataclass(frozen=True)
class TokenMaterial:
    token_id: str
    token_hash: str
    token_value: str


@dataclass(frozen=True)
class RuntimePaths:
    output_dir: Path
    data_dir: Path
    db_path: Path
    frps_config_path: Path
    frps_log_path: Path
    frpc_log_path: Path
    target_log_path: Path
    report_json_path: Path
    report_md_path: Path


@dataclass
class ManagedProcess:
    name: str
    command: list[str]
    cwd: Path
    log_path: Path
    log_handle: IO[str]
    popen: subprocess.Popen[str]


@dataclass
class PerfTargetHandle:
    server: "ThreadedPerfTargetServer"
    thread: threading.Thread


@dataclass(frozen=True)
class ProcessSnapshot:
    timestamp: float
    cpu_seconds: float
    rss_bytes: int


class ThreadedPerfTargetServer(ThreadingMixIn, TCPServer):
    allow_reuse_address = True
    daemon_threads = True
    request_queue_size = 1024
    request_timeout = 60.0
    send_chunk = b"x" * 65536


class PerfTargetRequestHandler(BaseRequestHandler):
    def handle(self) -> None:
        self.request.settimeout(self.server.request_timeout)
        command_line = read_line(self.request, LINE_LIMIT_BYTES)
        if not command_line:
            return

        parts = command_line.decode("ascii", errors="strict").strip().split(" ", 1)
        if len(parts) != 2:
            return

        command = parts[0].upper()
        try:
            byte_count = int(parts[1])
        except ValueError:
            return
        if byte_count < 0:
            return

        if command == "ECHO":
            payload = read_exact(self.request, byte_count)
            self.request.sendall(payload)
            return

        if command == "UPLOAD":
            read_exact(self.request, byte_count)
            self.request.sendall(f"OK {byte_count}\n".encode("ascii"))
            return

        if command == "DOWNLOAD":
            remaining = byte_count
            chunk = self.server.send_chunk
            while remaining > 0:
                payload = chunk if remaining >= len(chunk) else chunk[:remaining]
                self.request.sendall(payload)
                remaining -= len(payload)
            return

        if command == "SINK":
            read_exact(self.request, byte_count)
            return


class ProcessSampler:
    def __init__(self, processes: dict[str, ManagedProcess], interval_seconds: float) -> None:
        self.processes = processes
        self.interval_seconds = interval_seconds
        self._samples: dict[str, list[ProcessSnapshot]] = {name: [] for name in processes}
        self._stop_event = threading.Event()
        self._thread = threading.Thread(target=self._run, name="process-sampler", daemon=True)
        self._lock = threading.Lock()

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> dict[str, dict[str, float | int | None]]:
        self._stop_event.set()
        self._thread.join(timeout=5.0)
        self._sample_once()
        return self.summary()

    def summary(self) -> dict[str, dict[str, float | int | None]]:
        cpu_count = max(os.cpu_count() or 1, 1)
        summary: dict[str, dict[str, float | int | None]] = {}
        with self._lock:
            for name, samples in self._samples.items():
                if not samples:
                    summary[name] = {
                        "sample_count": 0,
                        "peak_rss_bytes": None,
                        "cpu_percent_avg": None,
                        "cpu_percent_peak": None,
                    }
                    continue

                peak_rss = max(sample.rss_bytes for sample in samples)
                cpu_avg = None
                cpu_peak = None
                if len(samples) >= 2:
                    deltas = []
                    for previous, current in zip(samples, samples[1:]):
                        elapsed = current.timestamp - previous.timestamp
                        if elapsed <= 0:
                            continue
                        cpu_seconds = max(current.cpu_seconds - previous.cpu_seconds, 0.0)
                        deltas.append(cpu_seconds / elapsed / cpu_count * 100.0)
                    if deltas:
                        cpu_avg = sum(deltas) / len(deltas)
                        cpu_peak = max(deltas)

                summary[name] = {
                    "sample_count": len(samples),
                    "peak_rss_bytes": peak_rss,
                    "cpu_percent_avg": cpu_avg,
                    "cpu_percent_peak": cpu_peak,
                }

        return summary

    def samples(self) -> dict[str, list[dict[str, float | int]]]:
        with self._lock:
            return {
                name: [
                    {
                        "timestamp": sample.timestamp,
                        "cpu_seconds": sample.cpu_seconds,
                        "rss_bytes": sample.rss_bytes,
                    }
                    for sample in samples
                ]
                for name, samples in self._samples.items()
            }

    def _run(self) -> None:
        while not self._stop_event.wait(self.interval_seconds):
            self._sample_once()

    def _sample_once(self) -> None:
        for name, process in self.processes.items():
            if process.popen.poll() is not None:
                continue
            snapshot = read_process_snapshot(process.popen.pid)
            if snapshot is None:
                continue
            with self._lock:
                bucket = self._samples[name]
                bucket.append(snapshot)
                if len(bucket) > PROCESS_SAMPLE_HISTORY_LIMIT:
                    del bucket[:-PROCESS_SAMPLE_HISTORY_LIMIT]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run a minimal TCP pressure benchmark against local frps/frpc to surface code-path failures.",
    )
    parser.add_argument("--frps-bin", help="Existing frps binary path. If omitted, build into the output directory.")
    parser.add_argument("--frpc-bin", help="Existing frpc binary path. If omitted, build into the output directory.")
    parser.add_argument(
        "--perf-target-bin",
        help="Existing Go perf target binary path. Only used when --target-mode=go.",
    )
    parser.add_argument(
        "--output-dir",
        help="Directory used for sqlite, logs, and reports. Default: test/tmp/perf-<timestamp>.",
    )
    parser.add_argument(
        "--target-mode",
        default="python",
        choices=("python", "go"),
        help="Perf target implementation. python keeps the in-process server; go uses a standalone Go target.",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_SECONDS,
        help=f"Timeout in seconds for readiness checks and socket operations. Default: {DEFAULT_TIMEOUT_SECONDS}.",
    )
    parser.add_argument(
        "--stability-duration",
        type=float,
        default=DEFAULT_STABILITY_DURATION_SECONDS,
        help=f"Seconds for the high-concurrency stability workload. Default: {DEFAULT_STABILITY_DURATION_SECONDS}.",
    )
    parser.add_argument(
        "--stability-concurrency",
        type=int,
        default=DEFAULT_STABILITY_CONCURRENCY,
        help=f"Concurrent clients for the stability workload. Default: {DEFAULT_STABILITY_CONCURRENCY}.",
    )
    parser.add_argument(
        "--stability-payload-bytes",
        type=int,
        default=DEFAULT_STABILITY_PAYLOAD_BYTES,
        help=f"Payload size per stability request. Default: {DEFAULT_STABILITY_PAYLOAD_BYTES}.",
    )
    parser.add_argument(
        "--transfer-concurrency",
        type=int,
        default=DEFAULT_TRANSFER_CONCURRENCY,
        help=f"Concurrent clients for symmetric transfer checks. Default: {DEFAULT_TRANSFER_CONCURRENCY}.",
    )
    parser.add_argument(
        "--transfer-bytes-per-connection",
        type=int,
        default=DEFAULT_TRANSFER_BYTES_PER_CONNECTION,
        help=(
            "Payload bytes transferred by each symmetric transfer connection. "
            f"Default: {DEFAULT_TRANSFER_BYTES_PER_CONNECTION}."
        ),
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
    output_dir.mkdir(parents=True, exist_ok=True)

    paths = RuntimePaths(
        output_dir=output_dir,
        data_dir=output_dir / "data",
        db_path=(output_dir / "data" / "frps.sqlite"),
        frps_config_path=(output_dir / "data" / "config.json"),
        frps_log_path=output_dir / "frps.log",
        frpc_log_path=output_dir / "frpc.log",
        target_log_path=output_dir / "target.log",
        report_json_path=output_dir / "report.json",
        report_md_path=output_dir / "report.md",
    )

    stage = "prepare"
    ports: Ports | None = None
    token: TokenMaterial | None = None
    processes: list[ManagedProcess] = []
    target_handle: PerfTargetHandle | None = None
    sampler: ProcessSampler | None = None

    try:
        print(f"[stage] {stage}")
        ensure_go_available(args)

        stage = "build or resolve binaries"
        print(f"[stage] {stage}")
        binaries = build_or_resolve_binaries(args, repo_root, output_dir)

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
            cwd=paths.output_dir,
            log_path=paths.frps_log_path,
        )
        processes.append(frps_process)

        stage = "wait for frps management api"
        print(f"[stage] {stage}")
        wait_http_ready(
            url=f"http://127.0.0.1:{ports.management}/readyz",
            timeout_seconds=args.timeout,
            processes=processes,
        )

        stage = "wait for frps control listener and schema bootstrap"
        print(f"[stage] {stage}")
        wait_log_contains(paths.frps_log_path, "frpc control listener ready", args.timeout, processes)
        wait_sqlite_schema(paths.db_path, min(args.timeout, 15.0))

        stage = "seed runtime data"
        print(f"[stage] {stage}")
        seed_runtime_data(paths.db_path, token, ports)

        stage = "start perf target server"
        print(f"[stage] {stage}")
        if args.target_mode == "python":
            target_handle = run_perf_target_server("127.0.0.1", ports.target)
        else:
            target_process = start_process(
                name="perf-target",
                command=[str(binaries["perf-target"]), "--listen", f"127.0.0.1:{ports.target}"],
                cwd=repo_root / "frps",
                log_path=paths.target_log_path,
            )
            processes.append(target_process)
            wait_tcp_ready("127.0.0.1", ports.target, args.timeout, processes)

        stage = "start frpc"
        print(f"[stage] {stage}")
        frpc_process = start_process(
            name="frpc",
            command=[
                str(binaries["frpc"]),
                "--server",
                f"127.0.0.1:{ports.control}",
                "--key",
                token.token_value,
            ],
            cwd=repo_root / "frpc",
            log_path=paths.frpc_log_path,
            env={"FRPC_LOG_LEVEL": args.frpc_log_level},
        )
        processes.append(frpc_process)

        stage = "wait for remote tcp listener"
        print(f"[stage] {stage}")
        wait_log_contains(paths.frps_log_path, "frpc control login succeeded", args.timeout, processes)
        wait_log_contains(paths.frps_log_path, "config acknowledged", args.timeout, processes)
        wait_log_contains(paths.frps_log_path, "tcp tunnel listener ready", args.timeout, processes)
        wait_log_contains(paths.frpc_log_path, "登录成功", args.timeout, processes)
        wait_log_contains(paths.frpc_log_path, "领取配置", args.timeout, processes)

        sampler = ProcessSampler({process.name: process for process in processes}, PROCESS_SAMPLE_INTERVAL_SECONDS)
        sampler.start()

        stage = "run symmetric upload throughput benchmark"
        print(f"[stage] {stage}")
        upload = run_transfer_workload(
            workload_name="upload_symmetric",
            host="127.0.0.1",
            port=ports.remote,
            concurrency=args.transfer_concurrency,
            bytes_per_connection=args.transfer_bytes_per_connection,
            timeout_seconds=args.timeout,
            processes=processes,
        )

        stage = "run symmetric download throughput benchmark"
        print(f"[stage] {stage}")
        download = run_transfer_workload(
            workload_name="download_symmetric",
            host="127.0.0.1",
            port=ports.remote,
            concurrency=args.transfer_concurrency,
            bytes_per_connection=args.transfer_bytes_per_connection,
            timeout_seconds=args.timeout,
            processes=processes,
        )

        stage = "run stability benchmark"
        print(f"[stage] {stage}")
        stability = run_stability_workload(
            host="127.0.0.1",
            port=ports.remote,
            concurrency=args.stability_concurrency,
            duration_seconds=args.stability_duration,
            payload_bytes=args.stability_payload_bytes,
            timeout_seconds=args.timeout,
            processes=processes,
        )

        stage = "collect process summary"
        print(f"[stage] {stage}")
        process_summary = sampler.stop() if sampler is not None else {}
        process_samples = sampler.samples() if sampler is not None else {}
        sampler = None

        report = build_report(
            repo_root=repo_root,
            paths=paths,
            ports=ports,
            args=args,
            stability=stability,
            upload=upload,
            download=download,
            process_summary=process_summary,
            process_samples=process_samples,
        )

        stage = "write reports"
        print(f"[stage] {stage}")
        paths.report_json_path.write_text(json.dumps(report, indent=2), encoding="utf-8")
        paths.report_md_path.write_text(render_markdown_report(report), encoding="utf-8")

        print("[ok] performance benchmark completed")
        print(f"[info] output_dir={paths.output_dir}")
        print(f"[info] report_json={paths.report_json_path}")
        print(f"[info] report_md={paths.report_md_path}")
        print(f"[info] stability_attempts={stability['attempts']} success={stability['success']} failures={stability['failures']}")
        print(
            "[info] upload_symmetric_mbps="
            f"{upload['throughput_mbps']:.2f} "
            f"download_symmetric_mbps={download['throughput_mbps']:.2f}"
        )
        return 0
    except Exception as exc:
        print(f"[FAIL] stage={stage}: {exc}", file=sys.stderr)
        if ports is not None:
            print(f"[FAIL] ports={asdict(ports)}", file=sys.stderr)
        if token is not None:
            print(f"[FAIL] token_id={token.token_id}", file=sys.stderr)
        print(f"[FAIL] output_dir={paths.output_dir}", file=sys.stderr)
        dump_process_logs(processes)
        return 1
    finally:
        if sampler is not None:
            try:
                sampler.stop()
            except Exception:
                pass
        cleanup(processes, target_handle)


def validate_args(args: argparse.Namespace) -> None:
    if args.timeout <= 0:
        raise ValueError("--timeout must be positive")
    if args.stability_duration <= 0:
        raise ValueError("--stability-duration must be positive")
    if args.stability_concurrency <= 0:
        raise ValueError("--stability-concurrency must be positive")
    if args.stability_payload_bytes <= 0:
        raise ValueError("--stability-payload-bytes must be positive")
    if args.transfer_concurrency <= 0:
        raise ValueError("--transfer-concurrency must be positive")
    if args.transfer_bytes_per_connection <= 0:
        raise ValueError("--transfer-bytes-per-connection must be positive")
    if args.target_mode != "go" and args.perf_target_bin:
        raise ValueError("--perf-target-bin only applies when --target-mode=go")


def resolve_output_dir(args: argparse.Namespace, repo_root: Path) -> Path:
    if args.output_dir:
        return Path(args.output_dir).resolve()
    timestamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S-%f")
    return (repo_root / "test" / "tmp" / f"perf-{timestamp}").resolve()


def ensure_go_available(args: argparse.Namespace) -> None:
    if args.frps_bin and args.frpc_bin:
        return
    if shutil.which("go") is None:
        raise RuntimeError("go executable not found in PATH; pass --frps-bin and --frpc-bin to skip building")


def build_or_resolve_binaries(args: argparse.Namespace, repo_root: Path, output_dir: Path) -> dict[str, Path]:
    suffix = ".exe" if os.name == "nt" else ""
    binaries: dict[str, Path] = {}

    if args.frps_bin:
        binaries["frps"] = Path(args.frps_bin).resolve()
    else:
        output_path = output_dir / f"frps{suffix}"
        build_go_binary(repo_root / "frps", "./cmd/frps", output_path)
        binaries["frps"] = output_path

    if args.frpc_bin:
        binaries["frpc"] = Path(args.frpc_bin).resolve()
    else:
        output_path = output_dir / f"frpc{suffix}"
        build_go_binary(repo_root / "frpc", "./cmd/frpc", output_path)
        binaries["frpc"] = output_path

    if args.target_mode == "go":
        if args.perf_target_bin:
            binaries["perf-target"] = Path(args.perf_target_bin).resolve()
        else:
            output_path = output_dir / f"perf-target{suffix}"
            build_go_binary(repo_root / "frps", "./cmd/perftarget", output_path)
            binaries["perf-target"] = output_path

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
        target=numbers[3],
    )


def build_token_material() -> TokenMaterial:
    token_id_bytes = os.urandom(16)
    token_secret_bytes = os.urandom(32)
    token_id = token_id_bytes.hex()
    token_secret = token_secret_bytes.hex()
    token_hash = hashlib.sha256(token_secret_bytes).hexdigest()
    return TokenMaterial(
        token_id=token_id,
        token_hash=token_hash,
        token_value=token_id + token_secret,
    )


def init_sqlite_db(db_path: Path) -> None:
    db_path.parent.mkdir(parents=True, exist_ok=True)
    with sqlite3.connect(db_path) as conn:
        conn.execute("PRAGMA busy_timeout = 5000")


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


def write_frps_config(config_path: Path, db_path: Path, ports: Ports, log_level: str) -> None:
    config_path.parent.mkdir(parents=True, exist_ok=True)
    config = {
        "control_listen_addr": f"127.0.0.1:{ports.control}",
        "management_listen_addr": f"127.0.0.1:{ports.management}",
        "read_header_timeout": "5s",
        "shutdown_timeout": "10s",
        "database": {
            "type": "sqlite",
            "path": "./frps.sqlite",
        },
        "log": {
            "level": log_level,
            "format": "text",
        },
    }
    config_path.write_text(json.dumps(config, indent=2), encoding="utf-8")


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
                created_at,
                updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?)
            """,
            (
                "perf-group",
                token.token_id,
                token.token_hash,
                "0.0.0.0",
                1,
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
                "perf-tcp",
                "tcp",
                "single",
                ports.remote,
                ports.remote,
                "127.0.0.1",
                ports.target,
                ports.target,
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


def wait_tcp_ready(host: str, port: int, timeout_seconds: float, processes: list[ManagedProcess]) -> None:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        ensure_processes_alive(processes)
        try:
            with socket.create_connection((host, port), timeout=1.0):
                return
        except OSError:
            time.sleep(0.1)
    raise TimeoutError(f"tcp readiness check timed out: {host}:{port}")


def ensure_processes_alive(processes: list[ManagedProcess]) -> None:
    for process in processes:
        return_code = process.popen.poll()
        if return_code is not None:
            raise RuntimeError(f"{process.name} exited unexpectedly with code {return_code}")


def run_perf_target_server(host: str, port: int) -> PerfTargetHandle:
    server = ThreadedPerfTargetServer((host, port), PerfTargetRequestHandler)
    thread = threading.Thread(target=server.serve_forever, name="perf-target", daemon=True)
    thread.start()
    return PerfTargetHandle(server=server, thread=thread)


def run_stability_workload(
    host: str,
    port: int,
    concurrency: int,
    duration_seconds: float,
    payload_bytes: int,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> dict[str, object]:
    barrier = threading.Barrier(concurrency + 1)
    lock = threading.Lock()
    attempts = 0
    success = 0
    latencies: list[float] = []
    errors = Counter()
    payload = build_payload(payload_bytes)
    deadline_holder = {"deadline": 0.0}

    def worker() -> None:
        nonlocal attempts, success
        local_attempts = 0
        local_success = 0
        local_latencies: list[float] = []
        local_errors = Counter()

        barrier.wait()
        while time.perf_counter() < deadline_holder["deadline"]:
            local_attempts += 1
            started_at = time.perf_counter()
            try:
                response = run_echo_request(host, port, payload, timeout_seconds)
                if response != payload:
                    raise RuntimeError(f"echo mismatch: sent={len(payload)} received={len(response)}")
                local_success += 1
                local_latencies.append(time.perf_counter() - started_at)
            except Exception as exc:
                local_errors[normalize_error(exc)] += 1
                ensure_processes_alive(processes)

        with lock:
            attempts += local_attempts
            success += local_success
            latencies.extend(local_latencies)
            errors.update(local_errors)

    threads = [threading.Thread(target=worker, name=f"stability-{index}", daemon=True) for index in range(concurrency)]
    for thread in threads:
        thread.start()

    started_at = time.perf_counter()
    deadline_holder["deadline"] = started_at + duration_seconds
    barrier.wait()
    for thread in threads:
        thread.join()
    ended_at = time.perf_counter()

    failures = attempts - success
    elapsed_seconds = ended_at - started_at
    return {
        "workload": "stability",
        "concurrency": concurrency,
        "duration_seconds": elapsed_seconds,
        "target_duration_seconds": duration_seconds,
        "payload_bytes": payload_bytes,
        "attempts": attempts,
        "success": success,
        "failures": failures,
        "success_rate": (success / attempts) if attempts else 0.0,
        "requests_per_second": (success / elapsed_seconds) if elapsed_seconds > 0 else 0.0,
        "latency_ms": summarize_latencies(latencies),
        "errors": dict(errors.most_common(10)),
    }


def run_transfer_workload(
    workload_name: str,
    host: str,
    port: int,
    concurrency: int,
    bytes_per_connection: int,
    timeout_seconds: float,
    processes: list[ManagedProcess],
) -> dict[str, object]:
    barrier = threading.Barrier(concurrency + 1)
    lock = threading.Lock()
    success = 0
    errors = Counter()
    durations: list[float] = []
    transferred_bytes = 0

    def worker() -> None:
        nonlocal success, transferred_bytes
        local_success = 0
        local_transferred_bytes = 0
        local_durations: list[float] = []
        local_errors = Counter()

        barrier.wait()
        started_at = time.perf_counter()
        try:
            if workload_name == "upload":
                run_upload_request(host, port, bytes_per_connection, timeout_seconds)
            elif workload_name == "download":
                run_download_request(host, port, bytes_per_connection, timeout_seconds)
            elif workload_name == "upload_symmetric":
                run_sink_request(host, port, bytes_per_connection, timeout_seconds)
            elif workload_name == "download_symmetric":
                run_download_request(host, port, bytes_per_connection, timeout_seconds)
            else:
                raise ValueError(f"unsupported workload: {workload_name}")
            local_success = 1
            local_transferred_bytes = bytes_per_connection
            local_durations.append(time.perf_counter() - started_at)
        except Exception as exc:
            local_errors[normalize_error(exc)] += 1
            ensure_processes_alive(processes)

        with lock:
            success += local_success
            transferred_bytes += local_transferred_bytes
            durations.extend(local_durations)
            errors.update(local_errors)

    threads = [threading.Thread(target=worker, name=f"{workload_name}-{index}", daemon=True) for index in range(concurrency)]
    for thread in threads:
        thread.start()

    started_at = time.perf_counter()
    barrier.wait()
    for thread in threads:
        thread.join()
    ended_at = time.perf_counter()

    elapsed_seconds = ended_at - started_at
    throughput_bps = (transferred_bytes / elapsed_seconds) if elapsed_seconds > 0 else 0.0
    failures = concurrency - success
    return {
        "workload": workload_name,
        "concurrency": concurrency,
        "bytes_per_connection": bytes_per_connection,
        "connections": concurrency,
        "success": success,
        "failures": failures,
        "success_rate": (success / concurrency) if concurrency else 0.0,
        "total_payload_bytes": transferred_bytes,
        "wall_seconds": elapsed_seconds,
        "throughput_bytes_per_second": throughput_bps,
        "throughput_mbps": throughput_bps * 8 / 1_000_000,
        "connection_seconds": summarize_transfer_durations(durations),
        "errors": dict(errors.most_common(10)),
    }


def run_echo_request(host: str, port: int, payload: bytes, timeout_seconds: float) -> bytes:
    with socket.create_connection((host, port), timeout=timeout_seconds) as conn:
        conn.settimeout(timeout_seconds)
        conn.sendall(f"ECHO {len(payload)}\n".encode("ascii"))
        conn.sendall(payload)
        return read_exact(conn, len(payload))


def run_upload_request(host: str, port: int, byte_count: int, timeout_seconds: float) -> None:
    payload = b"u" * min(TRANSFER_CHUNK_BYTES, max(byte_count, 1))
    with socket.create_connection((host, port), timeout=timeout_seconds) as conn:
        conn.settimeout(timeout_seconds)
        conn.sendall(f"UPLOAD {byte_count}\n".encode("ascii"))
        remaining = byte_count
        while remaining > 0:
            chunk = payload if remaining >= len(payload) else payload[:remaining]
            conn.sendall(chunk)
            remaining -= len(chunk)
        acknowledgement = read_line(conn, LINE_LIMIT_BYTES)
        expected = f"OK {byte_count}\n".encode("ascii")
        if acknowledgement != expected:
            raise RuntimeError(f"unexpected upload acknowledgement: {acknowledgement!r}")


def run_download_request(host: str, port: int, byte_count: int, timeout_seconds: float) -> None:
    with socket.create_connection((host, port), timeout=timeout_seconds) as conn:
        conn.settimeout(timeout_seconds)
        conn.sendall(f"DOWNLOAD {byte_count}\n".encode("ascii"))
        read_exact_into_sink(conn, byte_count)


def run_sink_request(host: str, port: int, byte_count: int, timeout_seconds: float) -> None:
    payload = b"s" * min(TRANSFER_CHUNK_BYTES, max(byte_count, 1))
    with socket.create_connection((host, port), timeout=timeout_seconds) as conn:
        conn.settimeout(timeout_seconds)
        conn.sendall(f"SINK {byte_count}\n".encode("ascii"))
        remaining = byte_count
        while remaining > 0:
            chunk = payload if remaining >= len(payload) else payload[:remaining]
            conn.sendall(chunk)
            remaining -= len(chunk)


def read_line(conn: socket.socket, limit: int) -> bytes:
    buffer = bytearray()
    while len(buffer) < limit:
        chunk = conn.recv(1)
        if not chunk:
            break
        buffer.extend(chunk)
        if chunk == b"\n":
            break
    return bytes(buffer)


def read_exact(conn: socket.socket, byte_count: int) -> bytes:
    buffer = bytearray()
    while len(buffer) < byte_count:
        chunk = conn.recv(min(65536, byte_count - len(buffer)))
        if not chunk:
            raise RuntimeError(f"connection closed early after {len(buffer)} of {byte_count} bytes")
        buffer.extend(chunk)
    return bytes(buffer)


def read_exact_into_sink(conn: socket.socket, byte_count: int) -> None:
    remaining = byte_count
    while remaining > 0:
        chunk = conn.recv(min(TRANSFER_CHUNK_BYTES, remaining))
        if not chunk:
            received = byte_count - remaining
            raise RuntimeError(f"connection closed early after {received} of {byte_count} bytes")
        remaining -= len(chunk)


def build_payload(byte_count: int) -> bytes:
    seed = b"frp-perf-payload"
    payload = bytearray()
    while len(payload) < byte_count:
        payload.extend(seed)
    return bytes(payload[:byte_count])


def summarize_latencies(latencies: list[float]) -> dict[str, float | None]:
    if not latencies:
        return {
            "min": None,
            "avg": None,
            "p50": None,
            "p95": None,
            "p99": None,
            "max": None,
        }

    sorted_values = sorted(latencies)
    return {
        "min": seconds_to_ms(sorted_values[0]),
        "avg": seconds_to_ms(sum(sorted_values) / len(sorted_values)),
        "p50": seconds_to_ms(percentile(sorted_values, 50)),
        "p95": seconds_to_ms(percentile(sorted_values, 95)),
        "p99": seconds_to_ms(percentile(sorted_values, 99)),
        "max": seconds_to_ms(sorted_values[-1]),
    }


def summarize_transfer_durations(durations: list[float]) -> dict[str, float | None]:
    if not durations:
        return {
            "min": None,
            "avg": None,
            "p50": None,
            "p95": None,
            "max": None,
        }

    sorted_values = sorted(durations)
    return {
        "min": sorted_values[0],
        "avg": sum(sorted_values) / len(sorted_values),
        "p50": percentile(sorted_values, 50),
        "p95": percentile(sorted_values, 95),
        "max": sorted_values[-1],
    }


def percentile(sorted_values: list[float], rank: float) -> float:
    if not sorted_values:
        raise ValueError("cannot compute percentile for empty data")
    if len(sorted_values) == 1:
        return sorted_values[0]
    position = (len(sorted_values) - 1) * rank / 100.0
    lower_index = int(math.floor(position))
    upper_index = int(math.ceil(position))
    if lower_index == upper_index:
        return sorted_values[lower_index]
    lower = sorted_values[lower_index]
    upper = sorted_values[upper_index]
    weight = position - lower_index
    return lower + (upper - lower) * weight


def seconds_to_ms(seconds: float) -> float:
    return seconds * 1000.0


def normalize_error(exc: Exception) -> str:
    return f"{type(exc).__name__}: {str(exc).strip()}"[:200]


def has_windows_ephemeral_port_error(workload: dict[str, object]) -> bool:
    errors = workload.get("errors")
    if not isinstance(errors, dict):
        return False
    return any("WinError 10048" in str(key) for key in errors)


def has_only_windows_ephemeral_port_errors(workload: dict[str, object]) -> bool:
    errors = workload.get("errors")
    if not isinstance(errors, dict) or not errors:
        return False
    return all("WinError 10048" in str(key) for key in errors)


def build_report(
    repo_root: Path,
    paths: RuntimePaths,
    ports: Ports,
    args: argparse.Namespace,
    stability: dict[str, object],
    upload: dict[str, object],
    download: dict[str, object],
    process_summary: dict[str, dict[str, float | int | None]],
    process_samples: dict[str, list[dict[str, float | int]]],
) -> dict[str, object]:
    analysis = analyze_report(stability, upload, download)
    return {
        "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "repo_root": str(repo_root),
        "output_dir": str(paths.output_dir),
        "ports": asdict(ports),
        "parameters": {
            "timeout_seconds": args.timeout,
            "target_mode": args.target_mode,
            "stability_duration_seconds": args.stability_duration,
            "stability_concurrency": args.stability_concurrency,
            "stability_payload_bytes": args.stability_payload_bytes,
            "transfer_concurrency": args.transfer_concurrency,
            "transfer_bytes_per_connection": args.transfer_bytes_per_connection,
            "frps_log_level": args.frps_log_level,
            "frpc_log_level": args.frpc_log_level,
        },
        "workloads": {
            "stability": stability,
            "upload": upload,
            "download": download,
        },
        "process_summary": process_summary,
        "process_samples": process_samples,
        "analysis": analysis,
    }


def analyze_report(
    stability: dict[str, object],
    upload: dict[str, object],
    download: dict[str, object],
) -> dict[str, object]:
    notes: list[str] = []
    verdict = "pass"
    notes.append(
        "Primary goal: detect proxy-chain or code-path failures under load. Throughput and latency are secondary signals."
    )

    stability_failures = int(stability["failures"])
    upload_failures = int(upload["failures"])
    download_failures = int(download["failures"])
    host_limited = (
        stability_failures > 0
        and upload_failures == 0
        and download_failures == 0
        and has_only_windows_ephemeral_port_errors(stability)
    )
    if stability_failures or upload_failures or download_failures:
        if host_limited:
            verdict = "host_limited"
            notes.append(
                "Failures only appeared as local WinError 10048 during short-connection stability churn; treat this as test-host pressure first."
            )
            notes.append(
                "Symmetric transfer workloads still completed, so this run does not yet provide direct evidence of a frps/frpc proxy-chain failure."
            )
        else:
            verdict = "attention"
            notes.append(
                "One or more workloads reported failures; prioritize raw error counters and frps/frpc logs before reading throughput numbers."
            )
    else:
        notes.append("All benchmark workloads completed without transport-level failures.")

    latency = stability["latency_ms"]
    if isinstance(latency, dict):
        p50 = latency.get("p50")
        p95 = latency.get("p95")
        if isinstance(p50, (int, float)) and isinstance(p95, (int, float)) and p50 > 0:
            if p95 > p50 * 3:
                verdict = "attention"
                notes.append("Tail latency is noticeably wider than median latency under the stability workload.")
            else:
                notes.append("Tail latency stayed close to the median during the stability workload.")

    upload_mbps = float(upload["throughput_mbps"])
    download_mbps = float(download["throughput_mbps"])
    if upload_mbps > 0 and download_mbps > 0:
        ratio = max(upload_mbps, download_mbps) / min(upload_mbps, download_mbps)
        if ratio > 1.5:
            notes.append("Symmetric upload and download transfer rates are imbalanced; inspect directional buffering and copy paths.")
        else:
            notes.append("Symmetric upload and download transfer rates stayed in a similar range.")

    if has_windows_ephemeral_port_error(stability) or has_windows_ephemeral_port_error(upload) or has_windows_ephemeral_port_error(download):
        notes.append(
            "Detected WinError 10048 in local loopback clients; this usually indicates ephemeral port or TIME_WAIT pressure on the test host."
        )

    if upload_mbps < 100 or download_mbps < 100:
        notes.append("Single-host loopback transfer rate is below 100 Mbps on at least one direction; inspect frame-copy overhead.")
    else:
        notes.append("Loopback transfer rate is above 100 Mbps in both directions.")

    return {
        "verdict": verdict,
        "notes": notes,
    }


def render_markdown_report(report: dict[str, object]) -> str:
    workloads = report["workloads"]
    stability = workloads["stability"]
    upload = workloads["upload"]
    download = workloads["download"]
    process_summary = report["process_summary"]
    analysis = report["analysis"]

    lines = [
        "# FRP TCP Pressure Report",
        "",
        f"- Generated at: `{report['generated_at']}`",
        f"- Output dir: `{report['output_dir']}`",
        f"- Target mode: `{report['parameters']['target_mode']}`",
        f"- Ports: `{json.dumps(report['ports'], ensure_ascii=True)}`",
        "",
        "## Workloads",
        "",
        (
            f"- Stability: concurrency `{stability['concurrency']}`, payload `{stability['payload_bytes']}` bytes, "
            f"duration `{stability['duration_seconds']:.2f}` s, success `{stability['success']}/{stability['attempts']}`"
        ),
        (
            f"- Symmetric upload: concurrency `{upload['concurrency']}`, payload `{upload['bytes_per_connection']}` bytes per connection, "
            f"throughput `{upload['throughput_mbps']:.2f}` Mbps, success `{upload['success']}/{upload['connections']}`"
        ),
        (
            f"- Symmetric download: concurrency `{download['concurrency']}`, payload `{download['bytes_per_connection']}` bytes per connection, "
            f"throughput `{download['throughput_mbps']:.2f}` Mbps, success `{download['success']}/{download['connections']}`"
        ),
        "",
        "## Stability",
        "",
        f"- Requests per second: `{stability['requests_per_second']:.2f}`",
        f"- Failures: `{stability['failures']}`",
        f"- Latency ms: `{json.dumps(stability['latency_ms'], ensure_ascii=True)}`",
        f"- Errors: `{json.dumps(stability['errors'], ensure_ascii=True)}`",
        "",
        "## Symmetric Transfer",
        "",
        (
            f"- Symmetric upload bytes/s: `{upload['throughput_bytes_per_second']:.2f}` "
            f"({upload['throughput_mbps']:.2f} Mbps)"
        ),
        f"- Symmetric upload per-connection seconds: `{json.dumps(upload['connection_seconds'], ensure_ascii=True)}`",
        f"- Symmetric upload errors: `{json.dumps(upload['errors'], ensure_ascii=True)}`",
        (
            f"- Symmetric download bytes/s: `{download['throughput_bytes_per_second']:.2f}` "
            f"({download['throughput_mbps']:.2f} Mbps)"
        ),
        f"- Symmetric download per-connection seconds: `{json.dumps(download['connection_seconds'], ensure_ascii=True)}`",
        f"- Symmetric download errors: `{json.dumps(download['errors'], ensure_ascii=True)}`",
        "",
        "## Process Summary",
        "",
    ]

    for name, summary in process_summary.items():
        lines.append(f"- {name}: `{json.dumps(summary, ensure_ascii=True)}`")

    lines.extend(
        [
            "",
            "## Analysis",
            "",
            f"- Verdict: `{analysis['verdict']}`",
        ]
    )
    for note in analysis["notes"]:
        lines.append(f"- {note}")

    lines.append("")
    return "\n".join(lines)


def read_process_snapshot(pid: int) -> ProcessSnapshot | None:
    if os.name == "nt":
        return read_windows_process_snapshot(pid)
    if os.name == "posix":
        return read_procfs_process_snapshot(pid)
    return None


def read_windows_process_snapshot(pid: int) -> ProcessSnapshot | None:
    script = (
        "$ErrorActionPreference='Stop'; "
        f"$p=Get-Process -Id {pid}; "
        "[pscustomobject]@{CPU=$p.CPU;WorkingSet64=$p.WorkingSet64}"
        " | ConvertTo-Json -Compress"
    )
    result = subprocess.run(
        ["powershell", "-NoProfile", "-Command", script],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0 or not result.stdout.strip():
        return None

    payload = json.loads(result.stdout)
    cpu_seconds = float(payload.get("CPU") or 0.0)
    rss_bytes = int(payload.get("WorkingSet64") or 0)
    return ProcessSnapshot(timestamp=time.time(), cpu_seconds=cpu_seconds, rss_bytes=rss_bytes)


def read_procfs_process_snapshot(pid: int) -> ProcessSnapshot | None:
    stat_path = Path(f"/proc/{pid}/stat")
    status_path = Path(f"/proc/{pid}/status")
    if not stat_path.exists():
        return None

    try:
        stat_text = stat_path.read_text(encoding="utf-8")
        status_text = status_path.read_text(encoding="utf-8")
    except OSError:
        return None

    right_paren = stat_text.rfind(")")
    if right_paren == -1:
        return None
    fields = stat_text[right_paren + 2 :].split()
    if len(fields) < 22:
        return None

    clock_ticks = os.sysconf(os.sysconf_names["SC_CLK_TCK"])
    utime = int(fields[11])
    stime = int(fields[12])
    cpu_seconds = (utime + stime) / clock_ticks

    rss_bytes = 0
    for line in status_text.splitlines():
        if line.startswith("VmRSS:"):
            parts = line.split()
            if len(parts) >= 2:
                rss_bytes = int(parts[1]) * 1024
            break

    return ProcessSnapshot(timestamp=time.time(), cpu_seconds=cpu_seconds, rss_bytes=rss_bytes)


def cleanup(processes: list[ManagedProcess], target_handle: PerfTargetHandle | None) -> None:
    if target_handle is not None:
        target_handle.server.shutdown()
        target_handle.server.server_close()
        target_handle.thread.join(timeout=5.0)

    for process in reversed(processes):
        terminate_process(process)


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


def timestamp_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f")


if __name__ == "__main__":
    sys.exit(main())
