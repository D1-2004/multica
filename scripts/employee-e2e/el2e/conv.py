"""Clean scenes for memory cases: one fresh group per case family.

Memory cases must not share a polluted conversation: EmployeeLoop reads the
scene's recent history and scene-layer memory, so an earlier case's codes or
conventions leak into the next one. Instead of visible markers in the chat,
every group case family runs in its own new group (a new scene_id), created
from a `fresh` template in the registry:

  "group_mem_g2": {"kind": "group", "cid": null, "readers": ["director"],
                   "fresh": {"title": "周报对齐", "creator": "director",
                             "members": ["director", "zhujue", "daiyu", "employee"]}}

DMs cannot be recreated; DM cases use `idle_before_min` (a quiet gap that
starts a new Host segment) instead.
"""

from __future__ import annotations

from typing import Any

from . import dwsgw
from .common import iso, now, registry, save_registry


def member_user_id(reg: dict[str, Any], name: str) -> str:
    """Organisation userId for chat creation in the employee's tenant org."""
    if name == "employee":
        return reg["employee"]["dws_user_id"]
    actor = reg["actors"][name]
    return actor.get("realniubility_user_id") or actor["profile"].split(":", 1)[1]


def find_cid(body: Any) -> str | None:
    """The new group's openConversationId, wherever the CLI nests it."""
    if isinstance(body, dict):
        for key in ("openConversationId", "conversationId", "chatId", "openConversationID"):
            value = body.get(key)
            if isinstance(value, str) and value.startswith("cid"):
                return value
        for value in body.values():
            found = find_cid(value)
            if found:
                return found
    if isinstance(body, list):
        for value in body:
            found = find_cid(value)
            if found:
                return found
    return None


def new_group(name: str, *, title: str | None = None, dry_run: bool = False) -> dict[str, Any]:
    reg = registry()
    conv = reg["conversations"].get(name)
    if conv is None or conv.get("kind") != "group" or "fresh" not in conv:
        raise RuntimeError(f"{name} is not a fresh-group template in the registry")
    if conv.get("cid"):
        raise RuntimeError(f"{name} already has cid {conv['cid']}; reset it to null for a new clean scene")
    fresh = conv["fresh"]
    creator = fresh.get("creator", "director")
    users = [member_user_id(reg, m) for m in fresh["members"] if m != creator]
    args = ["chat", "+chat-create", "--name", title or fresh["title"], "--users", ",".join(users), "--yes"]
    if dry_run:
        return {"dry_run": True, "profile": reg["actors"][creator]["profile"], "args": args}
    res = dwsgw.dws(reg["actors"][creator]["profile"], args, timeout=90)
    cid = find_cid(res.get("json"))
    if res["rc"] != 0 or not cid:
        raise RuntimeError(f"chat create failed rc={res['rc']}: {(res.get('stderr') or '')[-300:]}")
    conv.update({"cid": cid, "name": title or fresh["title"], "created_by": creator, "created_at": iso(now()),
                 "members": [m for m in fresh["members"] if m != "employee"], "scene_id": None})
    save_registry(reg)
    hint = None
    if "zhujue" in fresh["members"]:
        hint = "主角 acts cross-org: run `chat data-auth cross-org --all --grant-type timed --ttl 24h` as zhujue before sending"
    return {"conversation": name, "cid": cid, "hint": hint}
