from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "daemon_compat.py"
TOKEN = "mul_daemon_compat_test_token"


def fake_binary(
    directory: Path,
    name: str,
    providers: list[str],
    flags: list[str],
    daemon_status: str = "running",
) -> Path:
    path = directory / name
    payload = f"""#!/usr/bin/env python3
import json
import sys
args = sys.argv[1:]
if args == ["version"]:
    print("multica {name}")
elif args == ["daemon", "probe-runtimes"]:
    print(json.dumps({{"probe_result":"success","runtime_count":{len(providers)},"provider_summary":{json.dumps({provider: 1 for provider in providers})}}}))
elif args == ["daemon", "start", "--help"]:
    print("FLAGS:")
    for flag in {flags!r}:
        print("      " + flag + " value")
elif len(args) >= 5 and args[-3:] == ["status", "--output", "json"]:
    print(json.dumps({{"status":"{daemon_status}","daemon_id":"daemon-test","cli_version":"candidate-v1"}}))
elif "issue" in args and "runs" in args:
    print(json.dumps([{{
        "id":"task-1","runtime_id":"runtime-1","status":"completed",
        "started_at":"2026-09-03T00:00:00Z","completed_at":"2026-09-03T00:00:01Z",
        "work_dir":"/tmp/work","result":{{"output":"DAEMON_OK"}}
    }}]))
else:
    print("unexpected args: " + repr(args), file=sys.stderr)
    raise SystemExit(2)
"""
    path.write_text(payload, encoding="utf-8")
    path.chmod(0o755)
    return path


def run_script(
    args: list[str], env: dict[str, str] | None = None
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args],
        capture_output=True,
        text=True,
        check=False,
        env=env,
    )


class APIHandler(BaseHTTPRequestHandler):
    runtime_status = "online"
    workspace_ids = ["workspace-1"]
    extra_runtime_provider: str | None = None

    def log_message(self, *_args: Any) -> None:
        return

    def _json(self, value: Any) -> None:
        data = json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self) -> None:
        if self.headers.get("Authorization") != f"Bearer {TOKEN}":
            self.send_response(401)
            self.end_headers()
            return
        if self.path == "/api/workspaces":
            self._json([{"id": workspace_id} for workspace_id in self.workspace_ids])
        elif self.path == "/api/runtimes":
            workspace_id = self.headers.get("X-Workspace-ID", "")
            runtimes = []
            if workspace_id == "workspace-1":
                runtimes.append(
                    {
                        "id": "runtime-1",
                        "workspace_id": workspace_id,
                        "daemon_id": "daemon-test",
                        "provider": "codex",
                        "runtime_mode": "local",
                        "status": self.runtime_status,
                        "metadata": {"cli_version": "candidate-v1"},
                    }
                )
                if self.extra_runtime_provider:
                    runtimes.append(
                        {
                            "id": "runtime-extra",
                            "workspace_id": workspace_id,
                            "daemon_id": "daemon-test",
                            "provider": self.extra_runtime_provider,
                            "runtime_mode": "local",
                            "status": self.runtime_status,
                            "metadata": {"cli_version": "candidate-v1"},
                        }
                    )
            self._json(runtimes)
        else:
            self.send_response(404)
            self.end_headers()


class Server:
    def __enter__(self) -> str:
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), APIHandler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        host, port = self.server.server_address
        return f"http://{host}:{port}"

    def __exit__(self, *_args: Any) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


class DaemonCompatTest(unittest.TestCase):
    def setUp(self) -> None:
        APIHandler.runtime_status = "online"
        APIHandler.workspace_ids = ["workspace-1"]
        APIHandler.extra_runtime_provider = None

    def test_compare_rejects_removed_provider_and_required_flag(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            baseline = fake_binary(
                root,
                "baseline",
                ["codex", "zeroclaw"],
                [
                    "--daemon-id",
                    "--workspaces-root",
                    "--no-auto-update",
                    "--no-auto-reload",
                ],
            )
            candidate = fake_binary(
                root,
                "candidate",
                ["codex"],
                ["--daemon-id", "--no-auto-update", "--no-auto-reload"],
            )
            result = run_script(
                [
                    "compare",
                    "--baseline-bin",
                    str(baseline),
                    "--candidate-bin",
                    str(candidate),
                ]
            )
        self.assertEqual(result.returncode, 2, result.stderr)
        output = json.loads(result.stdout)
        self.assertFalse(output["compatible"])
        self.assertIn("zeroclaw", output["unapproved_provider_removals"])
        self.assertIn("--workspaces-root", output["missing_required_flags"])

    def test_compare_passes_equal_surfaces(self) -> None:
        flags = sorted(
            {
                "--daemon-id",
                "--device-name",
                "--runtime-name",
                "--workspaces-root",
                "--max-concurrent-tasks",
                "--no-auto-update",
                "--no-auto-reload",
            }
        )
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            baseline = fake_binary(root, "baseline", ["codex"], flags)
            candidate = fake_binary(root, "candidate", ["codex"], flags)
            result = run_script(
                [
                    "compare",
                    "--baseline-bin",
                    str(baseline),
                    "--candidate-bin",
                    str(candidate),
                ]
            )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads(result.stdout)["compatible"])

    def test_verify_live_checks_server_runtime_and_ws_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as tmp, Server() as server:
            root = Path(tmp)
            candidate = fake_binary(root, "candidate", ["codex"], [])
            profile_dir = root / ".multica" / "profiles" / "smoke"
            profile_dir.mkdir(parents=True)
            (profile_dir / "config.json").write_text(
                json.dumps({"server_url": server, "token": TOKEN}), encoding="utf-8"
            )
            (profile_dir / "daemon.log").write_text(
                "task wakeup websocket connected\nheartbeat: skipping HTTP tick, WS recently acked\n",
                encoding="utf-8",
            )
            env = os.environ.copy()
            env["HOME"] = str(root)
            result = run_script(
                [
                    "verify-live",
                    "--candidate-bin",
                    str(candidate),
                    "--profile",
                    "smoke",
                    "--daemon-id",
                    "daemon-test",
                    "--expected-cli-version",
                    "candidate-v1",
                    "--expected-provider",
                    "codex",
                ],
                env,
            )
        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads(result.stdout)
        self.assertTrue(output["verified"])
        self.assertEqual(output["runtime_ids"], ["runtime-1"])

    def test_verify_task_requires_exact_runtime_and_marker(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            candidate = fake_binary(Path(tmp), "candidate", ["codex"], [])
            result = run_script(
                [
                    "verify-task",
                    "--candidate-bin",
                    str(candidate),
                    "--profile",
                    "smoke",
                    "--issue-id",
                    "WS-1",
                    "--runtime-id",
                    "runtime-1",
                    "--expected-marker",
                    "DAEMON_OK",
                ]
            )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["task_id"], "task-1")

    def test_verify_task_can_select_task_and_explicit_wrapped_marker(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            candidate = fake_binary(Path(tmp), "candidate", ["codex"], [])
            wrapped_marker = run_script(
                [
                    "verify-task",
                    "--candidate-bin",
                    str(candidate),
                    "--profile",
                    "smoke",
                    "--issue-id",
                    "WS-1",
                    "--runtime-id",
                    "runtime-1",
                    "--task-id",
                    "task-1",
                    "--expected-marker",
                    "DAEMON_",
                    "--marker-mode",
                    "contains",
                ]
            )
        self.assertEqual(wrapped_marker.returncode, 0, wrapped_marker.stderr)
        output = json.loads(wrapped_marker.stdout)
        self.assertEqual(output["task_id"], "task-1")
        self.assertEqual(output["marker_mode"], "contains")

    def test_verify_stopped_requires_offline_runtime_ledger(self) -> None:
        APIHandler.runtime_status = "offline"
        with tempfile.TemporaryDirectory() as tmp, Server() as server:
            root = Path(tmp)
            candidate = fake_binary(
                root, "candidate", ["codex"], [], daemon_status="stopped"
            )
            profile_dir = root / ".multica" / "profiles" / "smoke"
            profile_dir.mkdir(parents=True)
            (profile_dir / "config.json").write_text(
                json.dumps({"server_url": server, "token": TOKEN}), encoding="utf-8"
            )
            env = os.environ.copy()
            env["HOME"] = str(root)
            result = run_script(
                [
                    "verify-stopped",
                    "--candidate-bin",
                    str(candidate),
                    "--profile",
                    "smoke",
                    "--daemon-id",
                    "daemon-test",
                ],
                env,
            )
        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads(result.stdout)
        self.assertTrue(output["verified"])
        self.assertTrue(output["all_remaining_offline"])

    def test_verify_live_rejects_unexpected_provider(self) -> None:
        APIHandler.extra_runtime_provider = "claude"
        with tempfile.TemporaryDirectory() as tmp, Server() as server:
            result = self.run_live_verifier(Path(tmp), server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("do not exactly match", result.stderr)

    def test_verify_live_requires_every_visible_workspace(self) -> None:
        APIHandler.workspace_ids = ["workspace-1", "workspace-2"]
        with tempfile.TemporaryDirectory() as tmp, Server() as server:
            result = self.run_live_verifier(Path(tmp), server)
        self.assertEqual(result.returncode, 1)
        self.assertIn("every PAT-visible Workspace", result.stderr)

    def run_live_verifier(
        self, root: Path, server: str
    ) -> subprocess.CompletedProcess[str]:
        candidate = fake_binary(root, "candidate", ["codex"], [])
        profile_dir = root / ".multica" / "profiles" / "smoke"
        profile_dir.mkdir(parents=True)
        (profile_dir / "config.json").write_text(
            json.dumps({"server_url": server, "token": TOKEN}), encoding="utf-8"
        )
        (profile_dir / "daemon.log").write_text(
            "task wakeup websocket connected\n"
            "heartbeat: skipping HTTP tick, WS recently acked\n",
            encoding="utf-8",
        )
        env = os.environ.copy()
        env["HOME"] = str(root)
        return run_script(
            [
                "verify-live",
                "--candidate-bin",
                str(candidate),
                "--profile",
                "smoke",
                "--daemon-id",
                "daemon-test",
                "--expected-cli-version",
                "candidate-v1",
                "--expected-provider",
                "codex",
            ],
            env,
        )


if __name__ == "__main__":
    unittest.main()
