#!/usr/bin/env python3

from __future__ import annotations

import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from chat_api_smoke import SmokeConfig, SmokeTransportError, run_smoke, wait_for_turn


class FakeMulticaHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"
    auth_headers: list[str] = []
    turn_reads = 0

    def log_message(self, *_args: object) -> None:
        pass

    def _reply(self, status: int, payload: object) -> None:
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _record_auth(self) -> None:
        type(self).auth_headers.append(self.headers.get("Authorization", ""))

    def do_PUT(self) -> None:  # noqa: N802
        self._record_auth()
        self._reply(
            200,
            {
                "id": "session-1",
                "session_key": "smoke-key",
                "reply_template": "dws-reply",
            },
        )

    def do_POST(self) -> None:  # noqa: N802
        self._record_auth()
        self._reply(201, {"task_id": "turn-1", "reply_template": "dws-reply"})

    def do_GET(self) -> None:  # noqa: N802
        self._record_auth()
        if self.path.endswith("/turns"):
            self._reply(200, [{"id": "turn-1"}])
            return
        type(self).turn_reads += 1
        if type(self).turn_reads == 1:
            self._reply(200, {"id": "turn-1", "status": "running"})
            return
        self._reply(
            200,
            {
                "id": "turn-1",
                "status": "completed",
                "reply_template": "dws-reply",
                "reply_delivery_status": "pending",
                "reply": {"content": "API smoke OK"},
            },
        )


class ChatAPISmokeTest(unittest.TestCase):
    def setUp(self) -> None:
        FakeMulticaHandler.auth_headers = []
        FakeMulticaHandler.turn_reads = 0
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), FakeMulticaHandler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)

    def test_idempotent_session_send_wait_and_list(self) -> None:
        config = SmokeConfig(
            base_url=f"http://127.0.0.1:{self.server.server_port}",
            token="mul_test_secret",
            workspace_id="workspace-1",
            agent_id="agent-1",
            session_key="smoke-key",
            reply_template="dws-reply",
            reply_config={"mode": "reply"},
            messages=("hello",),
            timeout=2,
            poll_interval=0,
            expect_reply_contains="smoke OK",
        )
        result = run_smoke(config)
        self.assertTrue(result["ok"])
        self.assertEqual(result["session_id"], "session-1")
        self.assertEqual(result["turns"][0]["reply_delivery_status"], "pending")
        self.assertTrue(FakeMulticaHandler.auth_headers)
        self.assertEqual(set(FakeMulticaHandler.auth_headers), {"Bearer mul_test_secret"})

    def test_turn_poll_retries_transient_transport_error(self) -> None:
        class FlakyClient:
            calls = 0

            def request(self, _method: str, _path: str) -> dict[str, object]:
                self.calls += 1
                if self.calls == 1:
                    raise SmokeTransportError("connection reset")
                return {"id": "turn-1", "status": "completed"}

        client = FlakyClient()
        turn = wait_for_turn(client, "session-1", "turn-1", timeout=1, poll_interval=0)
        self.assertEqual(turn["status"], "completed")
        self.assertEqual(client.calls, 2)


if __name__ == "__main__":
    unittest.main()
