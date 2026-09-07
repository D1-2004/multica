#!/usr/bin/env python3
"""Score a human-round token against the 'feels like a colleague' bar.

Only this token's IM and Host SLS count. Old-round leftover 处理中 is ignored.
dws must stay pre. Do not send as 菲迪.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", "..", ".."))
SLS = os.path.join(ROOT, "scripts", "query-coordinator-sls.sh")
DWS = os.path.expanduser("~/.agents/skills/dws-env/scripts/dws_env.py")
G1 = "cidShE01n1lg8XTpJFW5oknDw=="
G2 = "cid52dllVmkRJLpUZPwxi0jtw=="
R9A = "cidcfObvQakjFCMc4c2I5o34Q=="
R9B = "cidwybOKJur9YbbjyLpjnbPaA=="

BUSY_SPEECH = ("正在处理中", "不并进", "手头这件还在做")
STATUS_PING = ("已问 dxxh", "已确认线上", "已报群里", "已发到群里", "等待 dxxh")


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


def sls(*extra: str) -> str:
    cmd = [SLS, "--env", "pre", "--size", "80", *extra]
    proc = subprocess.run(cmd, capture_output=True, text=True, env=env())
    return (proc.stdout or "") + (proc.stderr or "")


def chat_list(role: str, cid: str) -> str:
    proc = subprocess.run(
        [
            "python3",
            DWS,
            "as",
            role,
            "--",
            "chat",
            "message",
            "list",
            "--conversation-id",
            cid,
            "--limit",
            "40",
            "--format",
            "json",
        ],
        capture_output=True,
        text=True,
        env=env(),
    )
    return (proc.stdout or "") + (proc.stderr or "")


def token_blob(blob: str, token: str) -> str:
    hits = []
    for raw in blob.splitlines():
        if token in raw:
            hits.append(raw)
    return "\n".join(hits)


def check(name: str, ok: bool, detail: str, rows: list[dict]) -> None:
    rows.append({"id": name, "ok": ok, "detail": detail})
    print(("PASS" if ok else "FAIL") + f" {name}: {detail}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--token", required=True)
    args = parser.parse_args()
    token = args.token
    rows: list[dict] = []

    decided = sls("--message", token, "--event", "inbound_coordinator_decided")
    parked = sls("--event", "inbound_coordinator_job_parked")
    r9b_decided = sls("--cid", R9B, "--event", "inbound_coordinator_decided")
    g2_decided = sls("--cid", G2, "--event", "inbound_coordinator_decided")
    g1 = chat_list("主角", G1)
    g2 = chat_list("主角", G2)
    r9b_im = chat_list("配角", R9B)
    g1t = token_blob(g1, token)
    g2t = token_blob(g2, token)
    r9bt = token_blob(r9b_im, token)
    imt = "\n".join([g1t, g2t, r9bt])

    check(
        "H-busy-park",
        "in-flight" in parked or "pending agent task" in parked,
        "W5 parks while two sandboxes are in flight",
        rows,
    )
    stuck = ("处理中" in g2t) or ("处理失败" in g2t and f"{token}-W5" in g2t)
    busy_line = [s for s in BUSY_SPEECH if s in imt]
    check(
        "H-busy-quiet",
        (not stuck) and (not busy_line),
        "this token's inbound is not left spinning; no 收到/正在处理中/不并进",
        rows,
    )
    pings = [s for s in STATUS_PING if s in g1t]
    check(
        "H-ping",
        not pings,
        "G1 does not status-ping 已问/已确认/已发到群里 after the meeting follow-up",
        rows,
    )
    check(
        "H-protocol",
        "shouldReply" not in imt,
        "IM does not leak shouldReply JSON",
        rows,
    )
    check(
        "H-cid",
        (R9B in r9b_decided) and (R9A not in r9b_decided),
        "R9B Host stays on R9B cid; does not continue R9A/G2 排期",
        rows,
    )
    check(
        "H-ack",
        "action=silence" in g2_decided,
        "thanks/不用回了 Host-silence",
        rows,
    )
    check(
        "H-wrap",
        ("住宿清单" in g1t) and ("已发到群里" not in g1t) and ("已报群里" not in g1t),
        "one 住宿清单 in G1; no recap",
        rows,
    )

    failed = [r["id"] for r in rows if not r["ok"]]
    print(json.dumps({"token": token, "failed": failed, "rows": rows}, ensure_ascii=False))
    if failed:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
