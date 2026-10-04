"""IM driver primitives: send with readback, transcript reads, reply polling.

Message lists from `dws chat +chat-messages` are newest first, carry a local
`createTime` string with one-second resolution, and an observer-relative
`senderId` (openDingTalkId). Employee replies quote the source message
(`quotedMessage.messageId`), which is how replies are attributed to steps.
Never use `--start`: it silently drops messages.
"""

from __future__ import annotations

import datetime as _dt
import json
import re
import time
from typing import Any

from . import dwsgw
from .common import iso, now, parse_dws_time, registry, stable_uuid

CLOCK_TOLERANCE = _dt.timedelta(seconds=2)

# DEAP platform noise: intro/placeholder cards posted when a DE joins a group,
# the fixed template an unconnected local_agent actor auto-replies with, and the
# gateway-offline reply seen when a message went through the wrong gateway.
PLACEHOLDER_PATTERNS = [
    re.compile(r"暂无可展示的最终产物"),
    re.compile(r"你们的工作搭子。目前可用于了解群定位的信息还不多"),
    re.compile(r"我还没有连接本地\s*Agent"),
    re.compile(r"本地\s*Agent\s*当前离线"),
]
GATEWAY_OFFLINE = re.compile(r"本地\s*Agent\s*当前离线")


def is_placeholder(text: str) -> bool:
    return any(p.search(text or "") for p in PLACEHOLDER_PATTERNS)


def read_messages(profile: str, cid: str, limit: int = 30) -> dict[str, Any]:
    """Read the latest `limit` messages, dedupe by messageId, return oldest first."""
    res = dwsgw.dws(profile, ["chat", "+chat-messages", "--chat-id", cid, "--limit", str(limit),
                              "--no-reactions"], timeout=60, retries=2)
    body = res.get("json") or {}
    seen: dict[str, dict[str, Any]] = {}
    for msg in body.get("messages") or []:
        mid = msg.get("messageId")
        if mid and mid not in seen:
            seen[mid] = msg
    msgs = sorted(seen.values(), key=lambda m: (m.get("createTime") or "", m.get("messageId") or ""))
    return {"ok": res["rc"] == 0 and body is not None and "messages" in body, "messages": msgs,
            "complete": body.get("complete"), "hasMore": body.get("hasMore"),
            "failures": body.get("failures"), "rc": res["rc"], "stderr": res.get("stderr", "")[-300:],
            "elapsed_s": res.get("elapsed_s")}


def read_window(profile: str, cid: str, since: _dt.datetime, *, limit: int = 100, max_pages: int = 10) -> dict[str, Any]:
    """Every message from `since` to now: newest page first, then older pages
    through the `--time` boundary (never `--start`, which drops messages).
    `covered` is true only when the oldest message read is at or before `since`
    or the conversation has no older messages."""
    seen: dict[str, dict[str, Any]] = {}
    boundary = None
    boundary_at = None
    covered = False
    pages = 0
    failures = []
    page_ledger = []
    stop_reason = "page_cap"
    for _ in range(max_pages):
        args = ["chat", "+chat-messages", "--chat-id", cid, "--limit", str(limit), "--no-reactions"]
        if boundary:
            args += ["--time", boundary]
        res = dwsgw.dws(profile, args, timeout=60, retries=2)
        pages += 1
        body = res.get("json")
        if not isinstance(body, dict):
            body = {}
        has_more = body.get("hasMore")
        complete = body.get("complete")
        batch = body.get("messages")
        ledger = {"page": pages, "time": boundary, "rc": res["rc"], "hasMore": has_more,
                  "complete": complete, "failures": body.get("failures"),
                  "partial": body.get("partial"), "truncated": body.get("truncated"),
                  "source_stop_reason": body.get("stopReason"), "nextPage": body.get("nextPage")}
        page_ledger.append(ledger)
        bad = (res["rc"] != 0 or not isinstance(batch, list) or not isinstance(has_more, bool)
               or not isinstance(complete, bool) or not isinstance(body.get("failures"), list)
               or bool(body.get("failures")) or body.get("partial") is True
               or body.get("truncated") is True or body.get("paginationKnown") is False
               or (body.get("failedCount") or 0) != 0 or (body.get("decryptFailedCount") or 0) != 0
               or (complete is False and has_more is False) or (complete is True and has_more is True)
               or body.get("stopReason") not in (None, "single_page", "source_complete"))
        if bad:
            stop_reason = "unverified_page"
            failures.append({**ledger, "reason": stop_reason})
            break
        new = 0
        times = []
        invalid_message = False
        for msg in batch:
            try:
                stamp = parse_dws_time(msg["createTime"])
                mid = msg["messageId"]
                if not isinstance(mid, str) or not mid:
                    raise ValueError("message id missing")
            except (AttributeError, KeyError, ValueError, TypeError):
                invalid_message = True
                continue
            times.append(stamp)
            if mid not in seen:
                seen[mid] = msg
                new += 1
        if invalid_message:
            stop_reason = "invalid_message"
            failures.append({"page": pages, "reason": stop_reason})
            break
        if not batch:
            covered = has_more is False and complete is True
            stop_reason = "source_complete" if covered else "empty_more_page"
            break
        oldest = min(times)
        ledger["oldest"] = oldest.isoformat(timespec="seconds")
        ledger["new_messages"] = new
        # complete=false describes remaining history, not failure, when hasMore=true.
        # A strict crossing avoids declaring the lower timestamp bucket complete.
        if oldest < since or (has_more is False and complete is True):
            covered = True
            stop_reason = "lower_bound_crossed" if oldest < since else "source_complete"
            break
        if new == 0:
            stop_reason = "no_new_messages"
            break
        if len(batch) >= limit and len(set(times)) == 1:
            stop_reason = "saturated_time_bucket"
            break
        next_page = body.get("nextPage") or {}
        candidate = next_page.get("time") if isinstance(next_page, dict) else None
        try:
            next_at = _dt.datetime.fromisoformat(candidate.replace("Z", "+00:00"))
            if next_at.tzinfo is None or next_page.get("direction") != "older":
                raise ValueError("unverified cursor")
        except (AttributeError, ValueError, TypeError):
            stop_reason = "next_cursor_unavailable"
            break
        if next_at < since <= oldest:
            stop_reason = "cursor_crossed_unread_boundary"
            break
        if (boundary_at is not None and next_at >= boundary_at) or next_at >= oldest + _dt.timedelta(seconds=1):
            stop_reason = "cursor_not_advancing"
            break
        boundary, boundary_at = candidate, next_at
    msgs = sorted(seen.values(), key=lambda m: (m.get("createTime") or "", m.get("messageId") or ""))
    return {"messages": [m for m in msgs if parse_dws_time(m["createTime"]) >= since - CLOCK_TOLERANCE],
            "covered": covered and not failures, "pages": pages, "failures": failures,
            "page_ledger": page_ledger, "stop_reason": stop_reason,
            "since": since.isoformat(timespec="seconds")}


def prefix_mentions(text: str) -> list[str]:
    """Display names @-mentioned at the start of a rendered message."""
    match = re.match(r"^((?:@\S+\s+)+)", text or "")
    return re.findall(r"@(\S+)", match.group(1)) if match else []


def render_text(text: str, at_ids: list[str]) -> str:
    """Group @: each mentioned id needs both the at-list entry and a <@id> placeholder."""
    prefix = " ".join(f"<@{oid}>" for oid in at_ids)
    return f"{prefix} {text}".strip() if prefix else text


def send(*, profile: str, cid: str, text: str, marker: str, at_ids: list[str] | None = None,
         match_key: str | None = None, readback_timeout: int = 40,
         exclude_sender_ids: set[str] | None = None, not_before: _dt.datetime | None = None,
         ai_tag: bool | None = None, reader_profile: str | None = None, sender_id: str | None = None,
         claimed_ids: set[str] | None = None, at_all: bool = False) -> dict[str, Any]:
    """Send once with an idempotency key, then confirm exactly one landing.

    `match_key` is a distinctive substring of the rendered message used to find
    it on readback; it defaults to the text itself (the @ prefix renders as a
    display name, so it is never part of the key). Our own sends also carry
    messageAiSendFlag=DWS, so only the employee's sender ids are excluded.
    `not_before` lowers the readback floor when a first attempt went out earlier
    (for example the send that opened a 1:1 chat).
    """
    at_ids = at_ids or []
    key = stable_uuid(marker)
    body = render_text(text, at_ids)
    if at_all:
        # @all needs both the flag and the <@all> placeholder in the body.
        body = f"<@all> {body}"
    args = ["chat", "+messages-send", "--as", "user", "--chat-id", cid, "--text", body, "--uuid", key, "--yes"]
    if at_ids:
        args += ["--at-open-dingtalk-ids", ",".join(at_ids)]
    if at_all:
        args.append("--at-all")
    if ai_tag is not None:
        args.append(f"--ai-tag={'true' if ai_tag else 'false'}")
    return _deliver(profile=profile, cid=cid, args=args, body=body, text=text, marker=marker, key=key, at_ids=at_ids,
                    match_key=match_key, readback_timeout=readback_timeout,
                    exclude_sender_ids=exclude_sender_ids, not_before=not_before,
                    reader_profile=reader_profile, sender_id=sender_id, claimed_ids=claimed_ids)


def reply(*, profile: str, cid: str, quoted_message_id: str, text: str, marker: str,
          at_ids: list[str] | None = None, match_key: str | None = None, readback_timeout: int = 40,
          exclude_sender_ids: set[str] | None = None, dm_open_id: str | None = None, ai_tag: bool | None = None,
          reader_profile: str | None = None, sender_id: str | None = None,
          claimed_ids: set[str] | None = None) -> dict[str, Any]:
    """Quote-reply to one exact message (how colleagues point at a line in DingTalk).

    A quote reply always @-mentions the quoted author (DingTalk renders
    `@<author> …`); quoting an employee message therefore addresses the
    employee and is delivered as a native @ event (measured 2026-10-03 with a
    DEAP actor). Explicit `at_ids` are passed to the CLI, which adds the
    `<@id>` placeholders itself, so the body carries none (no double @).
    In a 1:1 chat the target is the peer (`dm_open_id`), not the group.
    """
    at_ids = at_ids or []
    key = stable_uuid(marker)
    args = ["chat", "+messages-reply", "--ref-msg-id", quoted_message_id, "--content", text, "--uuid", key, "--yes"]
    args += ["--open-dingtalk-id", dm_open_id] if dm_open_id else ["--group", cid]
    if at_ids:
        args += ["--at-open-dingtalk-ids", ",".join(at_ids)]
    if ai_tag is not None:
        args.append(f"--ai-tag={'true' if ai_tag else 'false'}")
    rec = _deliver(profile=profile, cid=cid, args=args, body=text, text=text, marker=marker, key=key, at_ids=at_ids,
                   match_key=match_key, readback_timeout=readback_timeout, exclude_sender_ids=exclude_sender_ids,
                   reader_profile=reader_profile, sender_id=sender_id, claimed_ids=claimed_ids)
    rec["quoted_message_id"] = quoted_message_id
    rec["landed_quotes_source"] = [m.get("quotedMessageId") == quoted_message_id for m in rec["landed"]]
    return rec


def _deliver(*, profile: str, cid: str, args: list[str], body: str, text: str, marker: str, key: str,
             at_ids: list[str], match_key: str | None, readback_timeout: int,
             exclude_sender_ids: set[str] | None, not_before: _dt.datetime | None = None,
             reader_profile: str | None = None, sender_id: str | None = None,
             claimed_ids: set[str] | None = None) -> dict[str, Any]:
    """Send, then locate the landing as `reader_profile` (default: the sender).

    With `sender_id` (the sender as the reader sees it) a landing must come
    from exactly that sender; short lines like 「好嘞」 can no longer match an
    employee message with the same words. `claimed_ids` are messages already
    attributed to earlier steps of the case and never count as this landing.
    """
    # Legacy v1 callers also need observer-relative sender attribution.
    reg = registry()
    actors = reg.get("actors") or {}
    sender_actor = next((key for key, value in actors.items() if profile in (key, value.get("profile"))), None)
    reader = reader_profile or profile
    reader_actor = next((key for key, value in actors.items() if reader in (key, value.get("profile"))), None)
    if sender_id is None and sender_actor and reader_actor:
        viewer = "self" if sender_actor == reader_actor else reader_actor
        sender_id = (actors[sender_actor].get("open_ids") or {}).get(viewer)
    t0 = now()
    floor = min(t0, not_before) if not_before is not None else t0
    floor = floor.replace(microsecond=0) - _dt.timedelta(seconds=5)
    attempts = []
    for _ in range(3):
        res = dwsgw.dws(profile, args, timeout=45)
        out = res.get("json") if isinstance(res.get("json"), dict) else {}
        blob = (res.get("stderr") or "") + json.dumps(out, ensure_ascii=False)
        repeated = "repeated" in blob.lower() and "uuid" in blob.lower()
        attempts.append({"rc": res["rc"], "timeout": res.get("timeout"), "elapsed_s": res.get("elapsed_s"),
                         "success": out.get("success"), "repeated_uuid": repeated,
                         "error": None if res["rc"] == 0 else blob[-300:]})
        if res["rc"] == 0 or repeated:
            break
        time.sleep(3)
    key_text = (match_key or text).strip()
    excluded = set(exclude_sender_ids or ()) | set((reg.get("employee") or {}).get("open_ids", {}).values())

    claimed = claimed_ids or set()
    reader = reader_profile or profile

    def matches(snapshot: dict[str, Any]) -> list[dict[str, Any]]:
        return [m for m in snapshot["messages"]
                if key_text in (m.get("text") or "")
                and parse_dws_time(m["createTime"]) >= floor
                and m.get("messageId") not in claimed
                and (m.get("senderId") == sender_id if sender_id else m.get("senderId") not in excluded)]

    deadline = time.monotonic() + readback_timeout
    landed: list[dict[str, Any]] = []
    while time.monotonic() < deadline:
        landed = matches(read_messages(reader, cid, limit=20))
        if landed:
            # Give a possible duplicate one more read to show up before deciding.
            time.sleep(2)
            landed = matches(read_messages(reader, cid, limit=20))
            break
        time.sleep(2)
    record = {
        "marker": marker, "uuid": key, "profile": profile, "reader_profile": reader, "sender_id": sender_id,
        "cid": cid, "text": body, "match_key": key_text, "args_flags": [a for a in args if a.startswith("--")],
        "at_ids": at_ids, "sent_at": iso(t0), "attempts": attempts, "landing_count": len(landed),
        "landed": [{"messageId": m.get("messageId"), "createTime": m.get("createTime"),
                    "senderId": m.get("senderId"), "sender": m.get("sender"), "text": m.get("text"),
                    "quotedMessageId": (m.get("quotedMessage") or {}).get("messageId")} for m in landed],
    }
    record["ok"] = len(landed) == 1
    return record


def locate_new(*, reader_profile: str, cid: str, sender_id: str | None, floor: _dt.datetime,
               claimed: set[str], hint: str | None, timeout_s: int = 40, expect: int = 1) -> list[dict[str, Any]]:
    """Messages from `sender_id` after `floor` that no earlier step claimed (non-text sends).
    With `hint`, prefer messages whose raw record mentions it (a file name)."""
    deadline = time.monotonic() + timeout_s
    found: list[dict[str, Any]] = []
    while time.monotonic() < deadline:
        msgs = [m for m in read_messages(reader_profile, cid, limit=20)["messages"]
                if parse_dws_time(m["createTime"]) >= floor and m.get("messageId") not in claimed
                and (sender_id is None or m.get("senderId") == sender_id)]
        hinted = [m for m in msgs if hint and hint in json.dumps(m, ensure_ascii=False)]
        found = hinted or msgs
        if len(found) >= expect:
            time.sleep(2)
            break
        time.sleep(2)
    return found


def send_file(*, profile: str, cid: str, workdir: str, filename: str, marker: str, ai_tag: bool | None,
              reader_profile: str, sender_id: str | None, claimed_ids: set[str]) -> dict[str, Any]:
    """Send a local file (cwd-relative, as dws requires) and locate the landing by sender + time."""
    key = stable_uuid(marker)
    args = ["chat", "+messages-send", "--as", "user", "--chat-id", cid, "--msg-type", "file", "--file", filename,
            "--uuid", key, "--yes"]
    if ai_tag is not None:
        args.append(f"--ai-tag={'true' if ai_tag else 'false'}")
    t0 = now()
    res = dwsgw.dws(profile, args, timeout=90, cwd=workdir)
    blob = (res.get("stderr") or "") + json.dumps(res.get("json") or {}, ensure_ascii=False)
    landed = locate_new(reader_profile=reader_profile, cid=cid, sender_id=sender_id,
                        floor=t0.replace(microsecond=0) - _dt.timedelta(seconds=5), claimed=claimed_ids,
                        hint=filename, timeout_s=60)
    return {"marker": marker, "uuid": key, "profile": profile, "reader_profile": reader_profile, "sender_id": sender_id,
            "cid": cid, "text": f"[file] {filename}", "match_key": filename, "sent_at": iso(t0),
            "attempts": [{"rc": res["rc"], "error": None if res["rc"] == 0 else blob[-300:]}],
            "landing_count": len(landed), "ok": len(landed) == 1,
            "landed": [{"messageId": m.get("messageId"), "createTime": m.get("createTime"), "senderId": m.get("senderId"),
                        "sender": m.get("sender"), "text": m.get("text"), "raw_type": m.get("messageType") or m.get("msgType")}
                       for m in landed]}


def classify(msg: dict[str, Any], employee_ids: set[str], employee_name: str) -> str:
    text = msg.get("text") or ""
    if is_placeholder(text):
        return "placeholder"
    if msg.get("senderId") in employee_ids or (msg.get("sender") == employee_name and msg.get("messageAiSendFlag")):
        return "employee"
    return "other"


def poll(*, profile: str, cid: str, since: str, employee_ids: set[str], employee_name: str,
         source_message_id: str | None, mode: str, timeout_s: int = 150, settle_s: int = 25,
         window_s: int = 75, min_replies: int = 1, interval_s: int = 6) -> dict[str, Any]:
    """Wait for employee messages after `since` (a DWS createTime string).

    mode=reply: until at least `min_replies` employee messages arrived and no new
    one for `settle_s`, bounded by `timeout_s`.
    mode=silence: observe the full `window_s` and record anything that arrives.
    mode=fixed: same as silence but semantically just a pause.
    """
    since_ts = parse_dws_time(since) - CLOCK_TOLERANCE
    start = time.monotonic()
    last_new = None
    seen: dict[str, dict[str, Any]] = {}
    reads = 0
    gateway_offline = False
    while True:
        snap = read_messages(profile, cid, limit=30)
        reads += 1
        for m in snap["messages"]:
            if parse_dws_time(m["createTime"]) < since_ts:
                continue
            if GATEWAY_OFFLINE.search(m.get("text") or ""):
                gateway_offline = True
            if classify(m, employee_ids, employee_name) != "employee":
                continue
            if m["messageId"] not in seen:
                seen[m["messageId"]] = m
                last_new = time.monotonic()
        elapsed = time.monotonic() - start
        if mode == "reply":
            if len(seen) >= min_replies and last_new is not None and time.monotonic() - last_new >= settle_s:
                break
            if elapsed >= timeout_s:
                break
        else:
            if elapsed >= window_s:
                break
        time.sleep(interval_s)
    replies = sorted(seen.values(), key=lambda m: (m.get("createTime") or "", m.get("messageId") or ""))
    return {
        "mode": mode, "since": since, "reads": reads, "waited_s": round(time.monotonic() - start, 1),
        "gateway_offline_seen": gateway_offline,
        "replies": [{"messageId": m.get("messageId"), "createTime": m.get("createTime"), "text": m.get("text"),
                     "quotes": (m.get("quotedMessage") or {}).get("messageId"),
                     "quotes_source": bool(source_message_id) and
                     (m.get("quotedMessage") or {}).get("messageId") == source_message_id}
                    for m in replies],
    }
