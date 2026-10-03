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
from .common import iso, now, parse_dws_time, stable_uuid

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


def render_text(text: str, at_ids: list[str]) -> str:
    """Group @: each mentioned id needs both the at-list entry and a <@id> placeholder."""
    prefix = " ".join(f"<@{oid}>" for oid in at_ids)
    return f"{prefix} {text}".strip() if prefix else text


def send(*, profile: str, cid: str, text: str, marker: str, at_ids: list[str] | None = None,
         match_key: str | None = None, readback_timeout: int = 40,
         exclude_sender_ids: set[str] | None = None, not_before: _dt.datetime | None = None) -> dict[str, Any]:
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
    args = ["chat", "+messages-send", "--as", "user", "--chat-id", cid, "--text", body, "--uuid", key, "--yes"]
    if at_ids:
        args += ["--at-open-dingtalk-ids", ",".join(at_ids)]
    return _deliver(profile=profile, cid=cid, args=args, body=body, text=text, marker=marker, key=key, at_ids=at_ids,
                    match_key=match_key, readback_timeout=readback_timeout,
                    exclude_sender_ids=exclude_sender_ids, not_before=not_before)


def reply(*, profile: str, cid: str, quoted_message_id: str, text: str, marker: str,
          at_ids: list[str] | None = None, match_key: str | None = None, readback_timeout: int = 40,
          exclude_sender_ids: set[str] | None = None) -> dict[str, Any]:
    """Quote-reply to one exact message (how colleagues point at a line in DingTalk).

    A quote reply to an employee message also addresses the employee: DingTalk
    renders it as `@<employee> …` and delivers a native @ event (measured
    2026-10-03 with a DEAP actor). The CLI verifies the source message and its
    conversation before sending; the same idempotency and readback rules apply.
    """
    at_ids = at_ids or []
    key = stable_uuid(marker)
    body = render_text(text, at_ids)
    args = ["chat", "+messages-reply", "--group", cid, "--message-id", quoted_message_id, "--content", body,
            "--uuid", key, "--yes"]
    if at_ids:
        args += ["--at-open-dingtalk-ids", ",".join(at_ids)]
    rec = _deliver(profile=profile, cid=cid, args=args, body=body, text=text, marker=marker, key=key, at_ids=at_ids,
                   match_key=match_key, readback_timeout=readback_timeout, exclude_sender_ids=exclude_sender_ids)
    rec["quoted_message_id"] = quoted_message_id
    rec["landed_quotes_source"] = [m.get("quotedMessageId") == quoted_message_id for m in rec["landed"]]
    return rec


def _deliver(*, profile: str, cid: str, args: list[str], body: str, text: str, marker: str, key: str,
             at_ids: list[str], match_key: str | None, readback_timeout: int,
             exclude_sender_ids: set[str] | None, not_before: _dt.datetime | None = None) -> dict[str, Any]:
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
    excluded = exclude_sender_ids or set()

    def matches(snapshot: dict[str, Any]) -> list[dict[str, Any]]:
        return [m for m in snapshot["messages"]
                if key_text in (m.get("text") or "")
                and parse_dws_time(m["createTime"]) >= floor
                and m.get("senderId") not in excluded]

    deadline = time.monotonic() + readback_timeout
    landed: list[dict[str, Any]] = []
    while time.monotonic() < deadline:
        landed = matches(read_messages(profile, cid, limit=20))
        if landed:
            # Give a possible duplicate one more read to show up before deciding.
            time.sleep(2)
            landed = matches(read_messages(profile, cid, limit=20))
            break
        time.sleep(2)
    record = {
        "marker": marker, "uuid": key, "profile": profile, "cid": cid, "text": body, "match_key": key_text,
        "at_ids": at_ids, "sent_at": iso(t0), "attempts": attempts, "landing_count": len(landed),
        "landed": [{"messageId": m.get("messageId"), "createTime": m.get("createTime"),
                    "senderId": m.get("senderId"), "sender": m.get("sender"), "text": m.get("text"),
                    "quotedMessageId": (m.get("quotedMessage") or {}).get("messageId")} for m in landed],
    }
    record["ok"] = len(landed) == 1
    return record


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
