"""DEAP actor leases on the DWH team's lease board (a prod Multica issue's metadata).

Same protocol as multica-dwh-agents/tools/actor_lease.py, but for named
actors: key `act_<agentUuid>`, value `<holder>:<expires_unix>`, write then
read back (last writer wins, so a read-back mismatch means someone else holds it).
Only the harness's own holder is ever written or released.
"""

from __future__ import annotations

import json
import time
from typing import Any

from .common import clean_env, extract_json, registry, run_cmd


def _board() -> dict[str, Any]:
    return registry()["deap_pool"]["lease_board"]


def _mc(*args: str) -> dict[str, Any]:
    board = _board()
    return run_cmd(["multica", "--profile", board["profile"], *args],
                   env=clean_env({"MULTICA_WORKSPACE_ID": board["workspace_id"]}), timeout=90)


def read_all() -> dict[str, str]:
    res = _mc("issue", "metadata", "list", _board()["issue_id"], "--output", "json")
    try:
        body = extract_json(res["stdout"])
    except ValueError:
        raise RuntimeError(f"lease board unreadable: {(res['stderr'] or res['stdout'])[-200:]}")
    if isinstance(body, dict) and all(isinstance(v, str) for v in body.values()):
        return {k: v for k, v in body.items() if not k.startswith("_")}
    items = body if isinstance(body, list) else body.get("metadata") or body.get("items") or []
    return {i["key"]: str(i.get("value", "")) for i in items if isinstance(i, dict) and not str(i.get("key", "")).startswith("_")}


def holder_of(value: str | None, at: float | None = None) -> str | None:
    if not value:
        return None
    holder, _, exp = value.rpartition(":")
    try:
        return holder if float(exp) > (at or time.time()) else None
    except ValueError:
        return None


def acquire(actors: list[str], holder: str, ttl_s: int = 3600) -> dict[str, Any]:
    """Lease every named DEAP actor for `holder`; fail (and leave nothing behind) if any is held."""
    reg = registry()
    keys = {a: "act_" + reg["actors"][a]["agent_uuid"] for a in actors}
    board = read_all()
    busy = {a: holder_of(board.get(k)) for a, k in keys.items() if holder_of(board.get(k)) not in (None, holder)}
    if busy:
        return {"ok": False, "busy": busy}
    expires = int(time.time()) + ttl_s
    for key in keys.values():
        _mc("issue", "metadata", "set", _board()["issue_id"], "--key", key, "--value", f"{holder}:{expires}",
            "--type", "string")
    time.sleep(1.5)
    board = read_all()
    lost = {a: holder_of(board.get(k)) for a, k in keys.items() if holder_of(board.get(k)) != holder}
    if lost:
        release(actors, holder)
        return {"ok": False, "lost": lost}
    return {"ok": True, "holder": holder, "expires_at": expires, "keys": keys}


def release(actors: list[str], holder: str) -> dict[str, Any]:
    reg = registry()
    board = read_all()
    released = []
    for actor in actors:
        key = "act_" + reg["actors"][actor]["agent_uuid"]
        if holder_of(board.get(key)) == holder:
            _mc("issue", "metadata", "delete", _board()["issue_id"], "--key", key)
            released.append(actor)
    return {"released": released}


if __name__ == "__main__":  # pragma: no cover - manual inspection
    print(json.dumps(read_all(), ensure_ascii=False))
