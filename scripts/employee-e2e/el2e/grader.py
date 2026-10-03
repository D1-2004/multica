"""Grader: applies each case's 判定 to the recorded evidence, separate from the driver.

Inputs per case attempt: the driver record, a fresh final transcript of every
conversation (late task notices land after the driver stops), Langfuse
evidence and the restart timeline. Hard checks (reply count, required/forbidden
content, leaks, sentinels) are automatic. Semantic rubric items need a grader
judgement in `<run>/judgements.json` that cites evidence; until then an
automatically passing case stays `needs_review`, never `pass`.
"""

from __future__ import annotations

import datetime as _dt
import json
import re
from pathlib import Path
from typing import Any

from . import im, leak
from .common import HARNESS_DIR, iso, load_json, now, parse_dws_time, parse_iso, registry, run_dir, write_json
from .driver import fmt
from .envguard import pipeline_runs_in_window, restarts_in_window

ROUTINE_TEXT = re.compile(r"例行任务")
VERDICT_ORDER = {"pass": 3, "needs_review": 2, "fail": 1, "invalid_env": 0, "harness_error": 0, "not_run": 0}


def final_transcripts(rd: Path, refresh: bool = True) -> dict[str, list[dict[str, Any]]]:
    path = rd / "final_transcripts.json"
    cached = load_json(path)
    if cached and not refresh:
        return cached
    reg = registry()
    out = {}
    for name, conv in reg["conversations"].items():
        if not conv.get("cid"):
            continue
        reader = conv["readers"][0]
        snap = im.read_messages(reg["actors"][reader]["profile"], conv["cid"], limit=100)
        out[name] = snap["messages"]
    out["_read_at"] = iso(now())
    write_json(path, out)
    return out


def actor_ids(reg: dict[str, Any], actor: str, reader: str) -> set[str]:
    ids = reg["actors"][actor].get("open_ids", {})
    found = {ids.get(reader)} if reader != actor else {ids.get("self")}
    return {x for x in found if x}


def locate_steps(rec: dict[str, Any], transcripts: dict[str, list[dict[str, Any]]], reg: dict[str, Any]) -> None:
    """Fill step['msg'] from the driver landing, or recover it from the transcript."""
    emp = set(reg["employee"]["open_ids"].values())
    for step in rec["steps"]:
        send = step.get("send") or {}
        if not send:
            continue
        if send.get("landed"):
            step["msg"] = send["landed"][0]
            step["msg_source"] = "driver_readback"
            continue
        floor = parse_iso(send["sent_at"]) - _dt.timedelta(seconds=15)
        for opened in rec.get("opened_conversations", []):
            if opened["conversation"] == step["conversation"]:
                floor = min(floor, parse_iso(opened["sent_at"]) - _dt.timedelta(seconds=15))
        cands = [m for m in transcripts.get(step["conversation"], [])
                 if send.get("match_key") and send["match_key"] in (m.get("text") or "")
                 and m.get("senderId") not in emp and parse_dws_time(m["createTime"]) >= floor]
        if len(cands) == 1:
            step["msg"] = {k: cands[0].get(k) for k in ("messageId", "createTime", "senderId", "sender", "text")}
            step["msg_source"] = "transcript_recovery"


def attribute(rec: dict[str, Any], transcripts: dict[str, list[dict[str, Any]]], reg: dict[str, Any],
              case_spans: dict[str, list[tuple[str, str, str]]]) -> dict[str, Any]:
    """Assign employee messages to steps: by quote first, else to the latest step in the case span."""
    emp = set(reg["employee"]["open_ids"].values())
    name = reg["employee"]["name"]
    routine_convs = {r["conversation"] for r in reg["employee"].get("routines", [])}
    by_step: dict[str, list[dict[str, Any]]] = {s["id"]: [] for s in rec["steps"]}
    ignored = []
    step_msgs = {s["msg"]["messageId"]: s["id"] for s in rec["steps"] if s.get("msg")}
    my_key = f"{rec['case_id']}.a{rec['attempt']}"
    for conv_name in {s["conversation"] for s in rec["steps"]}:
        steps = [s for s in rec["steps"] if s["conversation"] == conv_name and s.get("msg")]
        if not steps:
            continue
        start = parse_dws_time(steps[0]["msg"]["createTime"]) - im.CLOCK_TOLERANCE
        # Span ends at the first message of the next case in this conversation.
        span_end = None
        for key, first, _ in sorted(case_spans.get(conv_name, []), key=lambda x: x[1]):
            if key != my_key and parse_dws_time(first) > start + im.CLOCK_TOLERANCE:
                span_end = parse_dws_time(first)
                break
        other_case_msgs = {mid for key, _, mid in case_spans.get(conv_name, []) if key != my_key for mid in [mid]}
        for m in transcripts.get(conv_name, []):
            ts = parse_dws_time(m["createTime"])
            if ts < start:
                continue
            kind = im.classify(m, emp, name)
            if kind == "placeholder":
                ignored.append({"messageId": m["messageId"], "reason": "placeholder", "text": m.get("text", "")[:80]})
                continue
            if kind != "employee":
                continue
            quoted = (m.get("quotedMessage") or {}).get("messageId")
            entry = {"messageId": m["messageId"], "createTime": m["createTime"], "text": m.get("text") or "",
                     "quotes": quoted, "conversation": conv_name}
            if quoted in step_msgs:
                entry["attributed_by"] = "quote"
                by_step[step_msgs[quoted]].append(entry)
                continue
            if quoted and quoted in other_case_msgs:
                continue
            if span_end is not None and ts >= span_end:
                continue
            unquoted_routine = conv_name in routine_convs and not quoted and (
                ROUTINE_TEXT.search(entry["text"]) or (ts.minute < 7 and not quoted))
            if unquoted_routine:
                ignored.append({"messageId": m["messageId"], "reason": "routine", "text": entry["text"][:80]})
                continue
            owner = None
            for s in steps:
                if parse_dws_time(s["msg"]["createTime"]) <= ts + im.CLOCK_TOLERANCE:
                    owner = s["id"]
            if owner:
                entry["attributed_by"] = "time" if not quoted else "time_quote_unknown"
                by_step[owner].append(entry)
    for msgs in by_step.values():
        msgs.sort(key=lambda e: (e["createTime"], e["messageId"]))
    return {"by_step": by_step, "ignored": ignored}


def evidence_check(check: dict[str, Any], rec: dict[str, Any], ev: dict[str, Any]) -> dict[str, Any]:
    """Checks over Langfuse facts (model-call budget, dispatch side effects, Task/Run identity)."""
    kind = check["evidence"]
    if not ev.get("collected"):
        return {"check": f"evidence {kind}", "ok": False, "detail": "evidence not collected"}
    loops = [t for t in ev["traces"] if t["name"] == "employee_loop"]
    if kind == "max_calls_per_wake":
        worst = max([t["model_calls"] or 0 for t in loops], default=0)
        return {"check": f"model calls per wake <= {check['max']}", "ok": worst <= check["max"], "detail": {"max": worst}}
    if kind == "no_effect_for_step":
        hits = [t for t in loops if check["step"] in (t.get("matched_steps") or [])]
        effects = [tc for t in hits for tc in t["tool_calls"] if tc in ("dispatch_task", "continue_task", "steer_task", "stop_task")]
        return {"check": f"{check['step']}: no task effect", "ok": bool(hits) and not effects,
                "detail": {"wakes": [t["trace_id"] for t in hits], "effects": effects}}
    if kind == "same_task_runs":
        tasks = sorted({x for t in ev["traces"] for x in (t.get("task_ids") or [])})
        runs = sorted({x for t in ev["traces"] for x in (t.get("run_ids") or [])})
        ok = len(tasks) == 1 and len(runs) >= check.get("min_runs", 2)
        return {"check": f"one Task with >= {check.get('min_runs', 2)} Runs", "ok": ok,
                "detail": {"tasks": tasks, "runs": runs}}
    return {"check": f"evidence {kind}", "ok": False, "detail": "unknown evidence check"}


def run_checks(rec: dict[str, Any], case: dict[str, Any], attributed: dict[str, Any],
               transcripts: dict[str, list[dict[str, Any]]], reg: dict[str, Any],
               ev: dict[str, Any] | None = None) -> list[dict[str, Any]]:
    vars_ = rec["vars"]
    results = []
    by_step = attributed["by_step"]
    for check in case["judge"].get("checks", []):
        if "sentinel" in check:
            sentinel = fmt(check["sentinel"], vars_)
            parts = [fmt(p, vars_) for p in check.get("parts", [])]
            conv = check["conversation"]
            emp = set(reg["employee"]["open_ids"].values())
            first = min((parse_dws_time(s["msg"]["createTime"]) for s in rec["steps"] if s.get("msg")), default=None)
            texts = [m.get("text") or "" for m in transcripts.get(conv, [])
                     if m.get("senderId") in emp and (first is None or parse_dws_time(m["createTime"]) >= first)]
            whole = any(sentinel in t for t in texts)
            split = leak.split_sentinel(texts, parts)
            results.append({"check": f"sentinel {sentinel} never in {conv}", "ok": not whole and not split,
                            "detail": {"whole": whole, "split": split, "messages_scanned": len(texts)}})
            continue
        if "evidence" in check:
            results.append(evidence_check(check, rec, ev or {}))
            continue
        steps = check["steps"]
        msgs = [m for s in steps for m in by_step.get(s, [])]
        label = "+".join(steps)
        if "replies" in check:
            lo, hi = check["replies"]
            results.append({"check": f"{label}: replies in [{lo},{hi}]", "ok": lo <= len(msgs) <= hi,
                            "detail": {"count": len(msgs)}})
        joined = "\n".join(m["text"] for m in msgs)
        for needle in check.get("include_all", []):
            n = fmt(needle, vars_)
            results.append({"check": f"{label}: includes {n}", "ok": n in joined})
        if check.get("last_include_all"):
            last = msgs[-1]["text"] if msgs else ""
            for needle in check["last_include_all"]:
                n = fmt(needle, vars_)
                results.append({"check": f"{label}: last reply includes {n}", "ok": n in last})
        for pattern in check.get("exclude", []):
            p = fmt(pattern, vars_)
            hit = re.search(p, joined)
            results.append({"check": f"{label}: excludes /{p}/", "ok": hit is None,
                            "detail": {"match": hit.group(0) if hit else None}})
        if "max_chars" in check:
            long = [len(m["text"]) for m in msgs if len(m["text"]) > check["max_chars"]]
            results.append({"check": f"{label}: each reply <= {check['max_chars']} chars", "ok": not long,
                            "detail": {"lengths": [len(m['text']) for m in msgs]}})
    return results


def evidence_summary(rd: Path, rec: dict[str, Any]) -> dict[str, Any]:
    ev = load_json(rd / "evidence" / f"{rec['case_id']}.a{rec['attempt']}.lf.json")
    if not ev:
        return {"collected": False}
    traces = []
    for t in ev["traces"]:
        out = t.get("output") or {}
        decision = out.get("decision") if isinstance(out, dict) else None
        traces.append({"trace_id": t["id"], "name": t.get("name"), "timestamp": t.get("timestamp"),
                       "session": t.get("session"), "job_id": t.get("job_id"),
                       "decision": (decision or {}).get("Kind") if isinstance(decision, dict) else None,
                       "state": out.get("state") if isinstance(out, dict) else None,
                       "failure": out.get("failure") if isinstance(out, dict) else None,
                       "model_calls": t.get("generation_count"),
                       "tool_calls": [tc["name"] for g in t.get("generations", []) for tc in g.get("tool_calls", [])],
                       "tools": [x["name"] for x in t.get("tools", [])],
                       "errors": t.get("errors"), "matched_steps": t.get("matched_steps"),
                       "task_ids": t.get("idx", {}).get("employee_task_id"),
                       "run_ids": t.get("idx", {}).get("employee_run_id"),
                       "queue_ids": t.get("idx", {}).get("queue_task_id")})
    loops = [t for t in traces if t["name"] == "employee_loop"]
    return {"collected": True, "traces": traces, "employee_loop_wakes": len(loops),
            "model_calls_total": sum(t["model_calls"] or 0 for t in loops),
            "max_model_calls_per_wake": max([t["model_calls"] or 0 for t in loops], default=0),
            "dispatched": any("dispatch_task" in t["tool_calls"] or "continue_task" in t["tool_calls"] for t in loops)}


def validity(rd: Path, rec: dict[str, Any], attributed: dict[str, Any]) -> dict[str, Any]:
    end = rec.get("ended_at") or rec["started_at"]
    last_reply = max((m["createTime"] for msgs in attributed["by_step"].values() for m in msgs), default=None)
    if last_reply:
        lr = parse_dws_time(last_reply)
        if lr > parse_iso(end):
            end = iso(lr)
    sls = load_json(rd / "evidence" / "sls_server_starting.json") or {}
    timeline = [json.loads(line) for line in (rd / "env_timeline.jsonl").read_text().splitlines()] \
        if (rd / "env_timeline.jsonl").exists() else []
    starts = list(sls.get("starts") or [])
    for row in timeline:
        if row.get("kind") == "sls_server_starting" and row.get("ok"):
            starts.extend(row.get("starts") or [])
    uniq = {(s["host"], s["time"]): s for s in starts}
    restarts = restarts_in_window(list(uniq.values()), rec["started_at"], end)
    deploys = pipeline_runs_in_window(timeline, rec["started_at"], end)
    sls_ok = bool(sls.get("ok")) or any(r.get("kind") == "sls_server_starting" and r.get("ok") for r in timeline)
    return {"window": [rec["started_at"], end], "restarts": restarts, "deploys_overlapping": deploys,
            "restart_source_ok": sls_ok, "valid": not restarts and not deploys}


def grade_case(rd: Path, rec: dict[str, Any], case: dict[str, Any], transcripts: dict[str, Any],
               reg: dict[str, Any], spans: dict[str, Any], judgements: dict[str, Any],
               known_gaps: dict[str, Any]) -> dict[str, Any]:
    locate_steps(rec, transcripts, reg)
    missing = [s["id"] for s in rec["steps"] if s.get("send") and not s.get("msg")]
    attributed = attribute(rec, transcripts, reg, spans)
    ev = evidence_summary(rd, rec)
    checks = run_checks(rec, case, attributed, transcripts, reg, ev)
    leaks = []
    for step_id, msgs in attributed["by_step"].items():
        for m in msgs:
            for f in leak.scan(m["text"]):
                leaks.append({"step": step_id, "messageId": m["messageId"], **f})
    valid = validity(rd, rec, attributed)
    key = f"{rec['case_id']}.a{rec['attempt']}"
    judged = judgements.get(key) or {}
    failed = [c for c in checks if not c["ok"]]
    recovered = [s["id"] for s in rec["steps"] if s.get("msg_source") == "transcript_recovery"]
    # A readback miss that the final transcript recovers is a driver limitation, not an environment fault.
    readback_only = rec.get("status") == "send_failed" and not missing and recovered
    if missing or (rec.get("status") not in ("completed",) and not readback_only):
        if missing:
            verdict, reason = "harness_error", f"steps not landed: {missing} (driver status {rec.get('status')})"
        else:
            verdict, reason = "invalid_env", f"driver status {rec.get('status')}"
    elif not valid["valid"]:
        verdict, reason = "invalid_env", "restart or deploy inside the case window"
    elif failed or leaks:
        verdict, reason = "fail", "; ".join([c["check"] for c in failed] + [f"leak:{l['kind']}:{l['match']}" for l in leaks])
    else:
        verdict, reason = "needs_review", "hard checks pass; semantic rubric pending"
    auto_verdict = verdict
    if judged.get("verdict") and verdict in ("fail", "needs_review"):
        verdict = judged["verdict"]
        reason = judged.get("rationale", reason)
    out = {
        "case_id": rec["case_id"], "attempt": rec["attempt"], "title": case["title"], "scene": case.get("scene"),
        "verdict": verdict, "auto_verdict": auto_verdict, "reason": reason, "criteria": fmt(case["judge"]["criteria"], rec["vars"]),
        "semantic_rubric": [fmt(s, rec["vars"]) for s in case["judge"].get("semantic", [])],
        "judgement": judged or None, "known_gap": known_gaps.get(rec["case_id"]),
        "vars": rec["vars"], "roles": rec["roles"],
        "steps": [{"id": s["id"], "actor": s["actor"], "conversation": s["conversation"],
                   "sent": (s.get("msg") or {}).get("text"), "messageId": (s.get("msg") or {}).get("messageId"),
                   "createTime": (s.get("msg") or {}).get("createTime"), "msg_source": s.get("msg_source"),
                   "replies": attributed["by_step"].get(s["id"], [])} for s in rec["steps"]],
        "recovered_steps": recovered, "driver_status": rec.get("status"),
        "ignored_messages": attributed["ignored"], "checks": checks, "leaks": leaks, "validity": valid,
        "evidence": ev, "graded_at": iso(now()),
    }
    if out["known_gap"] and verdict == "pass":
        out["ratchet"] = "known gap now passes: remove it from cases/known_gaps.json in the same change"
    return out


def case_spans(rd: Path, drivers: list[dict[str, Any]], transcripts: dict[str, Any], reg: dict[str, Any]) -> dict[str, Any]:
    spans: dict[str, list[tuple[str, str, str]]] = {}
    for rec in drivers:
        locate_steps(rec, transcripts, reg)
        key = f"{rec['case_id']}.a{rec['attempt']}"
        firsts: dict[str, str] = {}
        for s in rec["steps"]:
            if s.get("msg"):
                firsts.setdefault(s["conversation"], s["msg"]["createTime"])
                spans.setdefault(s["conversation"], []).append((key, firsts[s["conversation"]], s["msg"]["messageId"]))
    return spans


def diff_status(prev: str | None, cur: str, prev_failed: int | None, cur_failed: int) -> str:
    if prev is None:
        return "NEW"
    p, c = VERDICT_ORDER.get(prev, 0), VERDICT_ORDER.get(cur, 0)
    if prev == "fail" and cur in ("pass",):
        return "FIXED"
    if prev == "pass" and cur == "fail":
        return "REGRESSED"
    if prev == cur:
        if prev == "fail" and prev_failed is not None and cur_failed < prev_failed:
            return "IMPROVED"
        return "UNCHANGED"
    return "IMPROVED" if c > p else "REGRESSED"


def grade_run(run_id: str, *, baseline: str | None = None, refresh: bool = True,
              cases_files: list[Path] | None = None) -> int:
    rd = run_dir(run_id)
    reg = registry()
    specs = {}
    for path in cases_files or sorted((HARNESS_DIR / "cases").glob("*.json")):
        spec = load_json(path)
        if not spec or "cases" not in spec:
            continue
        for case in spec["cases"]:
            specs[case["id"]] = case
    known_gaps = (load_json(HARNESS_DIR / "cases" / "known_gaps.json") or {}).get("gaps", {})
    judgements = load_json(rd / "judgements.json") or {}
    transcripts = final_transcripts(rd, refresh=refresh)
    drivers = [load_json(p) for p in sorted((rd / "cases").glob("*.driver.json"))]
    spans = case_spans(rd, drivers, transcripts, reg)
    results = []
    (rd / "graded").mkdir(exist_ok=True)
    for rec in drivers:
        case = specs.get(rec["case_id"])
        if not case:
            continue
        res = grade_case(rd, rec, case, transcripts, reg, spans, judgements, known_gaps)
        write_json(rd / "graded" / f"{rec['case_id']}.a{rec['attempt']}.json", res)
        results.append(res)
    # Latest attempt per case decides; invalid attempts stay listed.
    latest: dict[str, dict[str, Any]] = {}
    for res in results:
        cur = latest.get(res["case_id"])
        if cur is None or res["attempt"] > cur["attempt"]:
            latest[res["case_id"]] = res
    base = load_json(run_dir(baseline) / "summary.json") if baseline else None
    base_cases = {c["case_id"]: c for c in (base or {}).get("cases", [])}
    rows = []
    for cid, res in latest.items():
        prev = base_cases.get(cid)
        nfail = sum(1 for c in res["checks"] if not c["ok"]) + len(res["leaks"])
        rows.append({"case_id": cid, "attempt": res["attempt"], "title": res["title"], "verdict": res["verdict"],
                     "auto_verdict": res["auto_verdict"], "failed_checks": nfail, "reason": res["reason"][:300],
                     "known_gap": bool(res.get("known_gap")),
                     "model_calls": res["evidence"].get("model_calls_total"),
                     "max_calls_per_wake": res["evidence"].get("max_model_calls_per_wake"),
                     "trace_ids": [t["trace_id"] for t in res["evidence"].get("traces", [])],
                     "diff": diff_status(prev["verdict"] if prev else None, res["verdict"],
                                         prev.get("failed_checks") if prev else None, nfail) if baseline else None})
    order = [c for c in specs]
    rows.sort(key=lambda r: order.index(r["case_id"]) if r["case_id"] in order else 999)
    counts: dict[str, int] = {}
    for r in rows:
        counts[r["verdict"]] = counts.get(r["verdict"], 0) + 1
    summary = {"run_id": run_id, "graded_at": iso(now()), "baseline": baseline, "counts": counts, "cases": rows,
               "attempts": [{"case_id": r["case_id"], "attempt": r["attempt"], "verdict": r["verdict"]} for r in results]}
    write_json(rd / "summary.json", summary)
    lines = [f"# {run_id} scoreboard", "", f"graded {summary['graded_at']}; counts {counts}", ""]
    for r in rows:
        diff = f" [{r['diff']}]" if r.get("diff") else ""
        lines.append(f"- {r['case_id']} a{r['attempt']} {r['verdict']}{diff}: {r['title']} | calls={r['model_calls']} | {r['reason'][:160]}")
    (rd / "summary.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
    print("\n".join(lines))
    return 0
