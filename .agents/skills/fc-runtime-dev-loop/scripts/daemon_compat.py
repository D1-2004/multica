#!/usr/bin/env python3
"""Verify local Daemon compatibility without creating or deleting resources.

Mutation remains an explicit operator workflow because a human PAT registers the
Daemon in every Workspace the user can access. This helper validates the binary
surface, live registration/heartbeat evidence, task completion, and shutdown.
"""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any, Sequence


PROFILE_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
FLAG_RE = re.compile(r"^\s+(--[a-z0-9-]+)(?:\s|$)", re.MULTILINE)
DEFAULT_REQUIRED_FLAGS = {
    "--daemon-id",
    "--device-name",
    "--runtime-name",
    "--workspaces-root",
    "--max-concurrent-tasks",
    "--no-auto-update",
    "--no-auto-reload",
}


class CompatError(RuntimeError):
    pass


def print_json(value: Any) -> None:
    json.dump(value, sys.stdout, ensure_ascii=False, indent=2, sort_keys=True)
    sys.stdout.write("\n")


def profile_name(value: str) -> str:
    value = value.strip()
    if value != "default" and not PROFILE_RE.fullmatch(value):
        raise argparse.ArgumentTypeError("invalid profile name")
    return value


def binary_path(value: str) -> str:
    path = Path(value).expanduser().resolve()
    if not path.is_file() or not os.access(path, os.X_OK):
        raise argparse.ArgumentTypeError(f"not an executable file: {path}")
    return str(path)


def clean_env() -> dict[str, str]:
    env = os.environ.copy()
    for key in list(env):
        if key.startswith("MULTICA_"):
            env.pop(key, None)
    return env


def run(command: Sequence[str], timeout: float = 60.0) -> str:
    try:
        completed = subprocess.run(
            list(command),
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout,
            env=clean_env(),
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise CompatError(f"failed to execute {command[0]}: {exc}") from exc
    if completed.returncode != 0:
        raise CompatError(
            f"command failed ({completed.returncode}): {command[0]}: "
            f"{(completed.stderr or completed.stdout).strip()}"
        )
    return completed.stdout


def load_json(raw: str, source: str) -> Any:
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise CompatError(f"{source} returned invalid JSON: {exc}") from exc


def probe_binary(binary: str) -> dict[str, Any]:
    version = run([binary, "version"]).splitlines()[0].strip()
    probe = load_json(run([binary, "daemon", "probe-runtimes"]), "daemon probe")
    if not isinstance(probe, dict) or probe.get("probe_result") != "success":
        raise CompatError(f"{binary} daemon probe did not succeed")
    providers = probe.get("provider_summary")
    if not isinstance(providers, dict):
        raise CompatError(f"{binary} daemon probe returned no provider summary")
    help_text = run([binary, "daemon", "start", "--help"])
    return {
        "binary": binary,
        "version": version,
        "providers": sorted(str(name) for name, count in providers.items() if count),
        "flags": sorted(set(FLAG_RE.findall(help_text))),
    }


def compare(args: argparse.Namespace) -> dict[str, Any]:
    baseline = probe_binary(args.baseline_bin)
    candidate = probe_binary(args.candidate_bin)
    baseline_providers = set(baseline["providers"])
    candidate_providers = set(candidate["providers"])
    baseline_flags = set(baseline["flags"])
    candidate_flags = set(candidate["flags"])
    allowed_provider_removals = set(args.allow_provider_removal)
    allowed_flag_removals = set(args.allow_flag_removal)
    removed_providers = sorted(baseline_providers - candidate_providers)
    removed_flags = sorted(baseline_flags - candidate_flags)
    required_flags = DEFAULT_REQUIRED_FLAGS | set(args.required_flag)
    missing_required_flags = sorted(required_flags - candidate_flags)
    unapproved_provider_removals = sorted(
        set(removed_providers) - allowed_provider_removals
    )
    unapproved_flag_removals = sorted(set(removed_flags) - allowed_flag_removals)
    compatible = not (
        unapproved_provider_removals
        or unapproved_flag_removals
        or missing_required_flags
    )
    return {
        "compatible": compatible,
        "baseline": baseline,
        "candidate": candidate,
        "added_providers": sorted(candidate_providers - baseline_providers),
        "removed_providers": removed_providers,
        "removed_flags": removed_flags,
        "missing_required_flags": missing_required_flags,
        "unapproved_provider_removals": unapproved_provider_removals,
        "unapproved_flag_removals": unapproved_flag_removals,
    }


def profile_path(profile: str) -> Path:
    if profile == "default":
        return Path.home() / ".multica" / "config.json"
    return Path.home() / ".multica" / "profiles" / profile / "config.json"


def load_profile(profile: str) -> tuple[dict[str, Any], Path]:
    path = profile_path(profile)
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise CompatError(f"cannot read profile {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise CompatError(f"profile {path} is not a JSON object")
    return value, path


def strict_server_url(value: str) -> str:
    value = value.strip().rstrip("/")
    parsed = urllib.parse.urlparse(value)
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.netloc
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path not in {"", "/"}
        or parsed.query
        or parsed.fragment
    ):
        raise CompatError(f"invalid server URL: {value!r}")
    if parsed.scheme == "http":
        hostname = parsed.hostname or ""
        loopback = hostname == "localhost"
        if not loopback:
            try:
                loopback = ipaddress.ip_address(hostname).is_loopback
            except ValueError:
                loopback = False
        if not loopback:
            raise CompatError("PAT-bearing Daemon verification requires HTTPS")
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(
        self, req: Any, fp: Any, code: int, msg: str, headers: Any, newurl: str
    ) -> None:
        raise urllib.error.HTTPError(
            req.full_url, code, "redirect refused", headers, fp
        )


def api_get(server: str, token: str, path: str, workspace_id: str = "") -> Any:
    headers = {"Authorization": f"Bearer {token}", "Accept": "application/json"}
    if workspace_id:
        headers["X-Workspace-ID"] = workspace_id
    request = urllib.request.Request(server + path, headers=headers, method="GET")
    try:
        with urllib.request.build_opener(NoRedirect()).open(
            request, timeout=30
        ) as response:
            return load_json(response.read().decode("utf-8"), path)
    except urllib.error.HTTPError as exc:
        raise CompatError(f"GET {path} returned HTTP {exc.code}") from exc
    except urllib.error.URLError as exc:
        raise CompatError(f"GET {path} failed: {exc.reason}") from exc


def runtime_ledger(
    server: str, token: str, daemon_id: str
) -> tuple[list[dict[str, Any]], set[str]]:
    workspaces = api_get(server, token, "/api/workspaces")
    if not isinstance(workspaces, list):
        raise CompatError("workspace list is not an array")
    ledger: list[dict[str, Any]] = []
    workspace_ids: set[str] = set()
    for workspace in workspaces:
        if not isinstance(workspace, dict):
            continue
        workspace_id = str(workspace.get("id", ""))
        if not workspace_id:
            raise CompatError("workspace list contains an entry without an ID")
        workspace_ids.add(workspace_id)
        runtimes = api_get(server, token, "/api/runtimes", workspace_id)
        if not isinstance(runtimes, list):
            raise CompatError(f"Runtime list for {workspace_id} is not an array")
        for runtime in runtimes:
            if isinstance(runtime, dict) and runtime.get("daemon_id") == daemon_id:
                row = dict(runtime)
                row.setdefault("workspace_id", workspace_id)
                ledger.append(row)
    return ledger, workspace_ids


def verify_live(args: argparse.Namespace) -> dict[str, Any]:
    profile, path = load_profile(args.profile)
    server = strict_server_url(str(profile.get("server_url", "")))
    token = str(profile.get("token", "")).strip()
    if not token:
        raise CompatError("selected profile has no PAT")
    status = load_json(
        run(
            [
                args.candidate_bin,
                "--profile",
                args.profile,
                "daemon",
                "status",
                "--output",
                "json",
            ]
        ),
        "daemon status",
    )
    if not isinstance(status, dict) or status.get("status") != "running":
        raise CompatError("candidate Daemon is not ready/running")
    if status.get("daemon_id") != args.daemon_id:
        raise CompatError("daemon status identity does not match expected daemon ID")
    if (
        args.expected_cli_version
        and status.get("cli_version") != args.expected_cli_version
    ):
        raise CompatError("daemon status CLI version does not match expected version")
    ledger, visible_workspace_ids = runtime_ledger(server, token, args.daemon_id)
    if not ledger:
        raise CompatError("server read-back found no Runtime for the candidate daemon")
    registered_workspace_ids = {
        str(runtime.get("workspace_id", "")) for runtime in ledger
    }
    if registered_workspace_ids != visible_workspace_ids:
        raise CompatError(
            "candidate Daemon did not register in every PAT-visible Workspace: "
            f"visible={sorted(visible_workspace_ids)}, "
            f"registered={sorted(registered_workspace_ids)}"
        )
    expected_providers = set(args.expected_provider)
    actual_providers = {str(runtime.get("provider", "")) for runtime in ledger}
    if expected_providers and expected_providers != actual_providers:
        raise CompatError(
            f"server Runtime providers {sorted(actual_providers)} do not exactly match "
            f"{sorted(expected_providers)}"
        )
    for runtime in ledger:
        metadata = runtime.get("metadata")
        if (
            runtime.get("runtime_mode") != "local"
            or runtime.get("status") != "online"
            or not isinstance(metadata, dict)
            or (
                args.expected_cli_version
                and metadata.get("cli_version") != args.expected_cli_version
            )
        ):
            raise CompatError(f"invalid live Runtime read-back: {runtime.get('id')}")
    log_path = path.parent / "daemon.log"
    try:
        log_text = log_path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        raise CompatError(f"cannot read Daemon log {log_path}: {exc}") from exc
    if "task wakeup websocket connected" not in log_text:
        raise CompatError("Daemon log has no WebSocket connection evidence")
    if "WS recently acked" not in log_text:
        raise CompatError(
            "Daemon log has no WebSocket heartbeat acknowledgement evidence"
        )
    return {
        "verified": True,
        "daemon_id": args.daemon_id,
        "cli_version": status.get("cli_version"),
        "workspace_count": len(registered_workspace_ids),
        "runtime_count": len(ledger),
        "providers": sorted(actual_providers),
        "runtime_ids": sorted(str(runtime.get("id", "")) for runtime in ledger),
        "websocket_connected": True,
        "heartbeat_acknowledged": True,
    }


def verify_task(args: argparse.Namespace) -> dict[str, Any]:
    runs = load_json(
        run(
            [
                args.candidate_bin,
                "--profile",
                args.profile,
                "issue",
                "runs",
                args.issue_id,
                "--output",
                "json",
            ]
        ),
        "issue runs",
    )
    if not isinstance(runs, list) or not runs:
        raise CompatError("issue has no execution runs")
    candidates = [
        item
        for item in runs
        if isinstance(item, dict)
        and item.get("runtime_id") == args.runtime_id
        and (not args.task_id or item.get("id") == args.task_id)
    ]
    if len(candidates) != 1:
        selector = f"task {args.task_id}" if args.task_id else "target Runtime"
        raise CompatError(f"expected exactly one run for {selector}")
    task = candidates[0]
    if task.get("status") != "completed":
        raise CompatError(f"selected task is not completed: {task.get('status')!r}")
    result = task.get("result")
    output = result.get("output") if isinstance(result, dict) else None
    marker_matches = (
        output == args.expected_marker
        if args.marker_mode == "exact"
        else isinstance(output, str) and args.expected_marker in output
    )
    if not marker_matches:
        raise CompatError("task output marker does not match")
    if (
        not task.get("started_at")
        or not task.get("completed_at")
        or not task.get("work_dir")
    ):
        raise CompatError("completed task is missing timing or workdir evidence")
    return {
        "verified": True,
        "task_id": task.get("id"),
        "runtime_id": task.get("runtime_id"),
        "status": task.get("status"),
        "output": output,
        "marker_mode": args.marker_mode,
        "marker_verified": True,
        "work_dir": task.get("work_dir"),
        "started_at": task.get("started_at"),
        "completed_at": task.get("completed_at"),
    }


def verify_stopped(args: argparse.Namespace) -> dict[str, Any]:
    profile, _ = load_profile(args.profile)
    server = strict_server_url(str(profile.get("server_url", "")))
    token = str(profile.get("token", "")).strip()
    if not token:
        raise CompatError("selected profile has no PAT")
    status = load_json(
        run(
            [
                args.candidate_bin,
                "--profile",
                args.profile,
                "daemon",
                "status",
                "--output",
                "json",
            ]
        ),
        "daemon status",
    )
    if not isinstance(status, dict) or status.get("status") != "stopped":
        raise CompatError("candidate Daemon is not stopped")
    ledger, _ = runtime_ledger(server, token, args.daemon_id)
    online = [
        runtime.get("id") for runtime in ledger if runtime.get("status") == "online"
    ]
    if online:
        raise CompatError(f"stopped Daemon still has online Runtimes: {online}")
    if args.require_absent and ledger:
        raise CompatError("candidate Daemon Runtimes still exist after cleanup")
    return {
        "verified": True,
        "daemon_id": args.daemon_id,
        "status": "stopped",
        "remaining_runtime_count": len(ledger),
        "all_remaining_offline": True,
    }


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(
        description="Verify persistent local Daemon compatibility"
    )
    commands = root.add_subparsers(dest="command", required=True)

    compare_parser = commands.add_parser(
        "compare", help="Compare released and candidate binary surfaces"
    )
    compare_parser.add_argument("--baseline-bin", required=True, type=binary_path)
    compare_parser.add_argument("--candidate-bin", required=True, type=binary_path)
    compare_parser.add_argument("--required-flag", action="append", default=[])
    compare_parser.add_argument("--allow-provider-removal", action="append", default=[])
    compare_parser.add_argument("--allow-flag-removal", action="append", default=[])

    live = commands.add_parser(
        "verify-live", help="Verify live registration and heartbeat evidence"
    )
    live.add_argument("--candidate-bin", required=True, type=binary_path)
    live.add_argument("--profile", required=True, type=profile_name)
    live.add_argument("--daemon-id", required=True)
    live.add_argument("--expected-cli-version")
    live.add_argument("--expected-provider", action="append", default=[])

    task = commands.add_parser(
        "verify-task", help="Verify one completed local Daemon task"
    )
    task.add_argument("--candidate-bin", required=True, type=binary_path)
    task.add_argument("--profile", required=True, type=profile_name)
    task.add_argument("--issue-id", required=True)
    task.add_argument("--runtime-id", required=True)
    task.add_argument(
        "--task-id",
        help="Select one exact task when an Issue has multiple runs (for example warm reuse)",
    )
    task.add_argument("--expected-marker", required=True)
    task.add_argument(
        "--marker-mode",
        choices=("exact", "contains"),
        default="exact",
        help="Use contains only when the platform/provider intentionally wraps canary output",
    )

    stopped = commands.add_parser(
        "verify-stopped", help="Verify stop/deregister or final cleanup"
    )
    stopped.add_argument("--candidate-bin", required=True, type=binary_path)
    stopped.add_argument("--profile", required=True, type=profile_name)
    stopped.add_argument("--daemon-id", required=True)
    stopped.add_argument("--require-absent", action="store_true")
    return root


def main(argv: Sequence[str] | None = None) -> int:
    args = parser().parse_args(argv)
    try:
        if args.command == "compare":
            result = compare(args)
            print_json(result)
            return 0 if result["compatible"] else 2
        if args.command == "verify-live":
            result = verify_live(args)
        elif args.command == "verify-task":
            result = verify_task(args)
        elif args.command == "verify-stopped":
            result = verify_stopped(args)
        else:
            raise CompatError(f"unknown command: {args.command}")
    except CompatError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    print_json(result)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
