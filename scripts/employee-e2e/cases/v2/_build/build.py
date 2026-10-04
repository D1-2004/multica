"""Validate the cases-v2 sources and emit G/M/C/P/T.json, world.json and SUITE.md."""
import json, re, sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent))
from common import *  # noqa
import g, m, c, p, t

OUT = Path(__file__).resolve().parent.parent  # cases/v2: regenerated in place
MODS = [g, m, c, p, t]
VAR_RE = re.compile(r"\{([A-Z][A-Z0-9_]*)\}")
ALIAS_RE = re.compile(r"\{=([A-Z]+)\}")
CHECK_KEYS = {"steps", "replies", "include_all", "include_any", "include_regex", "include_any_regex", "exclude", "exclude_sets",
              "max_chars", "last_include_all", "quotes_step", "requires", "tier", "note", "only_if", "match_count",
              "sentinel", "conversation", "scope", "parts", "evidence", "step", "max", "min_runs", "if_dispatched", "tools",
              "arg", "tool", "min", "if_dispatched_at", "values"}
V1_CHECK_KEYS = {"steps", "replies", "include_all", "last_include_all", "exclude", "max_chars", "sentinel", "parts", "conversation",
                 "evidence", "step", "max", "min_runs", "note"}
V1_EVIDENCE = {"max_calls_per_wake", "no_effect_for_step", "same_task_runs"}
STEP_KEYS = {"id", "actor", "text", "conversation", "at", "reply_to", "match_key", "wait", "observe", "kind", "segment", "burst",
             "at_all", "file", "target", "emoji", "text_emotion", "source", "create_group", "add_members", "delivery_id", "body",
             "expect_http", "quiet_window_s", "signature", "table", "record"}
STATUS = {"ready", "ready_partial", "blocked"}

errors = []


def err(cid, msg):
    errors.append(f"{cid}: {msg}")


def strings_of(case):
    """Yield (where, string, is_regex) for every templated string in a case."""
    for s in case["steps"]:
        for k in ("text", "match_key"):
            if s.get(k):
                yield (f"{s['id']}.{k}", s[k], False)
        if "observe" in s:
            yield (f"{s['id']}.observe", s["observe"]["until_regex"], True)
    for ch in case["judge"]["checks"]:
        for k in ("include_all", "include_any", "last_include_all"):
            for x in ch.get(k, []):
                yield ("check." + k, x, False)
        for k in ("include_regex", "include_any_regex", "exclude"):
            for x in ch.get(k, []):
                yield ("check." + k, x, True)
        if "match_count" in ch:
            yield ("check.match_count", ch["match_count"]["regex"], True)
        if "sentinel" in ch:
            yield ("check.sentinel", ch["sentinel"], False)
        for x in ch.get("parts", []):
            yield ("check.parts", x, False)
        if "only_if" in ch:
            for x in ch["only_if"].get("include_all", []):
                yield ("check.only_if", x, False)
    for k in ("criteria",):
        yield ("judge." + k, case["judge"][k], False)
    for x in case["judge"]["semantic"]:
        yield ("semantic", x, False)
    fx = case.get("fixtures") or {}
    for name, f in fx.items():
        if f.get("name"):
            yield (f"fixture.{name}.name", f["name"], False)
        if f.get("template"):
            yield (f"fixture.{name}.template", f["template"], False)


def expand(s, row):
    out = s
    for k, v in row.items():
        out = out.replace("{" + k + "}", v)
    out = ALIAS_RE.sub(lambda mm: ALIASES.get(mm.group(1), mm.group(0)), out)
    return out


def cap_ok(code):
    for part in code.lstrip("!").split("+"):
        if part not in PLATFORM and part not in HARNESS and part not in RELEASE:
            return False
    return True


def validate(case):
    cid = case["id"]
    if not re.fullmatch(r"[GMCPT]-\d{2}", cid):
        err(cid, "bad id")
    roles = case["roles"]
    for label, actor in roles.items():
        if actor not in ACTORS:
            err(cid, f"role {label} -> unknown actor {actor}")
    if case["conversation"] not in CONVERSATIONS:
        err(cid, f"unknown conversation {case['conversation']}")
    if case["status"]["now"] not in STATUS:
        err(cid, "bad status")
    req = case["requires"]
    for h in req["harness"]:
        if h not in HARNESS: err(cid, f"unknown harness {h}")
    for x in req["platform"]:
        if x not in PLATFORM: err(cid, f"unknown platform {x}")
    for x in req["release"]:
        if x not in RELEASE: err(cid, f"unknown release {x}")
    for x in req["ops"]:
        if x not in OPS: err(cid, f"unknown ops {x}")
    seen = []
    for s in case["steps"]:
        sid = s["id"]
        if sid in seen:
            err(cid, f"dup step {sid}")
        for k in s:
            if k not in STEP_KEYS:
                err(cid, f"{sid}: unknown step key {k}")
        conv = s.get("conversation", case["conversation"])
        if conv not in CONVERSATIONS:
            err(cid, f"{sid}: unknown conversation {conv}")
        if "observe" in s:
            ss = s["observe"].get("since_step")
            if ss and ss not in seen: err(cid, f"{sid}: observe since_step {ss} not earlier")
        else:
            if s.get("actor") and s["actor"] not in roles:
                err(cid, f"{sid}: actor {s['actor']} not in roles")
            if s.get("actor") and ACTORS.get(roles.get(s["actor"], ""), {}).get("kind") == "deap":
                if s.get("at"): err(cid, f"{sid}: DEAP actor cannot @")
                if CONVERSATIONS[conv]["kind"] == "dm": err(cid, f"{sid}: DEAP actor cannot DM the employee")
            for a in s.get("at", []):
                if a != "employee" and a not in roles: err(cid, f"{sid}: at {a} not in roles")
            if "kind" not in s and "text" not in s:
                err(cid, f"{sid}: no text")
            if "wait" in s:
                ss = s["wait"].get("since_step")
                if ss and ss not in seen + [sid]: err(cid, f"{sid}: since_step {ss} not earlier")
            rt = s.get("reply_to") or {}
            for k in ("step", "employee_reply_of", "observed"):
                if k in rt and rt[k] not in seen: err(cid, f"{sid}: reply_to.{k} {rt[k]} not earlier")
            tgt = (s.get("target") or {})
            for k in ("step", "employee_reply_of"):
                if k in tgt and tgt[k] not in seen: err(cid, f"{sid}: target.{k} not earlier")
            src = s.get("source") or {}
            for x in ([src["step"]] if "step" in src else []) + src.get("steps", []):
                if x not in seen: err(cid, f"{sid}: source step {x} not earlier")
        seen.append(sid)
    for ch in case["judge"]["checks"]:
        for k in ch:
            if k not in CHECK_KEYS: err(cid, f"unknown check key {k}")
        for x in ch.get("steps", []):
            if x not in seen: err(cid, f"check step {x} missing")
        if ch.get("step") and ch["step"] not in seen: err(cid, f"evidence step {ch['step']} missing")
        if ch.get("requires") and not cap_ok(ch["requires"]): err(cid, f"unknown requires {ch['requires']}")
        for x in ch.get("exclude_sets", []):
            if x not in PATTERN_SETS: err(cid, f"unknown pattern set {x}")
        if "conversation" in ch and ch["conversation"] not in CONVERSATIONS: err(cid, "sentinel conversation unknown")
        if "only_if" in ch and ch["only_if"]["step"] not in seen: err(cid, "only_if step missing")
    rows = case.get("var_sets") or [{}]
    for row in rows:
        for k in row:
            if k in GEN_VARS_RESERVED: err(cid, f"var {k} collides with gen_vars")
        if set(row) != set(rows[0]): err(cid, "var_sets rows have different keys")
    for where, s, is_re in strings_of(case):
        for name in VAR_RE.findall(s):
            if name not in rows[0]:
                err(cid, f"{where}: placeholder {{{name}}} undefined")
        for name in ALIAS_RE.findall(s):
            if name not in ALIASES: err(cid, f"{where}: alias {name} undefined")
        if is_re:
            for row in rows:
                try:
                    re.compile(expand(expand(s, row), row))
                except re.error as e:
                    err(cid, f"{where}: regex error {e}: {expand(s, row)}")
    for row in rows:  # values may embed aliases (e.g. OWNER_RE)
        for k, v in row.items():
            for name in ALIAS_RE.findall(v):
                if name not in ALIASES: err(cid, f"var {k}: alias {name} undefined")


def is_r0(case):
    if case["status"]["now"] == "blocked" or case["requires"]["harness"] or case.get("var_sets") or case.get("fixtures"):
        return False
    for s in case["steps"]:
        if any(k in s for k in ("reply_to", "kind", "segment", "burst", "at_all")):
            return False
        if "observe" in s and set(s["observe"]) - {"until_regex", "timeout_s", "since_step"}:
            return False
        if "wait" in s and s["wait"].get("since_step") and False:
            return False
    for ch in case["judge"]["checks"]:
        if set(ch) - V1_CHECK_KEYS:
            return False
        if "evidence" in ch and ch["evidence"] not in V1_EVIDENCE:
            return False
    for _, s, _ in strings_of(case):
        if ALIAS_RE.search(s):
            return False
    return True


ALL = []
for mod in MODS:
    for cs in mod.CASES:
        validate(cs)
        ALL.append((mod.CATEGORY, cs))
ids = [cs["id"] for _, cs in ALL]
for x in set(ids):
    if ids.count(x) > 1:
        errors.append(f"duplicate id {x}")
if errors:
    print("\n".join(errors))
    sys.exit(1)
print("validated", len(ALL), "cases")
if "--check" in sys.argv:
    sys.exit(0)
exec(open(Path(__file__).parent / "render.py", encoding="utf-8").read())
