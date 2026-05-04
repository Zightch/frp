#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import json
import math
import os
import socket
import sqlite3
import subprocess
import sys
import threading
import time
from collections import Counter
from dataclasses import asdict, dataclass
from pathlib import Path
from dataclasses import field
from socketserver import BaseRequestHandler, TCPServer, ThreadingMixIn

import e2e_tcp_perf as perf


DEFAULT_TIMEOUT_SECONDS = 45.0
DEFAULT_CONCURRENCY = 4
DEFAULT_THROUGHPUT_BYTES_PER_WORKER = 8 * 1024 * 1024
DEFAULT_TCP_THROUGHPUT_PAYLOAD_BYTES = 64 * 1024
DEFAULT_UDP_THROUGHPUT_PAYLOAD_BYTES = 1024
DEFAULT_THROUGHPUT_ITERATIONS_PER_WORKER = 128
DEFAULT_LATENCY_SAMPLES = 256
DEFAULT_LATENCY_PAYLOAD_BYTES = 32


@dataclass(frozen=True)
class Ports:
    control: int
    management: int
    tcp_remote: int
    tcp_echo: int
    udp_remote: int
    udp_echo: int


@dataclass(frozen=True)
class RuntimePaths:
    output_dir: Path
    data_dir: Path
    db_path: Path
    frps_config_path: Path
    frps_log_path: Path
    frpc_log_path: Path
    report_json_path: Path
    report_md_path: Path


@dataclass(frozen=True)
class TokenMaterial:
    token_id: str
    token_hash: str
    token_value: str


@dataclass
class ManagedProcess:
    name: str
    command: list[str]
    cwd: Path
    log_path: Path
    log_handle: object
    popen: subprocess.Popen[str]


@dataclass
class TcpEchoServerHandle:
    server: "ThreadedEchoServer"
    thread: threading.Thread


@dataclass
class UdpEchoServerHandle:
    sock: socket.socket
    thread: threading.Thread
    stop_event: threading.Event
    lock: threading.Lock = field(default_factory=threading.Lock)
    error: str | None = None


class ThreadedEchoServer(ThreadingMixIn, TCPServer):
    allow_reuse_address = True
    daemon_threads = True
    request_queue_size = 1024


class EchoRequestHandler(BaseRequestHandler):
    def handle(self) -> None:
        self.request.settimeout(60.0)
        while True:
            try:
                chunk = self.request.recv(64 * 1024)
            except socket.timeout:
                continue
            if not chunk:
                return
            self.request.sendall(chunk)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run TCP/UDP direct vs proxy throughput and latency benchmarks.")
    parser.add_argument("--output-dir", help="Directory used for logs and reports.")
    parser.add_argument("--frps-bin", help="Existing frps binary path. If omitted, build into output dir.")
    parser.add_argument("--frpc-bin", help="Existing frpc binary path. If omitted, build into output dir.")
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_SECONDS,
        help=f"Timeout in seconds for readiness checks and socket operations. Default: {DEFAULT_TIMEOUT_SECONDS}.",
    )
    parser.add_argument(
        "--concurrency",
        type=int,
        default=DEFAULT_CONCURRENCY,
        help=f"Concurrent workers per throughput benchmark. Default: {DEFAULT_CONCURRENCY}.",
    )
    parser.add_argument(
        "--throughput-bytes-per-worker",
        type=int,
        default=DEFAULT_THROUGHPUT_BYTES_PER_WORKER,
        help=f"Application payload bytes each worker must complete. Default: {DEFAULT_THROUGHPUT_BYTES_PER_WORKER}.",
    )
    parser.add_argument(
        "--throughput-iterations-per-worker",
        type=int,
        default=DEFAULT_THROUGHPUT_ITERATIONS_PER_WORKER,
        help=f"Round trips per worker for throughput runs. Default: {DEFAULT_THROUGHPUT_ITERATIONS_PER_WORKER}.",
    )
    parser.add_argument(
        "--latency-samples",
        type=int,
        default=DEFAULT_LATENCY_SAMPLES,
        help=f"Round-trip samples for latency runs. Default: {DEFAULT_LATENCY_SAMPLES}.",
    )
    parser.add_argument(
        "--latency-payload-bytes",
        type=int,
        default=DEFAULT_LATENCY_PAYLOAD_BYTES,
        help=f"Payload bytes used for latency runs. Default: {DEFAULT_LATENCY_PAYLOAD_BYTES}.",
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
        help="frpc log level passed to the client process.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    validate_args(args)

    repo_root = Path(__file__).resolve().parent.parent
    output_dir = resolve_output_dir(args, repo_root)
    output_dir.mkdir(parents=True, exist_ok=True)

    paths = RuntimePaths(
        output_dir=output_dir,
        data_dir=output_dir / "data",
        db_path=output_dir / "data" / "frps.sqlite",
        frps_config_path=output_dir / "data" / "config.json",
        frps_log_path=output_dir / "frps.log",
        frpc_log_path=output_dir / "frpc.log",
        report_json_path=output_dir / "report.json",
        report_md_path=output_dir / "report.md",
    )

    processes: list[ManagedProcess] = []
    tcp_server: TcpEchoServerHandle | None = None
    udp_server: UdpEchoServerHandle | None = None
    proxy_ports: Ports | None = None
    token: TokenMaterial | None = None

    try:
        print("[stage] build or resolve binaries")
        binaries = build_or_resolve_binaries(args, repo_root, output_dir)

        print("[stage] allocate ports")
        proxy_ports = allocate_ports()
        token = build_token_material()

        print("[stage] create sqlite and frps config")
        perf.init_sqlite_db(paths.db_path)
        perf.write_frps_config(paths.frps_config_path, paths.db_path, perf_ports(proxy_ports), args.frps_log_level)

        print("[stage] start direct echo servers")
        tcp_server = start_tcp_echo_server("127.0.0.1", proxy_ports.tcp_echo)
        udp_server = start_udp_echo_server("127.0.0.1", proxy_ports.udp_echo)

        direct_results = run_direct_benchmarks(proxy_ports, args)

        print("[stage] start frps")
        frps_process = perf.start_process(
            name="frps",
            command=[str(binaries["frps"])],
            cwd=paths.output_dir,
            log_path=paths.frps_log_path,
        )
        processes.append(frps_process)

        print("[stage] wait for frps readiness")
        perf.wait_http_ready(
            url=f"http://127.0.0.1:{proxy_ports.management}/readyz",
            timeout_seconds=args.timeout,
            processes=processes,
        )
        perf.wait_log_contains(paths.frps_log_path, "frpc control listener ready", args.timeout, processes)
        perf.wait_sqlite_schema(paths.db_path, min(args.timeout, 15.0))
        seed_runtime_data(paths.db_path, token, proxy_ports)

        print("[stage] start frpc")
        frpc_process = perf.start_process(
            name="frpc",
            command=[
                str(binaries["frpc"]),
                "--server",
                f"127.0.0.1:{proxy_ports.control}",
                "--key",
                token.token_value,
            ],
            cwd=paths.output_dir,
            log_path=paths.frpc_log_path,
            env={"FRPC_LOG_LEVEL": args.frpc_log_level},
        )
        processes.append(frpc_process)

        print("[stage] wait for proxy listeners")
        perf.wait_log_contains(paths.frps_log_path, "frpc control login succeeded", args.timeout, processes)
        perf.wait_log_contains(paths.frps_log_path, "config acknowledged", args.timeout, processes)
        perf.wait_log_contains(paths.frps_log_path, "tcp tunnel listener ready", args.timeout, processes)
        perf.wait_log_contains(paths.frps_log_path, "udp tunnel listener ready", args.timeout, processes)
        perf.wait_log_contains(paths.frpc_log_path, "登录成功", args.timeout, processes)
        perf.wait_log_contains(paths.frpc_log_path, "领取配置", args.timeout, processes)

        proxy_results = run_proxy_benchmarks(proxy_ports, args)
        report = build_report(paths, args, proxy_ports, direct_results, proxy_results)
        paths.report_json_path.write_text(json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")
        paths.report_md_path.write_text(render_markdown_report(report), encoding="utf-8")

        print("[ok] transport benchmark completed")
        print(f"[info] report_json={paths.report_json_path}")
        print(f"[info] report_md={paths.report_md_path}")
        for protocol in ("tcp", "udp"):
            direct = report["results"]["direct"][protocol]
            proxy = report["results"]["proxy"][protocol]
            print(
                f"[info] {protocol} "
                f"direct_throughput_mbps={direct['throughput']['throughput_mbps']:.2f} "
                f"proxy_throughput_mbps={proxy['throughput']['throughput_mbps']:.2f} "
                f"direct_p50_ms={direct['latency']['latency_ms']['p50']:.3f} "
                f"proxy_p50_ms={proxy['latency']['latency_ms']['p50']:.3f}"
            )
        return 0
    except Exception as exc:
        print(f"[FAIL] {exc}", file=sys.stderr)
        dump_process_logs(processes)
        return 1
    finally:
        if udp_server is not None:
            stop_udp_echo_server(udp_server)
        if tcp_server is not None:
            stop_tcp_echo_server(tcp_server)
        perf.cleanup(processes, None)


def validate_args(args: argparse.Namespace) -> None:
    if args.timeout <= 0:
        raise ValueError("--timeout must be positive")
    if args.concurrency <= 0:
        raise ValueError("--concurrency must be positive")
    if args.throughput_bytes_per_worker <= 0:
        raise ValueError("--throughput-bytes-per-worker must be positive")
    if args.throughput_iterations_per_worker <= 0:
        raise ValueError("--throughput-iterations-per-worker must be positive")
    if args.latency_samples <= 0:
        raise ValueError("--latency-samples must be positive")
    if args.latency_payload_bytes <= 0:
        raise ValueError("--latency-payload-bytes must be positive")


def resolve_output_dir(args: argparse.Namespace, repo_root: Path) -> Path:
    if args.output_dir:
        return Path(args.output_dir).resolve()
    timestamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S-%f")
    return (repo_root / "test" / "tmp" / f"transport-perf-{timestamp}").resolve()


def allocate_ports() -> Ports:
    tcp_ports = perf.allocate_ports()
    udp_ports = reserve_ports(socket.SOCK_DGRAM, 2)
    return Ports(
        control=tcp_ports.control,
        management=tcp_ports.management,
        tcp_remote=tcp_ports.remote,
        tcp_echo=tcp_ports.target,
        udp_remote=udp_ports[0],
        udp_echo=udp_ports[1],
    )


def perf_ports(ports: Ports) -> perf.Ports:
    return perf.Ports(
        control=ports.control,
        management=ports.management,
        remote=ports.tcp_remote,
        target=ports.tcp_echo,
    )


def reserve_ports(sock_type: int, count: int) -> list[int]:
    reserved: list[socket.socket] = []
    numbers: list[int] = []
    try:
        for _ in range(count):
            probe = socket.socket(socket.AF_INET, sock_type)
            probe.bind(("127.0.0.1", 0))
            if sock_type == socket.SOCK_STREAM:
                probe.listen(1)
            reserved.append(probe)
            numbers.append(probe.getsockname()[1])
    finally:
        for probe in reserved:
            probe.close()
    return numbers


def build_or_resolve_binaries(args: argparse.Namespace, repo_root: Path, output_dir: Path) -> dict[str, Path]:
    suffix = ".exe" if os.name == "nt" else ""
    binaries: dict[str, Path] = {}
    if args.frps_bin:
        binaries["frps"] = Path(args.frps_bin).resolve()
    else:
        binaries["frps"] = output_dir / f"frps{suffix}"
        perf.build_go_binary(repo_root / "frps", "./cmd/frps", binaries["frps"])
    if args.frpc_bin:
        binaries["frpc"] = Path(args.frpc_bin).resolve()
    else:
        binaries["frpc"] = output_dir / f"frpc{suffix}"
        perf.build_go_binary(repo_root / "frpc", "./cmd/frpc", binaries["frpc"])
    for name, path in binaries.items():
        if not path.exists():
            raise RuntimeError(f"{name} binary does not exist: {path}")
    return binaries


def build_token_material() -> TokenMaterial:
    token = perf.build_token_material()
    return TokenMaterial(token_id=token.token_id, token_hash=token.token_hash, token_value=token.token_value)


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
                "transport-perf-group",
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

        insert_tunnel(
            conn,
            group_id=int(group_id),
            name="transport-tcp",
            protocol="tcp",
            remote_port=ports.tcp_remote,
            local_port=ports.tcp_echo,
            created_at=created_at,
        )
        insert_tunnel(
            conn,
            group_id=int(group_id),
            name="transport-udp",
            protocol="udp",
            remote_port=ports.udp_remote,
            local_port=ports.udp_echo,
            created_at=created_at,
        )
        conn.commit()


def insert_tunnel(
    conn: sqlite3.Connection,
    *,
    group_id: int,
    name: str,
    protocol: str,
    remote_port: int,
    local_port: int,
    created_at: str,
) -> None:
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
            group_id,
            name,
            protocol,
            "single",
            remote_port,
            remote_port,
            "127.0.0.1",
            local_port,
            local_port,
            1,
            created_at,
            created_at,
        ),
    )


def start_tcp_echo_server(host: str, port: int) -> TcpEchoServerHandle:
    server = ThreadedEchoServer((host, port), EchoRequestHandler)
    thread = threading.Thread(target=server.serve_forever, name="tcp-echo-server", daemon=True)
    thread.start()
    return TcpEchoServerHandle(server=server, thread=thread)


def stop_tcp_echo_server(handle: TcpEchoServerHandle) -> None:
    handle.server.shutdown()
    handle.server.server_close()
    handle.thread.join(timeout=5.0)


def start_udp_echo_server(host: str, port: int) -> UdpEchoServerHandle:
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind((host, port))
    sock.settimeout(0.25)
    stop_event = threading.Event()
    handle = UdpEchoServerHandle(sock=sock, thread=threading.Thread(), stop_event=stop_event)

    def serve() -> None:
        while not stop_event.is_set():
            try:
                payload, addr = sock.recvfrom(65535)
            except socket.timeout:
                continue
            except OSError as exc:
                if stop_event.is_set():
                    return
                with handle.lock:
                    handle.error = str(exc)
                return
            try:
                sock.sendto(payload, addr)
            except OSError as exc:
                with handle.lock:
                    handle.error = str(exc)
                return

    handle.thread = threading.Thread(target=serve, name="udp-echo-server", daemon=True)
    handle.thread.start()
    return handle


def stop_udp_echo_server(handle: UdpEchoServerHandle) -> None:
    handle.stop_event.set()
    handle.sock.close()
    handle.thread.join(timeout=5.0)


def run_direct_benchmarks(ports: Ports, args: argparse.Namespace) -> dict[str, dict[str, object]]:
    direct = {
        "tcp": run_protocol_benchmarks(
            protocol="tcp",
            host="127.0.0.1",
            port=ports.tcp_echo,
            args=args,
        ),
        "udp": run_protocol_benchmarks(
            protocol="udp",
            host="127.0.0.1",
            port=ports.udp_echo,
            args=args,
        ),
    }
    return direct


def run_proxy_benchmarks(ports: Ports, args: argparse.Namespace) -> dict[str, dict[str, object]]:
    proxy = {
        "tcp": run_protocol_benchmarks(
            protocol="tcp",
            host="127.0.0.1",
            port=ports.tcp_remote,
            args=args,
        ),
        "udp": run_protocol_benchmarks(
            protocol="udp",
            host="127.0.0.1",
            port=ports.udp_remote,
            args=args,
        ),
    }
    return proxy


def run_protocol_benchmarks(protocol: str, host: str, port: int, args: argparse.Namespace) -> dict[str, object]:
    if protocol == "tcp":
        throughput_payload_bytes = DEFAULT_TCP_THROUGHPUT_PAYLOAD_BYTES
        throughput_iterations = args.throughput_bytes_per_worker // throughput_payload_bytes
        throughput_iterations = max(throughput_iterations, 1)
    elif protocol == "udp":
        throughput_payload_bytes = DEFAULT_UDP_THROUGHPUT_PAYLOAD_BYTES
        throughput_iterations = args.throughput_bytes_per_worker // throughput_payload_bytes
        throughput_iterations = max(throughput_iterations, 1)
    else:
        raise ValueError(f"unsupported protocol: {protocol}")

    throughput = run_throughput_benchmark(
        protocol=protocol,
        host=host,
        port=port,
        concurrency=args.concurrency,
        payload_bytes=throughput_payload_bytes,
        iterations_per_worker=throughput_iterations,
        timeout_seconds=args.timeout,
    )
    latency = run_latency_benchmark(
        protocol=protocol,
        host=host,
        port=port,
        samples=args.latency_samples,
        payload_bytes=args.latency_payload_bytes,
        timeout_seconds=args.timeout,
    )
    return {"throughput": throughput, "latency": latency}


def run_throughput_benchmark(
    *,
    protocol: str,
    host: str,
    port: int,
    concurrency: int,
    payload_bytes: int,
    iterations_per_worker: int,
    timeout_seconds: float,
) -> dict[str, object]:
    barrier = threading.Barrier(concurrency + 1)
    lock = threading.Lock()
    successes = 0
    errors = Counter()
    worker_seconds: list[float] = []
    payload = build_payload(payload_bytes)

    def worker() -> None:
        nonlocal successes
        try:
            barrier.wait()
            started_at = time.perf_counter()
            with open_client(protocol, host, port, timeout_seconds) as conn:
                for _ in range(iterations_per_worker):
                    round_trip(protocol, conn, payload)
            elapsed = time.perf_counter() - started_at
            with lock:
                successes += 1
                worker_seconds.append(elapsed)
        except Exception as exc:
            with lock:
                errors[normalize_error(exc)] += 1

    threads = [threading.Thread(target=worker, name=f"{protocol}-throughput-{index}", daemon=True) for index in range(concurrency)]
    for thread in threads:
        thread.start()

    started_at = time.perf_counter()
    barrier.wait()
    for thread in threads:
        thread.join()
    wall_seconds = time.perf_counter() - started_at

    if successes != concurrency:
        raise RuntimeError(f"{protocol} throughput benchmark failed: {dict(errors.most_common(10))}")

    total_round_trips = successes * iterations_per_worker
    total_payload_bytes = total_round_trips * payload_bytes
    throughput_bps = total_payload_bytes / wall_seconds if wall_seconds > 0 else 0.0
    return {
        "protocol": protocol,
        "payload_bytes": payload_bytes,
        "concurrency": concurrency,
        "iterations_per_worker": iterations_per_worker,
        "total_round_trips": total_round_trips,
        "total_payload_bytes": total_payload_bytes,
        "wall_seconds": wall_seconds,
        "throughput_bytes_per_second": throughput_bps,
        "throughput_mbps": throughput_bps * 8 / 1_000_000,
        "worker_seconds": summarize_seconds(worker_seconds),
    }


def run_latency_benchmark(
    *,
    protocol: str,
    host: str,
    port: int,
    samples: int,
    payload_bytes: int,
    timeout_seconds: float,
) -> dict[str, object]:
    payload = build_payload(payload_bytes)
    durations: list[float] = []
    started_at = time.perf_counter()
    with open_client(protocol, host, port, timeout_seconds) as conn:
        for _ in range(samples):
            sample_started_at = time.perf_counter()
            round_trip(protocol, conn, payload)
            durations.append((time.perf_counter() - sample_started_at) * 1000.0)
    elapsed_seconds = time.perf_counter() - started_at
    return {
        "protocol": protocol,
        "samples": samples,
        "payload_bytes": payload_bytes,
        "elapsed_seconds": elapsed_seconds,
        "latency_ms": summarize_latencies(durations),
    }


def open_client(protocol: str, host: str, port: int, timeout_seconds: float) -> socket.socket:
    if protocol == "tcp":
        conn = socket.create_connection((host, port), timeout=timeout_seconds)
        conn.settimeout(timeout_seconds)
        return conn
    if protocol == "udp":
        conn = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        conn.settimeout(timeout_seconds)
        conn.connect((host, port))
        return conn
    raise ValueError(f"unsupported protocol: {protocol}")


def round_trip(protocol: str, conn: socket.socket, payload: bytes) -> None:
    if protocol == "tcp":
        conn.sendall(payload)
        response = read_exact(conn, len(payload))
        if response != payload:
            raise RuntimeError("tcp response mismatch")
        return
    if protocol == "udp":
        conn.send(payload)
        response = conn.recv(65535)
        if response != payload:
            raise RuntimeError(f"udp response mismatch: got {len(response)} bytes want {len(payload)}")
        return
    raise ValueError(f"unsupported protocol: {protocol}")


def read_exact(conn: socket.socket, byte_count: int) -> bytes:
    buffer = bytearray()
    while len(buffer) < byte_count:
        chunk = conn.recv(min(64 * 1024, byte_count - len(buffer)))
        if not chunk:
            raise RuntimeError(f"connection closed early after {len(buffer)} of {byte_count} bytes")
        buffer.extend(chunk)
    return bytes(buffer)


def build_payload(byte_count: int) -> bytes:
    seed = b"frp-transport-perf"
    payload = bytearray()
    while len(payload) < byte_count:
        payload.extend(seed)
    return bytes(payload[:byte_count])


def summarize_latencies(latencies_ms: list[float]) -> dict[str, float | None]:
    if not latencies_ms:
        return {"min": None, "avg": None, "p50": None, "p95": None, "p99": None, "max": None}

    sorted_values = sorted(latencies_ms)
    return {
        "min": sorted_values[0],
        "avg": sum(sorted_values) / len(sorted_values),
        "p50": percentile(sorted_values, 50),
        "p95": percentile(sorted_values, 95),
        "p99": percentile(sorted_values, 99),
        "max": sorted_values[-1],
    }


def summarize_seconds(values: list[float]) -> dict[str, float | None]:
    if not values:
        return {"min": None, "avg": None, "p50": None, "p95": None, "max": None}

    sorted_values = sorted(values)
    return {
        "min": sorted_values[0],
        "avg": sum(sorted_values) / len(sorted_values),
        "p50": percentile(sorted_values, 50),
        "p95": percentile(sorted_values, 95),
        "max": sorted_values[-1],
    }


def percentile(sorted_values: list[float], rank: float) -> float:
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


def normalize_error(exc: Exception) -> str:
    return f"{type(exc).__name__}: {str(exc).strip()}"[:200]


def build_report(
    paths: RuntimePaths,
    args: argparse.Namespace,
    ports: Ports,
    direct_results: dict[str, dict[str, object]],
    proxy_results: dict[str, dict[str, object]],
) -> dict[str, object]:
    return {
        "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "output_dir": str(paths.output_dir),
        "ports": asdict(ports),
        "parameters": {
            "concurrency": args.concurrency,
            "throughput_bytes_per_worker": args.throughput_bytes_per_worker,
            "throughput_iterations_per_worker": args.throughput_iterations_per_worker,
            "latency_samples": args.latency_samples,
            "latency_payload_bytes": args.latency_payload_bytes,
            "timeout_seconds": args.timeout,
        },
        "results": {
            "direct": direct_results,
            "proxy": proxy_results,
        },
        "analysis": analyze_results(direct_results, proxy_results),
    }


def analyze_results(direct_results: dict[str, dict[str, object]], proxy_results: dict[str, dict[str, object]]) -> dict[str, object]:
    notes: list[str] = []
    for protocol in ("tcp", "udp"):
        direct_throughput = float(direct_results[protocol]["throughput"]["throughput_mbps"])
        proxy_throughput = float(proxy_results[protocol]["throughput"]["throughput_mbps"])
        direct_p50 = float(direct_results[protocol]["latency"]["latency_ms"]["p50"])
        proxy_p50 = float(proxy_results[protocol]["latency"]["latency_ms"]["p50"])
        throughput_loss = loss_pct(direct_throughput, proxy_throughput)
        latency_overhead = overhead_pct(direct_p50, proxy_p50)
        notes.append(
            f"{protocol.upper()} throughput loss={format_pct(throughput_loss)} latency_overhead={format_pct(latency_overhead)}"
        )
    return {"notes": notes}


def loss_pct(direct_value: float, proxy_value: float) -> float | None:
    if direct_value <= 0:
        return None
    return (1.0 - (proxy_value / direct_value)) * 100.0


def overhead_pct(direct_value: float, proxy_value: float) -> float | None:
    if direct_value <= 0:
        return None
    return ((proxy_value / direct_value) - 1.0) * 100.0


def format_pct(value: float | None) -> str:
    if value is None:
        return "n/a"
    return f"{value:.2f}%"


def render_markdown_report(report: dict[str, object]) -> str:
    lines = [
        "# Transport Perf Report",
        "",
        f"- Generated at: `{report['generated_at']}`",
        f"- Output dir: `{report['output_dir']}`",
        "",
        "## Results",
        "",
        "| Protocol | Mode | Throughput Mbps | Latency p50 ms | Latency p95 ms | Latency p99 ms |",
        "| --- | --- | ---: | ---: | ---: | ---: |",
    ]
    for protocol in ("tcp", "udp"):
        for mode in ("direct", "proxy"):
            result = report["results"][mode][protocol]
            lines.append(
                "| "
                f"{protocol.upper()} | {mode} | "
                f"{result['throughput']['throughput_mbps']:.2f} | "
                f"{result['latency']['latency_ms']['p50']:.3f} | "
                f"{result['latency']['latency_ms']['p95']:.3f} | "
                f"{result['latency']['latency_ms']['p99']:.3f} |"
            )

    lines.extend(["", "## Analysis", ""])
    for note in report["analysis"]["notes"]:
        lines.append(f"- {note}")
    lines.append("")
    return "\n".join(lines)


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
