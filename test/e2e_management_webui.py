#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import os
import posixpath
import shutil
import signal
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from http.cookiejar import CookieJar
from pathlib import Path
from typing import IO


DEFAULT_TIMEOUT_SECONDS = 30.0
DEFAULT_MANAGEMENT_SECRET = "frps-management-e2e-secret"


@dataclass(frozen=True)
class RuntimePaths:
    output_dir: Path
    workspace_dir: Path
    frps_bin_path: Path
    data_dir: Path
    config_path: Path
    auth_path: Path
    db_path: Path
    webui_dir: Path
    webui_dist_dir: Path
    frps_log_path: Path
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


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Run an end-to-end validation for frps direct startup from data/config.json, "
            "management secret initialization/login, and proxy group/tunnel CRUD."
        ),
    )
    parser.add_argument(
        "--frps-bin",
        help="Existing frps binary path. If omitted, build one into the output workspace.",
    )
    parser.add_argument(
        "--webui-dist",
        help="Existing built webui dist directory. If omitted, run npm build in frps/webui first.",
    )
    parser.add_argument(
        "--output-dir",
        help="Directory used for the isolated workspace, logs, sqlite database, and result.json.",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=DEFAULT_TIMEOUT_SECONDS,
        help=f"Timeout in seconds for build, startup, and HTTP checks. Default: {DEFAULT_TIMEOUT_SECONDS}.",
    )
    parser.add_argument(
        "--management-secret",
        default=DEFAULT_MANAGEMENT_SECRET,
        help="Management secret used during the init/login flow.",
    )
    parser.add_argument(
        "--webui-prefix",
        default="",
        help="Optional WebUI path prefix such as /frps for prefixed reverse proxy deployments.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    script_path = Path(__file__).resolve()
    repo_root = script_path.parent.parent
    if not (repo_root / "frps").is_dir():
        print(f"[FAIL] could not locate frps from script path: {script_path}", file=sys.stderr)
        return 1

    validate_args(args)
    webui_prefix = normalize_webui_prefix(args.webui_prefix)
    output_dir = resolve_output_dir(args, repo_root)
    paths = build_runtime_paths(output_dir)

    stage = "prepare output workspace"
    process: ManagedProcess | None = None
    summary: dict[str, object] = {}

    try:
        print(f"[stage] {stage}")
        prepare_output_dirs(paths)

        stage = "build or resolve frps binary"
        print(f"[stage] {stage}")
        build_or_copy_frps_binary(args, repo_root, paths)

        stage = "build or resolve webui dist"
        print(f"[stage] {stage}")
        source_dist = build_or_resolve_webui_dist(args, repo_root)
        copy_webui_dist(source_dist, paths.webui_dist_dir)

        stage = "write fixed data/config.json"
        print(f"[stage] {stage}")
        control_port, management_port, tunnel_ports = allocate_test_ports(count=6)
        write_config_file(paths.config_path, control_port, management_port, webui_prefix)

        stage = "start frps from isolated workspace"
        print(f"[stage] {stage}")
        process = start_process(
            name="frps",
            command=[str(paths.frps_bin_path)],
            cwd=paths.workspace_dir,
            log_path=paths.frps_log_path,
        )

        base_url = f"http://127.0.0.1:{management_port}"
        webui_base_url = join_webui_base_url(base_url, webui_prefix)
        api_base_url = f"{webui_base_url}/api/v1"

        stage = "wait for management api and sqlite schema"
        print(f"[stage] {stage}")
        wait_http_ready(f"{webui_base_url}/readyz", args.timeout, process)
        wait_sqlite_schema(paths.db_path, min(args.timeout, 15.0))

        cookie_jar = CookieJar()
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookie_jar))

        stage = "validate webui static entry and spa fallback"
        print(f"[stage] {stage}")
        root_html = request_text(opener, "GET", f"{webui_base_url}/", expected_status=200)
        if '<div id="app"></div>' not in root_html:
            raise RuntimeError("webui root did not return the built SPA entry")
        login_html = request_text(opener, "GET", f"{webui_base_url}/login", expected_status=200)
        if '<div id="app"></div>' not in login_html:
            raise RuntimeError("webui SPA fallback did not return the built SPA entry")

        stage = "validate pre-init auth state"
        print(f"[stage] {stage}")
        state_before = request_json(opener, "GET", f"{api_base_url}/auth/state", expected_status=200)
        assert_equal(state_before.get("initialized"), False, "pre-init initialized")
        assert_equal(state_before.get("authenticated"), False, "pre-init authenticated")

        unauthorized_before_init = request_json(
            opener,
            "GET",
            f"{api_base_url}/proxy-groups",
            expected_status=409,
        )
        assert_equal(
            unauthorized_before_init.get("error"),
            "management secret is not initialized",
            "pre-init protected api error",
        )

        stage = "initialize management secret"
        print(f"[stage] {stage}")
        key_hash = sha256_hex(args.management_secret)
        init_result = request_json(
            opener,
            "POST",
            f"{api_base_url}/auth/init",
            payload={"key_hash": key_hash},
            expected_status=201,
        )
        assert_equal(init_result.get("initialized"), True, "init response initialized")

        auth_payload = json.loads(paths.auth_path.read_text(encoding="utf-8"))
        assert_equal(
            auth_payload,
            {"key_hash": key_hash},
            "auth.json payload",
        )

        stage = "challenge login and session restore"
        print(f"[stage] {stage}")
        challenge = request_json(opener, "POST", f"{api_base_url}/auth/challenge", expected_status=200)
        challenge_id = str(challenge.get("challenge_id") or "").strip()
        salt = str(challenge.get("salt") or "").strip()
        if not challenge_id or not salt:
            raise RuntimeError(f"invalid challenge payload: {challenge!r}")

        login_result = request_json(
            opener,
            "POST",
            f"{api_base_url}/auth/login",
            payload={
                "challenge_id": challenge_id,
                "proof": build_management_proof(key_hash, salt),
            },
            expected_status=200,
        )
        assert_equal(login_result.get("initialized"), True, "login response initialized")
        assert_equal(login_result.get("authenticated"), True, "login response authenticated")
        if not list(cookie_jar):
            raise RuntimeError("login did not produce a management session cookie")

        state_after_login = request_json(opener, "GET", f"{api_base_url}/auth/state", expected_status=200)
        assert_equal(state_after_login.get("initialized"), True, "post-login initialized")
        assert_equal(state_after_login.get("authenticated"), True, "post-login authenticated")

        session_after_login = request_json(opener, "GET", f"{api_base_url}/auth/session", expected_status=200)
        assert_equal(session_after_login.get("authenticated"), True, "session authenticated")

        stage = "delete auth.json and confirm automatic auth reset"
        print(f"[stage] {stage}")
        paths.auth_path.unlink()

        state_after_delete = wait_for_auth_reset(opener, api_base_url, min(args.timeout, 10.0))
        assert_equal(state_after_delete.get("initialized"), False, "post-delete initialized")
        assert_equal(state_after_delete.get("authenticated"), False, "post-delete authenticated")

        protected_after_delete = request_json(
            opener,
            "GET",
            f"{api_base_url}/proxy-groups",
            expected_status=409,
        )
        assert_equal(
            protected_after_delete.get("error"),
            "management secret is not initialized",
            "post-delete protected api error",
        )

        stage = "reinitialize management secret after auth reset"
        print(f"[stage] {stage}")
        reinit_result = request_json(
            opener,
            "POST",
            f"{api_base_url}/auth/init",
            payload={"key_hash": key_hash},
            expected_status=201,
        )
        assert_equal(reinit_result.get("initialized"), True, "reinit response initialized")

        challenge_after_reset = request_json(opener, "POST", f"{api_base_url}/auth/challenge", expected_status=200)
        challenge_id = str(challenge_after_reset.get("challenge_id") or "").strip()
        salt = str(challenge_after_reset.get("salt") or "").strip()
        if not challenge_id or not salt:
            raise RuntimeError(f"invalid challenge payload after reset: {challenge_after_reset!r}")

        relogin_result = request_json(
            opener,
            "POST",
            f"{api_base_url}/auth/login",
            payload={
                "challenge_id": challenge_id,
                "proof": build_management_proof(key_hash, salt),
            },
            expected_status=200,
        )
        assert_equal(relogin_result.get("authenticated"), True, "relogin response authenticated")

        stage = "create and update proxy group"
        print(f"[stage] {stage}")
        group_effective_ip = "0.0.0.0"
        created_group = request_json(
            opener,
            "POST",
            f"{api_base_url}/proxy-groups",
            payload={
                "name": "e2e-group",
                "effective_ip": group_effective_ip,
                "enabled": True,
            },
            expected_status=201,
        )
        created_group_item = require_mapping(created_group, "item")
        created_group_id = int(created_group_item["id"])
        initial_group_key = require_string(created_group, "key")
        assert_equal(created_group_item.get("effective_ip"), group_effective_ip, "created proxy group effective_ip")
        assert_equal(created_group_item.get("status"), "启用", "created proxy group status")
        assert_key_matches_db(paths.db_path, created_group_id, initial_group_key)

        groups_after_create = request_json(opener, "GET", f"{api_base_url}/proxy-groups", expected_status=200)
        group_items = require_list(groups_after_create, "items")
        assert_equal(len(group_items), 1, "proxy group count after create")

        updated_group = request_json(
            opener,
            "PATCH",
            f"{api_base_url}/proxy-groups/{created_group_id}",
            payload={"name": "e2e-group-renamed", "enabled": True},
            expected_status=200,
        )
        updated_group_item = require_mapping(updated_group, "item")
        assert_equal(updated_group_item.get("name"), "e2e-group-renamed", "updated proxy group name")
        assert_equal(updated_group_item.get("effective_ip"), group_effective_ip, "patched proxy group preserved effective_ip")
        assert_equal(updated_group_item.get("status"), "启用", "patched proxy group status")

        stage = "reset proxy group key"
        print(f"[stage] {stage}")
        reset_group = request_json(
            opener,
            "POST",
            f"{api_base_url}/proxy-groups/{created_group_id}/key",
            expected_status=200,
        )
        reset_group_item = require_mapping(reset_group, "item")
        reset_group_key = require_string(reset_group, "key")
        if reset_group_key == initial_group_key:
            raise RuntimeError("proxy group key reset returned the previous key value")
        assert_equal(reset_group_item.get("id"), updated_group_item.get("id"), "reset proxy group id")
        assert_key_matches_db(paths.db_path, created_group_id, reset_group_key)

        stage = "create and update tunnel"
        print(f"[stage] {stage}")
        created_tunnel = request_json(
            opener,
            "POST",
            f"{api_base_url}/tunnels",
            payload={
                "group_id": created_group_id,
                "name": "e2e-tunnel",
                "protocol": "tcp",
                "remote_type": "single",
                "remote_start": tunnel_ports[0],
                "remote_end": tunnel_ports[0],
                "local_host": "127.0.0.1",
                "local_start": tunnel_ports[1],
                "local_end": tunnel_ports[1],
                "enabled": True,
            },
            expected_status=201,
        )
        created_tunnel_item = require_mapping(created_tunnel, "item")
        created_tunnel_id = int(created_tunnel_item["id"])
        assert_equal(created_tunnel_item.get("group_id"), created_group_id, "created tunnel group_id")

        tunnels_after_create = request_json(opener, "GET", f"{api_base_url}/tunnels", expected_status=200)
        tunnel_items = require_list(tunnels_after_create, "items")
        assert_equal(len(tunnel_items), 1, "tunnel count after create")

        updated_tunnel = request_json(
            opener,
            "PATCH",
            f"{api_base_url}/tunnels/{created_tunnel_id}",
            payload={
                "group_id": created_group_id,
                "name": "e2e-tunnel-renamed",
                "protocol": "tcp",
                "remote_type": "single",
                "remote_start": tunnel_ports[2],
                "remote_end": tunnel_ports[2],
                "local_host": "127.0.0.1",
                "local_start": tunnel_ports[3],
                "local_end": tunnel_ports[3],
                "enabled": False,
            },
            expected_status=200,
        )
        updated_tunnel_item = require_mapping(updated_tunnel, "item")
        assert_equal(updated_tunnel_item.get("name"), "e2e-tunnel-renamed", "updated tunnel name")
        assert_equal(updated_tunnel_item.get("enabled"), False, "updated tunnel enabled flag")

        stage = "delete tunnel and proxy group"
        print(f"[stage] {stage}")
        deleted_tunnel = request_json(
            opener,
            "DELETE",
            f"{api_base_url}/tunnels/{created_tunnel_id}",
            expected_status=200,
        )
        assert_equal(deleted_tunnel.get("deleted"), True, "delete tunnel response")
        assert_equal(count_rows(paths.db_path, "tunnels"), 0, "tunnel row count after delete")

        deleted_group = request_json(
            opener,
            "DELETE",
            f"{api_base_url}/proxy-groups/{created_group_id}",
            expected_status=200,
        )
        assert_equal(deleted_group.get("deleted"), True, "delete proxy group response")
        assert_equal(count_rows(paths.db_path, "proxy_groups"), 0, "proxy group row count after delete")

        stage = "logout and confirm protected api rejection"
        print(f"[stage] {stage}")
        logout_result = request_json(opener, "POST", f"{api_base_url}/auth/logout", expected_status=200)
        assert_equal(logout_result.get("logged_out"), True, "logout response")

        unauthorized_after_logout = request_json(
            opener,
            "GET",
            f"{api_base_url}/auth/session",
            expected_status=401,
        )
        assert_equal(
            unauthorized_after_logout.get("error"),
            "management session is required",
            "post-logout session error",
        )

        summary = {
            "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
            "output_dir": str(paths.output_dir),
            "workspace_dir": str(paths.workspace_dir),
            "management_url": base_url,
            "webui_prefix": webui_prefix,
            "webui_base_url": webui_base_url,
            "control_addr": f"127.0.0.1:{control_port}",
            "frps_log_path": str(paths.frps_log_path),
            "config_path": str(paths.config_path),
            "auth_path": str(paths.auth_path),
            "db_path": str(paths.db_path),
            "verified_steps": [
                "frps.exe direct startup from workspace/data/config.json",
                "webui built dist static entry and spa fallback",
                "management auth state before initialization",
                "management secret initialization to auth.json",
                "challenge login and session recovery",
                "auth.json deletion resets management auth state",
                "management secret reinitialization after auth reset",
                "proxy group create/list/update/delete",
                "proxy group login key reset with sqlite verification",
                "tunnel create/list/update/delete",
                "logout and protected api rejection",
            ],
        }
        paths.result_json_path.write_text(json.dumps(summary, indent=2), encoding="utf-8")

        print("[ok] management webui e2e succeeded")
        print(f"[info] output_dir={paths.output_dir}")
        print(f"[info] workspace_dir={paths.workspace_dir}")
        print(f"[info] result_json={paths.result_json_path}")
        print(f"[info] frps_log={paths.frps_log_path}")
        return 0
    except Exception as exc:
        print(f"[FAIL] stage={stage}: {exc}", file=sys.stderr)
        print(f"[FAIL] output_dir={paths.output_dir}", file=sys.stderr)
        print(f"[FAIL] frps_log={paths.frps_log_path}", file=sys.stderr)
        if process is not None:
            dump_process_log(process)
        return 1
    finally:
        if process is not None:
            terminate_process(process)


def validate_args(args: argparse.Namespace) -> None:
    if args.timeout <= 0:
        raise ValueError("--timeout must be positive")
    if not args.management_secret.strip():
        raise ValueError("--management-secret must not be empty")
    normalize_webui_prefix(args.webui_prefix)


def resolve_output_dir(args: argparse.Namespace, repo_root: Path) -> Path:
    if args.output_dir:
        return Path(args.output_dir).resolve()
    timestamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S-%f")
    return (repo_root / "test" / "tmp" / f"management-e2e-{timestamp}").resolve()


def build_runtime_paths(output_dir: Path) -> RuntimePaths:
    workspace_dir = output_dir / "workspace"
    data_dir = workspace_dir / "data"
    webui_dir = workspace_dir / "webui"
    return RuntimePaths(
        output_dir=output_dir,
        workspace_dir=workspace_dir,
        frps_bin_path=workspace_dir / executable_name("frps"),
        data_dir=data_dir,
        config_path=data_dir / "config.json",
        auth_path=data_dir / "auth.json",
        db_path=data_dir / "frps.db",
        webui_dir=webui_dir,
        webui_dist_dir=webui_dir / "dist",
        frps_log_path=output_dir / "frps.log",
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

    if shutil.which("go") is None:
        raise RuntimeError("go executable not found in PATH; pass --frps-bin to skip building")

    result = subprocess.run(
        ["go", "build", "-o", str(paths.frps_bin_path), "./cmd/frps"],
        cwd=str(repo_root / "frps"),
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(
            "go build failed for frps\n"
            f"stdout:\n{result.stdout}\n"
            f"stderr:\n{result.stderr}"
        )


def build_or_resolve_webui_dist(args: argparse.Namespace, repo_root: Path) -> Path:
    if args.webui_dist:
        source = Path(args.webui_dist).resolve()
        if not source.is_dir():
            raise RuntimeError(f"webui dist directory does not exist: {source}")
        return source

    existing_dist = (repo_root / "frps" / "webui" / "dist").resolve()
    if existing_dist.is_dir():
        return existing_dist

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

    dist_dir = (repo_root / "frps" / "webui" / "dist").resolve()
    if not dist_dir.is_dir():
        raise RuntimeError(f"webui dist directory was not produced: {dist_dir}")
    return dist_dir


def resolve_npm_executable() -> str | None:
    candidates = ("npm.cmd", "npm") if os.name == "nt" else ("npm",)
    for candidate in candidates:
        resolved = shutil.which(candidate)
        if resolved:
            return resolved
    return None


def copy_webui_dist(source_dist: Path, target_dist: Path) -> None:
    if target_dist.exists():
        shutil.rmtree(target_dist)
    shutil.copytree(source_dist, target_dist)


def allocate_test_ports(count: int) -> tuple[int, int, list[int]]:
    import socket

    reserved: list[socket.socket] = []
    ports: list[int] = []
    try:
        for _ in range(count):
            probe = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            probe.bind(("127.0.0.1", 0))
            probe.listen(1)
            reserved.append(probe)
            ports.append(probe.getsockname()[1])
    finally:
        for probe in reserved:
            probe.close()

    return ports[0], ports[1], ports[2:]


def write_config_file(config_path: Path, control_port: int, management_port: int, webui_prefix: str) -> None:
    config = {
        "control_listen_addr": f"127.0.0.1:{control_port}",
        "management_listen_addr": f"127.0.0.1:{management_port}",
        "read_header_timeout": "5s",
        "shutdown_timeout": "10s",
        "database": {
            "type": "sqlite",
            "path": "./frps.db",
        },
        "webui": {
            "dist_dir": "../webui/dist",
            "path_prefix": webui_prefix,
        },
        "log": {
            "level": "info",
            "format": "text",
        },
    }
    config_path.write_text(json.dumps(config, indent=2), encoding="utf-8")


def start_process(name: str, command: list[str], cwd: Path, log_path: Path) -> ManagedProcess:
    log_path.parent.mkdir(parents=True, exist_ok=True)
    log_handle = log_path.open("w", encoding="utf-8", buffering=1)
    creationflags = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0) if os.name == "nt" else 0
    try:
        popen = subprocess.Popen(
            command,
            cwd=str(cwd),
            stdout=log_handle,
            stderr=subprocess.STDOUT,
            text=True,
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


def wait_http_ready(url: str, timeout_seconds: float, process: ManagedProcess) -> None:
    deadline = time.time() + timeout_seconds
    opener = urllib.request.build_opener()
    while time.time() < deadline:
        ensure_process_alive(process)
        try:
            response = opener.open(url, timeout=1.0)
        except (OSError, urllib.error.URLError):
            time.sleep(0.25)
            continue

        with response:
            if response.status == 200:
                return
        time.sleep(0.25)

    raise TimeoutError(f"http readiness check timed out: {url}")


def wait_sqlite_schema(db_path: Path, timeout_seconds: float) -> None:
    deadline = time.time() + timeout_seconds
    required_tables = {
        "proxy_groups",
        "tunnels",
    }
    while time.time() < deadline:
        if not db_path.exists():
            time.sleep(0.25)
            continue
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


def wait_for_auth_reset(
    opener: urllib.request.OpenerDirector,
    api_base_url: str,
    timeout_seconds: float,
) -> dict[str, object]:
    deadline = time.time() + timeout_seconds
    while time.time() < deadline:
        state = request_json(opener, "GET", f"{api_base_url}/auth/state", expected_status=200)
        if state.get("initialized") is False and state.get("authenticated") is False:
            return state
        time.sleep(0.25)

    raise TimeoutError("management auth did not reset after auth.json deletion")


def request_text(
    opener: urllib.request.OpenerDirector,
    method: str,
    url: str,
    payload: dict[str, object] | None = None,
    expected_status: int = 200,
) -> str:
    result = perform_request(opener, method, url, payload, expected_status)
    return result.body.decode("utf-8", errors="replace")


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
    headers = {}
    data = None
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


def require_mapping(payload: dict[str, object], key: str) -> dict[str, object]:
    value = payload.get(key)
    if not isinstance(value, dict):
        raise RuntimeError(f"expected object field {key!r}, got {value!r}")
    return value


def require_list(payload: dict[str, object], key: str) -> list[object]:
    value = payload.get(key)
    if not isinstance(value, list):
        raise RuntimeError(f"expected list field {key!r}, got {value!r}")
    return value


def require_string(payload: dict[str, object], key: str) -> str:
    value = payload.get(key)
    if not isinstance(value, str) or not value.strip():
        raise RuntimeError(f"expected non-empty string field {key!r}, got {value!r}")
    return value


def assert_equal(actual: object, expected: object, label: str) -> None:
    if actual != expected:
        raise RuntimeError(f"{label} mismatch: got {actual!r} want {expected!r}")


def assert_key_matches_db(db_path: Path, group_id: int, key_value: str) -> None:
    client_id, client_secret_hash = decode_key_material(key_value)
    with sqlite3.connect(db_path, timeout=5.0) as conn:
        row = conn.execute(
            "SELECT client_id, client_secret_hash FROM proxy_groups WHERE id = ?",
            (group_id,),
        ).fetchone()
    if row is None:
        raise RuntimeError(f"proxy group row not found for id {group_id}")
    actual_client_id = str(row[0])
    actual_client_secret_hash = str(row[1])
    assert_equal(actual_client_id, client_id, f"proxy group {group_id} client_id")
    assert_equal(actual_client_secret_hash, client_secret_hash, f"proxy group {group_id} client_secret_hash")


def decode_key_material(key_value: str) -> tuple[str, str]:
    value = key_value.strip().lower()
    if len(value) != 96:
        raise RuntimeError(f"unexpected key length: {len(value)}")
    client_id = value[:32]
    client_secret_hex = value[32:]
    try:
        client_secret = bytes.fromhex(client_secret_hex)
    except ValueError as exc:
        raise RuntimeError(f"client secret is not valid hex: {key_value!r}") from exc
    client_secret_hash = hashlib.sha256(client_secret).hexdigest()
    return client_id, client_secret_hash


def count_rows(db_path: Path, table: str) -> int:
    with sqlite3.connect(db_path, timeout=5.0) as conn:
        row = conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()
    if row is None:
        raise RuntimeError(f"failed to count rows for table {table}")
    return int(row[0])


def ensure_process_alive(process: ManagedProcess) -> None:
    return_code = process.popen.poll()
    if return_code is not None:
        raise RuntimeError(f"{process.name} exited unexpectedly with code {return_code}")


def normalize_webui_prefix(value: str) -> str:
    trimmed = value.strip()
    if not trimmed or trimmed == "/":
        return ""
    if "\\" in trimmed:
        raise ValueError("--webui-prefix must use '/' separators")
    if "?" in trimmed or "#" in trimmed:
        raise ValueError("--webui-prefix must not contain query or fragment")

    normalized = trimmed if trimmed.startswith("/") else f"/{trimmed}"
    normalized = posixpath.normpath(normalized)
    if normalized == "." or normalized == "/":
        return ""
    if normalized == "/api" or normalized.startswith("/api/"):
        raise ValueError("--webui-prefix must not overlap reserved /api paths")
    if normalized == "/healthz" or normalized.startswith("/healthz/"):
        raise ValueError("--webui-prefix must not overlap reserved /healthz paths")
    if normalized == "/readyz" or normalized.startswith("/readyz/"):
        raise ValueError("--webui-prefix must not overlap reserved /readyz paths")
    return normalized


def join_webui_base_url(base_url: str, webui_prefix: str) -> str:
    return f"{base_url}{webui_prefix}" if webui_prefix else base_url


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


def dump_process_log(process: ManagedProcess) -> None:
    print(f"===== {process.name} log: {process.log_path} =====", file=sys.stderr)
    try:
        content = process.log_path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        print(f"<unable to read log: {exc}>", file=sys.stderr)
        return
    if content:
        print(content, file=sys.stderr, end="" if content.endswith("\n") else "\n")
    else:
        print("<empty>", file=sys.stderr)


if __name__ == "__main__":
    sys.exit(main())
