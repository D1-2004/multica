#!/usr/bin/env python3
"""Task-authenticated client for the Multica Factory Bot workflow.

The helper uses only the Python standard library. It consumes workspace
template snapshots already persisted by Multica; Agent creation never reads a
Git repository or compiles a local compatibility snapshot.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Any, Dict, Optional, Sequence
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


DEFAULT_TIMEOUT_SECONDS = 60


class FactoryAPIError(RuntimeError):
    """A safe, user-displayable API or configuration failure."""


class FactoryAPI:
    def __init__(self, server_url: str, workspace_id: str, token: str) -> None:
        self.server_url = server_url.rstrip("/")
        self.workspace_id = workspace_id.strip()
        self.token = token.strip()
        if not self.server_url:
            raise FactoryAPIError("MULTICA_SERVER_URL is required")
        if not self.workspace_id:
            raise FactoryAPIError("MULTICA_WORKSPACE_ID is required")
        if not self.token:
            raise FactoryAPIError("MULTICA_TOKEN is required")
        if not self.token.startswith("mat_"):
            raise FactoryAPIError(
                "MULTICA_TOKEN must be a task-scoped mat_ token inside an Agent task"
            )

    @classmethod
    def from_environment(cls) -> "FactoryAPI":
        return cls(
            os.environ.get("MULTICA_SERVER_URL", ""),
            os.environ.get("MULTICA_WORKSPACE_ID", ""),
            os.environ.get("MULTICA_TOKEN", ""),
        )

    def request(
        self, method: str, path: str, body: Optional[Dict[str, Any]] = None
    ) -> Any:
        payload = None if body is None else json.dumps(body).encode("utf-8")
        request = Request(
            self.server_url + path,
            data=payload,
            method=method,
            headers={
                "Accept": "application/json",
                "Authorization": "Bearer " + self.token,
                "Content-Type": "application/json",
                "User-Agent": "multica-factory-template/2",
                "X-Workspace-ID": self.workspace_id,
            },
        )
        try:
            with urlopen(request, timeout=DEFAULT_TIMEOUT_SECONDS) as response:
                raw = response.read()
        except HTTPError as exc:
            raw = exc.read()
            raise FactoryAPIError(
                f"Multica API returned HTTP {exc.code}: {_error_message(raw)}"
            ) from None
        except URLError as exc:
            raise FactoryAPIError(f"cannot reach Multica API: {exc.reason}") from None
        if not raw:
            return {}
        try:
            return json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise FactoryAPIError("Multica API returned invalid JSON") from None

    def template_list(self) -> Any:
        return self.request("GET", self._workspace_path("agent-templates"))

    def template_get(self, slug: str) -> Any:
        return self.request(
            "GET", self._workspace_path("agent-templates", quote(slug, safe=""))
        )

    def runtime_list(self) -> Any:
        return self.request("GET", "/api/runtimes")

    def agent_list(self) -> Any:
        return self.request(
            "GET", "/api/agents?" + urlencode({"workspace_id": self.workspace_id})
        )

    def agent_get(self, agent_id: str) -> Any:
        return self.request("GET", "/api/agents/" + quote(agent_id, safe=""))

    def agent_create(
        self, template_slug: str, runtime_id: str, name: str, description: str
    ) -> Any:
        return self.request(
            "POST",
            self._workspace_path(
                "agent-templates", quote(template_slug, safe=""), "agents"
            ),
            {
                "runtime_id": runtime_id,
                "name": name,
                "description": description,
            },
        )

    def dingtalk_begin(self, agent_id: str, allow_unbound: bool = False) -> Any:
        params = {"agent_id": agent_id}
        if allow_unbound:
            params["allow_unbound"] = "true"
        return self.request(
            "POST",
            self._workspace_path("dingtalk", "install", "begin")
            + "?"
            + urlencode(params),
            {},
        )

    def dingtalk_status(self, session_id: str) -> Any:
        return self.request(
            "GET",
            self._workspace_path(
                "dingtalk", "install", quote(session_id, safe=""), "status"
            ),
        )

    def _workspace_path(self, *parts: str) -> str:
        suffix = "/".join(part.strip("/") for part in parts)
        return f"/api/workspaces/{quote(self.workspace_id, safe='')}/{suffix}"


def _error_message(raw: bytes) -> str:
    try:
        decoded = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return raw.decode("utf-8", errors="replace").strip()[:500]
    if isinstance(decoded, dict):
        for key in ("error", "message"):
            value = decoded.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
    return json.dumps(decoded, ensure_ascii=False)[:500]


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Call Multica Factory Bot APIs")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("template-list")
    template_get = commands.add_parser("template-get")
    template_get.add_argument("template_slug")
    commands.add_parser("runtime-list")
    commands.add_parser("agent-list")
    agent_get = commands.add_parser("agent-get")
    agent_get.add_argument("agent_id")
    agent_create = commands.add_parser("agent-create")
    agent_create.add_argument("template_slug")
    agent_create.add_argument("--runtime-id", required=True)
    agent_create.add_argument("--name", required=True)
    agent_create.add_argument("--description", required=True)
    dingtalk_begin = commands.add_parser("dingtalk-begin")
    dingtalk_begin.add_argument("--agent-id", required=True)
    dingtalk_begin.add_argument("--allow-unbound", action="store_true")
    dingtalk_status = commands.add_parser("dingtalk-status")
    dingtalk_status.add_argument("session_id")
    return parser


def run(api: FactoryAPI, args: argparse.Namespace) -> Any:
    if args.command == "template-list":
        return api.template_list()
    if args.command == "template-get":
        return api.template_get(args.template_slug)
    if args.command == "runtime-list":
        return api.runtime_list()
    if args.command == "agent-list":
        return api.agent_list()
    if args.command == "agent-get":
        return api.agent_get(args.agent_id)
    if args.command == "agent-create":
        return api.agent_create(
            args.template_slug, args.runtime_id, args.name, args.description
        )
    if args.command == "dingtalk-begin":
        return api.dingtalk_begin(args.agent_id, args.allow_unbound)
    if args.command == "dingtalk-status":
        return api.dingtalk_status(args.session_id)
    raise FactoryAPIError("unsupported command")


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        result = run(FactoryAPI.from_environment(), args)
    except FactoryAPIError as exc:
        print(f"factory-api: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
