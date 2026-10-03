"""pg_read: PG facts of a case through the 预发 HTTP API (PG itself is unreachable).

Collected after a case (by `e2e.py collect`) into `evidence/<case>.a<N>.api.json`:
- employee tasks of the case's scenes created inside the case window, each
  with detail (state, latest run + verification gate, execution incl.
  exit_confirmed, waits, waiting_counts, collections expected/received,
  links, evidence_refs) and runs;
- scene routines of the case's scenes with their runs (status, source,
  failure_reason, created_at, completed_at).
Facts with no API are listed in API_GAPS and reported as `api_gap`.
"""

from __future__ import annotations

import datetime as _dt
from typing import Any

from . import preapi
from .common import iso, parse_iso

API_GAPS = {
    "pg_learning": "verification passed + verified learning rows: no read API for EmployeeLoop learnings",
    "workpacket_ref": "WorkPacket / ContextUsed learning refs: not exposed by /api/employee-tasks",
    "pg_invitation": "collection invitations and per-participant inputs: /api/employee-tasks/{id} exposes only collection expected/received/state",
    "pg_occurrence_planned_at": "routine occurrence planned_at and skip reason: routine runs expose status/source/failure_reason/created_at only",
}


def case_scenes(rec: dict[str, Any]) -> dict[str, str]:
    out = {}
    for step in rec.get("steps", []):
        name = step.get("conversation")
        if name and name not in out:
            sid = preapi.scene_id_for(name)
            if sid:
                out[name] = sid
    return out


def collect(rec: dict[str, Any], *, tail_minutes: int = 15) -> dict[str, Any]:
    scenes = case_scenes(rec)
    start = parse_iso(rec["started_at"]) - _dt.timedelta(seconds=30)
    end = parse_iso(rec.get("ended_at") or rec["started_at"]) + _dt.timedelta(minutes=tail_minutes)
    tasks = []
    for task in preapi.tasks_since(iso(start)):
        if task.get("scene_id") not in scenes.values():
            continue
        created = parse_iso(task["created_at"])
        if not (start <= created <= end):
            continue
        tasks.append({"summary": task, **preapi.task_detail(task["task_id"])})
    routines = {}
    for name, sid in scenes.items():
        try:
            items = preapi.routines(sid)
        except RuntimeError as exc:
            routines[name] = {"error": str(exc)}
            continue
        routines[name] = [{"routine": r, "runs": preapi.routine_runs(sid, r["id"])} for r in items]
    return {"case_id": rec["case_id"], "attempt": rec["attempt"], "window": [iso(start), iso(end)],
            "scenes": scenes, "tasks": tasks, "routines": routines, "api_gaps": API_GAPS}


def collections(api: dict[str, Any]) -> list[dict[str, Any]]:
    out = []
    for t in api.get("tasks", []):
        body = t.get("task") or {}
        for c in body.get("collections") or []:
            out.append({"task_id": t["summary"]["task_id"], **c})
    return out


def exit_states(api: dict[str, Any]) -> list[dict[str, Any]]:
    return [{"task_id": t["summary"]["task_id"], **((t.get("task") or {}).get("execution") or {})}
            for t in api.get("tasks", []) if (t.get("task") or {}).get("execution")]


def routine_runs(api: dict[str, Any]) -> list[dict[str, Any]]:
    out = []
    for name, items in (api.get("routines") or {}).items():
        if isinstance(items, dict):
            continue
        for item in items:
            for run in item.get("runs") or []:
                out.append({"conversation": name, "routine_id": item["routine"].get("id"),
                            "title": item["routine"].get("title"), **run})
    return out
