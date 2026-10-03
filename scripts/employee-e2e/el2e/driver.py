"""Case driver: acts like the human participants and records raw observations.

The driver never grades. It sends each step, waits as the step says, and
writes `<run>/cases/<case>.a<attempt>.driver.json` after every step so a crash
keeps everything observed so far. One case runs at a time per conversation:
EmployeeLoop reads the recent conversation, so interleaving pollutes context.
"""

from __future__ import annotations

import json
import random
import string
import time
from pathlib import Path
from typing import Any

from . import dwsgw, envguard, im
from .common import iso, load_json, now, parse_dws_time, parse_iso, registry, run_dir, save_registry, write_json

LETTERS = "ABCDEFGHJKLMNPQRSTUVWXYZ"
DIGITS = "23456789"


def gen_vars(seed: str) -> dict[str, str]:
    """GoldenCase codes plus the memory-case values (v1 and memory suites)."""
    return {**code_vars(seed), **memory_vars(seed)}


def code_vars(seed: str) -> dict[str, str]:
    """Per-case random codes (GoldenCase: '题里的编号只是示例，每次运行随机生成').
    cases-v2 merges only these with its own var_sets rows."""
    rng = random.Random(seed)
    letters = rng.sample(LETTERS, 9)
    first = sorted(letters[:3])
    second = sorted(letters[3:6])

    def code(letter: str) -> str:
        return letter + rng.choice(DIGITS)

    c = [code(x) for x in first]
    d = [code(x) for x in second]
    miss = rng.randrange(3)
    got = [c[i] for i in range(3) if i != miss]
    dmiss = rng.randrange(3)
    dgot = [d[i] for i in range(3) if i != dmiss]
    smiss = rng.choice([i for i in range(3) if i != miss])
    signed = [c[i] for i in range(3) if i != smiss]
    # Two distinctive parts so a split leak is detectable and cannot collide with a two-char code.
    secret_a = "K" + rng.choice(DIGITS) + rng.choice(DIGITS)
    secret_b = "".join(rng.choice(string.ascii_uppercase) for _ in range(4))
    return {
        "C1": c[0], "C2": c[1], "C3": c[2], "MISS": c[miss], "G1": got[0], "G2": got[1],
        "D1": d[0], "D2": d[1], "D3": d[2], "DMISS": d[dmiss], "DG1": dgot[0], "DG2": dgot[1],
        "SMISS": c[smiss], "S1": signed[0], "S2": signed[1],
        "W": letters[6] + rng.choice(DIGITS),
        "X": "P" + "".join(rng.choice(DIGITS) for _ in range(3)),
        "SECRET": f"{secret_a}-{secret_b}", "SECRET_A": secret_a, "SECRET_B": secret_b,
        "MARK": "EL" + "".join(rng.choice(LETTERS + DIGITS) for _ in range(4)),
    }


WEEKDAYS = ["周一", "周二", "周三", "周四", "周五"]
FORMATS = ["表格", "要点列表", "三段式"]
TABLES = ["销量", "订单", "线索", "库存"]
COLUMNS = ["x,y", "id,name", "date,amount"]
BUILDINGS = ["A", "B", "C", "D"]


def memory_vars(seed: str) -> dict[str, str]:
    """Natural randomized values for memory cases (a separate stream, so the
    GoldenCase codes above keep their values for the same seed)."""
    rng = random.Random(seed + ":memory")
    day, alt_day = rng.sample(WEEKDAYS, 2)
    fmt_, fmt_alt = rng.sample(FORMATS, 2)
    tables = rng.sample(TABLES, 4)
    rooms = rng.sample([f"{b}座{f}-{r:02d}" for b in BUILDINGS for f in range(3, 19) for r in range(1, 21)], 2)
    rows = rng.choice(["3", "4", "5"])
    return {
        "WEEKDAY": day, "ALT_WEEKDAY": alt_day, "HOUR": rng.choice(["17", "18", "19"]),
        "FMT": fmt_, "FMT_ALT": fmt_alt,
        "PHONE": "3" + "".join(rng.choice(DIGITS) for _ in range(4)),
        "CODENAME": rng.choice(LETTERS) + rng.choice(DIGITS) + rng.choice(LETTERS),
        "ROOM": rooms[0], "ROOM_ALT": rooms[1],
        "ROWS": rows, "ROWS_MORE": str(int(rows) + 1), "COLS": rng.choice(COLUMNS),
        "FILE_A": tables[0] + "_a.csv", "FILE_B": tables[1] + "_b.csv", "FILE_C": tables[2] + "_c.csv",
        "FILE_D": tables[3] + "_d.csv",
    }


def fmt(text: str, vars_: dict[str, str]) -> str:
    out = text
    for key, value in vars_.items():
        out = out.replace("{" + key + "}", value)
    return out


def next_attempt(cases_dir: Path, case_id: str) -> int:
    existing = sorted(cases_dir.glob(f"{case_id}.a*.driver.json"))
    return len(existing) + 1


def ensure_dm(reg: dict[str, Any], conv_name: str, actor: str, first_text: str, marker: str) -> dict[str, Any]:
    """Open a 1:1 chat with the employee by sending the first case line, then register its cid."""
    conv = reg["conversations"][conv_name]
    profile = reg["actors"][actor]["profile"]
    emp_open_id = reg["employee"]["open_ids"][actor]
    key = im.stable_uuid(marker)
    t0 = now()
    res = dwsgw.dws(profile, ["chat", "+messages-send", "--as", "user", "--open-dingtalk-id", emp_open_id,
                              "--text", first_text, "--uuid", key, "--yes"], timeout=45)
    lst = dwsgw.dws(profile, ["chat", "+conversation-list"], timeout=60)
    cid = None
    for item in ((lst.get("json") or {}).get("conversations") or (lst.get("json") or {}).get("result") or []):
        title = item.get("conversationName") or item.get("title") or ""
        # The list carries only name + id; a group would need the exact employee name as its title.
        if title == reg["employee"]["name"] and item.get("singleChat") is not False:
            cid = item.get("openConversationId") or item.get("conversationId")
            break
    if not cid:
        raise RuntimeError(f"could not resolve the new DM cid for {actor}: send rc={res['rc']} list rc={lst['rc']}")
    conv["cid"] = cid
    conv["opened_at"] = iso(t0)
    save_registry(reg)
    return {"send_rc": res["rc"], "cid": cid, "uuid": key, "sent_at": iso(t0)}


def step_profile(reg: dict[str, Any], roles: dict[str, str], role: str) -> tuple[str, str]:
    actor = roles[role]
    return actor, reg["actors"][actor]["profile"]


def at_ids(reg: dict[str, Any], roles: dict[str, str], sender: str, names: list[str]) -> list[str]:
    ids = []
    for name in names:
        if name == "employee":
            ids.append(reg["employee"]["open_ids"][sender])
        else:
            ids.append(reg["actors"][roles.get(name, name)]["open_ids"][sender])
    return ids


def precondition(case: dict[str, Any], reg: dict[str, Any], *, max_idle_wait_s: int, log=print) -> str | None:
    """Return a non-running status when the case cannot start cleanly.

    - A role mapped to null needs an actor the registry does not have yet
      (for example a third human): the whole case is `pending_actor`.
    - A conversation without a cid must be created first (`e2e.py conv new-group`).
    - `idle_before_min` asks for a quiet gap in each DM before the case, so the
      Host's segmentation (>=30 min idle starts a new segment) separates it from
      older cases instead of a visible marker. Wait up to max_idle_wait_s, else
      `deferred_idle`.
    """
    if any(actor is None for actor in case["roles"].values()):
        return "pending_actor"
    convs = {case["conversation"]} | {s.get("conversation") for s in case["steps"] + case.get("teardown", []) if s.get("conversation")}
    for name in sorted(convs):
        conv = reg["conversations"].get(name)
        if conv is None or (conv.get("kind") == "group" and not conv.get("cid")):
            log(f"[{case['id']}] conversation {name} has no cid; create it with `e2e.py conv new-group {name}`")
            return "missing_conversation"
    need = int(case.get("idle_before_min") or 0) * 60
    if not need:
        return None
    for name in sorted(convs):
        conv = reg["conversations"][name]
        if conv.get("kind") != "dm" or not conv.get("cid"):
            continue
        snap = im.read_messages(reg["actors"][conv["readers"][0]]["profile"], conv["cid"], limit=5)
        if not snap["messages"]:
            continue
        idle = (now() - parse_dws_time(snap["messages"][-1]["createTime"])).total_seconds()
        if idle >= need:
            continue
        remaining = need - idle
        if remaining > max_idle_wait_s:
            log(f"[{case['id']}] {name} idle {int(idle)}s < {need}s; deferring")
            return "deferred_idle"
        log(f"[{case['id']}] waiting {int(remaining)}s for {name} to go idle")
        time.sleep(remaining + 5)
    return None


def quoted_target(step: dict[str, Any], rec: dict[str, Any], landed_by_step: dict[str, dict[str, Any]]) -> str | None:
    """Message id a `reply_to` step quotes: the human line of a step, or the
    employee's first reply to it (how a colleague points at what it said)."""
    target = step.get("reply_to")
    if not target:
        return None
    if target.get("employee_reply"):
        for prior in rec["steps"]:
            if prior["id"] == target["step"]:
                replies = (prior.get("poll") or {}).get("replies") or []
                quoted = [r for r in replies if r.get("quotes_source")] or replies
                return quoted[0]["messageId"] if quoted else None
        return None
    landed = landed_by_step.get(target["step"])
    return landed["messageId"] if landed else None


def run_case(case: dict[str, Any], spec: dict[str, Any], run_id: str, rd: Path, *, skip_gate: bool,
             max_idle_wait_s: int = 0, log=print) -> dict[str, Any]:
    reg = registry()
    cases_dir = rd / "cases"
    cases_dir.mkdir(parents=True, exist_ok=True)
    attempt = next_attempt(cases_dir, case["id"])
    vars_ = gen_vars(f"{run_id}:{case['id']}:{attempt}")
    if case.get("vars_from"):
        # A chained case (G2 → G3 → W1, MEM-01 → MEM-02) continues the same
        # conversation facts: reuse the latest attempt's values of its parent.
        parents = sorted(cases_dir.glob(f"{case['vars_from']}.a*.driver.json"),
                         key=lambda x: int(x.name.split(".a")[-1].split(".")[0]))
        if parents:
            vars_ = load_json(parents[-1])["vars"]
    out_path = cases_dir / f"{case['id']}.a{attempt}.driver.json"
    rec: dict[str, Any] = {
        "case_id": case["id"], "title": case["title"], "attempt": attempt, "run_id": run_id,
        "suite": spec.get("suite"), "scene": case.get("scene"), "roles": case["roles"], "vars": vars_,
        "gate": None, "started_at": iso(now()), "steps": [], "status": "running", "override": case.get("override"),
        "employee": {"agent_id": reg["employee"]["agent_id"], "name": reg["employee"]["name"]},
        "collect": case.get("collect"),
    }
    blocked = precondition(case, reg, max_idle_wait_s=max_idle_wait_s, log=log)
    if blocked:
        rec["status"] = blocked
        rec["ended_at"] = iso(now())
        write_json(out_path, rec)
        return rec
    rec["gate"] = None if skip_gate else envguard.gate(rd, log=log)
    rec["started_at"] = iso(now())
    write_json(out_path, rec)
    defaults = spec.get("defaults", {}).get("wait", {})
    landed_by_step: dict[str, dict[str, Any]] = {}
    emp_ids = set(reg["employee"]["open_ids"].values())
    steps = list(case["steps"]) + [dict(t, teardown=True) for t in case.get("teardown", [])]
    for step in steps:
        reg = registry()
        conv_name = step.get("conversation", case["conversation"])
        conv = reg["conversations"][conv_name]
        if "observe" in step:
            # Observation-only step: wait for an employee message matching a pattern (e.g. a task result).
            obs = step["observe"]
            ref = landed_by_step[obs["since_step"]] if obs.get("since_step") else \
                landed_by_step[list(landed_by_step)[-1]]
            reader = conv["readers"][0]
            poll_rec = observe(reg, conv, reader, ref["createTime"], fmt(obs["until_regex"], vars_), emp_ids,
                               obs.get("timeout_s", 300))
            rec["steps"].append({"id": step["id"], "observe": obs, "conversation": conv_name, "cid": conv["cid"],
                                 "poll": poll_rec, "actor": None, "role": None})
            write_json(out_path, rec)
            log(f"[{case['id']}] {step['id']} observe: matched={poll_rec['matched']} in {poll_rec['waited_s']}s")
            continue
        if "actor" not in step and "pause_s" in step:
            # A deliberate idle gap, e.g. >30 min so the Host starts a new segment.
            rec["steps"].append({"id": step["id"], "conversation": conv_name, "pause_s": step["pause_s"],
                                 "actor": None, "role": None, "teardown": step.get("teardown", False)})
            write_json(out_path, rec)
            log(f"[{case['id']}] {step['id']} pause {step['pause_s']}s")
            time.sleep(step["pause_s"])
            continue
        if "burst" in step:
            # Background chatter (un-@ filler lines), sent in order with a short pause.
            burst_rec = {"id": step["id"], "conversation": conv_name, "cid": conv["cid"], "burst": [],
                         "actor": None, "role": None, "teardown": step.get("teardown", False)}
            for i, line in enumerate(step["burst"]):
                actor, profile = step_profile(reg, case["roles"], line["actor"])
                marker = f"{run_id}:{case['id']}:a{attempt}:{step['id']}:{i}"
                sent = im.send(profile=profile, cid=conv["cid"], text=fmt(line["text"], vars_), marker=marker,
                               exclude_sender_ids=emp_ids, readback_timeout=20)
                burst_rec["burst"].append({"actor": actor, "ok": sent["ok"], "landed": sent["landed"][:1],
                                           "landing_count": sent["landing_count"]})
                if sent["landed"] and "send" not in burst_rec:
                    # The first landed line anchors the burst, so an employee
                    # interjection during the chatter is attributed to it.
                    burst_rec["send"] = {"landed": sent["landed"][:1], "match_key": sent["match_key"], "sent_at": sent["sent_at"]}
                time.sleep(step.get("pause_s", 1))
            rec["steps"].append(burst_rec)
            write_json(out_path, rec)
            log(f"[{case['id']}] {step['id']} burst: {sum(1 for b in burst_rec['burst'] if b['ok'])}/{len(step['burst'])} landed")
            continue
        actor, profile = step_profile(reg, case["roles"], step["actor"])
        text = fmt(step["text"], vars_)
        marker = f"{run_id}:{case['id']}:a{attempt}:{step['id']}"
        quoted = quoted_target(step, rec, landed_by_step)
        if step.get("reply_to") and not quoted:
            rec["status"] = "missing_quote_target"
            break
        if quoted:
            send_rec = im.reply(profile=profile, cid=conv["cid"], quoted_message_id=quoted, text=text, marker=marker,
                                at_ids=at_ids(reg, case["roles"], actor, step.get("at", [])),
                                match_key=fmt(step.get("match_key", ""), vars_) or None, exclude_sender_ids=emp_ids)
        elif not conv.get("cid"):
            opened = ensure_dm(reg, conv_name, actor, text, marker)
            rec.setdefault("opened_conversations", []).append({"conversation": conv_name, **opened})
            reg = registry()
            conv = reg["conversations"][conv_name]
            send_rec = im.send(profile=profile, cid=conv["cid"], text=text, marker=marker,
                               match_key=fmt(step.get("match_key", ""), vars_) or None,
                               exclude_sender_ids=emp_ids, not_before=parse_iso(opened["sent_at"]))
        else:
            send_rec = im.send(profile=profile, cid=conv["cid"], text=text, marker=marker,
                               at_ids=at_ids(reg, case["roles"], actor, step.get("at", [])),
                               match_key=fmt(step.get("match_key", ""), vars_) or None,
                               exclude_sender_ids=emp_ids)
        step_rec: dict[str, Any] = {"id": step["id"], "role": step["actor"], "actor": actor, "conversation": conv_name,
                                    "cid": conv["cid"], "at": step.get("at", []), "send": send_rec,
                                    "teardown": step.get("teardown", False)}
        rec["steps"].append(step_rec)
        write_json(out_path, rec)
        log(f"[{case['id']}] {step['id']} as {actor}: landing={send_rec['landing_count']} text={text[:60]!r}")
        if not send_rec["ok"]:
            rec["status"] = "send_failed" if send_rec["landing_count"] == 0 else "send_duplicated"
            break
        landed = send_rec["landed"][0]
        landed_by_step[step["id"]] = landed
        wait = dict(defaults.get(step["wait"]["mode"], {}))
        wait.update(step["wait"])
        mode = wait.pop("mode")
        if mode in ("none", "pause"):
            # pause: a deliberate idle gap (e.g. >30 min to start a new Host segment).
            time.sleep(wait.get("pause_s", 2))
            continue
        since_step = wait.pop("since_step", None)
        since = landed_by_step[since_step]["createTime"] if since_step else landed["createTime"]
        reader = conv["readers"][0] if actor not in conv["readers"] else actor
        poll_mode = "reply" if mode == "reply" else "silence"
        kwargs = {k: wait[k] for k in ("timeout_s", "settle_s", "window_s", "min_replies") if k in wait}
        if mode == "optional":
            poll_rec = poll_optional(reg, conv, reader, since, landed["messageId"], emp_ids, **kwargs)
        else:
            poll_rec = im.poll(profile=reg["actors"][reader]["profile"], cid=conv["cid"], since=since,
                               employee_ids=emp_ids, employee_name=reg["employee"]["name"],
                               source_message_id=landed["messageId"], mode=poll_mode, **kwargs)
        poll_rec["reader"] = reader
        step_rec["wait"] = {"mode": mode, **wait, "since_step": since_step}
        step_rec["poll"] = poll_rec
        write_json(out_path, rec)
        log(f"[{case['id']}] {step['id']} {mode}: {len(poll_rec['replies'])} employee msg(s) in {poll_rec['waited_s']}s")
        if poll_rec.get("gateway_offline_seen"):
            rec["status"] = "gateway_offline"
            break
    if rec["status"] == "running":
        rec["status"] = "completed"
    # Trailing observation: late replies (e.g. a task completion) still land in the transcript.
    time.sleep(10)
    rec["ended_at"] = iso(now())
    rec["transcripts"] = {}
    for conv_name in sorted({s["conversation"] for s in rec["steps"] if s.get("conversation")}):
        conv = registry()["conversations"][conv_name]
        for reader in conv["readers"]:
            snap = im.read_messages(reg["actors"][reader]["profile"], conv["cid"], limit=60)
            first = next((s for s in rec["steps"] if (s.get("send") or {}).get("landed") and s["conversation"] == conv_name), None)
            start = parse_dws_time(first["send"]["landed"][0]["createTime"]) if first else None
            msgs = [m for m in snap["messages"] if start is None or parse_dws_time(m["createTime"]) >= start - im.CLOCK_TOLERANCE]
            rec["transcripts"][f"{conv_name}@{reader}"] = {"complete": snap["complete"], "hasMore": snap["hasMore"],
                                                           "messages": msgs}
    write_json(out_path, rec)
    return rec


def observe(reg: dict[str, Any], conv: dict[str, Any], reader: str, since: str, pattern: str,
            emp_ids: set[str], timeout_s: int) -> dict[str, Any]:
    import re
    rx = re.compile(pattern)
    start = time.monotonic()
    floor = parse_dws_time(since) - im.CLOCK_TOLERANCE
    matched = None
    seen: dict[str, dict[str, Any]] = {}
    while time.monotonic() - start < timeout_s:
        snap = im.read_messages(reg["actors"][reader]["profile"], conv["cid"], limit=30)
        for m in snap["messages"]:
            if parse_dws_time(m["createTime"]) < floor or im.classify(m, emp_ids, reg["employee"]["name"]) != "employee":
                continue
            seen.setdefault(m["messageId"], m)
            if rx.search(m.get("text") or "") and matched is None:
                matched = m
        if matched:
            break
        time.sleep(8)
    return {"mode": "observe", "pattern": pattern, "matched": bool(matched),
            "matched_message": {k: matched.get(k) for k in ("messageId", "createTime", "text")} if matched else None,
            "waited_s": round(time.monotonic() - start, 1), "reader": reader,
            "replies": [{"messageId": m["messageId"], "createTime": m["createTime"], "text": m.get("text")}
                        for m in sorted(seen.values(), key=lambda x: x["createTime"])]}


def poll_optional(reg: dict[str, Any], conv: dict[str, Any], reader: str, since: str, source_id: str,
                  emp_ids: set[str], window_s: int = 45, settle_s: int = 15, **_: Any) -> dict[str, Any]:
    """Wait up to window_s; stop early once a reply arrived and settled."""
    first = im.poll(profile=reg["actors"][reader]["profile"], cid=conv["cid"], since=since, employee_ids=emp_ids,
                    employee_name=reg["employee"]["name"], source_message_id=source_id, mode="reply",
                    timeout_s=window_s, settle_s=settle_s)
    first["mode"] = "optional"
    return first


def run(cases_path: Path, run_id: str, *, only: list[str], skip_gate: bool = False,
        conversation: str | None = None, roles: dict[str, str] | None = None, max_idle_wait_s: int = 0) -> int:
    spec = json.loads(Path(cases_path).read_text(encoding="utf-8"))
    rd = run_dir(run_id)
    manifest_path = rd / "manifest.json"
    manifest = load_json(manifest_path, {}) or {}
    manifest.setdefault("run_id", run_id)
    manifest.setdefault("started_at", iso(now()))
    manifest.setdefault("suites", [])
    if spec.get("suite") not in manifest["suites"]:
        manifest["suites"].append(spec.get("suite"))
    manifest["registry_snapshot"] = registry()
    write_json(manifest_path, manifest)
    dwsgw.prepare()
    selected = [c for c in spec["cases"] if not only or c["id"] in only]
    if only:
        order = {cid: i for i, cid in enumerate(only)}
        selected.sort(key=lambda c: order[c["id"]])
    rc = 0
    for case in selected:
        if conversation or roles:
            # Rerun in another conversation / with other actors (e.g. to escape a polluted history);
            # steps that name their own conversation keep it.
            case = json.loads(json.dumps(case))
            if conversation:
                case["conversation"] = conversation
            case["roles"].update(roles or {})
            case["override"] = {"conversation": conversation, "roles": roles}
        rec = run_case(case, spec, run_id, rd, skip_gate=skip_gate, max_idle_wait_s=max_idle_wait_s)
        print(json.dumps({"case": case["id"], "attempt": rec["attempt"], "status": rec["status"],
                          "started_at": rec["started_at"], "ended_at": rec["ended_at"]}, ensure_ascii=False), flush=True)
        if rec["status"] != "completed":
            rc = 1
    return rc
