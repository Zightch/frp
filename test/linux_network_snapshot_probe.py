#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime as dt
import json
import subprocess
import sys
from pathlib import Path


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Probe Linux local interfaces/IPs using the same ordering and de-dup semantics "
            "as frps internal/system/network_collect.go."
        ),
    )
    parser.add_argument(
        "--output",
        help="Optional JSON output file. Prints to stdout when omitted.",
    )
    parser.add_argument(
        "--expect-ip",
        action="append",
        default=[],
        help="Repeatable expected IP. Script exits non-zero when any expected IP is missing.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    raw = read_ip_addr_json()

    interfaces: list[dict[str, object]] = []
    available_map: dict[str, dict[str, str]] = {}
    sorted_raw = sorted(raw, key=lambda item: (int(item.get("ifindex") or 0), str(item.get("ifname") or "")))

    for item in sorted_raw:
        name = str(item.get("ifname") or "").strip()
        index = int(item.get("ifindex") or 0)
        ip_map: dict[str, dict[str, str]] = {}

        for addr_info in item.get("addr_info") or []:
            family, addr = normalize_addr_info(addr_info)
            if not family or not addr:
                continue
            next_ip = {"addr": addr, "family": family}
            ip_map[addr] = next_ip
            available_map[addr] = next_ip

        ips = sorted(ip_map.values(), key=lambda ip: (ip["family"], ip["addr"]))
        interfaces.append(
            {
                "name": name,
                "index": index,
                "ips": ips,
            }
        )

    available_ips = sorted(available_map.values(), key=lambda ip: (ip["family"], ip["addr"]))
    available_ip_strings = [item["addr"] for item in available_ips]
    missing = [ip for ip in args.expect_ip if ip not in available_map]

    payload: dict[str, object] = {
        "platform": "linux",
        "captured_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "interface_count": len(interfaces),
        "address_count": len(available_ips),
        "interfaces": interfaces,
        "available_ips": available_ips,
        "available_ip_strings": available_ip_strings,
        "expected_ips": args.expect_ip,
        "missing_expected_ips": missing,
    }

    text = json.dumps(payload, ensure_ascii=False, indent=2)
    if args.output:
        path = Path(args.output).resolve()
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text + "\n", encoding="utf-8")
        print(f"[ok] wrote {path}")
    else:
        print(text)

    if missing:
        print(f"[FAIL] missing expected IPs: {missing}", file=sys.stderr)
        return 1
    return 0


def read_ip_addr_json() -> list[dict[str, object]]:
    result = subprocess.run(
        ["ip", "-j", "addr", "show"],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(f"ip -j addr show failed: {result.stderr.strip()}")
    decoded = json.loads(result.stdout)
    if not isinstance(decoded, list):
        raise RuntimeError(f"unexpected ip -j output: {type(decoded).__name__}")
    return decoded


def normalize_addr_info(item: object) -> tuple[str, str]:
    if not isinstance(item, dict):
        return "", ""
    family = str(item.get("family") or "").strip().lower()
    addr = str(item.get("local") or "").strip().lower()
    if not addr:
        return "", ""
    if family == "inet":
        return "ipv4", addr
    if family == "inet6":
        return "ipv6", addr
    return "", ""


if __name__ == "__main__":
    sys.exit(main())
