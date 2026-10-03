"""cases-v2 driver: plays the people, records raw observations, then grades at once.

Differences from the v1 driver (harness-gaps §2):
- variables come from the case's var_sets row (seed run:case:attempt) on top
  of the driver codes; variables are rendered before aliases;
- `reply_to` quote-replies a resolved message (step / employee_reply_of /
  observed / employee_latest, with fallback) and never degrades to a plain send;
- DEAP actors never @ and never DM; their landings are read back by a human
  reader of the conversation by senderId;
- human sends carry --ai-tag=false; every landing is located by the sender's
  id as the reader sees it and can't reuse a message an earlier step claimed;
- the case window is read with paging and graded immediately after the case.
"""

from __future__ import annotations

import json
import re
import time
from pathlib import Path
from typing import Any

from . import cases_v2, dwsgw, envguard, im
from .common import iso, load_json, now, parse_dws_time, parse_iso, registry, run_dir, write_json
from .driver import ensure_dm, next_attempt


class StepError(RuntimeError):
    """A step the harness cannot perform faithfully: recorded as harness_error, never improvised."""


def human_reader(reg: dict[str, Any], conv: dict[str, Any], actor: str) -> str:
    """Who reads back a landing: the sender when human and a reader of the conversation, else the first human reader."""
    if reg["actors"][actor].get("kind") == "human" and (actor in conv["readers"] or conv.get("kind") == "dm"):
        return actor
    for reader in conv["readers"]:
        if reg["actors"][reader].get("kind") == "human":
            return reader
    raise StepError(f"conversation has no human reader for {actor}")


def view_id(reg: dict[str, Any], who: str, viewer: str) -> str | None:
    """`who`'s openDingTalkId as `viewer` sees it (ids are observer-relative)."""
    if who == "employee":
        return reg["employee"]["open_ids"].get(viewer)
    ids = reg["actors"][who].get("open_ids", {})
    return ids.get("self") if who == viewer else ids.get(viewer)


def transcript(reg: dict[str, Any], conv: dict[str, Any], rec: dict[str, Any]) -> list[dict[str, Any]]:
    reader = conv["readers"][0]
    since = parse_iso(rec["started_at"])
    return im.read_window(reg["actors"][reader]["profile"], conv["cid"], since, max_pages=4)["messages"]


def resolve_reply_to(step: dict[str, Any], rec: dict[str, Any], reg: dict[str, Any], conv_name: str,
                     conv: dict[str, Any], landed_by_step: dict[str, dict[str, Any]]) -> dict[str, Any]:
    """The quoted message and whether its author is the employee."""
    from . import grader
    target = step["reply_to"]
    emp = set(reg["employee"]["open_ids"].values())
    tried = []
    for kind in [k for k in cases_v2.REPLY_TO_TARGETS if k in target] + ([target["fallback"]] if target.get("fallback") else []):
        tried.append(kind)
        if kind == "step":
            landed = landed_by_step.get(target["step"])
            if landed:
                return {"messageId": landed["messageId"], "by_employee": False, "via": kind}
        elif kind == "observed":
            prior = next((s for s in rec["steps"] if s["id"] == target["observed"]), None)
            msg = ((prior or {}).get("poll") or {}).get("matched_message")
            if msg:
                return {"messageId": msg["messageId"], "by_employee": True, "via": kind}
        elif kind == "employee_reply_of":
            msgs = transcript(reg, conv, rec)
            partial = json.loads(json.dumps(rec))
            grader.locate_steps(partial, {conv_name: msgs}, reg)
            by_step = grader.attribute(partial, {conv_name: msgs}, reg, {})["by_step"]
            replies = by_step.get(target["employee_reply_of"]) or []
            if replies:
                return {"messageId": replies[-1]["messageId"], "by_employee": True, "via": kind}
        elif kind == "employee_latest":
            msgs = transcript(reg, conv, rec) if conv.get("kind") == "group" else \
                im.read_messages(reg["actors"][conv["readers"][0]]["profile"], conv["cid"], limit=30)["messages"]
            mine = [m for m in msgs if im.classify(m, emp, reg["employee"]["name"]) == "employee"]
            if not mine and conv.get("kind") == "group":
                mine = [m for m in im.read_messages(reg["actors"][conv["readers"][0]]["profile"], conv["cid"], limit=30)["messages"]
                        if im.classify(m, emp, reg["employee"]["name"]) == "employee"]
            if mine:
                return {"messageId": mine[-1]["messageId"], "by_employee": True, "via": kind}
    raise StepError(f"reply_to target not found (tried {tried})")


def observe_v2(reg: dict[str, Any], conv: dict[str, Any], since: str, pattern: str, timeout_s: int,
               include_placeholders: bool) -> dict[str, Any]:
    rx = re.compile(pattern)
    emp = set(reg["employee"]["open_ids"].values())
    reader = conv["readers"][0]
    floor = parse_dws_time(since) - im.CLOCK_TOLERANCE
    start = time.monotonic()
    matched, seen = None, {}
    while time.monotonic() - start < timeout_s:
        for m in im.read_messages(reg["actors"][reader]["profile"], conv["cid"], limit=30)["messages"]:
            kind = im.classify(m, emp, reg["employee"]["name"])
            if parse_dws_time(m["createTime"]) < floor:
                continue
            if kind == "employee" or (include_placeholders and kind == "placeholder" and m.get("senderId") in emp):
                seen.setdefault(m["messageId"], m)
                if matched is None and rx.search(m.get("text") or ""):
                    matched = m
        if matched:
            break
        time.sleep(8)
    return {"mode": "observe", "pattern": pattern, "matched": bool(matched), "reader": reader,
            "matched_message": {k: matched.get(k) for k in ("messageId", "createTime", "text")} if matched else None,
            "waited_s": round(time.monotonic() - start, 1),
            "replies": [{"messageId": m["messageId"], "createTime": m["createTime"], "text": m.get("text")}
                        for m in sorted(seen.values(), key=lambda x: x["createTime"])]}


def speak(step: dict[str, Any], case: dict[str, Any], spec: dict[str, Any], rec: dict[str, Any],
          reg: dict[str, Any], conv_name: str, conv: dict[str, Any], vars_: dict[str, str],
          landed_by_step: dict[str, dict[str, Any]], claimed: set[str], marker: str) -> dict[str, Any]:
    aliases = spec["defaults"].get("aliases") or {}
    actor = case["roles"][step["actor"]]
    info = reg["actors"][actor]
    deap = info.get("kind") == "deap_actor"
    if deap and (step.get("at") or step.get("at_all") or conv.get("kind") == "dm"):
        raise StepError(f"DEAP actor {actor} cannot @ or DM")
    text = cases_v2.render(step["text"], vars_, aliases)
    match_key = cases_v2.render(step.get("match_key", ""), vars_, aliases) or None
    ai_tag = None if deap else bool(spec["defaults"].get("human_send", {}).get("ai_tag", True))
    reader = human_reader(reg, conv, actor)
    sender_id = view_id(reg, actor, reader)
    emp_ids = set(reg["employee"]["open_ids"].values())
    at_names = list(step.get("at", []))
    quoted = None
    if step.get("reply_to"):
        quoted = resolve_reply_to(step, rec, reg, conv_name, conv, landed_by_step)
        if quoted["by_employee"] and "employee" in at_names:
            # A quote already @-mentions its author; a second <@id> would double the mention.
            at_names.remove("employee")
    at_ids = []
    for name in at_names:
        who = "employee" if name == "employee" else case["roles"][name]
        oid = view_id(reg, who, actor)
        if not oid:
            raise StepError(f"no id for {name} in {actor}'s view")
        at_ids.append(oid)
    common_kw = dict(profile=info["profile"], cid=conv["cid"], text=text, marker=marker, at_ids=at_ids,
                     match_key=match_key, exclude_sender_ids=emp_ids, ai_tag=ai_tag,
                     reader_profile=reg["actors"][reader]["profile"], sender_id=sender_id, claimed_ids=claimed)
    if quoted:
        dm_peer = view_id(reg, "employee", actor) if conv.get("kind") == "dm" else None
        sent = im.reply(quoted_message_id=quoted["messageId"], dm_open_id=dm_peer, **common_kw)
        sent["reply_to_resolved"] = quoted
    else:
        sent = im.send(**common_kw)
    expected = len(at_ids) + (1 if quoted else 0)
    found = im.prefix_mentions(sent["landed"][0]["text"]) if sent["landed"] else []
    sent["at_render"] = {"expected": expected, "found": found, "ok": len(found) >= expected}
    sent["actor"], sent["reader"], sent["deap"] = actor, reader, deap
    return sent


def run_case_v2(case: dict[str, Any], spec: dict[str, Any], run_id: str, rd: Path,
                caps: dict[str, dict[str, bool]], *, skip_gate: bool = False, use_lease: bool = True,
                log=print) -> dict[str, Any]:
    from . import grader_v2
    reg = registry()
    cases_dir = rd / "cases"
    cases_dir.mkdir(parents=True, exist_ok=True)
    attempt = next_attempt(cases_dir, case["id"])
    out_path = cases_dir / f"{case['id']}.a{attempt}.driver.json"
    vars_, row = cases_v2.select_vars(case, run_id, attempt)
    rec: dict[str, Any] = {
        "schema": "el2e.driver.v2", "case_id": case["id"], "title": case["title"], "attempt": attempt,
        "run_id": run_id, "suite": spec.get("suite"), "scene": case.get("scene"), "roles": case["roles"],
        "vars": vars_, "var_row": row, "capabilities": caps, "started_at": iso(now()), "steps": [],
        "status": "running", "employee": {"agent_id": reg["employee"]["agent_id"], "name": reg["employee"]["name"]},
    }
    plan = cases_v2.classify(case, spec, caps, reg)
    rec["plan"] = plan
    if plan["state"] not in ("runnable", "runnable_partial"):
        rec.update(status=f"not_run:{plan['state']}", ended_at=iso(now()))
        write_json(out_path, rec)
        return rec
    if not cases_v2.in_run_window(case.get("x_run_window")):
        rec.update(status="skipped_window", ended_at=iso(now()))
        write_json(out_path, rec)
        return rec
    deap = sorted({a for a in case["roles"].values() if reg["actors"][a].get("kind") == "deap_actor"})
    holder = f"el2e:{run_id}:{case['id']}"
    if deap and use_lease:
        from . import lease
        rec["lease"] = lease.acquire(deap, holder)
        if not rec["lease"].get("ok"):
            rec.update(status="not_run:actor_leased", ended_at=iso(now()))
            write_json(out_path, rec)
            return rec
    rec["gate"] = None if skip_gate else envguard.gate(rd, log=log)
    rec["started_at"] = iso(now())
    write_json(out_path, rec)
    defaults = spec["defaults"].get("wait", {})
    aliases = spec["defaults"].get("aliases") or {}
    landed_by_step: dict[str, dict[str, Any]] = {}
    claimed: set[str] = set()
    emp_ids = set(reg["employee"]["open_ids"].values())
    try:
        for step in case["steps"]:
            reg = registry()
            conv_name = step.get("conversation", case["conversation"])
            conv = reg["conversations"][conv_name]
            if "observe" in step:
                obs = step["observe"]
                ref = landed_by_step[obs["since_step"]] if obs.get("since_step") else list(landed_by_step.values())[-1]
                poll_rec = observe_v2(reg, conv, ref["createTime"], cases_v2.render(obs["until_regex"], vars_, aliases),
                                      obs.get("timeout_s", 300), bool(obs.get("include_placeholders")))
                rec["steps"].append({"id": step["id"], "observe": obs, "conversation": conv_name, "cid": conv["cid"],
                                     "poll": poll_rec, "actor": None, "role": None})
                write_json(out_path, rec)
                log(f"[{case['id']}] {step['id']} observe matched={poll_rec['matched']} in {poll_rec['waited_s']}s")
                continue
            marker = f"{run_id}:{case['id']}:a{attempt}:{step['id']}"
            if not conv.get("cid"):
                actor = case["roles"][step["actor"]]
                text = cases_v2.render(step["text"], vars_, aliases)
                opened = ensure_dm(reg, conv_name, actor, text, marker)
                rec.setdefault("opened_conversations", []).append({"conversation": conv_name, **opened})
                reg = registry()
                conv = reg["conversations"][conv_name]
            sent = speak(step, case, spec, rec, reg, conv_name, conv, vars_, landed_by_step, claimed, marker)
            step_rec: dict[str, Any] = {"id": step["id"], "role": step["actor"], "actor": sent["actor"],
                                        "conversation": conv_name, "cid": conv["cid"], "at": step.get("at", []),
                                        "send": sent}
            rec["steps"].append(step_rec)
            write_json(out_path, rec)
            log(f"[{case['id']}] {step['id']} as {sent['actor']}: landing={sent['landing_count']} {sent['text'][:50]!r}")
            if not sent["ok"]:
                rec["status"] = "send_failed" if sent["landing_count"] == 0 else "send_duplicated"
                break
            landed = sent["landed"][0]
            landed_by_step[step["id"]] = landed
            claimed.add(landed["messageId"])
            wait = dict(defaults.get(step["wait"]["mode"], {}))
            wait.update(step["wait"])
            mode = wait.pop("mode")
            if mode == "none":
                time.sleep(wait.get("pause_s", 5))
                continue
            since_step = wait.pop("since_step", None)
            since = landed_by_step[since_step]["createTime"] if since_step else landed["createTime"]
            reader = sent["reader"]
            kwargs = {k: wait[k] for k in ("timeout_s", "settle_s", "window_s", "min_replies") if k in wait}
            if mode == "optional":
                poll_rec = im.poll(profile=reg["actors"][reader]["profile"], cid=conv["cid"], since=since,
                                   employee_ids=emp_ids, employee_name=reg["employee"]["name"],
                                   source_message_id=landed["messageId"], mode="reply",
                                   timeout_s=wait.get("window_s", 45), settle_s=wait.get("settle_s", 15))
                poll_rec["mode"] = "optional"
            else:
                poll_rec = im.poll(profile=reg["actors"][reader]["profile"], cid=conv["cid"], since=since,
                                   employee_ids=emp_ids, employee_name=reg["employee"]["name"],
                                   source_message_id=landed["messageId"],
                                   mode="reply" if mode == "reply" else "silence", **kwargs)
            poll_rec["reader"] = reader
            step_rec["wait"] = {"mode": mode, **wait, "since_step": since_step}
            step_rec["poll"] = poll_rec
            write_json(out_path, rec)
            log(f"[{case['id']}] {step['id']} {mode}: {len(poll_rec['replies'])} employee msg(s)")
            if poll_rec.get("gateway_offline_seen"):
                rec["status"] = "gateway_offline"
                break
    except StepError as exc:
        rec["status"] = "harness_error"
        rec["harness_error"] = str(exc)
    finally:
        if deap and use_lease and (rec.get("lease") or {}).get("ok"):
            from . import lease
            rec["lease_release"] = lease.release(deap, holder)
    if rec["status"] == "running":
        rec["status"] = "completed"
    time.sleep(10)  # late replies (task results) still land in the snapshot
    rec["ended_at"] = iso(now())
    rec["transcripts"] = {}
    since = min((parse_dws_time(s["send"]["landed"][0]["createTime"]) for s in rec["steps"]
                 if (s.get("send") or {}).get("landed")), default=parse_iso(rec["started_at"]))
    for conv_name in sorted({s["conversation"] for s in rec["steps"]}):
        conv = registry()["conversations"][conv_name]
        reader = conv["readers"][0]
        snap = im.read_window(reg["actors"][reader]["profile"], conv["cid"], since - im.CLOCK_TOLERANCE)
        rec["transcripts"][f"{conv_name}@{reader}"] = {"covered": snap["covered"], "pages": snap["pages"],
                                                       "messages": snap["messages"]}
    write_json(out_path, rec)
    grader_v2.grade_and_write(rd, rec, case, spec, caps)
    return rec


def run_v2(paths: list[Path], run_id: str, *, only: list[str], caps: dict[str, dict[str, bool]],
           skip_gate: bool = False, use_lease: bool = True) -> int:
    rd = run_dir(run_id)
    manifest = load_json(rd / "manifest.json", {}) or {}
    manifest.setdefault("run_id", run_id)
    manifest.setdefault("started_at", iso(now()))
    manifest["registry_snapshot"] = registry()
    manifest["capabilities_v2"] = caps
    write_json(rd / "manifest.json", manifest)
    dwsgw.prepare()
    pairs = cases_v2.load_all(paths)
    if only:
        order = {cid: i for i, cid in enumerate(only)}
        pairs = sorted([p for p in pairs if p[1]["id"] in order], key=lambda p: order[p[1]["id"]])
    rc = 0
    for spec, case in pairs:
        rec = run_case_v2(case, spec, run_id, rd, caps, skip_gate=skip_gate, use_lease=use_lease)
        print(json.dumps({"case": case["id"], "attempt": rec["attempt"], "status": rec["status"]}, ensure_ascii=False),
              flush=True)
        if rec["status"] != "completed":
            rc = 1
    return rc
