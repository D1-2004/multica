#!/usr/bin/env python3
"""List Langfuse traces for the inbound-coordinator closed loop.

Self-hosted Langfuse 3.x on unify-aipilot. Do not use langfuse-cli
`observations list` (v2) — it 404s. Use /api/public/traces and
/api/public/observations.

Credentials from the environment or ~/.grok/langfuse.env. Never print keys.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone
from pathlib import Path

ENV_FILE = Path.home() / ".grok" / "langfuse.env"


def load_env() -> None:
    if ENV_FILE.is_file():
        for raw in ENV_FILE.read_text().splitlines():
            line = raw.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            key = key.strip()
            if key and key not in os.environ:
                os.environ[key] = value.strip()
    host = os.environ.get("LANGFUSE_HOST") or os.environ.get("LANGFUSE_BASE_URL")
    if host:
        os.environ["LANGFUSE_HOST"] = host.rstrip("/")
        os.environ.setdefault("LANGFUSE_BASE_URL", os.environ["LANGFUSE_HOST"])


def require_creds() -> tuple[str, str, str]:
    load_env()
    host = (os.environ.get("LANGFUSE_HOST") or os.environ.get("LANGFUSE_BASE_URL") or "").rstrip("/")
    pk = os.environ.get("LANGFUSE_PUBLIC_KEY") or ""
    sk = os.environ.get("LANGFUSE_SECRET_KEY") or ""
    if not host or not pk or not sk:
        sys.stderr.write(
            "Langfuse credentials missing. Put LANGFUSE_PUBLIC_KEY, "
            "LANGFUSE_SECRET_KEY, LANGFUSE_HOST in ~/.grok/langfuse.env "
            "(mode 600). Do not paste keys into chat or the repo.\n"
        )
        sys.exit(2)
    return host, pk, sk


def api_get(path: str, query: dict | None = None) -> dict:
    host, pk, sk = require_creds()
    token = base64.b64encode(f"{pk}:{sk}".encode()).decode()
    url = host + path
    if query:
        url += "?" + urllib.parse.urlencode({k: v for k, v in query.items() if v not in (None, "", [])}, doseq=True)
    req = urllib.request.Request(
        url,
        headers={
            "Authorization": "Basic " + token,
            "Accept": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as err:
        body = err.read()[:400].decode("utf-8", "replace")
        sys.stderr.write(f"Langfuse HTTP {err.code} {path}: {body}\n")
        sys.exit(1)


def compact_meta(meta: dict) -> dict:
    keep = (
        "agent_id",
        "agent_name",
        "channel",
        "coord_trace_id",
        "conversation_id",
        "issue_id",
        "loop",
        "provider",
        "runtime_name",
        "status",
        "task_id",
        "workspace_id",
    )
    return {k: meta.get(k) for k in keep if meta.get(k) not in (None, "", "null")}


def summarize_trace(row: dict) -> dict:
    meta = row.get("metadata") or {}
    if not isinstance(meta, dict):
        meta = {}
    return {
        "id": row.get("id"),
        "name": row.get("name"),
        "timestamp": row.get("timestamp"),
        "environment": row.get("environment"),
        "sessionId": row.get("sessionId"),
        "metadata": compact_meta(meta),
    }


UUID_RE = re.compile(
    r"^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$"
)


def looks_like_uuid(value: str) -> bool:
    return bool(UUID_RE.match((value or "").strip()))


def match_trace(row: dict, args: argparse.Namespace) -> bool:
    meta = row.get("metadata") if isinstance(row.get("metadata"), dict) else {}
    if args.trace:
        needle = args.trace.replace("-", "").lower()
        hay = [
            str(row.get("id") or ""),
            str(meta.get("coord_trace_id") or ""),
            str(meta.get("trace_id") or ""),
        ]
        if not any(needle in h.replace("-", "").lower() for h in hay):
            return False
    if args.issue:
        if str(meta.get("issue_id") or "") != args.issue:
            return False
    if args.agent:
        name = str(meta.get("agent_name") or "")
        aid = str(meta.get("agent_id") or "")
        if args.agent not in name and args.agent not in aid:
            return False
    if args.cid:
        cid = str(meta.get("conversation_id") or "")
        if args.cid not in cid:
            return False
    if args.loop:
        if str(meta.get("loop") or row.get("name") or "") != args.loop:
            return False
    return True


def iso_from(value: str) -> str:
    if value.isdigit():
        return datetime.fromtimestamp(int(value), tz=timezone.utc).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    m = re.fullmatch(r"(\d+)([smhd])", value.strip())
    if m:
        n = int(m.group(1))
        unit = m.group(2)
        delta = {
            "s": timedelta(seconds=n),
            "m": timedelta(minutes=n),
            "h": timedelta(hours=n),
            "d": timedelta(days=n),
        }[unit]
        return (datetime.now(timezone.utc) - delta).strftime("%Y-%m-%dT%H:%M:%SZ")
    return value


def list_traces(query_base: dict, pages: int, page_size: int) -> list[dict]:
    rows: list[dict] = []
    seen: set[str] = set()
    for page in range(1, pages + 1):
        query = dict(query_base)
        query["limit"] = str(page_size)
        query["page"] = str(page)
        payload = api_get("/api/public/traces", query)
        batch = payload.get("data") or []
        for row in batch:
            tid = row.get("id")
            if tid and tid not in seen:
                seen.add(tid)
                rows.append(row)
        if len(batch) < page_size:
            break
    return rows


def trace_blob(row: dict) -> str:
    return "\n".join(
        [
            json.dumps(row.get("input"), ensure_ascii=False),
            json.dumps(row.get("output"), ensure_ascii=False),
            json.dumps(row.get("metadata") or {}, ensure_ascii=False),
            str(row.get("sessionId") or ""),
        ]
    )


def match_message(row: dict, needle: str) -> tuple[bool, dict]:
    if not needle:
        return True, row
    if needle.casefold() in trace_blob(row).casefold():
        return True, row
    tid = row.get("id")
    if not tid:
        return False, row
    full = api_get(f"/api/public/traces/{tid}")
    return needle.casefold() in trace_blob(full).casefold(), full


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Query Langfuse traces for Coordinator / sandbox closed-loop checks."
    )
    parser.add_argument("--name", default="", help="trace name, e.g. inbound_coordinator")
    parser.add_argument("--loop", default="", help="metadata.loop: inbound_coordinator | agent_task")
    parser.add_argument("--trace", default="", help="coord_trace_id or Langfuse trace id")
    parser.add_argument("--issue", default="", help="metadata.issue_id")
    parser.add_argument("--agent", default="", help="agent_name, agent_id, or agent UUID")
    parser.add_argument("--cid", default="", help="conversation_id substring")
    parser.add_argument("--message", default="", help="inbound text substring (hydrates traces whose list payload omitted input)")
    parser.add_argument("--from", dest="from_ts", default="", help="ISO8601, unix seconds, or 7d/24h")
    parser.add_argument("--limit", type=int, default=20)
    parser.add_argument("--observations", action="store_true", help="also list observations for the first match")
    parser.add_argument("--raw", action="store_true")
    args = parser.parse_args()

    from_ts = args.from_ts
    if not from_ts:
        # Agent/text lookups must see more than the last 20 traces of the
        # whole project; default a week so a UUID search actually hits.
        hours = 24 * 7 if (args.agent or args.message) else 6
        from_ts = (datetime.now(timezone.utc) - timedelta(hours=hours)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    else:
        from_ts = iso_from(from_ts)

    query: dict[str, str] = {
        "fromTimestamp": from_ts,
        "orderBy": "timestamp.desc",
    }
    if args.name:
        query["name"] = args.name
    if looks_like_uuid(args.agent):
        # tags is AND. Pair agent-<uuid> with the loop tag so scene_memory
        # flushes do not drown coordinator turns. (name= is less reliable
        # here than the static loop tag.)
        loop_tag = "inbound_coordinator"
        if args.name == "scene_memory_flush" or args.loop == "scene_memory":
            loop_tag = "scene_memory"
        elif args.name == "agent_task" or args.loop == "agent_task":
            loop_tag = "agent_task"
        elif args.name:
            query["name"] = args.name
            loop_tag = ""
        query["tags"] = [f"agent-{args.agent.strip()}"] + ([loop_tag] if loop_tag else [])
    elif args.agent and not args.name and not args.loop:
        query["name"] = "inbound_coordinator"

    scan_pages = 6 if (args.agent or args.message or args.cid or args.issue) else 1
    rows = list_traces(query, pages=scan_pages, page_size=50)
    matched = [row for row in rows if match_trace(row, args)]
    if args.message:
        hits = []
        for row in matched:
            ok, full = match_message(row, args.message)
            if ok:
                hits.append(full)
            if len(hits) >= args.limit:
                break
        matched = hits
    else:
        matched = matched[: args.limit]
    if args.raw:
        json.dump({"data": matched}, sys.stdout, ensure_ascii=False, indent=2)
        sys.stdout.write("\n")
        return

    sys.stderr.write(
        f"# langfuse traces name={query.get('name', '')} tags={query.get('tags', '')} "
        f"from={from_ts} listed={len(rows)} matched={len(matched)}\n"
    )
    if not matched:
        print("no hits")
        return
    for row in matched:
        summary = summarize_trace(row)
        print("=" * 72)
        print(
            "id={id} name={name} ts={ts} env={env} loop={loop} agent={agent} "
            "coord_trace_id={coord} issue_id={issue} cid={cid}".format(
                id=summary.get("id"),
                name=summary.get("name"),
                ts=summary.get("timestamp"),
                env=summary.get("environment"),
                loop=(summary.get("metadata") or {}).get("loop", ""),
                agent=(summary.get("metadata") or {}).get("agent_name", ""),
                coord=(summary.get("metadata") or {}).get("coord_trace_id", ""),
                issue=(summary.get("metadata") or {}).get("issue_id", ""),
                cid=(summary.get("metadata") or {}).get("conversation_id", ""),
            )
        )
        meta = summary.get("metadata") or {}
        for key in ("status", "channel", "provider", "runtime_name", "task_id"):
            if meta.get(key):
                print(f"  {key}: {meta[key]}")
        preview = row.get("input")
        if preview not in (None, "", {}, []):
            text = preview if isinstance(preview, str) else json.dumps(preview, ensure_ascii=False)
            print(f"  input: {text[:180]}")

    if args.observations:
        first = matched[0]
        tid = first.get("id")
        obs = api_get("/api/public/observations", {"traceId": tid, "limit": "50"})
        print("\n--- observations ---")
        for item in obs.get("data") or []:
            print(
                "{start} type={typ} name={name} id={oid}".format(
                    start=item.get("startTime"),
                    typ=item.get("type"),
                    name=item.get("name"),
                    oid=item.get("id"),
                )
            )


if __name__ == "__main__":
    main()
