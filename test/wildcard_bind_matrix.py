#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import os
import platform
import socket
import sys
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class BindCase:
    name: str
    first: str
    second: str


CASES: tuple[BindCase, ...] = (
    BindCase("any4_any4", "0.0.0.0", "0.0.0.0"),
    BindCase("any6_any6", "::", "::"),
    BindCase("any4_any6", "0.0.0.0", "::"),
    BindCase("any6_any4", "::", "0.0.0.0"),
    BindCase("any4_v4loop", "0.0.0.0", "127.0.0.1"),
    BindCase("v4loop_any4", "127.0.0.1", "0.0.0.0"),
    BindCase("any6_v6loop", "::", "::1"),
    BindCase("v6loop_any6", "::1", "::"),
    BindCase("any4_v6loop", "0.0.0.0", "::1"),
    BindCase("v6loop_any4", "::1", "0.0.0.0"),
    BindCase("any6_v4loop", "::", "127.0.0.1"),
    BindCase("v4loop_any6", "127.0.0.1", "::"),
    BindCase("v4loop_v4loop", "127.0.0.1", "127.0.0.1"),
    BindCase("v6loop_v6loop", "::1", "::1"),
    BindCase("v4loop_v6loop", "127.0.0.1", "::1"),
    BindCase("v6loop_v4loop", "::1", "127.0.0.1"),
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Probe wildcard/specific bind conflicts for TCP+UDP on the current platform.",
    )
    parser.add_argument(
        "--output",
        help="Optional JSON output file. Prints to stdout when omitted.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    matrix: list[dict[str, object]] = []
    for protocol in ("tcp", "udp"):
        for case in CASES:
            matrix.append(run_case(protocol, case))

    payload: dict[str, object] = {
        "os_name": os.name,
        "platform": platform.platform(),
        "python": platform.python_version(),
        "results": matrix,
    }

    text = json.dumps(payload, ensure_ascii=False, indent=2)
    if args.output:
        path = Path(args.output).resolve()
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text + "\n", encoding="utf-8")
        print(f"[ok] wrote {path}")
    else:
        print(text)
    return 0


def run_case(protocol: str, case: BindCase) -> dict[str, object]:
    result: dict[str, object] = {
        "protocol": protocol,
        "name": case.name,
        "first": case.first,
        "second": case.second,
        "first_ok": False,
        "second_ok": False,
    }

    socket_type = socket.SOCK_STREAM if protocol == "tcp" else socket.SOCK_DGRAM
    first_sock: socket.socket | None = None
    second_sock: socket.socket | None = None

    try:
        first_sock = socket.socket(family_for_host(case.first), socket_type)
        tune_socket(first_sock, protocol)
        first_sock.bind((case.first, 0))
        if protocol == "tcp":
            first_sock.listen(1)
        first_port = first_sock.getsockname()[1]
        result["first_ok"] = True
        result["first_addr"] = format_sockname(first_sock.getsockname())
        result["port"] = first_port

        second_sock = socket.socket(family_for_host(case.second), socket_type)
        tune_socket(second_sock, protocol)
        second_sock.bind((case.second, first_port))
        if protocol == "tcp":
            second_sock.listen(1)
        result["second_ok"] = True
        result["second_addr"] = format_sockname(second_sock.getsockname())
    except OSError as exc:
        if not result["first_ok"]:
            result["first_err"] = f"[Errno {exc.errno}] {exc.strerror}"
        else:
            result["second_err"] = f"[Errno {exc.errno}] {exc.strerror}"
    finally:
        if second_sock is not None:
            second_sock.close()
        if first_sock is not None:
            first_sock.close()

    return result


def family_for_host(host: str) -> socket.AddressFamily:
    return socket.AF_INET6 if ":" in host else socket.AF_INET


def tune_socket(sock: socket.socket, protocol: str) -> None:
    _ = protocol
    # Approximate Go listener defaults:
    # - Windows: exclusive bind
    # - Unix-like: reuseaddr
    if os.name == "nt" and hasattr(socket, "SO_EXCLUSIVEADDRUSE"):
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
        return
    if hasattr(socket, "SO_REUSEADDR"):
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)


def format_sockname(value: tuple[object, ...]) -> str:
    if len(value) >= 2:
        return f"{value[0]}:{value[1]}"
    return str(value)


if __name__ == "__main__":
    sys.exit(main())
