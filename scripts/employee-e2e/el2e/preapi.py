"""Read (and a few narrowly scoped writes) against the 预发 Multica HTTP API.

预发 PostgreSQL is not reachable from this machine, so every PG fact the
cases need comes from the HTTP API with the `pre-fde` profile (冬翔's token,
never printed). A fact with no API is reported as an API gap, never guessed.
"""

from __future__ import annotations

import urllib.parse
from typing import Any

from .common import parse_iso, registry


def call(method: str, path: str, body: Any = None) -> tuple[int, Any]:
    from .evidence import pre_api  # asserts the profile points at the 预发 server
    return pre_api(method, path, body)


def _agent() -> str:
    return registry()["employee"]["agent_id"]


def _org() -> str:
    return registry()["employee"]["tenant_org_id"]


def quote(value: str) -> str:
    return urllib.parse.quote(value, safe="")


# ------------------------------------------------------------ scenes

def scene_index() -> dict[str, str]:
    """cid -> scene_id for every group and DM scene of the employee in its tenant org."""
    out: dict[str, str] = {}
    status, body = call("GET", f"/api/agents/{_agent()}/tenants/{_org()}/groups")
    if status == 200:
        for s in body.get("scenes") or []:
            if s.get("conversation_id") and s.get("scene_id"):
                out[s["conversation_id"]] = s["scene_id"]
    return out


def scene_id_for(conv_name: str) -> str | None:
    conv = registry()["conversations"].get(conv_name) or {}
    if conv.get("scene_id"):
        return conv["scene_id"]
    if conv.get("cid"):
        return scene_index().get(conv["cid"])
    return None


def scene_memory(scene_id: str) -> dict[str, Any]:
    status, body = call("GET", f"/api/agents/{_agent()}/scene-memory/{scene_id}")
    return {"status": status, "body": body if status == 200 else None,
            "error": None if status in (200, 404) else str(body)[:200]}


def reset_scene_memory(scene_id: str) -> dict[str, Any]:
    status, body = call("POST", f"/api/agents/{_agent()}/scene-memory/{scene_id}/reset", {})
    return {"status": status, "detail": body if status != 200 else "reset"}


# ------------------------------------------------------------ employee tasks

def tasks_since(since: str, *, max_pages: int = 6) -> list[dict[str, Any]]:
    """Employee tasks of the agent updated at or after `since` (newest first, paged)."""
    floor = parse_iso(since)
    out, cursor = [], None
    for _ in range(max_pages):
        path = f"/api/employee-tasks?agent_id={_agent()}&limit=50" + (f"&cursor={quote(cursor)}" if cursor else "")
        status, body = call("GET", path)
        if status != 200:
            break
        page = body.get("tasks") or []
        out.extend(t for t in page if parse_iso(t["updated_at"]) >= floor)
        cursor = body.get("next_cursor")
        if not cursor or not page or parse_iso(page[-1]["updated_at"]) < floor:
            break
    return out


def task_detail(task_id: str) -> dict[str, Any]:
    status, body = call("GET", f"/api/employee-tasks/{task_id}")
    runs_status, runs = call("GET", f"/api/employee-tasks/{task_id}/runs")
    return {"status": status, "task": body if status == 200 else None,
            "runs": (runs.get("runs") if isinstance(runs, dict) else runs) if runs_status == 200 else None}


# ------------------------------------------------------------ scene routines

def _routine_base(scene_id: str) -> str:
    return f"/api/agents/{_agent()}/tenants/{_org()}/context/scene/{scene_id}/routines"


def routines(scene_id: str) -> list[dict[str, Any]]:
    status, body = call("GET", _routine_base(scene_id))
    if status != 200:
        raise RuntimeError(f"routine list failed {status}: {str(body)[:200]}")
    return body.get("routines") if isinstance(body, dict) else body


def routine(scene_id: str, routine_id: str) -> dict[str, Any]:
    found = [r for r in routines(scene_id) if r.get("id") == routine_id]
    if not found:
        raise RuntimeError(f"routine {routine_id} not found in scene {scene_id}")
    return found[0]


def set_routine_enabled(scene_id: str, routine_id: str, enabled: bool) -> dict[str, Any]:
    status, body = call("PATCH", f"{_routine_base(scene_id)}/{routine_id}", {"enabled": enabled})
    if status != 200:
        raise RuntimeError(f"routine patch failed {status}: {str(body)[:200]}")
    return body


def routine_runs(scene_id: str, routine_id: str) -> list[dict[str, Any]]:
    status, body = call("GET", f"{_routine_base(scene_id)}/{routine_id}/runs")
    if status != 200:
        return []
    return body.get("runs") if isinstance(body, dict) else body
