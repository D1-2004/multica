"""Restart-window guard for the shared 预发 environment.

预发 is redeployed by many sessions (pipeline 66 runs ~28 times a day). A case
whose window contains a pod restart or a new deploy is `invalid_env`: it is
rerun, never graded pass or fail. Both `a1` and `normandy` take 1–2 minutes
per call on this machine, so a background watcher records a timeline and the
grader intersects case windows with one SLS pull of 'server starting'.
"""

from __future__ import annotations

import datetime as _dt
import re
import time
from pathlib import Path
from typing import Any

from .common import (append_jsonl, clean_env, extract_json, iso, now, parse_iso, read_jsonl,
                     registry, run_cmd, TZ)

DEPLOY_STAGE = re.compile(r"部署")
SERVER_STARTING = re.compile(r"^(\d\d:\d\d:\d\d\.\d+)\s+INF server starting")


def pipeline_snapshot() -> dict[str, Any]:
    reg = registry()["env"]["pipeline"]
    res = run_cmd(["a1", "cd-pipeline", "run", "get", "--latest", "--pipeline-id", str(reg["pipeline_id"]),
                   "--app", str(reg["app_id"]), "--format", "json"], env=clean_env(), timeout=240)
    try:
        body = extract_json(res["stdout"])
    except ValueError:
        return {"ok": False, "at": iso(now()), "rc": res["rc"], "error": (res["stderr"] or res["stdout"])[-300:]}
    stages = [{"name": s.get("name"), "status": s.get("status"), "started_at": s.get("started_at"),
               "completed_at": s.get("completed_at")} for s in body.get("stages") or []]
    deploy = next((s for s in stages if DEPLOY_STAGE.search(s["name"] or "") and "集成" not in (s["name"] or "")), None)
    return {"ok": True, "at": iso(now()), "runId": body.get("runId"), "status": body.get("status"),
            "releaseBranch": body.get("releaseBranch"), "submitter": body.get("submitter"),
            "stages": stages, "deploy": deploy}


def sls_server_starts(start: _dt.datetime, end: _dt.datetime | None = None, *, retries: int = 2) -> dict[str, Any]:
    """Per-pod 'server starting' times from backend.log within [start, end]."""
    env = registry()["env"]["sls"]
    # The log path is a tag, not searchable text; backend.log is filtered locally below.
    query = f'__tag__:__user_defined_id__: {env["prehost_tag"]} and "server starting"'
    args = ["normandy", "log", "list", "--source", "sls", "--project", env["project"], "--logstore",
            env["logstore"], "--query", query, "--from", iso(start), "--size", "100", "-o", "json"]
    if end is not None:
        args += ["--to", iso(end)]
    last_err = ""
    for attempt in range(retries + 1):
        res = run_cmd(args, env=clean_env(), timeout=240)
        try:
            rows = extract_json(res["stdout"])
        except ValueError:
            last_err = (res["stderr"] or res["stdout"])[-300:]
            time.sleep(5 * (attempt + 1))
            continue
        starts = []
        for row in rows or []:
            content = row.get("content") or ""
            if "backend.log" not in (row.get("__tag__:__path__") or "") or "server starting" not in content:
                continue
            ts = _dt.datetime.fromtimestamp(int(row.get("__time__")), TZ)
            starts.append({"host": row.get("__tag__:__hostname__"), "time": iso(ts), "content": content[:120]})
        starts.sort(key=lambda s: s["time"])
        return {"ok": True, "from": iso(start), "to": iso(end) if end else None, "starts": starts}
    return {"ok": False, "from": iso(start), "error": last_err}


def watch(run_dir: Path, *, pipeline_every_s: int = 120, sls_every_s: int = 600, stop_after_s: int = 6 * 3600) -> None:
    """Append pipeline and SLS restart snapshots to env_timeline.jsonl until killed."""
    timeline = run_dir / "env_timeline.jsonl"
    started = now()
    next_pipe = next_sls = 0.0
    t_end = time.monotonic() + stop_after_s
    while time.monotonic() < t_end:
        mono = time.monotonic()
        if mono >= next_pipe:
            append_jsonl(timeline, {"kind": "pipeline", **pipeline_snapshot()})
            next_pipe = time.monotonic() + pipeline_every_s
        if mono >= next_sls:
            append_jsonl(timeline, {"kind": "sls_server_starting",
                                    **sls_server_starts(started - _dt.timedelta(minutes=30))})
            next_sls = time.monotonic() + sls_every_s
        time.sleep(10)


def latest(run_dir: Path, kind: str) -> dict[str, Any] | None:
    rows = [r for r in read_jsonl(run_dir / "env_timeline.jsonl") if r.get("kind") == kind and r.get("ok")]
    return rows[-1] if rows else None


def deploy_state(snap: dict[str, Any] | None) -> dict[str, Any]:
    """Classify the latest pipeline run: is a 预发 deploy pending or just finished?"""
    if not snap:
        return {"state": "unknown"}
    deploy = snap.get("deploy") or {}
    status = (deploy.get("status") or "").upper()
    run_status = (snap.get("status") or "").upper()
    if run_status in ("CANCEL", "CANCELED", "FAILED", "FAIL"):
        if status == "RUNNING":
            return {"state": "deploy_interrupted", "runId": snap.get("runId")}
        return {"state": "idle", "runId": snap.get("runId")}
    if status in ("SUCCESS",):
        return {"state": "deployed", "runId": snap.get("runId"), "completed_at": deploy.get("completed_at")}
    if run_status == "RUNNING":
        return {"state": "deploy_pending", "runId": snap.get("runId"), "deploy_status": status}
    return {"state": "idle", "runId": snap.get("runId")}


def gate(run_dir: Path, *, settle_s: int = 150, max_wait_s: int = 1800, log=print) -> dict[str, Any]:
    """Block until no deploy is pending and the last deploy settled `settle_s` ago."""
    t0 = time.monotonic()
    while True:
        snap = latest(run_dir, "pipeline")
        state = deploy_state(snap)
        age = None
        if snap:
            age = (now() - parse_iso(snap["at"])).total_seconds()
        ok = state["state"] in ("idle", "deployed")
        if ok and state["state"] == "deployed" and state.get("completed_at"):
            since_deploy = (now() - parse_iso(state["completed_at"])).total_seconds()
            ok = since_deploy >= settle_s
            state["since_deploy_s"] = round(since_deploy)
        if ok and (age is None or age > 600):
            ok = False
            state["stale_snapshot_age_s"] = age
        if ok:
            return {"ok": True, "waited_s": round(time.monotonic() - t0), "pipeline": snap, "state": state}
        if time.monotonic() - t0 > max_wait_s:
            return {"ok": False, "waited_s": round(time.monotonic() - t0), "pipeline": snap, "state": state}
        log(f"[gate] waiting: {state} snapshot_age={age}")
        time.sleep(20)


def restarts_in_window(starts: list[dict[str, Any]], start: str, end: str, *, pad_s: int = 20) -> list[dict[str, Any]]:
    lo = parse_iso(start) - _dt.timedelta(seconds=pad_s)
    hi = parse_iso(end) + _dt.timedelta(seconds=pad_s)
    return [s for s in starts if lo <= parse_iso(s["time"]) <= hi]


def pipeline_runs_in_window(timeline: list[dict[str, Any]], start: str, end: str) -> list[dict[str, Any]]:
    """Pipeline runs whose deploy stage overlapped the case window."""
    lo, hi = parse_iso(start), parse_iso(end)
    hits = {}
    for row in timeline:
        if row.get("kind") != "pipeline" or not row.get("ok"):
            continue
        deploy = row.get("deploy") or {}
        ds, dc = deploy.get("started_at"), deploy.get("completed_at")
        if not ds:
            continue
        d_lo = parse_iso(ds)
        d_hi = parse_iso(dc) if dc else parse_iso(row["at"])
        if d_lo <= hi and d_hi >= lo:
            hits[row.get("runId")] = {"runId": row.get("runId"), "deploy_started": ds, "deploy_completed": dc}
    return list(hits.values())
