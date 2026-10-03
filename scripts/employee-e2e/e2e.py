#!/usr/bin/env python3
"""EmployeeLoop real-IM e2e harness CLI (Python 3.11, stdlib only).

  e2e.py gw prepare [--refresh]           private prod-gateway DWS dir (+ token refresh)
  e2e.py env watch --run-id R             background pipeline/SLS restart timeline
  e2e.py env gate --run-id R              block until no 预发 deploy is pending
  e2e.py read <conversation> [--as A] [--limit N]
  e2e.py send <conversation> --as A --text T [--at employee,<actor>] [--marker M]
  e2e.py run <cases.json> --run-id R [--only ID,...] [--max-idle-wait S]   driver (never grades)
  e2e.py conv new-group <conversation> [--title T] [--dry-run]           fresh group for a clean scene
  e2e.py collect --run-id R [--only ID,...]                Langfuse / SLS evidence
  e2e.py grade --run-id R [--baseline R0]                  grader (separate step)

Evidence goes to $EL2E_EVIDENCE_ROOT/<run-id>/ (default ~/d1/employee-e2e-evidence).
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from el2e import common, dwsgw, envguard, im  # noqa: E402


def actor_profile(reg: dict, actor: str) -> str:
    return reg["actors"][actor]["profile"]


def employee_ids(reg: dict) -> set[str]:
    return set(reg["employee"]["open_ids"].values())


def cmd_gw(args: argparse.Namespace) -> int:
    reg = common.registry()
    profiles = [a["profile"] for a in reg["actors"].values()] if args.refresh else None
    path = dwsgw.prepare(profiles)
    print(json.dumps({"config_dir": str(path), **dwsgw.assert_prod()}, ensure_ascii=False))
    return 0


def cmd_env(args: argparse.Namespace) -> int:
    rd = common.run_dir(args.run_id)
    if args.action == "watch":
        envguard.watch(rd)
    elif args.action == "gate":
        print(json.dumps(envguard.gate(rd), ensure_ascii=False, indent=1))
    elif args.action == "snapshot":
        print(json.dumps(envguard.pipeline_snapshot(), ensure_ascii=False, indent=1))
    return 0


def cmd_read(args: argparse.Namespace) -> int:
    reg = common.registry()
    conv = reg["conversations"][args.conversation]
    actor = args.actor or conv["readers"][0]
    snap = im.read_messages(actor_profile(reg, actor), conv["cid"], limit=args.limit)
    for m in snap["messages"]:
        kind = im.classify(m, employee_ids(reg), reg["employee"]["name"])
        quoted = (m.get("quotedMessage") or {}).get("messageId") or ""
        print(f"{m['createTime']} [{kind}] {m.get('sender')} ({m.get('senderId')}) {m['messageId']} q={quoted}")
        print("   " + (m.get("text") or "").replace("\n", "\n   ")[:1200])
    print(f"# complete={snap['complete']} hasMore={snap['hasMore']} failures={snap['failures']}")
    return 0


def resolve_at(reg: dict, sender: str, names: list[str]) -> list[str]:
    ids = []
    for name in names:
        if name == "employee":
            ids.append(reg["employee"]["open_ids"][sender])
        else:
            ids.append(reg["actors"][name]["open_ids"][sender])
    return ids


def cmd_send(args: argparse.Namespace) -> int:
    reg = common.registry()
    conv = reg["conversations"][args.conversation]
    at = resolve_at(reg, args.actor, [a for a in (args.at or "").split(",") if a])
    marker = args.marker or f"adhoc-{common.now().strftime('%H%M%S')}"
    rec = im.send(profile=actor_profile(reg, args.actor), cid=conv["cid"], text=args.text, marker=marker,
                  at_ids=at, match_key=args.match_key)
    print(json.dumps(rec, ensure_ascii=False, indent=1))
    return 0 if rec["ok"] else 2


def cmd_run(args: argparse.Namespace) -> int:
    from el2e import driver
    roles = dict(r.split("=", 1) for r in (args.role or []))
    return driver.run(Path(args.cases), args.run_id, only=[x for x in (args.only or "").split(",") if x],
                      skip_gate=args.skip_gate, conversation=args.conversation, roles=roles or None,
                      max_idle_wait_s=args.max_idle_wait)


def cmd_conv(args: argparse.Namespace) -> int:
    from el2e import conv
    print(json.dumps(conv.new_group(args.conversation, title=args.title, dry_run=args.dry_run), ensure_ascii=False, indent=1))
    return 0


def cmd_collect(args: argparse.Namespace) -> int:
    from el2e import evidence
    return evidence.collect_run(args.run_id, only=[x for x in (args.only or "").split(",") if x],
                                with_sls=not args.no_sls)


def cmd_grade(args: argparse.Namespace) -> int:
    from el2e import grader
    return grader.grade_run(args.run_id, baseline=args.baseline)


def main(argv: list[str] | None = None) -> int:
    common.scrub_process_proxies()
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    g = sub.add_parser("gw")
    g.add_argument("action", choices=["prepare"])
    g.add_argument("--refresh", action="store_true")
    g.set_defaults(fn=cmd_gw)
    e = sub.add_parser("env")
    e.add_argument("action", choices=["watch", "gate", "snapshot"])
    e.add_argument("--run-id", required=True)
    e.set_defaults(fn=cmd_env)
    r = sub.add_parser("read")
    r.add_argument("conversation")
    r.add_argument("--as", dest="actor")
    r.add_argument("--limit", type=int, default=20)
    r.set_defaults(fn=cmd_read)
    s = sub.add_parser("send")
    s.add_argument("conversation")
    s.add_argument("--as", dest="actor", required=True)
    s.add_argument("--text", required=True)
    s.add_argument("--at")
    s.add_argument("--marker")
    s.add_argument("--match-key")
    s.set_defaults(fn=cmd_send)
    ru = sub.add_parser("run")
    ru.add_argument("cases")
    ru.add_argument("--run-id", required=True)
    ru.add_argument("--only")
    ru.add_argument("--skip-gate", action="store_true")
    ru.add_argument("--conversation", help="override the case conversation (rerun elsewhere)")
    ru.add_argument("--role", action="append", help="override a role, e.g. 主管=zhujue")
    ru.add_argument("--max-idle-wait", type=int, default=0,
                    help="seconds the driver may wait for a DM to reach a case's idle_before_min; otherwise deferred")
    ru.set_defaults(fn=cmd_run)
    cv = sub.add_parser("conv")
    cv.add_argument("action", choices=["new-group"])
    cv.add_argument("conversation")
    cv.add_argument("--title")
    cv.add_argument("--dry-run", action="store_true")
    cv.set_defaults(fn=cmd_conv)
    c = sub.add_parser("collect")
    c.add_argument("--run-id", required=True)
    c.add_argument("--only")
    c.add_argument("--no-sls", action="store_true")
    c.set_defaults(fn=cmd_collect)
    gr = sub.add_parser("grade")
    gr.add_argument("--run-id", required=True)
    gr.add_argument("--baseline")
    gr.set_defaults(fn=cmd_grade)
    args = p.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    raise SystemExit(main())
