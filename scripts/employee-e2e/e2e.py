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
  e2e.py v2 dry-run [--suite G,M,...] [--capabilities k=on,...] [--json OUT]   parse+validate, no send
  e2e.py v2 run --run-id R [--suite G] [--only G-01,...] [--capabilities ...] [--no-lease]
  e2e.py v2 grade --run-id R [--baseline R0] [--capabilities ...]   regrade after `collect`
  e2e.py v2 sync-gaps                                       cases/v2/known_gaps.json from known_gap fields

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


def _v2_paths(arg: str | None) -> list[Path] | None:
    from el2e import cases_v2
    if not arg:
        return None
    return [cases_v2.V2_DIR / f"{name.strip()}.json" for name in arg.split(",") if name.strip()]


def cmd_v2(args: argparse.Namespace) -> int:
    from el2e import cases_v2
    caps = cases_v2.load_capabilities(args.capabilities)
    if args.action == "dry-run":
        res = cases_v2.dry_run(_v2_paths(args.suite), caps)
        if args.json:
            Path(args.json).write_text(json.dumps(res, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
        for row in res["cases"]:
            print(f"{row['id']:6} {row['state']:18} {row['conversation']:12} {'; '.join(map(str, row['reasons']))[:110]}")
        print(json.dumps({"total": res["total"], "counts": res["counts"], "suite_errors": res["suite_errors"]},
                         ensure_ascii=False))
        return 1 if res["suite_errors"] or res["counts"].get("blocked_validation") else 0
    if args.action == "sync-gaps":
        print(json.dumps(cases_v2.sync_known_gaps(), ensure_ascii=False, indent=1))
        return 0
    if not args.run_id:
        raise SystemExit("--run-id is required")
    if args.action == "run":
        from el2e import driver_v2
        return driver_v2.run_v2(_v2_paths(args.suite) or [cases_v2.V2_DIR / n for n in cases_v2.SUITE_FILES],
                                args.run_id, only=[x for x in (args.only or "").split(",") if x], caps=caps,
                                skip_gate=args.skip_gate, use_lease=not args.no_lease)
    from el2e import grader_v2
    return grader_v2.grade_run_v2(args.run_id, baseline=args.baseline,
                                  caps=caps if args.capabilities else None)


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
    v2 = sub.add_parser("v2")
    v2.add_argument("action", choices=["dry-run", "run", "grade", "sync-gaps"])
    v2.add_argument("--run-id")
    v2.add_argument("--suite", help="comma list of G,M,C,P,T (default: all)")
    v2.add_argument("--only")
    v2.add_argument("--capabilities", help="platform/release/ops overrides, e.g. G_memory=on,routine_pause=on")
    v2.add_argument("--baseline")
    v2.add_argument("--json")
    v2.add_argument("--skip-gate", action="store_true")
    v2.add_argument("--no-lease", action="store_true")
    v2.set_defaults(fn=cmd_v2)
    args = p.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    raise SystemExit(main())
