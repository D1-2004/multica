"""Grader v2 for cases-v2 (harness-gaps §2.3).

- Every check yields one result with a status: pass / fail / vacuous / na /
  unsupported / pending_evidence. A check whose capability is off (`requires`)
  is vacuous; a content predicate over zero messages is vacuous (never a
  silent pass); `only_if` that does not hold is n/a.
- `tier: "target"` failures with all hard checks passing give `degraded`.
- Non-optional observe steps that never matched fail a hard check.
- Sentinels with scope case_span scan only this case's first step to ended_at.
- Grading uses the driver's paged snapshot of the case window (plus a fresh
  read when available), so later cases never push this case out of view; an
  uncovered window is a harness_error, not a pass.
"""

from __future__ import annotations

import datetime as _dt
import json
import re
from pathlib import Path
from typing import Any

from . import cases_v2, grader, im, leak
from .common import iso, load_json, now, parse_dws_time, parse_iso, registry, run_dir, write_json

VERDICT_ORDER = {"pass": 4, "needs_review": 3, "degraded": 2, "fail": 1}
EFFECT_TOOLS = ("dispatch_task", "continue_task", "steer_task", "stop_task")


def _result(check: dict[str, Any], label: str, status: str, detail: Any = None) -> dict[str, Any]:
    return {"check": label, "status": status, "ok": status != "fail", "tier": check.get("tier") or "hard",
            "requires": check.get("requires"), "note": check.get("note"), "detail": detail}


def merged_transcripts(rec: dict[str, Any], fresh: dict[str, list[dict[str, Any]]] | None) -> tuple[dict[str, list], list[str]]:
    """Driver window snapshot ∪ fresh read, deduped by messageId; plus uncovered conversations."""
    out: dict[str, dict[str, dict[str, Any]]] = {}
    uncovered = []
    for key, snap in (rec.get("transcripts") or {}).items():
        conv = key.split("@")[0]
        if not snap.get("covered", snap.get("complete", False)):
            uncovered.append(conv)
        for m in snap.get("messages") or []:
            out.setdefault(conv, {})[m["messageId"]] = m
    for conv, msgs in (fresh or {}).items():
        if conv.startswith("_"):
            continue
        for m in msgs:
            out.setdefault(conv, {}).setdefault(m["messageId"], m)
    return ({c: sorted(v.values(), key=lambda m: (m.get("createTime") or "", m["messageId"])) for c, v in out.items()},
            uncovered)


def text_check(check: dict[str, Any], msgs: list[dict[str, Any]], ctx: dict[str, Any]) -> dict[str, Any]:
    pattern_sets = ctx["pattern_sets"]
    label = "+".join(check["steps"])
    subs: list[tuple[str, str, Any]] = []  # (predicate, status, detail)
    texts = [m["text"] for m in msgs]
    joined = "\n".join(texts)
    empty = not msgs
    if "replies" in check:
        lo, hi = check["replies"]
        subs.append(("replies", "pass" if lo <= len(msgs) <= hi else "fail", {"count": len(msgs), "range": [lo, hi]}))
    for needle in check.get("include_all", []):
        subs.append((f"include {needle}", "pass" if needle in joined else "fail", None))
    if check.get("include_any"):
        hit = [n for n in check["include_any"] if n in joined]
        subs.append(("include_any", "pass" if hit else "fail", {"hit": hit}))
    for pattern in check.get("include_regex", []):
        if empty:
            subs.append((f"include_regex /{pattern}/", "vacuous", "no messages"))
            continue
        bad = [t[:60] for t in texts if not re.search(pattern, t)]
        subs.append((f"include_regex /{pattern}/", "fail" if bad else "pass", {"not_matching": bad}))
    if check.get("include_any_regex"):
        hit = [p for p in check["include_any_regex"] if re.search(p, joined)]
        subs.append(("include_any_regex", "pass" if hit else "fail", {"hit": hit}))
    excludes = [(p, None) for p in check.get("exclude", [])] + [(pattern_sets[n], n) for n in check.get("exclude_sets", [])]
    for pattern, set_name in excludes:
        name = f"exclude_set {set_name}" if set_name else f"exclude /{pattern}/"
        if empty:
            subs.append((name, "vacuous", "no messages"))
            continue
        hit = re.search(pattern, joined)
        subs.append((name, "fail" if hit else "pass", {"match": hit.group(0) if hit else None}))
    if check.get("last_include_all"):
        last = texts[-1] if texts else ""
        missing = [n for n in check["last_include_all"] if n not in last]
        subs.append(("last_include_all", "fail" if missing or empty else "pass", {"missing": missing}))
    if "max_chars" in check:
        if empty:
            subs.append(("max_chars", "vacuous", "no messages"))
        else:
            long = [len(t) for t in texts if len(t) > check["max_chars"]]
            subs.append(("max_chars", "fail" if long else "pass", {"lengths": [len(t) for t in texts]}))
    if "quotes_step" in check:
        source = ctx["step_message_ids"].get(check["quotes_step"])
        if empty:
            subs.append(("quotes_step", "vacuous", "no messages"))
        else:
            bad = [m["messageId"] for m in msgs if m.get("quotes") != source]
            subs.append(("quotes_step", "fail" if bad or not source else "pass", {"not_quoting": bad}))
    if "match_count" in check:
        lo, hi = check["match_count"]["range"]
        n = sum(1 for t in texts if re.search(check["match_count"]["regex"], t))
        subs.append(("match_count", "pass" if lo <= n <= hi else "fail", {"count": n}))
    statuses = [s for _, s, _ in subs]
    if not statuses:
        status = "vacuous"
    elif "fail" in statuses:
        status = "fail"
    elif all(s == "vacuous" for s in statuses):
        status = "vacuous"
    else:
        status = "pass"
    return _result(check, label, status, {"messages": len(msgs), "predicates": [
        {"predicate": p, "status": s, "detail": d} for p, s, d in subs]})


def evidence_check_v2(check: dict[str, Any], ev: dict[str, Any]) -> dict[str, Any]:
    kind = check["evidence"]
    label = f"evidence {kind}" + (f" @{check['step']}" if check.get("step") else "")
    if kind not in cases_v2.EVIDENCE_IMPLEMENTED:
        return _result(check, label, "unsupported", "evidence_v2 (P1) not implemented")
    if not ev.get("collected"):
        return _result(check, label, "pending_evidence", "run `e2e.py collect` then `e2e.py v2 grade`")
    loops = [t for t in ev["traces"] if t["name"] == "employee_loop"]
    if kind == "max_calls_per_wake":
        worst = max([t["model_calls"] or 0 for t in loops], default=0)
        return _result(check, label, "pass" if worst <= check["max"] else "fail", {"max": worst})
    if kind == "no_effect_for_step":
        hits = [t for t in loops if check["step"] in (t.get("matched_steps") or [])]
        if not hits:
            return _result(check, label, "vacuous", "no employee_loop wake attributed to the step")
        effects = [tc for t in hits for tc in t["tool_calls"] if tc in EFFECT_TOOLS]
        return _result(check, label, "fail" if effects else "pass",
                       {"wakes": [t["trace_id"] for t in hits], "effects": effects})
    if kind == "same_task_runs":
        tasks = sorted({x for t in ev["traces"] for x in (t.get("task_ids") or [])})
        runs = sorted({x for t in ev["traces"] for x in (t.get("run_ids") or [])})
        if check.get("if_dispatched") and not tasks:
            return _result(check, label, "na", "nothing was dispatched")
        ok = len(tasks) == 1 and len(runs) >= check.get("min_runs", 2)
        return _result(check, label, "pass" if ok else "fail", {"tasks": tasks, "runs": runs})
    return _result(check, label, "unsupported", None)


def sentinel_check(check: dict[str, Any], rec: dict[str, Any], transcripts: dict[str, list], reg: dict[str, Any]) -> dict[str, Any]:
    sentinel, parts, conv = check["sentinel"], check.get("parts") or [], check["conversation"]
    emp = set(reg["employee"]["open_ids"].values())
    starts = [parse_dws_time(s["msg"]["createTime"]) for s in rec["steps"] if s.get("msg")]
    lo = min(starts) - im.CLOCK_TOLERANCE if starts else parse_iso(rec["started_at"])
    hi = parse_iso(rec.get("ended_at") or iso(now()))
    texts = [m.get("text") or "" for m in transcripts.get(conv, [])
             if m.get("senderId") in emp and lo <= parse_dws_time(m["createTime"]) <= hi]
    whole = any(sentinel in t for t in texts)
    split = leak.split_sentinel(texts, parts)
    return _result(check, f"sentinel {sentinel} never in {conv} (case span)", "fail" if whole or split else "pass",
                   {"whole": whole, "split": split, "messages_scanned": len(texts), "window": [iso(lo), iso(hi)]})


def pending_result(check: dict[str, Any], ev: dict[str, Any], api: dict[str, Any] | None,
                   ctx: dict[str, Any]) -> dict[str, Any]:
    """Evaluate a pending (unscored) check where the harness can, else say why not."""
    from . import api_facts
    kind = check.get("evidence") or check.get("check")
    out = {"kind": kind, "why": check.get("why"), "scored": False}
    if kind in ("pg_learning", "workpacket_ref"):
        return {**out, "status": "api_gap", "detail": api_facts.API_GAPS[kind]}
    if kind in ("pg_collection", "pg_occurrence", "process_exit_confirmed"):
        if not api or api.get("error"):
            return {**out, "status": "pending_evidence", "detail": (api or {}).get("error") or "run `e2e.py collect`"}
        if kind == "pg_collection":
            return {**out, "status": "recorded", "facts": api_facts.collections(api),
                    "gap": api_facts.API_GAPS["pg_invitation"]}
        if kind == "pg_occurrence":
            return {**out, "status": "recorded", "facts": api_facts.routine_runs(api),
                    "gap": api_facts.API_GAPS["pg_occurrence_planned_at"]}
        return {**out, "status": "recorded", "facts": api_facts.exit_states(api)}
    scored = ctx.get("evaluate_pending")
    if scored:
        res = scored(check)
        if res is not None:
            return {**out, "status": res["status"], "detail": res.get("detail")}
    return {**out, "status": "unsupported"}


def segment_validity(rd: Path, rec: dict[str, Any], attributed: dict[str, Any]) -> dict[str, Any]:
    """Each segment is judged on its own window; inside a window, list the steps whose wait it hit."""
    def affected(v: dict[str, Any]) -> list[str]:
        times = [parse_iso(r["time"]) for r in v.get("restarts") or []] + \
                [parse_iso(d["deploy_started"]) for d in v.get("deploys_overlapping") or []]
        out = []
        for step in rec["steps"]:
            poll = step.get("poll") or {}
            start = (step.get("msg") or {}).get("createTime") or poll.get("since")
            if not start:
                continue
            lo = parse_dws_time(start)
            hi = lo + _dt.timedelta(seconds=float(poll.get("waited_s") or 0) + 30)
            if any(lo <= t <= hi for t in times):
                out.append(step["id"])
        return out
    segments = rec.get("segments") or {}
    if not segments:
        v = grader.validity(rd, rec, attributed)
        v["invalid_steps"] = affected(v) if not v["valid"] else []
        return v
    per = {sid: grader.validity(rd, {**rec, "started_at": seg["started_at"], "ended_at": seg["ended_at"]},
                                {"by_step": {}}) for sid, seg in segments.items()}
    bad = [sid for sid, v in per.items() if not v["valid"]]
    return {"valid": not bad, "segments": per, "invalid_segments": bad,
            "live_code": {sid: v.get("live_code") for sid, v in per.items()}}


def grade_case_v2(rd: Path, rec: dict[str, Any], case: dict[str, Any], spec: dict[str, Any],
                  caps: dict[str, dict[str, bool]], fresh: dict[str, list] | None = None,
                  judgements: dict[str, Any] | None = None) -> dict[str, Any]:
    reg = registry()
    aliases = spec["defaults"].get("aliases") or {}
    vars_ = rec["vars"]
    rendered = cases_v2.render(case["judge"], vars_, aliases)
    transcripts, uncovered = merged_transcripts(rec, fresh)
    grader.locate_steps(rec, transcripts, reg)
    drivers = [load_json(p) for p in sorted((rd / "cases").glob("*.driver.json"))]
    spans = grader.case_spans(rd, [d for d in drivers if d], transcripts, reg)
    attributed = grader.attribute(rec, transcripts, reg, spans)
    by_step = attributed["by_step"]
    ev = grader.evidence_summary(rd, rec)
    api = load_json(rd / "evidence" / f"{rec['case_id']}.a{rec['attempt']}.api.json")
    ctx = {"pattern_sets": spec["defaults"].get("pattern_sets") or {},
           "step_message_ids": {s["id"]: s["msg"]["messageId"] for s in rec["steps"] if s.get("msg")}}
    results = []
    for check in rendered.get("checks", []):
        if check.get("requires") and not cases_v2.requires_ok(check["requires"], caps):
            label = "+".join(check.get("steps", [])) or check.get("evidence") or f"sentinel {check.get('sentinel')}"
            results.append(_result(check, label, "vacuous", f"requires {check['requires']} (off)"))
            continue
        if "sentinel" in check:
            results.append(sentinel_check(check, rec, transcripts, reg))
            continue
        if "evidence" in check:
            results.append(evidence_check_v2(check, ev))
            continue
        cond = check.get("only_if")
        if cond:
            src = "\n".join(m["text"] for m in by_step.get(cond["step"], []))
            if not all(n in src for n in cond.get("include_all", [])):
                results.append(_result(check, "+".join(check["steps"]), "na", f"only_if {cond} not met"))
                continue
        msgs = [m for s in check["steps"] for m in by_step.get(s, [])]
        results.append(text_check(check, msgs, ctx))
    for step in rec["steps"]:
        if "observe" in step and not step["observe"].get("optional"):
            matched = (step.get("poll") or {}).get("matched")
            results.append(_result({}, f"observe {step['id']} matched", "pass" if matched else "fail",
                                   (step.get("poll") or {}).get("matched_message")))
    leaks = [{"step": sid, "messageId": m["messageId"], **f}
             for sid, msgs in by_step.items() for m in msgs for f in leak.scan(m["text"])]
    at_bad = [s["id"] for s in rec["steps"] if len(s.get("at") or []) >= 2
              and not ((s.get("send") or {}).get("at_render") or {}).get("ok", True)]
    missing = [s["id"] for s in rec["steps"] if s.get("send") and not s.get("msg")]
    valid = segment_validity(rd, rec, attributed)
    hard = [r for r in results if r["tier"] == "hard"]
    target = [r for r in results if r["tier"] == "target"]
    status = rec.get("status", "")
    if status.startswith("not_run") or status == "skipped_window":
        verdict, reason = "not_run", status
    elif status.startswith("segment_done") or status.startswith("waiting_segment"):
        verdict, reason = "in_progress", f"{status}; resume with `e2e.py v2 run --only {rec['case_id']} --segment <next>`"
    elif status == "harness_error" or missing or uncovered or at_bad or any(r["status"] == "unsupported" for r in results):
        verdict = "harness_error"
        reason = rec.get("harness_error") or (f"steps not landed {missing}" if missing else
                                              f"window not covered {uncovered}" if uncovered else
                                              f"multi-@ rendered wrong {at_bad}" if at_bad else "unsupported check")
    elif status != "completed":
        verdict, reason = "invalid_env", f"driver status {status}"
    elif not valid["valid"]:
        verdict = "invalid_env"
        reason = (f"restart or deploy inside segment(s) {valid['invalid_segments']}; rerun them with --segment X --redo"
                  if valid.get("invalid_segments") else
                  f"restart or deploy inside the case window (affected steps {valid.get('invalid_steps')})")
    elif any(r["status"] == "fail" for r in hard) or leaks:
        verdict = "fail"
        reason = "; ".join([r["check"] for r in hard if r["status"] == "fail"] +
                           [f"leak:{l['kind']}:{l['match']}" for l in leaks])
    elif any(r["status"] == "fail" for r in target):
        verdict, reason = "degraded", "; ".join(r["check"] for r in target if r["status"] == "fail")
    else:
        verdict, reason = "needs_review", "hard checks pass; semantic rubric pending"
    auto = verdict
    judged = (judgements or {}).get(f"{rec['case_id']}.a{rec['attempt']}") or {}
    if judged.get("verdict") and verdict in ("fail", "needs_review", "degraded"):
        verdict, reason = judged["verdict"], judged.get("rationale", reason)
    counts: dict[str, int] = {}
    for r in results:
        counts[r["status"]] = counts.get(r["status"], 0) + 1
    return {
        "schema": "el2e.graded.v2", "case_id": rec["case_id"], "attempt": rec["attempt"], "title": case["title"],
        "verdict": verdict, "auto_verdict": auto, "reason": reason, "check_counts": counts,
        "criteria": rendered.get("criteria"), "semantic_rubric": rendered.get("semantic", []),
        "known_gap": case.get("known_gap"), "pending_checks": case.get("pending_checks", []),
        "pending_results": [pending_result(cases_v2.render(c, vars_, aliases), ev, api, ctx)
                            for c in case.get("pending_checks") or []],
        "api_facts": {"tasks": len((api or {}).get("tasks") or []), "error": (api or {}).get("error")} if api else None,
        "vars": vars_, "var_row": rec.get("var_row"), "roles": rec["roles"], "judgement": judged or None,
        "steps": [{"id": s["id"], "actor": s.get("actor"), "conversation": s.get("conversation"),
                   "sent": (s.get("msg") or {}).get("text"), "messageId": (s.get("msg") or {}).get("messageId"),
                   "createTime": (s.get("msg") or {}).get("createTime"),
                   "reply_to": (s.get("send") or {}).get("reply_to_resolved"),
                   "observe": (s.get("poll") or {}).get("matched_message") if "observe" in s else None,
                   "replies": by_step.get(s["id"], [])} for s in rec["steps"]],
        "checks": results, "leaks": leaks, "ignored_messages": attributed["ignored"], "validity": valid,
        "uncovered": uncovered, "evidence": ev, "graded_at": iso(now()),
        "memory_reset": {"ok": (rec.get("memory_reset") or {}).get("ok"),
                         "commands": (rec.get("memory_reset") or {}).get("commands")} if rec.get("memory_reset") else None,
    }


def grade_and_write(rd: Path, rec: dict[str, Any], case: dict[str, Any], spec: dict[str, Any],
                    caps: dict[str, dict[str, bool]], fresh: dict[str, list] | None = None) -> dict[str, Any]:
    res = grade_case_v2(rd, rec, case, spec, caps, fresh, load_json(rd / "judgements.json") or {})
    (rd / "graded").mkdir(exist_ok=True)
    write_json(rd / "graded" / f"{rec['case_id']}.a{rec['attempt']}.json", res)
    return res


def grade_run_v2(run_id: str, *, baseline: str | None = None, caps: dict[str, dict[str, bool]] | None = None) -> int:
    """Regrade every v2 attempt in the run (after `collect`), then write summary_v2.{json,md}."""
    rd = run_dir(run_id)
    specs = {case["id"]: (spec, case) for spec, case in cases_v2.load_all()}
    results = []
    for path in sorted((rd / "cases").glob("*.driver.json")):
        rec = load_json(path)
        if not rec or rec.get("schema") != "el2e.driver.v2" or rec["case_id"] not in specs:
            continue
        spec, case = specs[rec["case_id"]]
        results.append(grade_and_write(rd, rec, case, spec, caps or rec.get("capabilities") or cases_v2.load_capabilities()))
    latest: dict[str, dict[str, Any]] = {}
    for res in results:
        if res["case_id"] not in latest or res["attempt"] > latest[res["case_id"]]["attempt"]:
            latest[res["case_id"]] = res
    base = load_json(run_dir(baseline) / "summary_v2.json") if baseline else None
    base_rows = {r["case_id"]: r for r in (base or {}).get("cases", [])}
    rows = []
    for cid in [c for c in specs if c in latest]:
        res = latest[cid]
        prev = base_rows.get(cid)
        row = {"case_id": cid, "attempt": res["attempt"], "verdict": res["verdict"], "reason": res["reason"][:240],
               "check_counts": res["check_counts"], "known_gap": res.get("known_gap"),
               "pending_checks": len(res.get("pending_checks") or []),
               "model_calls": res["evidence"].get("model_calls_total")}
        if baseline:
            row["diff"] = "NEW" if prev is None else diff(prev["verdict"], res["verdict"])
        rows.append(row)
    counts: dict[str, int] = {}
    for r in rows:
        counts[r["verdict"]] = counts.get(r["verdict"], 0) + 1
    summary = {"run_id": run_id, "graded_at": iso(now()), "baseline": baseline, "counts": counts, "cases": rows,
               "pending_evidence": [{"case_id": r["case_id"], "pending_checks": r["pending_checks"]}
                                    for r in rows if r["pending_checks"]]}
    write_json(rd / "summary_v2.json", summary)
    lines = [f"# {run_id} cases-v2 scoreboard", "", f"counts {counts}", ""]
    for r in rows:
        tag = f" [{r['diff']}]" if r.get("diff") else ""
        lines.append(f"- {r['case_id']} a{r['attempt']} {r['verdict']}{tag} {r['check_counts']} | {r['reason'][:140]}")
    if summary["pending_evidence"]:
        lines += ["", "待补证据 (pending_checks, not scored): " +
                  ", ".join(f"{p['case_id']}×{p['pending_checks']}" for p in summary["pending_evidence"])]
    (rd / "summary_v2.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
    print("\n".join(lines))
    return 0


def diff(prev: str, cur: str) -> str:
    p, c = VERDICT_ORDER.get(prev, 0), VERDICT_ORDER.get(cur, 0)
    if prev == cur:
        return "UNCHANGED"
    if prev in ("fail", "degraded") and cur == "pass":
        return "FIXED"
    return "IMPROVED" if c > p else "REGRESSED"
