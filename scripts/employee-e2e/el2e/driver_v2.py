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

import datetime as _dt
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


def latest_attempt(cases_dir: Path, case_id: str) -> tuple[Path, dict[str, Any]] | None:
    found = sorted(cases_dir.glob(f"{case_id}.a*.driver.json"), key=lambda x: int(x.name.split(".a")[-1].split(".")[0]))
    return (found[-1], load_json(found[-1])) if found else None


class CaseRun:
    """One case attempt (or one segment of it) as the driver plays it."""

    def __init__(self, case: dict[str, Any], spec: dict[str, Any], rec: dict[str, Any], out_path: Path,
                 rd: Path, log=print) -> None:
        self.case, self.spec, self.rec, self.out_path, self.rd, self.log = case, spec, rec, out_path, rd, log
        self.vars = rec["vars"]
        self.aliases = spec["defaults"].get("aliases") or {}
        self.defaults = spec["defaults"].get("wait", {})
        self.landed_by_step: dict[str, dict[str, Any]] = {}
        self.claimed: set[str] = set()
        for step in rec["steps"]:  # resume: earlier segments' landings stay referable
            landed = (step.get("send") or {}).get("landed") or []
            if landed:
                self.landed_by_step[step["id"]] = landed[0]
                self.claimed.add(landed[0]["messageId"])

    # -- plumbing
    def reg(self) -> dict[str, Any]:
        reg = registry()
        reg["conversations"].update(self.rec.get("conversations_overlay") or {})
        return reg

    def save(self) -> None:
        write_json(self.out_path, self.rec)

    def render(self, value: Any) -> Any:
        return cases_v2.render(value, self.vars, self.aliases)

    def emp_ids(self, reg: dict[str, Any]) -> set[str]:
        return set(reg["employee"]["open_ids"].values())

    # -- steps
    def run_steps(self, steps: list[dict[str, Any]]) -> None:
        for step in steps:
            reg = self.reg()
            conv_name = step.get("conversation", self.case["conversation"])
            if "observe" in step:
                self.observe(step, reg, conv_name)
            elif step.get("kind"):
                self.action(step, reg, conv_name)
            else:
                self.say(step, reg, conv_name)
            if self.rec["status"] != "running":
                return

    def observe(self, step: dict[str, Any], reg: dict[str, Any], conv_name: str) -> None:
        obs = step["observe"]
        conv = reg["conversations"][conv_name]
        ref = self.landed_by_step[obs["since_step"]] if obs.get("since_step") else list(self.landed_by_step.values())[-1]
        poll_rec = observe_v2(reg, conv, ref["createTime"], self.render(obs["until_regex"]),
                              obs.get("timeout_s", 300), bool(obs.get("include_placeholders")))
        self.rec["steps"].append({"id": step["id"], "observe": obs, "conversation": conv_name, "cid": conv["cid"],
                                  "segment": step.get("segment"), "poll": poll_rec, "actor": None, "role": None})
        self.save()
        self.log(f"[{self.case['id']}] {step['id']} observe matched={poll_rec['matched']} in {poll_rec['waited_s']}s")

    def action(self, step: dict[str, Any], reg: dict[str, Any], conv_name: str) -> None:
        handler = ACTIONS.get(step["kind"])
        if handler is None:
            raise StepError(f"action kind {step['kind']} is not implemented")
        handler(self, step, reg, conv_name)

    def say(self, step: dict[str, Any], reg: dict[str, Any], conv_name: str) -> None:
        conv = reg["conversations"][conv_name]
        marker = f"{self.rec['run_id']}:{self.case['id']}:a{self.rec['attempt']}:{step['id']}"
        if not conv.get("cid"):
            actor = self.case["roles"][step["actor"]]
            opened = ensure_dm(reg, conv_name, actor, self.render(step["text"]), marker)
            self.rec.setdefault("opened_conversations", []).append({"conversation": conv_name, **opened})
            reg = self.reg()
            conv = reg["conversations"][conv_name]
        sent = speak(step, self.case, self.spec, self.rec, reg, conv_name, conv, self.vars, self.landed_by_step,
                     self.claimed, marker)
        self.record_send(step, conv_name, conv, sent)

    def record_send(self, step: dict[str, Any], conv_name: str, conv: dict[str, Any], sent: dict[str, Any]) -> None:
        step_rec: dict[str, Any] = {"id": step["id"], "role": step.get("actor"), "actor": sent["actor"],
                                    "conversation": conv_name, "cid": conv["cid"], "at": step.get("at", []),
                                    "segment": step.get("segment"), "kind": step.get("kind"), "send": sent}
        self.rec["steps"].append(step_rec)
        self.save()
        self.log(f"[{self.case['id']}] {step['id']} as {sent['actor']}: landing={sent['landing_count']} "
                 f"{str(sent.get('text', ''))[:50]!r}")
        if not sent["ok"]:
            self.rec["status"] = "send_failed" if sent["landing_count"] == 0 else "send_duplicated"
            return
        landed = sent["landed"][0]
        self.landed_by_step[step["id"]] = landed
        self.claimed.add(landed["messageId"])
        self.wait(step, step_rec, landed, sent["reader"], conv)

    def wait(self, step: dict[str, Any], step_rec: dict[str, Any], landed: dict[str, Any], reader: str,
             conv: dict[str, Any]) -> None:
        if not step.get("wait"):
            return
        reg = self.reg()
        emp_ids = self.emp_ids(reg)
        wait = dict(self.defaults.get(step["wait"]["mode"], {}))
        wait.update(step["wait"])
        mode = wait.pop("mode")
        if mode == "none":
            time.sleep(wait.get("pause_s", 5))
            return
        since_step = wait.pop("since_step", None)
        since = self.landed_by_step[since_step]["createTime"] if since_step else landed["createTime"]
        common = dict(profile=reg["actors"][reader]["profile"], cid=conv["cid"], since=since, employee_ids=emp_ids,
                      employee_name=reg["employee"]["name"], source_message_id=landed["messageId"])
        if mode == "optional":
            poll_rec = im.poll(mode="reply", timeout_s=wait.get("window_s", 45), settle_s=wait.get("settle_s", 15),
                               **common)
            poll_rec["mode"] = "optional"
        else:
            kwargs = {k: wait[k] for k in ("timeout_s", "settle_s", "window_s", "min_replies") if k in wait}
            poll_rec = im.poll(mode="reply" if mode == "reply" else "silence", **kwargs, **common)
        poll_rec["reader"] = reader
        step_rec["wait"] = {"mode": mode, **wait, "since_step": since_step}
        step_rec["poll"] = poll_rec
        self.save()
        self.log(f"[{self.case['id']}] {step['id']} {mode}: {len(poll_rec['replies'])} employee msg(s)")
        if poll_rec.get("gateway_offline_seen"):
            self.rec["status"] = "gateway_offline"

    def snapshot(self, since: _dt.datetime, label: str) -> None:
        reg = self.reg()
        for conv_name in sorted({s["conversation"] for s in self.rec["steps"] if s.get("conversation")}):
            conv = reg["conversations"][conv_name]
            reader = conv["readers"][0]
            snap = im.read_window(reg["actors"][reader]["profile"], conv["cid"], since - im.CLOCK_TOLERANCE)
            self.rec.setdefault("transcripts", {})[f"{conv_name}@{reader}{label}"] = {
                "covered": snap["covered"], "pages": snap["pages"], "messages": snap["messages"]}


ACTIONS: dict[str, Any] = {}
FIXTURE_DIR = cases_v2.V2_DIR / "fixtures"


def fixture_bytes(run: "CaseRun", key: str) -> tuple[str, bytes]:
    """(file name, content) of a case fixture for this attempt's var_sets row."""
    fx = (run.case.get("fixtures") or {}).get(key)
    if fx is None:
        raise StepError(f"fixture {key} not declared")
    name = run.render(fx["name"])
    row = run.rec.get("var_row") or 0
    if "template" in fx:
        return name, run.render(fx["template"]).encode("utf-8")
    if "by_row" in fx:
        return name, run.render(fx["by_row"][row]).encode("utf-8")
    if "render" in fx:
        path = FIXTURE_DIR / run.case["id"] / f"{key}.row{row}{Path(name).suffix}"
        if not path.exists():
            raise StepError(f"pre-rendered fixture {path.name} missing; run cases/v2/fixtures/render_png.py")
        return name, path.read_bytes()
    raise StepError(f"fixture {key} has no template/by_row/render")


def act_file(run: "CaseRun", step: dict[str, Any], reg: dict[str, Any], conv_name: str) -> None:
    conv = reg["conversations"][conv_name]
    actor = run.case["roles"][step["actor"]]
    if reg["actors"][actor].get("kind") != "human":
        raise StepError("file sends are human-only in this harness")
    name, data = fixture_bytes(run, step["file"])
    workdir = run.rd / "fixtures" / f"{run.case['id']}.a{run.rec['attempt']}.{step['id']}"
    workdir.mkdir(parents=True, exist_ok=True)
    (workdir / name).write_bytes(data)
    reader = human_reader(reg, conv, actor)
    sent = im.send_file(profile=reg["actors"][actor]["profile"], cid=conv["cid"], workdir=str(workdir), filename=name,
                        marker=f"{run.rec['run_id']}:{run.case['id']}:a{run.rec['attempt']}:{step['id']}",
                        ai_tag=bool(run.spec["defaults"].get("human_send", {}).get("ai_tag", True)),
                        reader_profile=reg["actors"][reader]["profile"], sender_id=view_id(reg, actor, reader),
                        claimed_ids=run.claimed)
    import hashlib
    sent.update(actor=actor, reader=reader, deap=False, file={"name": name, "bytes": len(data),
                                                             "sha256": hashlib.sha256(data).hexdigest()})
    run.record_send(step, conv_name, conv, sent)


ACTIONS["file"] = act_file


def segment_ids(case: dict[str, Any]) -> list[str]:
    return [s["id"] for s in case.get("segments") or []]


def renew_access(case: dict[str, Any], reg: dict[str, Any]) -> dict[str, Any]:
    """Before a later segment: refresh every actor token; renew 主角's 24 h cross-org grant."""
    actors = sorted({a for a in case["roles"].values() if a})
    out = {"refresh": dwsgw.refresh([reg["actors"][a]["profile"] for a in actors])}
    dwsgw.prepare()
    if "zhujue" in actors:
        res = dwsgw.dws(reg["actors"]["zhujue"]["profile"],
                        ["chat", "data-auth", "cross-org", "--all", "--grant-type", "timed", "--ttl", "24h", "--yes"])
        out["cross_org_grant"] = {"rc": res["rc"]}
    return out


def run_case_v2(case: dict[str, Any], spec: dict[str, Any], run_id: str, rd: Path,
                caps: dict[str, dict[str, bool]], *, skip_gate: bool = False, use_lease: bool = True,
                segment: str | None = None, redo: bool = False, log=print) -> dict[str, Any]:
    from . import grader_v2
    reg = registry()
    cases_dir = rd / "cases"
    cases_dir.mkdir(parents=True, exist_ok=True)
    segs = segment_ids(case)
    if segment and segment not in segs:
        raise SystemExit(f"{case['id']} has no segment {segment} (segments: {segs})")
    resuming = bool(segs) and segment not in (None, segs[0])
    if resuming:
        prev = latest_attempt(cases_dir, case["id"])
        if not prev:
            raise SystemExit(f"{case['id']}: run segment {segs[0]} first")
        out_path, rec = prev
        done = [k for k, v in (rec.get("segments") or {}).items() if v.get("status") == "done"]
        need = segs[:segs.index(segment)]
        if [x for x in need if x not in done]:
            raise SystemExit(f"{case['id']}: segments {need} must be done before {segment} (done: {done})")
        if segment in done and not redo:
            raise SystemExit(f"{case['id']}: segment {segment} already done; pass --redo to rerun it")
        spec_seg = next(x for x in case["segments"] if x["id"] == segment)
        last_end = parse_iso(rec["segments"][need[-1]]["ended_at"])
        not_before = last_end + _dt.timedelta(hours=float(spec_seg.get("not_before_hours") or 0))
        if now() < not_before:
            log(f"[{case['id']}] segment {segment} not before {iso(not_before)}")
            return {**rec, "status": f"waiting_segment:{segment}", "not_before": iso(not_before)}
        if redo:
            rec.setdefault("discarded_steps", []).extend(s for s in rec["steps"] if s.get("segment") == segment)
            rec["steps"] = [s for s in rec["steps"] if s.get("segment") != segment]
        rec["status"] = "running"
        rec["access_renewal"] = renew_access(case, reg)
    else:
        attempt = next_attempt(cases_dir, case["id"])
        out_path = cases_dir / f"{case['id']}.a{attempt}.driver.json"
        vars_, row = cases_v2.select_vars(case, run_id, attempt)
        rec = {
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
    segment = segment or (segs[0] if segs else None)
    if not cases_v2.in_run_window(case.get("x_run_window")):
        if not resuming:
            rec.update(status="skipped_window", ended_at=iso(now()))
            write_json(out_path, rec)
        return {**rec, "status": "skipped_window"}
    deap = sorted({a for a in case["roles"].values() if reg["actors"][a].get("kind") == "deap_actor"})
    holder = f"el2e:{run_id}:{case['id']}"
    if deap and use_lease:
        from . import lease
        rec["lease"] = lease.acquire(deap, holder)
        if not rec["lease"].get("ok"):
            if not resuming:
                rec.update(status="not_run:actor_leased", ended_at=iso(now()))
            write_json(out_path, rec)
            return {**rec, "status": "not_run:actor_leased"}
    rec["gate"] = None if skip_gate else envguard.gate(rd, log=log)
    needs_reset = "memory_reset" in case["requires"].get("harness", [])
    final = not segs or segment == segs[-1]
    if needs_reset and not resuming:
        from . import memory
        rec["memory_before"] = memory.snapshot(case)
    seg_start = now()
    if not resuming:
        rec["started_at"] = iso(seg_start)
    write_json(out_path, rec)
    run = CaseRun(case, spec, rec, out_path, rd, log)
    steps = [s for s in case["steps"] if not segs or s.get("segment") == segment]
    try:
        run.run_steps(steps)
    except StepError as exc:
        rec["status"] = "harness_error"
        rec["harness_error"] = str(exc)
    finally:
        if deap and use_lease and (rec.get("lease") or {}).get("ok"):
            from . import lease
            rec["lease_release"] = lease.release(deap, holder)
    time.sleep(10)  # late replies (task results) still land in the snapshot
    seg_end = now()
    landed_here = [parse_dws_time(s["send"]["landed"][0]["createTime"]) for s in rec["steps"]
                   if (s.get("send") or {}).get("landed") and (not segs or s.get("segment") == segment)]
    run.snapshot(min(landed_here, default=seg_start), f"#{segment}" if segs else "")
    if segs:
        rec.setdefault("segments", {})[segment] = {
            "started_at": iso(seg_start), "ended_at": iso(seg_end),
            "status": "done" if rec["status"] == "running" else rec["status"]}
    if rec["status"] == "running":
        rec["status"] = "completed" if final else f"segment_done:{segment}"
    rec["ended_at"] = iso(seg_end)
    write_json(out_path, rec)
    if final and needs_reset and any((s.get("send") or {}).get("landed") for s in rec["steps"]):
        # Cleanup after the window snapshot, so the reset lines are never graded as case content.
        from . import memory
        rec["memory_after"] = memory.snapshot(case)
        rec["memory_reset"] = memory.reset(case, f"{run_id}:{case['id']}:a{rec['attempt']}")
        write_json(out_path, rec)
        log(f"[{case['id']}] memory reset ok={rec['memory_reset']['ok']}")
    grader_v2.grade_and_write(rd, rec, case, spec, caps)
    return rec


def run_v2(paths: list[Path], run_id: str, *, only: list[str], caps: dict[str, dict[str, bool]],
           skip_gate: bool = False, use_lease: bool = True, segment: str | None = None, redo: bool = False) -> int:
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
    if segment and len(pairs) != 1:
        raise SystemExit("--segment needs exactly one case (--only)")
    rc = 0
    for spec, case in pairs:
        rec = run_case_v2(case, spec, run_id, rd, caps, skip_gate=skip_gate, use_lease=use_lease,
                          segment=segment, redo=redo)
        print(json.dumps({"case": case["id"], "attempt": rec["attempt"], "status": rec["status"]}, ensure_ascii=False),
              flush=True)
        if rec["status"] != "completed" and not rec["status"].startswith("segment_done"):
            rc = 1
    return rc
