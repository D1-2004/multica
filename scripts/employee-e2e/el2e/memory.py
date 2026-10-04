"""memory_reset: snapshot and clean the employee's memory around a case (harness-gaps §3.9.2).

Cleaning uses only existing product paths:
- the EmployeeLoop `/reset-memory` scene command, sent by each human of the
  case in each conversation it used (DM as-is; group as `@employee /reset-memory`).
  It clears the scene's shared Employee memory and that sender's private
  memory there (employee_scene_entry_memory.go);
- `POST /api/agents/{id}/scene-memory/{sceneId}/reset` for the Coordinator
  scene memory of the same scene.
Scope is fixed by the coordinator: only Qwen-Real's scenes in our three test
groups and the actor DMs. Private Employee memory has no read API, so the
snapshot records it as an API gap; the reset command's reply is the evidence.
"""

from __future__ import annotations

import re
import time
from typing import Any

from . import im, preapi
from .common import iso, now, registry

RESET_ALLOWED = ("g_team", "group_p_hx", "group_t", "dm_director", "dm_zhujue", "dm_dxxh")
RESET_COMMAND = "/reset-memory"
RESET_DONE = re.compile(r"已清理本会话")


def conversations_of(case: dict[str, Any]) -> list[str]:
    names = {case["conversation"]} | {s["conversation"] for s in case["steps"] if s.get("conversation")}
    return sorted(names)


def snapshot(case: dict[str, Any]) -> dict[str, Any]:
    out: dict[str, Any] = {"at": iso(now()), "scenes": {},
                           "employee_private_memory": "api_gap: no read API for EmployeeLoop learnings"}
    for name in conversations_of(case):
        sid = preapi.scene_id_for(name)
        out["scenes"][name] = {"scene_id": sid, "coordinator_scene_memory": preapi.scene_memory(sid) if sid else None}
    return out


def plan(case: dict[str, Any]) -> list[tuple[str, str]]:
    """(conversation, human actor) pairs that need a reset: every human who spoke there."""
    reg = registry()
    pairs = set()
    for step in case["steps"]:
        actor = case["roles"].get(step.get("actor") or "")
        if not actor or reg["actors"][actor].get("kind") != "human":
            continue
        conv = step.get("conversation", case["conversation"])
        pairs.add((conv, actor))
    return sorted(pairs)


def reset(case: dict[str, Any], marker: str, *, wait_s: int = 90) -> dict[str, Any]:
    from .driver_v2 import human_reader, view_id
    reg = registry()
    emp_ids = set(reg["employee"]["open_ids"].values())
    results = []
    for conv_name, actor in plan(case):
        if conv_name not in RESET_ALLOWED:
            results.append({"conversation": conv_name, "actor": actor, "skipped": "outside the reset scope"})
            continue
        conv = reg["conversations"][conv_name]
        if not conv.get("cid"):
            results.append({"conversation": conv_name, "actor": actor, "skipped": "no cid"})
            continue
        at = [view_id(reg, "employee", actor)] if conv.get("kind") == "group" else []
        reader = human_reader(reg, conv, actor)
        sent = im.send(profile=reg["actors"][actor]["profile"], cid=conv["cid"], text=RESET_COMMAND,
                       marker=f"{marker}:reset:{conv_name}:{actor}", at_ids=at, ai_tag=False,
                       reader_profile=reg["actors"][reader]["profile"], sender_id=view_id(reg, actor, reader),
                       exclude_sender_ids=emp_ids)
        reply = None
        if sent["ok"]:
            src = sent["landed"][0]
            deadline = time.monotonic() + wait_s
            while time.monotonic() < deadline and reply is None:
                for m in im.read_messages(reg["actors"][reader]["profile"], conv["cid"], limit=15)["messages"]:
                    if m.get("senderId") in emp_ids and (m.get("quotedMessage") or {}).get("messageId") == src["messageId"]:
                        reply = {"messageId": m["messageId"], "text": m.get("text")}
                if reply is None:
                    time.sleep(6)
        results.append({"conversation": conv_name, "actor": actor, "sent_ok": sent["ok"],
                        "message_id": (sent["landed"][0]["messageId"] if sent["landed"] else None),
                        "reply": reply, "cleared": bool(reply and RESET_DONE.search(reply["text"] or ""))})
    scenes = {}
    for conv_name in {c for c, _ in plan(case)} & set(RESET_ALLOWED):
        sid = preapi.scene_id_for(conv_name)
        if sid:
            scenes[conv_name] = {"scene_id": sid, **preapi.reset_scene_memory(sid)}
    return {"at": iso(now()), "commands": results, "coordinator_scene_memory": scenes,
            "ok": all(r.get("cleared") or r.get("skipped") for r in results)}
