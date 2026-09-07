#!/usr/bin/env python3
"""Replay the 14:04 collect incident using real pre-release DWS messages.

Use a fresh output directory for each of three rounds. This runner captures
evidence; Router receipts and same-cid replies must be reviewed before PASS.
See docs/evals/coordinator-collect-e2e.json and the accompanying plan.
"""
from __future__ import annotations

import argparse
import concurrent.futures
import datetime as dt
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[4]
DWS = Path.home() / ".agents/skills/dws-env/scripts/dws_env.py"
MULTICA = Path.home() / ".agents/skills/e2e-verification/scripts/multica_ext.py"
CASE = json.loads((ROOT / "docs/evals/coordinator-collect-e2e.json").read_text())
FIXTURE = CASE["fixture"]
ACTIVE = {"queued", "dispatched", "running", "waiting_local_directory", "deferred"}
ENV = {k: v for k, v in os.environ.items() if k.lower() not in {"all_proxy", "http_proxy", "https_proxy"}}


def now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


class Blocked(Exception):
    pass


def command(*argv: str) -> object:
    result = subprocess.run(list(argv), capture_output=True, text=True, env=ENV, timeout=60)
    data = None
    for stream in (result.stdout, result.stderr):
        try:
            data = json.loads(stream)
            break
        except ValueError:
            continue
    if data is None:
        raise Blocked(f"{Path(argv[0]).name}: non-JSON response (exit {result.returncode})")
    if result.returncode or (isinstance(data, dict) and data.get("success") is False):
        # Auth responses may carry credentials or links; retain only error identity.
        detail = data.get("data") or {} if isinstance(data, dict) else {}
        raise Blocked(json.dumps({"code": data.get("code") if isinstance(data, dict) else None, "scope": detail.get("scope"), "exit": result.returncode}, ensure_ascii=False))
    return data


def dws(*args: str) -> object:
    return command("python3", str(DWS), "as", FIXTURE["senderRole"], "--", *args, "--format", "json")


def multica(profile: str, *args: str) -> object:
    return command("python3", str(MULTICA), "--profile", profile, *args)


def save(path: Path, data: object) -> None:
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def send(text: str) -> dict:
    started = now()
    at = FIXTURE["atOpenDingTalkId"]
    data = dws("chat", "+messages-send", "--as", "user", "--chat-id", FIXTURE["conversationId"], "--text", f"<@{at}> {text}", "--at-open-dingtalk-ids", at, "--ai-tag=false", "--yes")
    # Exit zero is acceptance only, never proof of reply delivery.
    return {"sent_at": started, "returned_at": now(), "text": text, "send_result": data}


def scene_tasks(profile: str) -> list[dict]:
    recall = multica(profile, "assoc-recall", "--agent-id", FIXTURE["agentId"], "--conversation", FIXTURE["conversationId"], "--since", "48h")
    ids = {row.get("issue_id") or row.get("issue") for row in recall.get("items", [])}
    tasks = multica(profile, "agent-tasks", "--agent", FIXTURE["agentId"], "--limit", "100")
    return [{k: row.get(k) for k in ("id", "issue_id", "status", "created_at", "started_at", "completed_at")} for row in tasks if row.get("issue_id") in ids]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", choices=("preflight", "prepare", "pings", "burst", "third", "observe"))
    parser.add_argument("--profile", required=True, help="Explicit pre-release profile scoped to the fixture workspace")
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--deployment-run", required=True)
    parser.add_argument("--revision", required=True)
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True, mode=0o700)
    report = {"case": CASE["id"], "phase": args.phase, "started_at": now(), "deployment_run": args.deployment_run, "revision": args.revision, "fixture": FIXTURE, "status": "INCOMPLETE"}
    try:
        profile_path = Path.home() / ".multica/profiles" / args.profile / "config.json"
        profile = json.loads(profile_path.read_text())
        if profile.get("server_url", "").rstrip("/") != "https://pre-fde-workbench.dingtalk.com" or profile.get("workspace_id") != FIXTURE["workspaceId"]:
            raise Blocked("profile endpoint or workspace differs from the declared fixture")
        status = command("python3", str(DWS), "status")
        if status.get("environment") != "pre":
            raise Blocked("DWS must be pre-release")
        agent = multica(args.profile, "agent-get", "--agent", FIXTURE["agentId"])
        if agent.get("inbound_coordinator") is not True:
            raise Blocked("fixture Coordinator is disabled")
        # A failed independent read blocks sending; do not create unverifiable work.
        history = dws("chat", "message", "list", "--conversation-id", FIXTURE["conversationId"], "--limit", "40")
        save(args.out / f"{args.phase}-im.json", history)
        tasks = scene_tasks(args.profile)
        report["tasks_before"] = tasks
        token = args.out.name
        first = f"这是预发 e2e 测试 {token}-A：请实际用命令等待 150 秒，再写一行测试完成消息发到本会话；不访问任何业务资源。"
        if args.phase == "prepare":
            if any(t["status"] in ACTIVE for t in tasks):
                raise Blocked("fixture already has active work; wait for it to finish before preparing this round")
            report["sends"] = [send(first)]
            time.sleep(6)
            report["sends"].append(send(f"另一项独立的预发 e2e 测试 {token}-B：请实际用命令等待 150 秒，再写一句不同的测试结果发到本会话；不访问任何业务资源。"))
        elif args.phase == "pings":
            if len({t["issue_id"] for t in tasks if t["status"] in ACTIVE}) != 2:
                raise Blocked("busy fixture requires exactly two active scene matters")
            report["sends"] = []
            for text in [first, "你干了吗？", "你没干活啊", "你说话", "你", "说话", "你好"]:
                current = scene_tasks(args.profile)
                if len({t["issue_id"] for t in current if t["status"] in ACTIVE}) != 2:
                    raise Blocked("capacity ceased to be full during the ping play; do not score the remaining pings as busy coverage")
                report["sends"].append(send(text))
                save(args.out / "pings.json", report)
                time.sleep(6)
        elif args.phase == "burst":
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                a = pool.submit(send, "你")
                time.sleep(0.8)
                b = pool.submit(send, "说话")
                report["sends"] = [a.result(), b.result()]
            report["timing_gate"] = "Verify server arrival order and gap <4s in Router; client start times alone are insufficient."
        elif args.phase == "third":
            if len({t["issue_id"] for t in tasks if t["status"] in ACTIVE}) != 2:
                raise Blocked("third-work fixture requires two active matters")
            report["sends"] = [send(f"请为测试 {token}-C 另外生成一个三行的纯文本测试清单并发到本会话。")]
            time.sleep(6)
            report["sends"].append(send("谢谢"))
            time.sleep(6)
            report["sends"].append(send("你说话"))
        elif args.phase == "observe":
            prior = json.loads((args.out / "prepare.json").read_text())
            query = '__tag__:__user_defined_id__: acni_ag_dt-fde-multica_default_prehost and "' + FIXTURE["conversationId"] + '"'
            rows = []
            for offset in range(0, 1000, 100):
                batch = command("normandy", "log", "list", "--source", "sls", "--project", "dt-fde-multica-sls", "--logstore", "application-log", "--query", query, "--from", prior["started_at"], "--to", now(), "--size", "100", "--offset", str(offset), "--output", "json")
                rows.extend(batch)
                if len(batch) < 100:
                    break
            else:
                raise Blocked("SLS evidence exceeds bounded pagination; narrow this round")
            save(args.out / "sls.json", rows)
            decisions = []
            for row in rows:
                content = row.get("content", "")
                fields = {k: json.loads(v) if v.startswith('"') else v for k, v in re.findall(r'(\w+)=("(?:\\.|[^"\\])*"|[^ ]+)', content)}
                if fields.get("event") == "inbound_coordinator_decided":
                    decisions.append({k: fields.get(k) for k in ("coord_trace_id", "current_message", "action", "tools_used", "text")})
            report["decisions"] = decisions
            report["review_required"] = ["Router receipt times and no premature sync-silence", "DWS same-cid visible replies within 30s", "Task/Issue counts unchanged for pings and repeat", "4s/12s timing and no cross-window absorption", "Third work executes exactly once after a slot frees"]
        report["status"] = "CAPTURED_REVIEW_REQUIRED" if args.phase != "preflight" else "PREFLIGHT_OK"
    except (Blocked, OSError, ValueError, subprocess.TimeoutExpired) as error:
        report.update(status="BLOCKED", error=str(error))
    save(args.out / f"{args.phase}.json", report)
    print(json.dumps({"status": report["status"], "evidence": str(args.out.resolve()), "error": report.get("error")}, ensure_ascii=False))
    return 2 if report["status"] == "BLOCKED" else 0


if __name__ == "__main__":
    sys.exit(main())
