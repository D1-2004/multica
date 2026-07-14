#!/usr/bin/env python3
"""Token-authenticated smoke test for Multica Chat Session/Turn APIs."""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import uuid
from dataclasses import dataclass
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen


TERMINAL_STATUSES = {"completed", "failed", "cancelled"}


class SmokeError(RuntimeError):
    pass


@dataclass(frozen=True)
class SmokeConfig:
    base_url: str
    token: str
    workspace_id: str
    agent_id: str
    session_key: str
    reply_template: str
    reply_config: dict[str, Any]
    messages: tuple[str, ...]
    timeout: float
    poll_interval: float
    expect_reply_contains: str


class APIClient:
    def __init__(self, config: SmokeConfig) -> None:
        self.base_url = config.base_url.rstrip("/")
        self.token = config.token
        self.workspace_id = config.workspace_id

    def request(self, method: str, path: str, body: Any | None = None) -> Any:
        headers = {
            "Accept": "application/json",
            "Authorization": f"Bearer {self.token}",
            "User-Agent": "multica-chat-api-smoke/1",
            "X-Client-Platform": "api-smoke",
        }
        data = None
        if self.workspace_id:
            headers["X-Workspace-ID"] = self.workspace_id
        if body is not None:
            headers["Content-Type"] = "application/json"
            data = json.dumps(body, ensure_ascii=False).encode("utf-8")
        request = Request(self.base_url + path, data=data, headers=headers, method=method)
        try:
            with urlopen(request, timeout=30) as response:
                payload = response.read()
        except HTTPError as exc:
            detail = exc.read(4096).decode("utf-8", errors="replace")
            raise SmokeError(f"{method} {path} returned HTTP {exc.code}: {detail}") from exc
        except URLError as exc:
            raise SmokeError(f"{method} {path} failed: {exc.reason}") from exc
        if not payload:
            return None
        try:
            return json.loads(payload)
        except json.JSONDecodeError as exc:
            raise SmokeError(f"{method} {path} returned invalid JSON") from exc


def require_text(value: Any, field: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise SmokeError(f"response field {field!r} is missing or empty")
    return value


def wait_for_turn(
    client: APIClient,
    session_id: str,
    turn_id: str,
    timeout: float,
    poll_interval: float,
) -> dict[str, Any]:
    deadline = time.monotonic() + timeout
    path = f"/api/chat/sessions/{quote(session_id, safe='')}/turns/{quote(turn_id, safe='')}"
    while True:
        turn = client.request("GET", path)
        if not isinstance(turn, dict):
            raise SmokeError("Turn endpoint did not return an object")
        status = turn.get("status")
        if status in TERMINAL_STATUSES:
            return turn
        if time.monotonic() >= deadline:
            raise SmokeError(f"Turn {turn_id} did not finish within {timeout:g}s (last status={status!r})")
        time.sleep(poll_interval)


def run_smoke(config: SmokeConfig) -> dict[str, Any]:
    client = APIClient(config)
    key_path = "/api/chat/sessions/by-key/" + quote(config.session_key, safe="")
    create_body = {
        "agent_id": config.agent_id,
        "title": "Chat API smoke test",
        "reply_template": config.reply_template,
        "reply_config": config.reply_config,
    }

    first = client.request("PUT", key_path, create_body)
    second = client.request("PUT", key_path, create_body)
    if not isinstance(first, dict) or not isinstance(second, dict):
        raise SmokeError("Session create endpoint did not return objects")
    session_id = require_text(first.get("id"), "session.id")
    if second.get("id") != session_id:
        raise SmokeError("SessionKey create is not idempotent: repeated PUT returned another Session")
    if first.get("session_key") != config.session_key:
        raise SmokeError("Session response did not preserve session_key")
    if config.reply_template and first.get("reply_template") != config.reply_template:
        raise SmokeError("Session response did not preserve reply_template")

    turns: list[dict[str, Any]] = []
    for message in config.messages:
        sent = client.request("POST", key_path + "/messages", {"content": message})
        if not isinstance(sent, dict):
            raise SmokeError("Message endpoint did not return an object")
        turn_id = require_text(sent.get("task_id"), "message.task_id")
        turn = wait_for_turn(client, session_id, turn_id, config.timeout, config.poll_interval)
        if turn.get("status") != "completed":
            raise SmokeError(
                f"Turn {turn_id} ended as {turn.get('status')!r}: "
                f"{turn.get('error') or turn.get('failure_reason') or 'no error detail'}"
            )
        reply = turn.get("reply")
        content = reply.get("content") if isinstance(reply, dict) else None
        require_text(content, "turn.reply.content")
        if config.expect_reply_contains and config.expect_reply_contains not in content:
            raise SmokeError(
                f"Turn {turn_id} reply does not contain {config.expect_reply_contains!r}"
            )
        if config.reply_template and turn.get("reply_template") != config.reply_template:
            raise SmokeError(f"Turn {turn_id} did not snapshot reply_template")
        turns.append(turn)

    listed = client.request(
        "GET", f"/api/chat/sessions/{quote(session_id, safe='')}/turns"
    )
    if not isinstance(listed, list):
        raise SmokeError("Turn list endpoint did not return an array")
    listed_ids = {turn.get("id") for turn in listed if isinstance(turn, dict)}
    missing = [turn["id"] for turn in turns if turn.get("id") not in listed_ids]
    if missing:
        raise SmokeError(f"Turn list is missing newly created ids: {missing}")

    return {
        "ok": True,
        "session_id": session_id,
        "session_key": config.session_key,
        "turns": [
            {
                "id": turn.get("id"),
                "status": turn.get("status"),
                "reply_delivery_status": turn.get("reply_delivery_status"),
                "reply": turn.get("reply"),
            }
            for turn in turns
        ],
    }


def parse_json_object(raw: str) -> dict[str, Any]:
    if not raw:
        return {}
    if raw.startswith("@"):
        with open(raw[1:], "r", encoding="utf-8") as handle:
            raw = handle.read()
    try:
        value = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise argparse.ArgumentTypeError(f"invalid JSON: {exc}") from exc
    if not isinstance(value, dict):
        raise argparse.ArgumentTypeError("reply config must be a JSON object")
    return value


def parse_args(argv: list[str] | None = None) -> SmokeConfig:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default=os.getenv("MULTICA_SERVER_URL", ""))
    parser.add_argument("--token", default=os.getenv("MULTICA_TOKEN", ""))
    parser.add_argument("--workspace-id", default=os.getenv("MULTICA_WORKSPACE_ID", ""))
    parser.add_argument("--agent-id", default=os.getenv("MULTICA_AGENT_ID", ""))
    parser.add_argument("--session-key", default=f"api-smoke:{uuid.uuid4()}")
    parser.add_argument("--reply-template", default="")
    parser.add_argument("--reply-config", type=parse_json_object, default={})
    parser.add_argument("--message", action="append", dest="messages")
    parser.add_argument("--timeout", type=float, default=900)
    parser.add_argument("--poll-interval", type=float, default=2)
    parser.add_argument("--expect-reply-contains", default="")
    args = parser.parse_args(argv)

    missing = [
        name
        for name, value in (
            ("--base-url or MULTICA_SERVER_URL", args.base_url),
            ("--token or MULTICA_TOKEN", args.token),
            ("--agent-id or MULTICA_AGENT_ID", args.agent_id),
        )
        if not str(value).strip()
    ]
    if missing:
        parser.error("missing " + ", ".join(missing))
    if args.timeout <= 0 or args.poll_interval < 0:
        parser.error("--timeout must be positive and --poll-interval cannot be negative")
    messages = tuple(args.messages or ("Reply with a short API smoke-test acknowledgement.",))
    return SmokeConfig(
        base_url=args.base_url,
        token=args.token,
        workspace_id=args.workspace_id,
        agent_id=args.agent_id,
        session_key=args.session_key,
        reply_template=args.reply_template,
        reply_config=args.reply_config,
        messages=messages,
        timeout=args.timeout,
        poll_interval=args.poll_interval,
        expect_reply_contains=args.expect_reply_contains,
    )


def main(argv: list[str] | None = None) -> int:
    try:
        result = run_smoke(parse_args(argv))
    except (SmokeError, OSError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
