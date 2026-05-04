#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import json
import subprocess
import sys
from dataclasses import asdict, dataclass
from pathlib import Path

import e2e_tcp_perf as perf


DEFAULT_TRANSFER_CONCURRENCY = 4
DEFAULT_BYTES_PER_CONNECTION = 32 * 1024 * 1024
DEFAULT_TIMEOUT_SECONDS = 60.0


@dataclass(frozen=True)
class Scenario:
    name: str
    description: str
    pattern: perf.TransferPattern


DEFAULT_SCENARIOS = [
    Scenario(
        name="tiny_continuous",
        description="大流量 + 256B 连续发送",
        pattern=perf.TransferPattern(write_chunk_bytes=256, burst_chunks=0, burst_pause_micros=0),
    ),
    Scenario(
        name="small_continuous",
        description="大流量 + 1KiB 连续发送",
        pattern=perf.TransferPattern(write_chunk_bytes=1024, burst_chunks=0, burst_pause_micros=0),
    ),
    Scenario(
        name="page_continuous",
        description="大流量 + 4KiB 连续发送",
        pattern=perf.TransferPattern(write_chunk_bytes=4096, burst_chunks=0, burst_pause_micros=0),
    ),
    Scenario(
        name="large_continuous",
        description="大流量 + 64KiB 连续发送",
        pattern=perf.TransferPattern(write_chunk_bytes=64 * 1024, burst_chunks=0, burst_pause_micros=0),
    ),
    Scenario(
        name="small_fragmented",
        description="碎片化 + 1KiB 发送 32 次后暂停 500us",
        pattern=perf.TransferPattern(write_chunk_bytes=1024, burst_chunks=32, burst_pause_micros=500),
    ),
    Scenario(
        name="large_fragmented",
        description="碎片化 + 64KiB 每次发送后暂停 200us",
        pattern=perf.TransferPattern(write_chunk_bytes=64 * 1024, burst_chunks=1, burst_pause_micros=200),
    ),
]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run direct-vs-proxy TCP send-pattern throughput matrix against local frps/frpc and perftarget.",
    )
    parser.add_argument(
        "--output-dir",
        help="Directory used for logs and reports. Default: test/tmp/perf-pattern-matrix-<timestamp>.",
    )
    parser.add_argument(
        "--transfer-concurrency",
        type=int,
        default=DEFAULT_TRANSFER_CONCURRENCY,
        help=f"Concurrent transfer connections per scenario. Default: {DEFAULT_TRANSFER_CONCURRENCY}.",
    )
    parser.add_argument(
        "--bytes-per-connection",
        type=int,
        default=DEFAULT_BYTES_PER_CONNECTION,
        help=f"Bytes transferred per connection. Default: {DEFAULT_BYTES_PER_CONNECTION}.",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_SECONDS,
        help=f"Socket and process readiness timeout in seconds. Default: {DEFAULT_TIMEOUT_SECONDS}.",
    )
    parser.add_argument(
        "--scenario",
        action="append",
        choices=[scenario.name for scenario in DEFAULT_SCENARIOS],
        help="Optional scenario filter. Repeat to run multiple named scenarios.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    validate_args(args)

    repo_root = Path(__file__).resolve().parent.parent
    output_dir = resolve_output_dir(args, repo_root)
    output_dir.mkdir(parents=True, exist_ok=True)

    scenarios = select_scenarios(args)
    binaries = build_binaries(repo_root, output_dir)

    print(f"[stage] build complete output_dir={output_dir}")
    direct_results = run_direct_baseline(repo_root, output_dir, binaries["perf-target"], scenarios, args)
    proxy_results = run_proxy_matrix(repo_root, output_dir, binaries, scenarios, args)
    report = build_report(output_dir, args, scenarios, direct_results, proxy_results)

    report_json_path = output_dir / "report.json"
    report_md_path = output_dir / "report.md"
    report_json_path.write_text(json.dumps(report, indent=2, ensure_ascii=False), encoding="utf-8")
    report_md_path.write_text(render_markdown_report(report), encoding="utf-8")

    print("[ok] tcp perf pattern matrix completed")
    print(f"[info] report_json={report_json_path}")
    print(f"[info] report_md={report_md_path}")
    for row in report["scenarios"]:
        print(
            "[info] "
            f"{row['name']} "
            f"direct_up={row['direct']['upload_mbps']:.2f} "
            f"proxy_up={row['proxy']['upload_mbps']:.2f} "
            f"direct_down={row['direct']['download_mbps']:.2f} "
            f"proxy_down={row['proxy']['download_mbps']:.2f}"
        )
    return 0


def validate_args(args: argparse.Namespace) -> None:
    if args.transfer_concurrency <= 0:
        raise ValueError("--transfer-concurrency must be positive")
    if args.bytes_per_connection <= 0:
        raise ValueError("--bytes-per-connection must be positive")
    if args.timeout <= 0:
        raise ValueError("--timeout must be positive")


def resolve_output_dir(args: argparse.Namespace, repo_root: Path) -> Path:
    if args.output_dir:
        return Path(args.output_dir).resolve()
    timestamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S-%f")
    return (repo_root / "test" / "tmp" / f"perf-pattern-matrix-{timestamp}").resolve()


def select_scenarios(args: argparse.Namespace) -> list[Scenario]:
    if not args.scenario:
        return list(DEFAULT_SCENARIOS)
    selected = set(args.scenario)
    return [scenario for scenario in DEFAULT_SCENARIOS if scenario.name in selected]


def build_binaries(repo_root: Path, output_dir: Path) -> dict[str, Path]:
    suffix = ".exe" if sys.platform == "win32" else ""
    binaries = {
        "frps": output_dir / f"frps{suffix}",
        "frpc": output_dir / f"frpc{suffix}",
        "perf-target": output_dir / f"perf-target{suffix}",
    }
    perf.build_go_binary(repo_root / "frps", "./cmd/frps", binaries["frps"])
    perf.build_go_binary(repo_root / "frpc", "./cmd/frpc", binaries["frpc"])
    perf.build_go_binary(repo_root / "frps", "./cmd/perftarget", binaries["perf-target"])
    return binaries


def run_direct_baseline(
    repo_root: Path,
    output_dir: Path,
    perf_target_bin: Path,
    scenarios: list[Scenario],
    args: argparse.Namespace,
) -> dict[str, dict[str, float]]:
    ports = perf.allocate_ports()
    log_path = output_dir / "direct-target.log"
    process = perf.start_process(
        name="perf-target-direct",
        command=[str(perf_target_bin), "--listen", f"127.0.0.1:{ports.target}"],
        cwd=repo_root / "frps",
        log_path=log_path,
    )
    processes = [process]
    try:
        perf.wait_tcp_ready("127.0.0.1", ports.target, args.timeout, processes)
        results: dict[str, dict[str, float]] = {}
        for scenario in scenarios:
            print(f"[stage] direct scenario={scenario.name}")
            upload = perf.run_transfer_workload(
                workload_name="upload_symmetric",
                host="127.0.0.1",
                port=ports.target,
                concurrency=args.transfer_concurrency,
                bytes_per_connection=args.bytes_per_connection,
                transfer_pattern=scenario.pattern,
                timeout_seconds=args.timeout,
                processes=processes,
            )
            download = perf.run_transfer_workload(
                workload_name="download_symmetric",
                host="127.0.0.1",
                port=ports.target,
                concurrency=args.transfer_concurrency,
                bytes_per_connection=args.bytes_per_connection,
                transfer_pattern=scenario.pattern,
                timeout_seconds=args.timeout,
                processes=processes,
            )
            results[scenario.name] = {
                "upload_mbps": float(upload["throughput_mbps"]),
                "download_mbps": float(download["throughput_mbps"]),
            }
        return results
    finally:
        perf.cleanup(processes, None)


def run_proxy_matrix(
    repo_root: Path,
    output_dir: Path,
    binaries: dict[str, Path],
    scenarios: list[Scenario],
    args: argparse.Namespace,
) -> dict[str, dict[str, float]]:
    results: dict[str, dict[str, float]] = {}
    for scenario in scenarios:
        print(f"[stage] proxy scenario={scenario.name}")
        scenario_output_dir = output_dir / "proxy-runs" / scenario.name
        command = [
            sys.executable,
            "test/e2e_tcp_perf.py",
            "--target-mode",
            "go",
            "--skip-stability",
            "--timeout",
            str(args.timeout),
            "--transfer-concurrency",
            str(args.transfer_concurrency),
            "--transfer-bytes-per-connection",
            str(args.bytes_per_connection),
            "--transfer-write-chunk-bytes",
            str(scenario.pattern.write_chunk_bytes),
            "--transfer-write-burst-chunks",
            str(scenario.pattern.burst_chunks),
            "--transfer-write-burst-pause-micros",
            str(scenario.pattern.burst_pause_micros),
            "--frps-bin",
            str(binaries["frps"]),
            "--frpc-bin",
            str(binaries["frpc"]),
            "--perf-target-bin",
            str(binaries["perf-target"]),
            "--output-dir",
            str(scenario_output_dir),
        ]
        result = subprocess.run(
            command,
            cwd=str(repo_root),
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            raise RuntimeError(
                f"proxy scenario failed: {scenario.name}\nstdout:\n{result.stdout}\nstderr:\n{result.stderr}"
            )
        report = json.loads((scenario_output_dir / "report.json").read_text(encoding="utf-8"))
        results[scenario.name] = {
            "upload_mbps": float(report["workloads"]["upload"]["throughput_mbps"]),
            "download_mbps": float(report["workloads"]["download"]["throughput_mbps"]),
        }
    return results


def build_report(
    output_dir: Path,
    args: argparse.Namespace,
    scenarios: list[Scenario],
    direct_results: dict[str, dict[str, float]],
    proxy_results: dict[str, dict[str, float]],
) -> dict[str, object]:
    rows = []
    for scenario in scenarios:
        direct = direct_results[scenario.name]
        proxy = proxy_results[scenario.name]
        rows.append(
            {
                "name": scenario.name,
                "description": scenario.description,
                "pattern": asdict(scenario.pattern),
                "direct": direct,
                "proxy": proxy,
                "loss": {
                    "upload_pct": calculate_loss_pct(direct["upload_mbps"], proxy["upload_mbps"]),
                    "download_pct": calculate_loss_pct(direct["download_mbps"], proxy["download_mbps"]),
                },
            }
        )

    return {
        "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "output_dir": str(output_dir),
        "parameters": {
            "transfer_concurrency": args.transfer_concurrency,
            "bytes_per_connection": args.bytes_per_connection,
            "timeout_seconds": args.timeout,
        },
        "scenarios": rows,
        "analysis": analyze_rows(rows),
    }


def calculate_loss_pct(direct_mbps: float, proxy_mbps: float) -> float | None:
    if direct_mbps <= 0:
        return None
    return (1.0 - (proxy_mbps / direct_mbps)) * 100.0


def analyze_rows(rows: list[dict[str, object]]) -> dict[str, object]:
    if not rows:
        return {"notes": ["No scenarios were selected."]}

    worst_upload = max(rows, key=lambda row: metric_or_negative_infinity(row["loss"]["upload_pct"]))  # type: ignore[index]
    worst_download = max(rows, key=lambda row: metric_or_negative_infinity(row["loss"]["download_pct"]))  # type: ignore[index]

    notes = [
        (
            "Worst upload loss scenario: "
            f"{worst_upload['name']} ({format_pct(worst_upload['loss']['upload_pct'])})"
        ),
        (
            "Worst download loss scenario: "
            f"{worst_download['name']} ({format_pct(worst_download['loss']['download_pct'])})"
        ),
    ]

    tiny = next((row for row in rows if row["name"] == "tiny_continuous"), None)
    large = next((row for row in rows if row["name"] == "large_continuous"), None)
    if tiny is not None and large is not None:
        tiny_loss = tiny["loss"]["upload_pct"]  # type: ignore[index]
        large_loss = large["loss"]["upload_pct"]  # type: ignore[index]
        if isinstance(tiny_loss, (int, float)) and isinstance(large_loss, (int, float)):
            if tiny_loss > large_loss + 10.0:
                notes.append("Upload loss rises materially on tiny writes; small user-space send granularity is a primary pressure point.")
            else:
                notes.append("Upload loss stays in a similar range from tiny to large writes; proxy overhead is not sharply size-sensitive in this matrix.")

    fragmented = next((row for row in rows if row["name"] == "large_fragmented"), None)
    if fragmented is not None:
        down_loss = fragmented["loss"]["download_pct"]  # type: ignore[index]
        if isinstance(down_loss, (int, float)) and down_loss > 15.0:
            notes.append("Fragmented large writes notably hurt download throughput; burst pacing amplifies scheduling and wake-up overhead.")
        else:
            notes.append("Fragmented large writes stay near direct baseline; the proxy chain tolerates bursty large sends in this matrix.")

    return {"notes": notes}


def format_pct(value: float | None) -> str:
    if value is None:
        return "n/a"
    return f"{value:.2f}%"


def metric_or_negative_infinity(value: object) -> float:
    if isinstance(value, (int, float)):
        return float(value)
    return float("-inf")


def render_markdown_report(report: dict[str, object]) -> str:
    lines = [
        "# TCP Send Pattern Matrix Report",
        "",
        f"- Generated at: `{report['generated_at']}`",
        f"- Output dir: `{report['output_dir']}`",
        (
            f"- Parameters: `concurrency={report['parameters']['transfer_concurrency']} "
            f"bytes_per_connection={report['parameters']['bytes_per_connection']} "
            f"timeout_seconds={report['parameters']['timeout_seconds']}`"
        ),
        "",
        "## Results",
        "",
        "| Scenario | Pattern | Direct Up Mbps | Proxy Up Mbps | Up Loss | Direct Down Mbps | Proxy Down Mbps | Down Loss |",
        "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]

    for row in report["scenarios"]:
        pattern = row["pattern"]
        lines.append(
            "| "
            f"{row['name']} | "
            f"chunk={pattern['write_chunk_bytes']}, burst={pattern['burst_chunks']}, pause_us={pattern['burst_pause_micros']} | "
            f"{row['direct']['upload_mbps']:.2f} | "
            f"{row['proxy']['upload_mbps']:.2f} | "
            f"{format_pct(row['loss']['upload_pct'])} | "
            f"{row['direct']['download_mbps']:.2f} | "
            f"{row['proxy']['download_mbps']:.2f} | "
            f"{format_pct(row['loss']['download_pct'])} |"
        )

    lines.extend(["", "## Analysis", ""])
    for note in report["analysis"]["notes"]:
        lines.append(f"- {note}")

    lines.append("")
    return "\n".join(lines)


if __name__ == "__main__":
    sys.exit(main())
