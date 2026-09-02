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


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "fc_runtime_dev.py"
RUNTIME_COMMIT = "52b615d1c0b6b84712f66364e62aa0afa1d0ec90"
RUNTIME_BRANCH = "codex/fc-runtime-dev-loop-20260903"
MULTICA_COMMIT = "2a46c86eef665eaecfbd59fa2e49762574cac079"
TEMPLATE_ID = "template-candidate-123"
TOKEN = "mul_super_secret_test_token"


class RuntimeAPIHandler(BaseHTTPRequestHandler):
    token = TOKEN
    workspace_id = "workspace-1"
    template_id = TEMPLATE_ID
    runtime_channel = "candidate"
    mutations: list[dict[str, Any]] = []
    runtimes: list[dict[str, Any]] = []

    @classmethod
    def reset(cls, channel: str = "candidate") -> None:
        cls.runtime_channel = channel
        cls.mutations = []
        cls.runtimes = [
            {
                "id": "runtime-1",
                "name": "FC candidate",
                "runtime_mode": "cloud",
                "provider": "hermes",
                "metadata": {
                    "kind": "fc-e2b",
                    "sandbox_backend": "aliyun_fc",
                    "template_channel": channel,
                    "template_id": "template-old",
                },
            }
        ]

    def log_message(self, *_args: Any) -> None:
        return

    def _authorized(self) -> bool:
        return (
            self.headers.get("Authorization") == f"Bearer {self.token}"
            and self.headers.get("X-Workspace-ID") == self.workspace_id
        )

    def _json(self, status: int, value: Any) -> None:
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _body(self) -> dict[str, Any]:
        size = int(self.headers.get("Content-Length", "0"))
        return json.loads(self.rfile.read(size) or b"{}")

    def do_GET(self) -> None:
        if not self._authorized():
            self._json(401, {"error": "invalid token"})
            return
        if self.path == "/api/runtimes/fc-e2b/stable-channel":
            self._json(
                200, {"current": None, "active_release": None, "can_publish": True}
            )
            return
        if self.path == "/api/runtimes/fc-e2b/templates":
            self._json(
                200,
                [
                    {
                        "id": self.template_id,
                        "template": "candidate-alias",
                        "status": "ready",
                        "providers": ["hermes", "opencode"],
                    }
                ],
            )
            return
        if self.path == "/api/runtimes":
            self._json(200, self.runtimes)
            return
        self._json(404, {"error": "not found"})

    def do_PATCH(self) -> None:
        if not self._authorized():
            self._json(401, {"error": "invalid token"})
            return
        if self.path != "/api/runtimes/runtime-1/fc-e2b-template":
            self._json(404, {"error": "not found"})
            return
        body = self._body()
        self.mutations.append({"method": "PATCH", "path": self.path, "body": body})
        self.runtimes[0]["metadata"]["template_id"] = body["template_id"]
        self._json(200, self.runtimes[0])

    def do_POST(self) -> None:
        if not self._authorized():
            self._json(401, {"error": "invalid token"})
            return
        if self.path != "/api/runtimes/fc-e2b":
            self._json(404, {"error": "not found"})
            return
        body = self._body()
        self.mutations.append({"method": "POST", "path": self.path, "body": body})
        runtime = {
            "id": "runtime-created",
            "name": body["name"],
            "runtime_mode": "cloud",
            "provider": body["provider"],
            "metadata": {
                "kind": "fc-e2b",
                "sandbox_backend": "aliyun_fc",
                "template_channel": "candidate",
                "template_id": body["template_id"],
            },
        }
        self.runtimes.append(runtime)
        self._json(201, runtime)


class RuntimeServer:
    def __enter__(self) -> str:
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), RuntimeAPIHandler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        host, port = self.server.server_address
        return f"http://{host}:{port}"

    def __exit__(self, *_args: Any) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


def write_fake_a1(directory: Path, status: str = "SUCCESS") -> Path:
    path = directory / "fake-a1"
    payload = f'''#!/usr/bin/env python3
import json
import sys

args = sys.argv[1:]
if args[:3] == ["ci", "pipeline", "get"]:
    print(json.dumps({{
        "id": 295064,
        "name": "FC candidate",
        "repo": "dingtalk-ai-lab/multica-fc-hermes-runtime",
        "path": ".aoneci/runtime-fc-runtime-dev-loop-candidate.yaml",
        "status": "NORMAL"
    }}))
elif args[:3] == ["ci", "pipeline", "run"]:
    if "-q" in args or args[-2:] != ["-f", "json"]:
        print("pipeline submission must use unambiguous JSON output", file=sys.stderr)
        raise SystemExit(3)
    print(json.dumps({{"id": 90000001}}))
elif args[:3] == ["ci", "run", "get"]:
    print(json.dumps({{
        "id": 90000001,
        "pipelineId": 295064,
        "status": "{status}",
        "branch": "{RUNTIME_BRANCH}",
        "commit": "{RUNTIME_COMMIT}",
        "params": {{"multica_ref": "{MULTICA_COMMIT}"}},
        "url": "https://code.example.test/run/90000001"
    }}))
elif args[:3] == ["ci", "run", "log"]:
    log = """template_id: {TEMPLATE_ID}
runtime_commit: {RUNTIME_COMMIT}
provider_fingerprint: a2eb67817f146ef4
display_alias: candidate-test
"""
    print(json.dumps({{
        "jobs": [{{"name": "build-publish-and-verify", "steps": [{{
            "name": "build-and-verify-e2b-template", "log": log
        }}]}}]
    }}))
else:
    print("unexpected fake a1 args: " + repr(args), file=sys.stderr)
    raise SystemExit(2)
'''
    path.write_text(payload, encoding="utf-8")
    path.chmod(0o755)
    return path


def run_script(
    args: list[str], env: dict[str, str]
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(SCRIPT), *args],
        text=True,
        capture_output=True,
        env=env,
        check=False,
    )


class FCRuntimeDevLoopTest(unittest.TestCase):
    def setUp(self) -> None:
        RuntimeAPIHandler.reset()

    def base_env(self, fake_a1: Path) -> dict[str, str]:
        env = os.environ.copy()
        env.update({"A1_BIN": str(fake_a1), "MULTICA_TOKEN": TOKEN})
        env.pop("FC_RUNTIME_CANDIDATE_PIPELINE_ID", None)
        env.pop("FC_RUNTIME_CANDIDATE_PIPELINE_PATH", None)
        return env

    def test_cutover_switches_only_after_successful_build_and_reads_back(self) -> None:
        with tempfile.TemporaryDirectory() as tmp, RuntimeServer() as server_url:
            fake_a1 = write_fake_a1(Path(tmp))
            result = run_script(
                [
                    "cutover",
                    "--runtime-ref",
                    RUNTIME_BRANCH,
                    "--multica-ref",
                    MULTICA_COMMIT,
                    "--runtime-id",
                    "runtime-1",
                    "--server-url",
                    server_url,
                    "--workspace-id",
                    RuntimeAPIHandler.workspace_id,
                    "--poll-interval-seconds",
                    "0.01",
                ],
                self.base_env(fake_a1),
            )

        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads(result.stdout)
        self.assertTrue(output["complete"])
        self.assertEqual(output["build"]["template_id"], TEMPLATE_ID)
        self.assertEqual(output["runtime"]["template_id"], TEMPLATE_ID)
        self.assertEqual(output["runtime"]["mutation"], "switched")
        self.assertEqual(len(RuntimeAPIHandler.mutations), 1)
        self.assertEqual(RuntimeAPIHandler.mutations[0]["method"], "PATCH")
        self.assertNotIn(TOKEN, result.stdout + result.stderr)

    def test_failed_build_blocks_runtime_mutation(self) -> None:
        with tempfile.TemporaryDirectory() as tmp, RuntimeServer() as server_url:
            fake_a1 = write_fake_a1(Path(tmp), status="FAILED")
            result = run_script(
                [
                    "cutover",
                    "--runtime-ref",
                    RUNTIME_BRANCH,
                    "--multica-ref",
                    MULTICA_COMMIT,
                    "--runtime-id",
                    "runtime-1",
                    "--server-url",
                    server_url,
                    "--workspace-id",
                    RuntimeAPIHandler.workspace_id,
                    "--poll-interval-seconds",
                    "0.01",
                ],
                self.base_env(fake_a1),
            )

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("candidate Runtime mutation is blocked", result.stderr)
        self.assertEqual(RuntimeAPIHandler.mutations, [])
        self.assertNotIn(TOKEN, result.stdout + result.stderr)

    def test_stable_runtime_cannot_be_switched(self) -> None:
        RuntimeAPIHandler.reset(channel="stable")
        with tempfile.TemporaryDirectory() as tmp, RuntimeServer() as server_url:
            fake_a1 = write_fake_a1(Path(tmp))
            result = run_script(
                [
                    "switch",
                    "--template-id",
                    TEMPLATE_ID,
                    "--runtime-id",
                    "runtime-1",
                    "--server-url",
                    server_url,
                    "--workspace-id",
                    RuntimeAPIHandler.workspace_id,
                ],
                self.base_env(fake_a1),
            )

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("stable-managed", result.stderr)
        self.assertEqual(RuntimeAPIHandler.mutations, [])

    def test_create_uses_candidate_channel_and_private_visibility(self) -> None:
        with tempfile.TemporaryDirectory() as tmp, RuntimeServer() as server_url:
            fake_a1 = write_fake_a1(Path(tmp))
            result = run_script(
                [
                    "create",
                    "--template-id",
                    TEMPLATE_ID,
                    "--name",
                    "isolated candidate",
                    "--server-url",
                    server_url,
                    "--workspace-id",
                    RuntimeAPIHandler.workspace_id,
                ],
                self.base_env(fake_a1),
            )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            RuntimeAPIHandler.mutations[0]["body"]["template_channel"], "candidate"
        )
        self.assertEqual(
            RuntimeAPIHandler.mutations[0]["body"]["visibility"], "private"
        )
        self.assertEqual(json.loads(result.stdout)["runtime"]["mutation"], "created")

    def test_dry_run_never_prints_token(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            fake_a1 = write_fake_a1(Path(tmp))
            env = self.base_env(fake_a1)
            env.update(
                {
                    "MULTICA_SERVER_URL": "https://pre.example.test",
                    "MULTICA_WORKSPACE_ID": RuntimeAPIHandler.workspace_id,
                }
            )
            result = run_script(
                [
                    "cutover",
                    "--runtime-ref",
                    RUNTIME_BRANCH,
                    "--multica-ref",
                    MULTICA_COMMIT,
                    "--runtime-id",
                    "runtime-1",
                    "--dry-run",
                ],
                env,
            )

        self.assertEqual(result.returncode, 0, result.stderr)
        output = result.stdout + result.stderr
        self.assertNotIn(TOKEN, output)
        self.assertIn("environment-or-profile (value never printed)", output)

    def test_default_pipeline_rejects_another_feature_branch(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            fake_a1 = write_fake_a1(Path(tmp))
            result = run_script(
                [
                    "build",
                    "--runtime-ref",
                    "codex/someone-elses-branch",
                    "--multica-ref",
                    MULTICA_COMMIT,
                    "--dry-run",
                ],
                self.base_env(fake_a1),
            )

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("dedicated --pipeline-id and --pipeline-path", result.stderr)


if __name__ == "__main__":
    unittest.main()
