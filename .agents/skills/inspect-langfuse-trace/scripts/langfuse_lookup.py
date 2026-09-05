#!/usr/bin/env python3
"""Look up Multica traces in Langfuse by business ids.

Reads LANGFUSE_PUBLIC_KEY, LANGFUSE_SECRET_KEY and LANGFUSE_BASE_URL (or
LANGFUSE_HOST) from the environment; never prints them. Strips proxy variables
because the Langfuse host is only reachable directly.

Commands (see SKILL.md for the id -> command table):
  trace   <id>                  open one trace (UUID, 32-hex, coord_trace_id, job id, task id)
  session <cid|chat session id> traces of one conversation
  tag     <tag> [<tag>...]      traces carrying all of these tags (dash form: source-web, agent-<uuid>)
  key     <key> <value>         traces indexed by idx.<key>.<value> (task_id, issue_id, person_id, ...)
  related <id>                  the coordinator turn, memory flushes and task of one interaction
  recent  [name] [limit]        newest traces, optionally by name
  obs     <trace id>            raw observations of a trace (type, name, usage, io)
Add --json for raw JSON.
"""
import base64
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

for proxy_var in ("ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"):
    os.environ.pop(proxy_var, None)

TIMEOUT = 30
UUID_RE = re.compile(r"^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$")


class Client:
    def __init__(self) -> None:
        public = os.environ.get("LANGFUSE_PUBLIC_KEY", "").strip()
        secret = os.environ.get("LANGFUSE_SECRET_KEY", "").strip()
        base = (os.environ.get("LANGFUSE_BASE_URL") or os.environ.get("LANGFUSE_HOST") or "").strip().rstrip("/")
        if not (public and secret and base):
            raise SystemExit("set LANGFUSE_PUBLIC_KEY, LANGFUSE_SECRET_KEY and LANGFUSE_BASE_URL in the environment")
        self.base = base
        self.auth = "Basic " + base64.b64encode(f"{public}:{secret}".encode()).decode()
        self.environment = os.environ.get("LANGFUSE_QUERY_ENVIRONMENT", "").strip()

    def get(self, path: str, params: dict | None = None):
        params = {k: v for k, v in (params or {}).items() if v not in (None, "", [])}
        if self.environment and "environment" not in params and path.startswith("/api/public/traces"):
            params["environment"] = self.environment
        url = self.base + path
        if params:
            url += "?" + urllib.parse.urlencode(params, doseq=True)
        req = urllib.request.Request(url, headers={"Authorization": self.auth, "Accept": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:  # noqa: S310 - fixed https host from env
                return json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as err:
            body = err.read().decode("utf-8", "replace")[:300]
            raise SystemExit(f"HTTP {err.code} for {path}: {body}")


def trace_id_hex(raw: str) -> str:
    """Multica ids are UUIDs; the Langfuse trace id is the same UUID without dashes."""
    raw = raw.strip()
    if UUID_RE.match(raw):
        return raw.replace("-", "").lower()
    return raw


def index_token(value: str) -> str:
    """Mirror of langfuse.IndexToken: colons and whitespace become underscores."""
    return re.sub(r"[:\s]", "_", value.strip())


def tag(key: str, value: str) -> str:
    return f"{key}-{index_token(value)}"


def index_name(key: str, value: str) -> str:
    return f"idx.{key}.{index_token(value)}"


def summarize_trace(tr: dict) -> dict:
    meta = tr.get("metadata") or {}
    return {
        "id": tr.get("id"),
        "name": tr.get("name"),
        "timestamp": tr.get("timestamp"),
        "environment": tr.get("environment"),
        "session": tr.get("sessionId"),
        "tags": tr.get("tags"),
        "agent": meta.get("agent_name"),
        "conversation": meta.get("conversation_name") or meta.get("conversation_id"),
        "action": meta.get("action"),
        "status": meta.get("status"),
        "coord_trace_id": meta.get("coord_trace_id"),
        "task_id": meta.get("task_id"),
        "issue_id": meta.get("issue_id"),
        "url": f"{tr.get('htmlPath', '')}" if tr.get("htmlPath") else None,
    }


def print_traces(traces: list, as_json: bool) -> None:
    if as_json:
        print(json.dumps(traces, ensure_ascii=False, indent=2))
        return
    if not traces:
        print("no traces")
        return
    for tr in traces:
        print(json.dumps(summarize_trace(tr), ensure_ascii=False))


def cmd_trace(client: Client, args: list, as_json: bool) -> None:
    tr = client.get(f"/api/public/traces/{trace_id_hex(args[0])}")
    if as_json:
        print(json.dumps(tr, ensure_ascii=False, indent=2))
        return
    print(json.dumps(summarize_trace(tr), ensure_ascii=False))
    meta = tr.get("metadata") or {}
    print("metadata:", json.dumps({k: v for k, v in meta.items() if k not in ("attributes", "resourceAttributes", "scope")}, ensure_ascii=False))
    print("input:", json.dumps(tr.get("input"), ensure_ascii=False)[:800])
    print("output:", json.dumps(tr.get("output"), ensure_ascii=False)[:800])
    for obs in sorted(tr.get("observations", []), key=lambda o: (o.get("startTime") or "", o.get("name") or "")):
        if str(obs.get("name", "")).startswith("idx.") or obs.get("name") == "index":
            continue
        usage = obs.get("usageDetails") or {}
        print(f" - {obs.get('type')} {obs.get('name')} level={obs.get('level')} model={obs.get('model') or ''} "
              f"usage={usage.get('input', 0)}/{usage.get('output', 0)} latency_ms={obs.get('latency')} "
              f"in={json.dumps(obs.get('input'), ensure_ascii=False)[:100]} out={json.dumps(obs.get('output'), ensure_ascii=False)[:140]}")
    indexed = sorted(o.get("name") for o in tr.get("observations", []) if str(o.get("name", "")).startswith("idx."))
    if indexed:
        print("index keys:", ", ".join(indexed))


def cmd_session(client: Client, args: list, as_json: bool) -> None:
    data = client.get("/api/public/traces", {"sessionId": args[0], "limit": args[1] if len(args) > 1 else 50, "orderBy": "timestamp.desc"})
    print_traces(data.get("data", []), as_json)


def cmd_tag(client: Client, args: list, as_json: bool) -> None:
    data = client.get("/api/public/traces", {"tags": args, "limit": 50, "orderBy": "timestamp.desc"})
    print_traces(data.get("data", []), as_json)


# Ids the exporter does not index as events because a tag, the session or the
# trace id already makes them searchable; `key` falls back to those lookups so
# any key of the id table works.
TAG_KEYS = {
    "agent_id": "agent", "workspace_id": "workspace", "task_id": "task", "issue_id": "issue",
    "person_id": "user", "dws_uid": "user", "user_id": "user", "initiator_user_id": "user", "originator_user_id": "user",
}
SESSION_KEYS = {"conversation_id", "scene_key", "chat_session_id"}
TRACE_ID_KEYS = {"coord_trace_id", "job_id", "task_id", "chat_trace_id"}


def looks_like_uuid(value: str) -> bool:
    bare = value.strip().replace("-", "")
    return len(bare) == 32 and all(c in "0123456789abcdefABCDEF" for c in bare)


def traces_by_key(client: Client, key: str, value: str) -> list:
    found = {}

    def add(traces: list) -> None:
        for tr in traces:
            if tr.get("id") and tr["id"] not in found:
                found[tr["id"]] = tr

    data = client.get("/api/public/observations", {"name": index_name(key, value), "limit": 100})
    for obs in data.get("data", []):
        tid = obs.get("traceId")
        if tid and tid not in found:
            found[tid] = client.get(f"/api/public/traces/{tid}")
    if key in TAG_KEYS:
        add(client.get("/api/public/traces", {"tags": [tag(TAG_KEYS[key], value)], "limit": 50, "orderBy": "timestamp.desc"}).get("data", []))
    if key in SESSION_KEYS:
        add(client.get("/api/public/traces", {"sessionId": value, "limit": 50, "orderBy": "timestamp.desc"}).get("data", []))
    if key in TRACE_ID_KEYS and looks_like_uuid(value):
        try:
            add([client.get(f"/api/public/traces/{trace_id_hex(value)}")])
        except SystemExit:
            pass
    return sorted(found.values(), key=lambda t: t.get("timestamp") or "", reverse=True)


def cmd_key(client: Client, args: list, as_json: bool) -> None:
    print_traces(traces_by_key(client, args[0], args[1]), as_json)


def cmd_recent(client: Client, args: list, as_json: bool) -> None:
    name = args[0] if args and not args[0].isdigit() else None
    limit = next((a for a in args if a.isdigit()), "20")
    data = client.get("/api/public/traces", {"name": name, "limit": limit, "orderBy": "timestamp.desc"})
    print_traces(data.get("data", []), as_json)


def cmd_obs(client: Client, args: list, as_json: bool) -> None:
    data = client.get("/api/public/observations", {"traceId": trace_id_hex(args[0]), "limit": 200})
    if as_json:
        print(json.dumps(data, ensure_ascii=False, indent=2))
        return
    for obs in sorted(data.get("data", []), key=lambda o: (o.get("startTime") or "", o.get("name") or "")):
        print(json.dumps({k: obs.get(k) for k in ("id", "type", "name", "level", "model", "usageDetails", "startTime", "latency", "parentObservationId")}, ensure_ascii=False))


def cmd_related(client: Client, args: list, as_json: bool) -> None:
    """Coordinator turn <-> memory flush <-> task: everything that shares the id."""
    raw = args[0].strip()
    hex_id = trace_id_hex(raw)
    dashed = raw if "-" in raw else f"{hex_id[:8]}-{hex_id[8:12]}-{hex_id[12:16]}-{hex_id[16:20]}-{hex_id[20:]}"
    found = {}
    try:
        tr = client.get(f"/api/public/traces/{hex_id}")
        found[tr["id"]] = tr
    except SystemExit:
        pass
    for key in ("coord_trace_id", "job_id", "task_id", "issue_id", "chat_session_id"):
        data = client.get("/api/public/observations", {"name": index_name(key, dashed), "limit": 100})
        for obs in data.get("data", []):
            tid = obs.get("traceId")
            if tid and tid not in found:
                found[tid] = client.get(f"/api/public/traces/{tid}")
    # Task-owned traces carry task and issue ids as tags rather than events.
    for tag_key in ("task", "issue"):
        for tr in client.get("/api/public/traces", {"tags": [tag(tag_key, dashed)], "limit": 50, "orderBy": "timestamp.desc"}).get("data", []):
            found.setdefault(tr["id"], tr)
    traces = sorted(found.values(), key=lambda t: t.get("timestamp") or "")
    print_traces(traces, as_json)


COMMANDS = {
    "trace": (cmd_trace, 1), "session": (cmd_session, 1), "tag": (cmd_tag, 1), "key": (cmd_key, 2),
    "related": (cmd_related, 1), "recent": (cmd_recent, 0), "obs": (cmd_obs, 1),
}


def main(argv: list) -> None:
    as_json = "--json" in argv
    argv = [a for a in argv if a != "--json"]
    if not argv or argv[0] not in COMMANDS:
        print(__doc__)
        raise SystemExit(2)
    handler, arity = COMMANDS[argv[0]]
    if len(argv) - 1 < arity:
        print(__doc__)
        raise SystemExit(2)
    handler(Client(), argv[1:], as_json)


if __name__ == "__main__":
    main(sys.argv[1:])
