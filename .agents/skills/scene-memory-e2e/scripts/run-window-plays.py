#!/usr/bin/env python3
"""Replay Coordinator scene-window plays against the pre-release fixtures.

See references/scene-window-plays.md. dws must stay pre. Do not send as 菲迪.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time

SCRIPT = os.path.expanduser("~/.agents/skills/dws-env/scripts/dws_env.py")
G1 = "cidShE01n1lg8XTpJFW5oknDw=="
G2 = "cid52dllVmkRJLpUZPwxi0jtw=="
AT_DONGXIANG = "DIBwz3Bm4ugAGaaIaZvSXyAiEiE"
AT_DXXH = "Dl2XMiS9sbxHgSb1GMrRWVz6DHXBFkLb6iP"
ACKS = ("谢谢", "好的", "收到", "嗯", "行", "辛苦了", "不用回了", "没事")


def env() -> dict[str, str]:
    out = os.environ.copy()
    for key in (
        "ALL_PROXY",
        "all_proxy",
        "HTTP_PROXY",
        "http_proxy",
        "HTTPS_PROXY",
        "https_proxy",
    ):
        out.pop(key, None)
    return out


def run_as(role: str, *args: str) -> subprocess.CompletedProcess[str]:
    cmd = ["python3", SCRIPT, "as", role, "--", *args]
    return subprocess.run(cmd, capture_output=True, text=True, env=env())


def send(role: str, cid: str, at: str, text: str) -> None:
    body = f"<@{at}> {text}"
    proc = run_as(
        role,
        "chat",
        "+messages-send",
        "--as",
        "user",
        "--chat-id",
        cid,
        "--text",
        body,
        "--at-open-dingtalk-ids",
        at,
        "--ai-tag=false",
        "--yes",
        "--format",
        "json",
    )
    blob = (proc.stdout or "") + (proc.stderr or "")
    ok = proc.returncode == 0 and (
        "SUCCESS" in blob or "openTaskId" in blob or '"sendStatus"' in blob
    )
    print(json.dumps({"role": role, "ok": ok, "rc": proc.returncode, "text": text[:80]}, ensure_ascii=False))
    if not ok:
        sys.stderr.write(blob[-800:] + "\n")
        raise SystemExit(1)


def status() -> None:
    proc = subprocess.run(
        ["python3", SCRIPT, "status"], capture_output=True, text=True, env=env()
    )
    data = json.loads(proc.stdout or "{}")
    print(json.dumps({"environment": data.get("environment"), "mcp": (data.get("mcp") or {}).get("url")}, ensure_ascii=False))
    if data.get("environment") != "pre":
        raise SystemExit("dws is not pre; switch pre before sending")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("step", choices=("status", "w3a", "w3b", "w5", "acks", "wrap", "w2"))
    parser.add_argument("--token", default="WIN")
    args = parser.parse_args()
    if args.step == "status":
        status()
        return
    status()
    token = args.token
    if args.step == "w3a":
        send("主角", G2, AT_DONGXIANG, f"帮我问 dxxh 下周排期，token={token}-W3A")
    elif args.step == "w3b":
        send("配角", G2, AT_DXXH, f"查一下本月文档配额，token={token}-W3B")
    elif args.step == "w5":
        send("主角", G2, AT_DONGXIANG, f"帮我订下周去上海的高铁，token={token}-W5")
    elif args.step == "acks":
        for word in ACKS:
            send("主角", G2, AT_DONGXIANG, word)
            time.sleep(0.4)
    elif args.step == "wrap":
        send("主角", G1, AT_DONGXIANG, f"帮我写一份竞业限制说明发到群里，token={token}-WRAP")
    elif args.step == "w2":
        send("主角", G1, AT_DONGXIANG, f"帮我约 dxxh 明天开会，token={token}-W2")
        time.sleep(3)
        send("主角", G1, AT_DONGXIANG, f"就约线上，token={token}-W2B")


if __name__ == "__main__":
    main()
