#!/usr/bin/env python3
"""Look up Multica traces in Langfuse by business ids and inbound text.

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
  search  [--agent id|name] [--text needle] [--name inbound_coordinator]
                                find traces by agent (UUID or 花名) and/or inbound text
  obs     <trace id>            raw observations of a trace (type, name, usage, io)

Global flags (anywhere): --json  --limit N  --from ISO|7d|24h  --environment pre|production
A bare UUID passed to `tag` is treated as agent-<uuid>. A non-prefixed 花名 is
treated as agent_name-<name> (new traces) and metadata.agent_name (old traces).
"""
from __future__ import annotations

import base64
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

for proxy_var in ("ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"):
    os.environ.pop(proxy_var, None)

TIMEOUT = 60
UUID_RE = re.compile(r"^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$")
KNOWN_TAG_PREFIXES = (
    "agent-", "agent_name-", "workspace-", "user-", "task-", "issue-",
    "source-", "kind-", "runtime-", "provider-", "channel-",
)
KNOWN_EXACT_TAGS = {"inbound_coordinator", "scene_memory", "agent_task"}
DEFAULT_SEARCH_WINDOW = "7d"
PAGE_SIZE = 50
MAX_HYDRATE = 80


class Client:
    def __init__(self, environment: str = "", to_timestamp: str = "", max_pages: int = 0) -> None:
        env_file = Path.home() / ".grok" / "langfuse.env"
        if env_file.is_file():
            for raw in env_file.read_text().splitlines():
                line = raw.strip().removeprefix("export ")
                if line and not line.startswith("#") and "=" in line:
                    key, value = line.split("=", 1)
                    if key.strip().startswith("LANGFUSE_"):
                        os.environ.setdefault(key.strip(), value.strip().strip("\"'"))
        public = os.environ.get("LANGFUSE_PUBLIC_KEY", "").strip()
        secret = os.environ.get("LANGFUSE_SECRET_KEY", "").strip()
        base = (os.environ.get("LANGFUSE_BASE_URL") or os.environ.get("LANGFUSE_HOST") or "").strip().rstrip("/")
        if not (public and secret and base):
            raise SystemExit("set LANGFUSE_PUBLIC_KEY, LANGFUSE_SECRET_KEY and LANGFUSE_BASE_URL in the environment")
        self.base = base
        self.auth = "Basic " + base64.b64encode(f"{public}:{secret}".encode()).decode()
        self.environment = environment.strip() or os.environ.get("LANGFUSE_QUERY_ENVIRONMENT", "").strip()
        self.to_timestamp = to_timestamp
        self.from_timestamp = ""
        self.max_pages = max_pages
        self.scan_limit = None
        self.hydrate_limit = MAX_HYDRATE
        self.workers = 4
        self.last_scan = {}
        self.scans = []
        self.requests = []
        self.search_stats = {}
        self.started = time.monotonic()

    def get(self, path: str, params: dict | None = None):
        params = {k: v for k, v in (params or {}).items() if v not in (None, "", [])}
        if path == "/api/public/traces":
            if self.environment:
                params.setdefault("environment", self.environment)
            if self.to_timestamp:
                params.setdefault("toTimestamp", self.to_timestamp)
        url = self.base + path
        if params:
            url += "?" + urllib.parse.urlencode(params, doseq=True)
        req = urllib.request.Request(url, headers={"Authorization": self.auth, "Accept": "application/json"})
        started = time.monotonic()
        try:
            with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:  # noqa: S310 - fixed https host from env
                data = json.loads(resp.read().decode("utf-8"))
                if path == "/api/public/observations":
                    count = len(data.get("data") or [])
                    total = (data.get("meta") or {}).get("totalItems")
                    if total is not None and total > count:
                        self.scans.append({"resource": "observations", "returned": count,
                                           "api_total_rows": total, "truncated": True})
                return data
        except urllib.error.HTTPError as err:
            body = err.read().decode("utf-8", "replace")[:300]
            raise SystemExit(f"HTTP {err.code} for {path}: {body}")
        finally:
            self.requests.append({"path": path, "elapsed_ms": round((time.monotonic() - started) * 1000)})

    def in_scope(self, trace: dict) -> bool:
        if self.environment and trace.get("environment") != self.environment:
            return False
        timestamp = timestamp_key(trace.get("timestamp"))
        return not ((self.from_timestamp and timestamp < timestamp_key(self.from_timestamp))
                    or (self.to_timestamp and timestamp >= timestamp_key(self.to_timestamp)))

    def report_stats(self, force: bool = False) -> None:
        truncated = any(s.get("truncated") for s in self.scans) or self.search_stats.get("truncated", False)
        if force or truncated:
            print("# query_stats " + json.dumps({
                "requests": len(self.requests), "request_elapsed_ms": sum(r["elapsed_ms"] for r in self.requests),
                "wall_elapsed_ms": round((time.monotonic() - self.started) * 1000),
                "scans": self.scans, "search": self.search_stats, "truncated": truncated,
            }, ensure_ascii=False), file=sys.stderr)


def looks_like_uuid(value: str) -> bool:
    return bool(UUID_RE.match((value or "").strip()))


def timestamp_key(value: str | None) -> float:
    if not value:
        return 0
    return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()


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


def expand_tag(raw: str) -> str:
    """Map a bare agent UUID or 花名 to the dash-style tag this Langfuse can filter on."""
    raw = (raw or "").strip()
    if not raw:
        return raw
    if raw in KNOWN_EXACT_TAGS or raw.startswith(KNOWN_TAG_PREFIXES):
        return raw
    if looks_like_uuid(raw):
        return tag("agent", raw)
    return tag("agent_name", raw)


def parse_from_timestamp(raw: str | None) -> str | None:
    if not raw:
        return None
    raw = raw.strip()
    if raw.isdigit():
        return datetime.fromtimestamp(int(raw), tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    m = re.fullmatch(r"(\d+)([smhd])", raw)
    if m:
        n = int(m.group(1))
        unit = m.group(2)
        delta = {"s": timedelta(seconds=n), "m": timedelta(minutes=n), "h": timedelta(hours=n), "d": timedelta(days=n)}[unit]
        return (datetime.now(timezone.utc) - delta).strftime("%Y-%m-%dT%H:%M:%SZ")
    return raw


def parse_global_flags(argv: list[str]) -> tuple[list[str], dict]:
    """Strip --json/--limit/--from/--environment so they never become tags."""
    flags = {"json": False, "stats": False, "limit": None, "from": None, "to": None,
             "environment": "", "pages": 0, "scan": None, "hydrate": MAX_HYDRATE, "workers": 4}
    out: list[str] = []
    i = 0
    while i < len(argv):
        a = argv[i]
        if a in ("--json", "--stats"):
            flags[a[2:]] = True
            i += 1
            continue
        key = a.split("=", 1)[0]
        if key in ("--limit", "--from", "--to", "--environment", "--env", "--pages", "--scan", "--hydrate", "--workers"):
            if "=" in a:
                value = a.split("=", 1)[1]
                consumed = 1
            elif i + 1 < len(argv):
                value = argv[i + 1]
                consumed = 2
            else:
                raise SystemExit(f"{key} needs a value")
            name = "environment" if key == "--env" else key[2:]
            if name in ("limit", "pages", "scan", "hydrate", "workers"):
                try:
                    value = int(value)
                except ValueError:
                    raise SystemExit(f"{key} needs an integer")
                if value < 0 or (name in ("limit", "scan", "workers") and value == 0):
                    raise SystemExit(f"{key} is out of range")
            flags[name] = value
            i += consumed
            continue
        out.append(a)
        i += 1
    return out, flags


def clip_preview(value, n: int = 160) -> str:
    if value is None:
        return ""
    if isinstance(value, (dict, list)):
        text = json.dumps(value, ensure_ascii=False)
    else:
        text = str(value)
    text = " ".join(text.split())
    if len(text) <= n:
        return text
    return text[: n - 1] + "…"


def coordinator_roots(tr: dict) -> list:
    return sorted([o for o in tr.get("observations", [])
                   if isinstance(o, dict) and o.get("name") == "inbound_coordinator"],
                  key=lambda o: o.get("startTime") or "")


def coordinator_root(tr: dict) -> dict:
    roots = coordinator_roots(tr)
    return max(roots, key=lambda o: o.get("endTime") or o.get("startTime") or "") if roots else {}


def trace_io(tr: dict) -> tuple:
    # A later task root can overwrite the shared trace's timestamp and I/O.
    root = coordinator_root(tr)
    return (coordinator_roots(tr)[0].get("input"), root.get("output")) if root else (tr.get("input"), tr.get("output"))


def summarize_trace(tr: dict) -> dict:
    meta = tr.get("metadata") or {}
    inp, output = trace_io(tr)
    user_text = ""
    if isinstance(output, dict):
        user_text = str(output.get("user_text") or output.get("text") or "")
    elif isinstance(output, str):
        user_text = output
    return {
        "id": tr.get("id"),
        "name": tr.get("name"),
        "timestamp": tr.get("timestamp"),
        "environment": tr.get("environment"),
        "session": tr.get("sessionId"),
        "tags": tr.get("tags"),
        "agent": meta.get("agent_name"),
        "agent_id": meta.get("agent_id"),
        "conversation": meta.get("conversation_name") or meta.get("conversation_id"),
        "action": output.get("action", meta.get("action")) if isinstance(output, dict) else meta.get("action"),
        "status": meta.get("status"),
        "coord_trace_id": meta.get("coord_trace_id"),
        "task_id": meta.get("task_id"),
        "issue_id": meta.get("issue_id"),
        "input_preview": clip_preview(inp),
        "user_text_preview": clip_preview(user_text),
        "io_source": "coordinator_first_input_latest_output" if coordinator_root(tr) else "trace",
        "coordinator_attempts": [
            {"startTime": o.get("startTime"), "action": (o.get("output") or {}).get("action") if isinstance(o.get("output"), dict) else None,
             "level": o.get("level"), "error": o.get("statusMessage")}
            for o in coordinator_roots(tr)
        ],
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


def list_traces(client: Client, params: dict, limit: int) -> list:
    found: dict[str, dict] = {}
    page = 1
    page_size = min(PAGE_SIZE, max(limit, 1))
    raw_count = 0
    total = None
    exhausted = False
    page_cap = getattr(client, "max_pages", 0)
    while len(found) < limit and (not page_cap or page <= page_cap):
        query = dict(params)
        query["limit"] = page_size
        query["page"] = page
        query.setdefault("orderBy", "timestamp.desc")
        data = client.get("/api/public/traces", query)
        batch = data.get("data") or []
        raw_count += len(batch)
        total = (data.get("meta") or {}).get("totalItems", total)
        for tr in batch:
            tid = tr.get("id")
            if tid and (tid not in found or timestamp_key(tr.get("timestamp")) > timestamp_key(found[tid].get("timestamp"))):
                found[tid] = tr
        if len(batch) < page_size or (total is not None and raw_count >= total):
            exhausted = True
            break
        page += 1
    result = sorted(found.values(), key=lambda t: timestamp_key(t.get("timestamp")), reverse=True)
    client.last_scan = {"pages": page if exhausted else page - 1, "raw_count": raw_count,
                        "unique_count": len(found), "duplicate_count": raw_count - len(found),
                        "api_total_rows": total, "returned": min(len(result), limit),
                        "limit": limit, "page_limit": page_cap,
                        "truncated": not exhausted or len(result) > limit}
    if not hasattr(client, "scans"):
        client.scans = []
    client.scans.append(client.last_scan)
    return result[:limit]


def trace_search_blob(tr: dict) -> str:
    inp, output = trace_io(tr)
    parts = [
        json.dumps(inp, ensure_ascii=False),
        json.dumps(output, ensure_ascii=False),
        json.dumps(tr.get("metadata") or {}, ensure_ascii=False),
        str(tr.get("sessionId") or ""),
        str(tr.get("name") or ""),
        " ".join(tr.get("tags") or []),
    ]
    return "\n".join(parts)


def blob_has_text(blob: str, needle: str) -> bool:
    if not needle:
        return True
    return needle.casefold() in blob.casefold()


def input_missing(tr: dict) -> bool:
    inp = tr.get("input")
    if inp in (None, "", {}, [], "null", "None"):
        return True
    if isinstance(inp, str) and inp.strip() in ("null", "None", "{}"):
        return True
    return False


def hydrate_if_needed(client: Client, tr: dict, needle: str, hydrate_budget: list[int]) -> dict:
    blob = trace_search_blob(tr)
    if blob_has_text(blob, needle):
        return tr
    # List payloads often omit input or serialize it as the string "null".
    # If the needle is not already in the list row, fetch the full trace.
    if not needle or hydrate_budget[0] <= 0:
        return tr
    tid = tr.get("id")
    if not tid:
        return tr
    hydrate_budget[0] -= 1
    return client.get(f"/api/public/traces/{tid}")


def search_text(client: Client, candidates: list, needle: str, limit: int) -> list:
    hits = []
    hydrated = 0
    examined = 0
    skipped = 0
    workers = min(max(client.workers, 1), 16)
    with ThreadPoolExecutor(max_workers=workers) as pool:
        for start in range(0, len(candidates), workers):
            batch = candidates[start:start + workers]
            pending = []
            for tr in batch:
                if blob_has_text(trace_search_blob(tr), needle):
                    pending.append((tr, None))
                elif hydrated < client.hydrate_limit and tr.get("id"):
                    pending.append((tr, pool.submit(client.get, f"/api/public/traces/{tr['id']}")))
                    hydrated += 1
                else:
                    pending.append((tr, None))
                    skipped += 1
            for tr, future in pending:
                full = future.result() if future else tr
                examined += 1
                if blob_has_text(trace_search_blob(full), needle) and client.in_scope(full):
                    hits.append(full)
            if len(hits) >= limit:
                break
    client.search_stats = {"candidates": len(candidates), "examined": examined, "hydrated": hydrated,
                           "hydrate_limit": client.hydrate_limit, "unhydrated": skipped,
                           "workers": workers, "returned": min(len(hits), limit),
                           "truncated": skipped > 0 or examined < len(candidates) or len(hits) > limit}
    return hits[:limit]


def merge_traces(*groups: list) -> list:
    found: dict[str, dict] = {}
    for group in groups:
        for tr in group:
            tid = tr.get("id")
            if tid and (tid not in found or timestamp_key(tr.get("timestamp")) > timestamp_key(found[tid].get("timestamp"))):
                found[tid] = tr
    return sorted(found.values(), key=lambda t: timestamp_key(t.get("timestamp")), reverse=True)


def cmd_trace(client: Client, args: list, as_json: bool, _limit: int | None, _from_ts: str | None) -> None:
    tr = client.get(f"/api/public/traces/{trace_id_hex(args[0])}")
    if not client.in_scope(tr):
        raise SystemExit("trace is outside the requested environment/time window")
    if as_json:
        print(json.dumps(tr, ensure_ascii=False, indent=2))
        return
    print(json.dumps(summarize_trace(tr), ensure_ascii=False))
    meta = tr.get("metadata") or {}
    print("metadata:", json.dumps({k: v for k, v in meta.items() if k not in ("attributes", "resourceAttributes", "scope")}, ensure_ascii=False))
    inp, output = trace_io(tr)
    if coordinator_root(tr):
        print("io_source: first Coordinator attempt input / latest attempt output; see coordinator_attempts in summary")
    print("input:", json.dumps(inp, ensure_ascii=False)[:800])
    print("output:", json.dumps(output, ensure_ascii=False)[:800])
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


def cmd_session(client: Client, args: list, as_json: bool, limit: int | None, from_ts: str | None) -> None:
    params = {"sessionId": args[0]}
    if from_ts:
        params["fromTimestamp"] = from_ts
    traces = list_traces(client, params, limit or (int(args[1]) if len(args) > 1 and args[1].isdigit() else 50))
    print_traces(traces, as_json)


def cmd_tag(client: Client, args: list, as_json: bool, limit: int | None, from_ts: str | None) -> None:
    tags = [expand_tag(a) for a in args]
    params = {"tags": tags}
    if from_ts:
        params["fromTimestamp"] = from_ts
    print_traces(list_traces(client, params, limit or 50), as_json)


TAG_KEYS = {
    "agent_id": "agent", "workspace_id": "workspace", "task_id": "task", "issue_id": "issue",
    "person_id": "user", "dws_uid": "user", "user_id": "user", "initiator_user_id": "user", "originator_user_id": "user",
    "agent_name": "agent_name",
}
SESSION_KEYS = {"conversation_id", "scene_key", "chat_session_id"}
TRACE_ID_KEYS = {"coord_trace_id", "job_id", "task_id", "chat_trace_id"}


def traces_by_key(client: Client, key: str, value: str, limit: int | None = None, from_ts: str | None = None) -> list:
    found = {}

    def add(traces: list) -> None:
        for tr in traces:
            if client.in_scope(tr) and tr.get("id") and (tr["id"] not in found or timestamp_key(tr.get("timestamp")) > timestamp_key(found[tr["id"]].get("timestamp"))):
                found[tr["id"]] = tr

    data = client.get("/api/public/observations", {"name": index_name(key, value), "limit": 100})
    for obs in data.get("data", []):
        tid = obs.get("traceId")
        if tid and tid not in found:
            add([client.get(f"/api/public/traces/{tid}")])
    params_extra = {}
    if from_ts:
        params_extra["fromTimestamp"] = from_ts
    cap = limit or 50
    if key in TAG_KEYS:
        add(list_traces(client, {"tags": [tag(TAG_KEYS[key], value)], **params_extra}, cap))
    if key in SESSION_KEYS:
        add(list_traces(client, {"sessionId": value, **params_extra}, cap))
    if key in TRACE_ID_KEYS and looks_like_uuid(value):
        try:
            add([client.get(f"/api/public/traces/{trace_id_hex(value)}")])
        except SystemExit:
            pass
    return sorted(found.values(), key=lambda t: t.get("timestamp") or "", reverse=True)[:cap]


def cmd_key(client: Client, args: list, as_json: bool, limit: int | None, from_ts: str | None) -> None:
    print_traces(traces_by_key(client, args[0], args[1], limit, from_ts), as_json)


def cmd_recent(client: Client, args: list, as_json: bool, limit: int | None, from_ts: str | None) -> None:
    name = args[0] if args and not args[0].isdigit() else None
    n = limit or int(next((a for a in args if a.isdigit()), "20"))
    params = {}
    if name:
        params["name"] = name
    if from_ts:
        params["fromTimestamp"] = from_ts
    print_traces(list_traces(client, params, n), as_json)


def cmd_obs(client: Client, args: list, as_json: bool, _limit: int | None, _from_ts: str | None) -> None:
    trace = client.get(f"/api/public/traces/{trace_id_hex(args[0])}")
    if not client.in_scope(trace):
        raise SystemExit("trace is outside the requested environment/time window")
    observations = trace.get("observations") or []
    data = {"data": observations, "meta": {"totalItems": len(observations)}}
    if as_json:
        print(json.dumps(data, ensure_ascii=False, indent=2))
        return
    for obs in sorted(data.get("data", []), key=lambda o: (o.get("startTime") or "", o.get("name") or "")):
        print(json.dumps({k: obs.get(k) for k in ("id", "type", "name", "level", "model", "usageDetails", "startTime", "latency", "parentObservationId")}, ensure_ascii=False))


def cmd_related(client: Client, args: list, as_json: bool, _limit: int | None, _from_ts: str | None) -> None:
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
    for tag_key in ("task", "issue"):
        for tr in client.get("/api/public/traces", {"tags": [tag(tag_key, dashed)], "limit": 50, "orderBy": "timestamp.desc"}).get("data", []):
            found.setdefault(tr["id"], tr)
    traces = sorted((t for t in found.values() if client.in_scope(t)), key=lambda t: timestamp_key(t.get("timestamp")))
    print_traces(traces, as_json)


def parse_search_args(args: list[str]) -> dict:
    out = {"agent": "", "text": "", "name": ""}
    positional: list[str] = []
    i = 0
    while i < len(args):
        a = args[i]
        if a in ("--agent", "--text", "--name", "--message") and i + 1 < len(args):
            key = "text" if a == "--message" else a[2:]
            out[key] = args[i + 1]
            i += 2
            continue
        if a.startswith("--"):
            raise SystemExit(f"unknown search flag: {a}")
        positional.append(a)
        i += 1
    if not out["agent"] and positional and looks_like_uuid(positional[0]):
        out["agent"] = positional.pop(0)
    if not out["text"] and positional:
        out["text"] = positional.pop(0)
    if not out["agent"] and positional:
        out["agent"] = positional.pop(0)
    return out


LOOP_TAGS = {
    "inbound_coordinator": "inbound_coordinator",
    "scene_memory_flush": "scene_memory",
    "agent_task": "agent_task",
}


def loop_list_params(agent_tag: str, loop_name: str | None, from_ts: str | None) -> dict:
    params: dict = {}
    if from_ts:
        params["fromTimestamp"] = from_ts
    tags = [agent_tag]
    if loop_name in LOOP_TAGS:
        tags.append(LOOP_TAGS[loop_name])
    elif loop_name:
        params["name"] = loop_name
    params["tags"] = tags
    return params


def collect_agent_candidates(client: Client, agent: str, name: str, from_ts: str | None, scan: int) -> list:
    loop_name = None if name in ("all", "*") else (name or "inbound_coordinator")
    if looks_like_uuid(agent):
        return list_traces(client, loop_list_params(tag("agent", agent), loop_name, from_ts), scan)
    by_name_tag = list_traces(client, loop_list_params(tag("agent_name", agent), loop_name, from_ts), scan)
    named = list_traces(client, {"name": loop_name or "inbound_coordinator", **({"fromTimestamp": from_ts} if from_ts else {})}, scan)
    named = [
        t for t in named
        if agent in str((t.get("metadata") or {}).get("agent_name") or "")
        or agent in str((t.get("metadata") or {}).get("agent_id") or "")
    ]
    return merge_traces(by_name_tag, named)


def cmd_search(client: Client, args: list, as_json: bool, limit: int | None, from_ts: str | None) -> None:
    spec = parse_search_args(args)
    agent, text, name = spec["agent"], spec["text"], spec["name"]
    if not agent and not text:
        raise SystemExit("search needs --agent <id|name> and/or --text <needle>")
    cap = limit or 20
    scan = client.scan_limit or max(cap * 10, 100)
    window = from_ts or parse_from_timestamp(DEFAULT_SEARCH_WINDOW)
    if agent:
        candidates = collect_agent_candidates(client, agent, name, window, scan)
    else:
        params = {}
        if name not in ("all", "*"):
            params["name"] = name or "inbound_coordinator"
        if window:
            params["fromTimestamp"] = window
        candidates = list_traces(client, params, scan)
    if not text:
        client.search_stats = {"candidates": len(candidates), "returned": min(len(candidates), cap),
                               "truncated": len(candidates) > cap}
        print_traces(candidates[:cap], as_json)
        if not candidates:
            hint = expand_tag(agent) if agent else (name or "inbound_coordinator")
            print(f"# scanned {scan} with from={window} filter={hint}", file=sys.stderr)
        return
    hits = search_text(client, candidates, text, cap)
    print_traces(hits, as_json)
    if not hits:
        print(
            f"# no text hits for {text!r} in {len(candidates)} traces "
            f"(agent={agent or '-'} name={name or 'inbound_coordinator'} from={window} hydrated={client.search_stats['hydrated']})",
            file=sys.stderr,
        )


COMMANDS = {
    "trace": (cmd_trace, 1), "session": (cmd_session, 1), "tag": (cmd_tag, 1), "key": (cmd_key, 2),
    "related": (cmd_related, 1), "recent": (cmd_recent, 0), "obs": (cmd_obs, 1), "search": (cmd_search, 0),
}


def main(argv: list) -> None:
    argv, flags = parse_global_flags(argv)
    if not argv or argv[0] not in COMMANDS:
        print(__doc__)
        raise SystemExit(2)
    handler, arity = COMMANDS[argv[0]]
    if len(argv) - 1 < arity:
        print(__doc__)
        raise SystemExit(2)
    from_ts = parse_from_timestamp(flags["from"])
    client = Client(environment=flags["environment"], to_timestamp=parse_from_timestamp(flags["to"]) or "", max_pages=flags["pages"])
    client.from_timestamp = from_ts or ""
    client.scan_limit = flags["scan"]
    client.hydrate_limit = flags["hydrate"]
    client.workers = flags["workers"]
    handler(client, argv[1:], flags["json"], flags["limit"], from_ts)
    client.report_stats(flags["stats"])


if __name__ == "__main__":
    main(sys.argv[1:])
