"""Evidence collectors: Langfuse traces, SLS lines and Multica API reads.

Langfuse: employee_loop traces have session = scene_id and trace id = job id
without dashes; agent_task traces have session = cid. Ingestion lags 30-60 s,
so collection runs after the case, not inside the driver. Raw trace JSON is
kept for the reasoning-chain analysis (what the model saw, which tools ran).
SLS: `content` is not SQL-analysable; page raw lines (100/page) and slice locally.
"""

from __future__ import annotations

import datetime as _dt
import json
import re
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

from .common import (REPO_ROOT, clean_env, extract_json, iso, load_json, now, parse_iso, read_jsonl, registry,
                     run_cmd, run_dir, write_json, TZ)

UUID_IN_TEXT = re.compile(r"\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b")
KV_RE = re.compile(r"\b([a-z_]+_id|job|task|run|queue|receipt)=([0-9a-f]{8}-[0-9a-f-]{27})")


def _lf(args: list[str], timeout: int = 180) -> Any:
    script = REPO_ROOT / registry()["env"]["langfuse_lookup"]
    res = run_cmd(["python3", str(script), *args], env=clean_env(), timeout=timeout)
    if res["rc"] != 0:
        return {"error": (res["stderr"] or res["stdout"])[-400:]}
    try:
        return extract_json(res["stdout"])
    except ValueError:
        return {"error": "no json", "stdout": res["stdout"][-400:]}


def utc(ts: _dt.datetime) -> str:
    return ts.astimezone(_dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def list_agent_traces(start: _dt.datetime, end: _dt.datetime) -> list[dict[str, Any]]:
    reg = registry()
    out = _lf(["tag", "agent-" + reg["employee"]["agent_id"], "--environment", reg["env"]["langfuse_environment"],
               "--from", utc(start), "--to", utc(end), "--limit", "200", "--json"])
    return out if isinstance(out, list) else []


def fetch_trace(trace_id: str) -> dict[str, Any]:
    out = _lf(["trace", trace_id, "--json"], timeout=120)
    return out if isinstance(out, dict) else {"error": str(out)[:300]}


def _gen_summary(obs: dict[str, Any]) -> dict[str, Any]:
    inp = obs.get("input")
    messages = inp.get("messages") if isinstance(inp, dict) else (inp if isinstance(inp, list) else None)
    last_user = None
    if isinstance(messages, list):
        for m in reversed(messages):
            if isinstance(m, dict) and m.get("role") == "user":
                content = m.get("content")
                last_user = content if isinstance(content, str) else json.dumps(content, ensure_ascii=False)
                break
    out = obs.get("output") or {}
    msg = {}
    if isinstance(out, dict) and out.get("choices"):
        msg = (out["choices"][0] or {}).get("message") or {}
    tool_calls = [{"name": (tc.get("function") or {}).get("name"),
                   "arguments": (tc.get("function") or {}).get("arguments", "")[:8000]}
                  for tc in (msg.get("tool_calls") or []) if isinstance(tc, dict)]
    usage = obs.get("usageDetails") or {}
    return {"name": obs.get("name"), "model": obs.get("model"), "level": obs.get("level"),
            "latency_s": obs.get("latency"), "usage": {"input": usage.get("input"), "output": usage.get("output")},
            "n_messages": len(messages) if isinstance(messages, list) else None,
            "last_user": (last_user or "")[-1500:], "content": (msg.get("content") or "")[:1200],
            "reasoning": (msg.get("reasoning_content") or "")[:1500], "tool_calls": tool_calls,
            "status_message": obs.get("statusMessage")}


MEMORY_PREFIX = "Existing memory snapshot (data):"
HISTORY_PREFIXES = ("Recent conversation snapshot", "Recent conversation (temporary dialogue data", "[History ")
WINDOW_PREFIX = "Current conversation window:"
# employee_loop trace metadata the memory design adds (12-memory-design §7.4).
MEMORY_METADATA_KEYS = ("employee_job_id", "memory_manifest", "memory_query_terms", "memory_hits", "memory_pinned",
                        "memory_bytes_by_section", "transcript_status", "transcript_reason", "transcript_lines",
                        "transcript_bytes", "transcript_elapsed_ms", "history_lower_bound", "history_segments",
                        "history_collapsed", "host_facts_bytes")


def _text(content: Any) -> str:
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(p.get("text", "") for p in content if isinstance(p, dict))
    return json.dumps(content, ensure_ascii=False) if content is not None else ""


def first_request(trace: dict[str, Any]) -> dict[str, str]:
    """What the model was shown on the wake's first request, split by role:
    the frozen persona (system), the memory block, the recent conversation
    (history snapshot, history turns, Host-ledgered assistant turns) and the
    current window. Assertions on memory/history read these, never replies."""
    obs = sorted(trace.get("observations") or [], key=lambda o: o.get("startTime") or "")
    gen = next((o for o in obs if o.get("type") == "GENERATION"), None)
    out = {"system": "", "memory": "", "history": "", "current_window": ""}
    if not gen:
        return out
    inp = gen.get("input")
    messages = inp.get("messages") if isinstance(inp, dict) else (inp if isinstance(inp, list) else [])
    history: list[str] = []
    for m in messages or []:
        if not isinstance(m, dict):
            continue
        role, text = m.get("role"), _text(m.get("content"))
        if role in ("system", "developer"):
            out["system"] += text
        elif text.startswith(MEMORY_PREFIX):
            out["memory"] = text[len(MEMORY_PREFIX):].lstrip("\n")
        elif text.startswith(WINDOW_PREFIX):
            out["current_window"] = text
        elif role == "assistant" or text.startswith(HISTORY_PREFIXES):
            history.append(f"[{role}] {text}")
    out["history"] = "\n".join(history)
    return {k: v[:40000] for k, v in out.items()}


def summarize(trace: dict[str, Any]) -> dict[str, Any]:
    obs = sorted(trace.get("observations") or [], key=lambda o: o.get("startTime") or "")
    idx = {}
    for o in obs:
        name = str(o.get("name") or "")
        if name.startswith("idx."):
            _, key, value = name.split(".", 2)
            idx.setdefault(key, []).append(value)
    gens = [_gen_summary(o) for o in obs if o.get("type") == "GENERATION"]
    tools = [{"name": o.get("name"), "level": o.get("level"),
              "input": json.dumps(o.get("input"), ensure_ascii=False)[:8000],
              "output": json.dumps(o.get("output"), ensure_ascii=False)[:800]}
             for o in obs if o.get("type") == "TOOL"]
    errors = [{"name": o.get("name"), "type": o.get("type"), "status": o.get("statusMessage")}
              for o in obs if o.get("level") == "ERROR"]
    meta = trace.get("metadata") or {}
    return {"id": trace.get("id"), "name": trace.get("name"), "timestamp": trace.get("timestamp"),
            "session": trace.get("sessionId"), "latency_s": trace.get("latency"),
            "job_id": meta.get("employee_job_id") or meta.get("job_id"), "receipt_ids": meta.get("receipt_ids"),
            "scene_id": meta.get("scene_id"), "output": trace.get("output"), "generation_count": len(gens),
            "request": first_request(trace) if trace.get("name") == "employee_loop" else None,
            "metadata": {k: meta[k] for k in MEMORY_METADATA_KEYS if k in meta},
            "observation_names": sorted({str(o.get("name")) for o in trace.get("observations") or []}),
            "input_text": json.dumps(trace.get("input"), ensure_ascii=False)[:60000] if trace.get("name") == "agent_task" else None,
            "generations": gens, "tools": tools, "errors": errors, "idx": idx,
            "langfuse_url_hint": f"traces/{trace.get('id')}"}


def sls_agent_lines(start: _dt.datetime, end: _dt.datetime, *, max_pages: int = 30) -> dict[str, Any]:
    reg = registry()
    env = reg["env"]["sls"]
    query = f'__tag__:__user_defined_id__: {env["prehost_tag"]} and {reg["employee"]["agent_id"]}'
    rows: list[dict[str, Any]] = []
    errors = []
    for page in range(max_pages):
        args = ["normandy", "log", "list", "--source", "sls", "--project", env["project"], "--logstore",
                env["logstore"], "--query", query, "--from", iso(start), "--to", iso(end), "--size", "100",
                "--offset", str(page * 100), "-o", "json"]
        batch = None
        for _ in range(3):
            res = run_cmd(args, env=clean_env(), timeout=240)
            try:
                batch = extract_json(res["stdout"])
                break
            except ValueError:
                errors.append((res["stderr"] or res["stdout"])[-200:])
        if batch is None:
            break
        rows.extend(batch)
        if len(batch) < 100:
            break
    lines = []
    for r in rows:
        ts = _dt.datetime.fromtimestamp(int(r.get("__time__")), TZ)
        lines.append({"time": iso(ts), "host": r.get("__tag__:__hostname__"), "path": r.get("__tag__:__path__"),
                      "content": r.get("content")})
    lines.sort(key=lambda x: x["time"])
    return {"from": iso(start), "to": iso(end), "count": len(lines), "errors": errors[-3:], "lines": lines}


def pre_api(method: str, path: str, body: Any = None) -> tuple[int, Any]:
    """Minimal pre-release Multica API read using the pre-fde profile token (never logged)."""
    reg = registry()
    cfg = json.loads((Path.home() / ".multica" / "profiles" / reg["env"]["multica_profile"] / "config.json").read_text())
    base = cfg["server_url"].rstrip("/")
    if base != reg["env"]["server"]:
        raise RuntimeError(f"profile {reg['env']['multica_profile']} points at {base}, not {reg['env']['server']}")
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + cfg["token"])
    req.add_header("X-Workspace-ID", reg["env"]["workspace_id"])
    if data is not None:
        req.add_header("Content-Type", "application/json")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req, timeout=60) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as err:
        raw = err.read()
        try:
            return err.code, json.loads(raw)
        except ValueError:
            return err.code, raw.decode(errors="replace")[:500]


def agent_config_snapshot() -> dict[str, Any]:
    reg = registry()
    status, body = pre_api("GET", f"/api/agents/{reg['employee']['agent_id']}")
    if status != 200 or not isinstance(body, dict):
        return {"status": status}
    import hashlib
    keep = ("name", "model", "coordination_mode", "employee_loop_ready", "event_trigger_enabled", "inbound_coordinator",
            "dingtalk_response_enabled", "dingtalk_response_policy_revision", "runtime_id", "updated_at",
            "scene_memory_write_enabled", "scene_memory_recall_enabled", "task_finished_loop_enabled")
    snap = {k: body.get(k) for k in keep}
    snap["instructions_sha256"] = hashlib.sha256((body.get("instructions") or "").encode()).hexdigest()
    snap["persona_sha256"] = hashlib.sha256((body.get("persona") or "").encode()).hexdigest()
    return snap


def scene_memory_snapshot(scene_id: str) -> dict[str, Any]:
    """Shared scene-layer learnings as the management API reports them (the
    private layer is never readable here, by design)."""
    reg = registry()
    status, body = pre_api("GET", f"/api/agents/{reg['employee']['agent_id']}/scene-memory/{scene_id}?loop=employee")
    if status != 200 or not isinstance(body, dict):
        return {"status": status, "error": str(body)[:300]}
    return {"status": status, "scene_id": scene_id, "revision": body.get("memory_revision"),
            "learnings": body.get("learnings") or [], "memory_text": body.get("memory_text") or ""}


def case_window(rec: dict[str, Any]) -> tuple[_dt.datetime, _dt.datetime]:
    start = parse_iso(rec["started_at"]) - _dt.timedelta(seconds=10)
    end = parse_iso(rec.get("ended_at") or rec["started_at"]) + _dt.timedelta(seconds=90)
    return start, end


def case_texts(rec: dict[str, Any]) -> list[str]:
    return [s["send"]["match_key"] for s in rec["steps"] if s.get("send")]


def step_message_ids(rec: dict[str, Any]) -> dict[str, str]:
    """messageId -> step id, from the driver landing or recovered from the case transcript."""
    from .grader import locate_steps
    transcripts = {}
    for key, snap in (rec.get("transcripts") or {}).items():
        transcripts.setdefault(key.split("@")[0], snap["messages"])
    locate_steps(rec, transcripts, registry())
    return {s["msg"]["messageId"]: s["id"] for s in rec["steps"] if s.get("msg")}


def current_window_text(summary: dict[str, Any]) -> str:
    """The first model request's last user turn is the current window (history precedes it)."""
    gens = summary.get("generations") or []
    return gens[0].get("last_user", "") if gens else ""


def collect_case(rd: Path, rec: dict[str, Any], scene_ids: dict[str, str]) -> dict[str, Any]:
    """Attribute traces exactly: employee_loop by the step openMsgId in its current window,
    agent_task by idx.employee_job_id of an attributed employee_loop wake."""
    start, end = case_window(rec)
    end = end + _dt.timedelta(minutes=8)  # background task traces start after the wake
    msg_ids = step_message_ids(rec)
    listed = list_agent_traces(start, end)
    lf_dir = rd / "langfuse"
    lf_dir.mkdir(exist_ok=True)
    summaries = []
    for item in listed:
        tid = item.get("id")
        path = lf_dir / f"{tid}.json"
        full = load_json(path) or fetch_trace(tid)
        if "error" in full:
            summaries.append({"id": tid, "error": full.get("error")})
            continue
        write_json(path, full)
        summaries.append(summarize(full))
    traces = []
    jobs = set()
    for summ in summaries:
        if summ.get("name") != "employee_loop":
            continue
        window = current_window_text(summ)
        hit = [step for mid, step in msg_ids.items() if mid and mid in window]
        if hit:
            summ["matched_by"] = "current_window_openMsgId"
            summ["matched_steps"] = hit
            jobs.add(summ.get("job_id"))
            traces.append(summ)
    for summ in summaries:
        if summ.get("name") == "agent_task" and set(summ.get("idx", {}).get("employee_job_id", [])) & jobs:
            summ["matched_by"] = "employee_job_id"
            traces.append(summ)
    traces.sort(key=lambda t: t.get("timestamp") or "")
    # Spans that are their own traces (verified distill, transcript reads) are
    # kept by name so cases can assert they happened inside the window.
    named = [s for s in summaries if s.get("name") in ("employee_verified_distill", "employee_scene_transcript",
                                                         "employee_agent_profile_refresh", "employee_scene_digest")]
    out = {"case_id": rec["case_id"], "attempt": rec["attempt"], "window": [iso(start), iso(end)],
           "listed": len(listed), "step_message_ids": msg_ids, "traces": traces, "named_traces": named}
    scene_memory = {}
    reg = registry()
    for conv_name in (rec.get("collect") or {}).get("scene_memory", []):
        sid = scene_ids.get(conv_name) or (reg["conversations"].get(conv_name) or {}).get("scene_id")
        if not sid:
            for t in traces:
                if t.get("name") == "employee_loop" and t.get("session") and any(
                        s["conversation"] == conv_name for s in rec["steps"] if s["id"] in (t.get("matched_steps") or [])):
                    sid = t["session"]
        scene_memory[conv_name] = scene_memory_snapshot(sid) if sid else {"error": "scene id unknown"}
    out["scene_memory"] = scene_memory
    return out


def discover_scene_ids(rd: Path) -> dict[str, str]:
    """Map conversation name -> scene_id from employee_loop traces attributed by openMsgId."""
    reg = registry()
    found = {name: c["scene_id"] for name, c in reg["conversations"].items() if c.get("scene_id")}
    for ev_path in sorted((rd / "evidence").glob("*.lf.json")):
        ev = load_json(ev_path)
        drv = load_json(rd / "cases" / ev_path.name.replace(".lf.json", ".driver.json"))
        if not ev or not drv:
            continue
        conv_of = {s["id"]: s["conversation"] for s in drv["steps"]}
        for t in ev["traces"]:
            if t.get("name") != "employee_loop" or not t.get("session"):
                continue
            for step_id in t.get("matched_steps", []):
                found.setdefault(conv_of[step_id], t["session"])
    return found


def collect_run(run_id: str, *, only: list[str], with_sls: bool = True) -> int:
    rd = run_dir(run_id)
    (rd / "evidence").mkdir(exist_ok=True)
    drivers = sorted((rd / "cases").glob("*.driver.json"))
    reg = registry()
    scene_ids = {name: c["scene_id"] for name, c in reg["conversations"].items() if c.get("scene_id")}
    windows = []
    for path in drivers:
        rec = load_json(path)
        if only and rec["case_id"] not in only:
            continue
        if not any((s.get("send") or {}).get("landed") for s in rec.get("steps", [])):
            continue  # nothing was sent (not_run / skipped): no traces to attribute
        out_path = rd / "evidence" / path.name.replace(".driver.json", ".lf.json")
        ev = collect_case(rd, rec, scene_ids)
        write_json(out_path, ev)
        if rec.get("schema") == "el2e.driver.v2":
            # pg_read: PG facts through the 预发 HTTP API (PG is unreachable from here).
            from . import api_facts
            try:
                facts = api_facts.collect(rec)
            except Exception as exc:  # recorded, never fatal for the other cases
                facts = {"error": str(exc)[:300]}
            write_json(rd / "evidence" / path.name.replace(".driver.json", ".api.json"), facts)
        windows.append(case_window(rec))
        print(json.dumps({"case": rec["case_id"], "attempt": rec["attempt"], "traces": len(ev["traces"]),
                          "listed": ev["listed"]}, ensure_ascii=False), flush=True)
        scene_ids.update(discover_scene_ids(rd))
    # Persist newly discovered scene ids into the registry.
    changed = False
    for name, sid in scene_ids.items():
        if reg["conversations"].get(name) is not None and not reg["conversations"][name].get("scene_id"):
            reg["conversations"][name]["scene_id"] = sid
            changed = True
    if changed:
        from .common import save_registry
        save_registry(reg)
    if windows and with_sls:
        start = min(w[0] for w in windows)
        end = max(w[1] for w in windows)
        write_json(rd / "evidence" / "sls_agent_lines.json", sls_agent_lines(start, end))
        from . import envguard
        write_json(rd / "evidence" / "sls_server_starting.json",
                   envguard.sls_server_starts(start - _dt.timedelta(minutes=5), end + _dt.timedelta(minutes=5)))
    return 0
