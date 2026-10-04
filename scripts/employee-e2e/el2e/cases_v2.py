"""cases-v2 suite: strict schema, var_sets, templating, capabilities and dry-run.

Spec: _shared/cases-v2/harness-gaps.md §0 (format) and §2.1–2.5 (P0).
Every unknown key at suite, case, step and check level is an error: a
silently skipped check would let a wrong answer pass.
"""

from __future__ import annotations

import datetime as _dt
import json
import random
import re
from pathlib import Path
from typing import Any

from .common import HARNESS_DIR, TZ, load_json, now, registry, write_json

SCHEMA = "el2e.cases.v2"
V2_DIR = HARNESS_DIR / "cases" / "v2"
SUITE_FILES = ["G.json", "M.json", "C.json", "P.json", "T.json"]

TOP_KEYS = {"schema", "suite", "source", "category", "notes", "defaults", "cases"}
DEFAULT_KEYS = {"wait", "human_send", "world", "templating", "pattern_sets", "aliases", "grading"}
CASE_KEYS = {"id", "title", "focus", "maps_to", "scene", "conversation", "roles", "var_sets", "fixtures",
             "x_run_window", "segments", "x_long_running", "requires", "status", "known_gap", "steps", "judge",
             "pending_checks", "doc"}
SPEAK_KEYS = {"id", "actor", "text", "conversation", "at", "at_all", "reply_to", "match_key", "burst", "segment", "wait"}
ACTION_BASE_KEYS = {"id", "kind", "actor", "conversation", "wait", "segment"}
KIND_PARAMS = {
    "file": {"file"}, "recall": {"target"}, "react": {"target", "emoji", "text_emotion"},
    "forward": {"source"}, "combine_forward": {"source"}, "setup": {"create_group", "add_members"},
    "webhook": {"delivery_id", "body", "expect_http", "quiet_window_s", "signature"},
    "aitable_insert": {"table", "record"},
}
# Action kinds and step flags are P1/P2 harness capabilities (harness-gaps §3/§4).
KIND_CAPABILITY = {"file": "file_send", "recall": "recall", "react": "react", "forward": "forward",
                   "combine_forward": "combine_forward", "setup": "setup_group", "webhook": "webhook",
                   "aitable_insert": "aitable_insert"}
OBSERVE_STEP_KEYS = {"id", "conversation", "observe", "segment"}
OBSERVE_KEYS = {"until_regex", "timeout_s", "since_step", "optional", "negative", "include_placeholders"}
WAIT_KEYS = {"mode", "timeout_s", "settle_s", "window_s", "pause_s", "since_step", "min_replies"}
WAIT_MODES = {"reply", "silence", "optional", "none"}
REPLY_TO_TARGETS = ("step", "employee_reply_of", "observed", "employee_latest")
REPLY_TO_KEYS = set(REPLY_TO_TARGETS) | {"fallback"}
TEXT_CHECK_KEYS = {"steps", "replies", "max_chars", "include_all", "include_any", "include_regex", "include_any_regex",
                   "exclude", "exclude_sets", "last_include_all", "quotes_step", "match_count", "only_if", "requires",
                   "tier", "note"}
SENTINEL_KEYS = {"sentinel", "parts", "conversation", "scope", "requires", "tier", "note"}
EVIDENCE_KEYS = {"evidence", "step", "steps", "max", "min", "min_runs", "if_dispatched", "if_dispatched_at", "tool",
                 "tools", "arg", "values", "count", "target_task", "requires", "tier", "note"}
EVIDENCE_KINDS = {"max_calls_per_wake", "no_effect_for_step", "same_task_runs", "tool_arg_present", "task_count",
                  "effect_for_step", "tool_called", "tool_arg_contains"}
EVIDENCE_IMPLEMENTED = set(EVIDENCE_KINDS)
JUDGE_KEYS = {"criteria", "checks", "semantic"}
REQUIRES_CATEGORIES = ("harness", "platform", "release", "ops")
# P0 harness capabilities implemented here; everything else in requires.harness blocks a case.
HARNESS_IMPLEMENTED = {"var_sets", "quote_reply", "deap_multi", "grader_v2", "memory_reset", "pg_read", "segments", "evidence_v2", "file_send",
                       "file_download", "forward", "combine_forward", "at_all", "setup_group",
                       "burst", "negative_observe"}

VAR_REF = re.compile(r"\{([A-Z][A-Z0-9_]*)\}")
ALIAS_REF = re.compile(r"\{=([A-Z][A-Z0-9_]*)\}")


class CaseError(ValueError):
    pass


def load_suite(path: Path) -> dict[str, Any]:
    spec = json.loads(Path(path).read_text(encoding="utf-8"))
    if spec.get("schema") != SCHEMA:
        raise CaseError(f"{path}: schema {spec.get('schema')!r} is not {SCHEMA}")
    return spec


def load_all(paths: list[Path] | None = None) -> list[tuple[dict[str, Any], dict[str, Any]]]:
    """[(spec, case)] for every case of the given (default: all five) suites."""
    out = []
    for path in paths or [V2_DIR / name for name in SUITE_FILES]:
        spec = load_suite(path)
        out.extend((spec, case) for case in spec["cases"])
    return out


def gen_keys() -> set[str]:
    from .driver import code_vars
    return set(code_vars("keys").keys())


# ---------------------------------------------------------------- templating

def render(value: Any, vars_: dict[str, str], aliases: dict[str, str]) -> Any:
    """Variables first, then aliases ({=NAME}); recurses into lists and dicts."""
    if isinstance(value, str):
        out = VAR_REF.sub(lambda m: vars_.get(m.group(1), m.group(0)), value)
        return ALIAS_REF.sub(lambda m: aliases.get(m.group(1), m.group(0)), out)
    if isinstance(value, list):
        return [render(v, vars_, aliases) for v in value]
    if isinstance(value, dict):
        return {k: render(v, vars_, aliases) for k, v in value.items()}
    return value


def unresolved(value: Any) -> list[str]:
    blob = json.dumps(value, ensure_ascii=False)
    return sorted(set(VAR_REF.findall(blob)) | {"=" + a for a in ALIAS_REF.findall(blob)})


def select_vars(case: dict[str, Any], run_id: str, attempt: int) -> tuple[dict[str, str], int | None]:
    """Driver codes merged with one var_sets row chosen by seed run:case:attempt; case vars win
    (a name clash is a load error, so 'win' never silently hides a driver code)."""
    from .driver import code_vars
    seed = f"{run_id}:{case['id']}:{attempt}"
    base = code_vars(seed)
    rows = case.get("var_sets") or []
    if not rows:
        return base, None
    idx = random.Random(seed + ":var_sets").randrange(len(rows))
    clash = sorted(set(rows[idx]) & set(base))
    if clash:
        raise CaseError(f"{case['id']}: var_sets names collide with gen_vars: {clash}")
    return {**base, **{k: str(v) for k, v in rows[idx].items()}}, idx


# ---------------------------------------------------------------- capabilities

def load_capabilities(overrides: str | None = None) -> dict[str, dict[str, bool]]:
    caps = load_json(V2_DIR / "capabilities.json") or {}
    out = {cat: dict(caps.get(cat) or {}) for cat in REQUIRES_CATEGORIES}
    # Harness switches follow the code, never the file: only implemented ones are on.
    out["harness"] = {name: name in HARNESS_IMPLEMENTED for name in out["harness"]}
    out["harness"].update({name: True for name in HARNESS_IMPLEMENTED})
    for item in [x for x in (overrides or "").split(",") if x.strip()]:
        name, _, val = item.partition("=")
        name, val = name.strip(), val.strip().lower()
        if val not in ("on", "off", "true", "false"):
            raise CaseError(f"capability override {item!r}: use name=on|off")
        cat = next((c for c in REQUIRES_CATEGORIES if name in out[c]), None)
        if cat is None:
            raise CaseError(f"unknown capability {name!r}")
        if cat == "harness":
            raise CaseError(f"harness capability {name!r} follows the code and cannot be overridden")
        out[cat][name] = val in ("on", "true")
    return out


def cap_known(caps: dict[str, dict[str, bool]], name: str) -> bool:
    return any(name in caps[c] for c in REQUIRES_CATEGORIES) or name in KIND_CAPABILITY.values()


def cap_on(caps: dict[str, dict[str, bool]], name: str) -> bool:
    return any(caps[c].get(name) for c in REQUIRES_CATEGORIES)


def requires_ok(expr: str, caps: dict[str, dict[str, bool]]) -> bool:
    """'a' needs a on; '!a' applies only while a is off; 'a+b' needs both."""
    for term in expr.split("+"):
        term = term.strip()
        negate = term.startswith("!")
        name = term.lstrip("!")
        if cap_on(caps, name) == negate:
            return False
    return True


def requires_names(expr: str) -> list[str]:
    return [t.strip().lstrip("!") for t in expr.split("+")]


# ---------------------------------------------------------------- validation

def _unknown(errors: list[str], where: str, obj: dict[str, Any], allowed: set[str]) -> None:
    extra = sorted(set(obj) - allowed)
    if extra:
        errors.append(f"{where}: unknown keys {extra}")


def validate_suite(spec: dict[str, Any], caps: dict[str, dict[str, bool]]) -> list[str]:
    errors: list[str] = []
    _unknown(errors, spec.get("suite", "?"), spec, TOP_KEYS)
    _unknown(errors, f"{spec.get('suite')}.defaults", spec.get("defaults", {}), DEFAULT_KEYS)
    for mode in spec.get("defaults", {}).get("wait", {}):
        if mode not in WAIT_MODES:
            errors.append(f"{spec.get('suite')}.defaults.wait: unknown mode {mode}")
    for name, pattern in (spec.get("defaults", {}).get("pattern_sets") or {}).items():
        try:
            re.compile(pattern)
        except re.error as exc:
            errors.append(f"pattern_sets.{name}: {exc}")
    return errors


def validate_case(case: dict[str, Any], spec: dict[str, Any], caps: dict[str, dict[str, bool]],
                  reg: dict[str, Any] | None = None) -> list[str]:
    """Schema, references, capability names and DEAP constraints for one case."""
    cid = case.get("id", "?")
    errors: list[str] = []
    _unknown(errors, cid, case, CASE_KEYS)
    missing = [key for key in ("id", "title", "conversation", "roles", "requires", "status", "steps", "judge")
               if key not in case]
    if missing:
        return errors + [f"{cid}: missing {missing}"]
    defaults = spec.get("defaults", {})
    pattern_sets = defaults.get("pattern_sets") or {}
    roles = case["roles"]
    _unknown(errors, f"{cid}.requires", case["requires"], set(REQUIRES_CATEGORIES))
    for cat, names in case["requires"].items():
        for name in names:
            if not cap_known(caps, name):
                errors.append(f"{cid}.requires.{cat}: unknown capability {name}")
    clash = sorted({k for row in case.get("var_sets") or [] for k in row} & gen_keys())
    if clash:
        errors.append(f"{cid}.var_sets: names collide with gen_vars {clash}")
    reg = reg if reg is not None else registry()
    seen: list[str] = []
    kinds = {s["id"]: ("observe" if "observe" in s else s.get("kind", "speak")) for s in case["steps"] if "id" in s}
    for step in case["steps"]:
        sid = f"{cid}.{step.get('id', '?')}"
        if "id" not in step:
            errors.append(f"{sid}: missing id")
            continue
        if step["id"] in seen:
            errors.append(f"{sid}: duplicate step id")
        if "observe" in step:
            _unknown(errors, sid, step, OBSERVE_STEP_KEYS)
            _unknown(errors, f"{sid}.observe", step["observe"], OBSERVE_KEYS)
            ref = step["observe"].get("since_step")
            if ref and ref not in seen:
                errors.append(f"{sid}.observe.since_step {ref} is not an earlier step")
        elif "kind" in step:
            kind = step["kind"]
            if kind not in KIND_PARAMS:
                errors.append(f"{sid}: unknown kind {kind}")
            else:
                _unknown(errors, sid, step, ACTION_BASE_KEYS | KIND_PARAMS[kind])
        else:
            _unknown(errors, sid, step, SPEAK_KEYS)
            for key in ("actor", "text", "wait"):
                if key not in step:
                    errors.append(f"{sid}: missing {key}")
            for name in step.get("at", []):
                if name != "employee" and name not in roles:
                    errors.append(f"{sid}.at: {name} is not a role")
            rt = step.get("reply_to")
            if rt is not None:
                _unknown(errors, f"{sid}.reply_to", rt, REPLY_TO_KEYS)
                targets = [k for k in REPLY_TO_TARGETS if k in rt]
                if len(targets) != 1:
                    errors.append(f"{sid}.reply_to: exactly one target needed, got {targets}")
                for key in ("step", "employee_reply_of", "observed"):
                    if key in rt and rt[key] not in seen:
                        errors.append(f"{sid}.reply_to.{key} {rt[key]} is not an earlier step")
                if "observed" in rt and kinds.get(rt["observed"]) != "observe":
                    errors.append(f"{sid}.reply_to.observed {rt['observed']} is not an observe step")
                if rt.get("fallback") not in (None, "employee_latest"):
                    errors.append(f"{sid}.reply_to.fallback {rt.get('fallback')} unsupported")
        if "actor" in step and step["actor"] not in roles:
            errors.append(f"{sid}: actor {step['actor']} is not a role")
        wait = step.get("wait")
        if wait is not None:
            _unknown(errors, f"{sid}.wait", wait, WAIT_KEYS)
            if wait.get("mode") not in WAIT_MODES:
                errors.append(f"{sid}.wait: mode {wait.get('mode')} unknown")
            if wait.get("since_step") and wait["since_step"] not in seen:
                errors.append(f"{sid}.wait.since_step {wait['since_step']} is not an earlier step")
        # DEAP actors cannot @ anyone and cannot DM; they address the employee only by quote-reply.
        actor_key = roles.get(step.get("actor"))
        actor = (reg.get("actors") or {}).get(actor_key or "") or {}
        if actor.get("kind") == "deap_actor":
            conv = (reg.get("conversations") or {}).get(step.get("conversation", case["conversation"])) or {}
            if step.get("at") or step.get("at_all"):
                errors.append(f"{sid}: DEAP actor {actor_key} cannot @")
            if conv.get("kind") == "dm":
                errors.append(f"{sid}: DEAP actor {actor_key} cannot speak in a 1:1 chat")
        seen.append(step["id"])
    step_ids = set(seen)
    seg_ids = [x.get("id") for x in case.get("segments") or []]
    for x in case.get("segments") or []:
        _unknown(errors, f"{cid}.segments", x, {"id", "not_before_hours", "note"})
    if seg_ids:
        for step in case["steps"]:
            if step.get("segment") not in seg_ids:
                errors.append(f"{cid}.{step.get('id')}: segment {step.get('segment')!r} not in {seg_ids}")
    _unknown(errors, f"{cid}.judge", case["judge"], JUDGE_KEYS)
    for i, check in enumerate(case["judge"].get("checks", [])):
        errors.extend(validate_check(check, f"{cid}.checks[{i}]", step_ids, pattern_sets, caps))
    return errors


def validate_check(check: dict[str, Any], where: str, step_ids: set[str], pattern_sets: dict[str, str],
                   caps: dict[str, dict[str, bool]]) -> list[str]:
    errors: list[str] = []
    if "sentinel" in check:
        _unknown(errors, where, check, SENTINEL_KEYS)
        if check.get("scope") not in (None, "case_span"):
            errors.append(f"{where}: sentinel scope {check.get('scope')} unsupported")
    elif "evidence" in check:
        _unknown(errors, where, check, EVIDENCE_KEYS)
        if check["evidence"] not in EVIDENCE_KINDS:
            errors.append(f"{where}: unknown evidence {check['evidence']}")
        for key in ("step", "if_dispatched_at"):
            if key in check and check[key] not in step_ids:
                errors.append(f"{where}.{key} {check[key]} is not a step")
    else:
        _unknown(errors, where, check, TEXT_CHECK_KEYS)
        if "steps" not in check:
            errors.append(f"{where}: text check without steps")
        for sid in check.get("steps", []):
            if sid not in step_ids:
                errors.append(f"{where}: step {sid} unknown")
        for name in check.get("exclude_sets", []):
            if name not in pattern_sets:
                errors.append(f"{where}: exclude_set {name} unknown")
        if "quotes_step" in check and check["quotes_step"] not in step_ids:
            errors.append(f"{where}.quotes_step unknown")
        if "only_if" in check:
            _unknown(errors, f"{where}.only_if", check["only_if"], {"step", "include_all"})
            if check["only_if"].get("step") not in step_ids:
                errors.append(f"{where}.only_if.step unknown")
        if "match_count" in check:
            _unknown(errors, f"{where}.match_count", check["match_count"], {"regex", "range"})
    if check.get("tier") not in (None, "target"):
        errors.append(f"{where}: tier {check.get('tier')} unknown")
    if "requires" in check:
        for name in requires_names(check["requires"]):
            if not cap_known(caps, name):
                errors.append(f"{where}: requires unknown capability {name}")
    return errors


REGEX_FIELDS = ("include_regex", "include_any_regex", "exclude")


def render_errors(case: dict[str, Any], spec: dict[str, Any]) -> list[str]:
    """Render the case with every var_sets row; nothing may stay unresolved and every regex must compile."""
    aliases = spec.get("defaults", {}).get("aliases") or {}
    from .driver import code_vars
    base = code_vars("render-check")
    rows = case.get("var_sets") or [{}]
    errors: list[str] = []
    for key, fx in (case.get("fixtures") or {}).items():
        if "render" in fx:
            suffix = Path(fx["name"]).suffix
            for idx in range(len(rows)):
                if not (V2_DIR / "fixtures" / case["id"] / f"{key}.row{idx}{suffix}").exists():
                    errors.append(f"{case['id']}: pre-rendered fixture {key}.row{idx}{suffix} missing")
        elif "by_row" in fx and len(fx["by_row"]) != len(rows):
            errors.append(f"{case['id']}: fixture {key} by_row has {len(fx['by_row'])} rows, var_sets {len(rows)}")
    for idx, row in enumerate(rows):
        vars_ = {**base, **{k: str(v) for k, v in row.items()}}
        body = render({"steps": case["steps"], "checks": case["judge"].get("checks", []),
                       "fixtures": case.get("fixtures") or {}}, vars_, aliases)
        left = unresolved(body)
        if left:
            errors.append(f"{case['id']} row {idx}: unresolved {left}")
        for step in body["steps"]:
            pattern = (step.get("observe") or {}).get("until_regex")
            if pattern:
                try:
                    re.compile(pattern)
                except re.error as exc:
                    errors.append(f"{case['id']}.{step['id']}: until_regex {exc}")
        for check in body["checks"]:
            patterns = [p for f in REGEX_FIELDS for p in check.get(f, [])]
            if "match_count" in check:
                patterns.append(check["match_count"]["regex"])
            for pattern in patterns:
                try:
                    re.compile(pattern)
                except re.error as exc:
                    errors.append(f"{case['id']}: regex {pattern!r}: {exc}")
    return errors


# ---------------------------------------------------------------- dry-run

def classify(case: dict[str, Any], spec: dict[str, Any], caps: dict[str, dict[str, bool]],
             reg: dict[str, Any]) -> dict[str, Any]:
    """runnable / runnable_partial / waiting_release / waiting_ops / blocked_* without sending anything."""
    errors = validate_case(case, spec, caps, reg)
    if not errors:
        errors = render_errors(case, spec)
    if errors:
        return {"state": "blocked_validation", "reasons": errors}
    need_harness = set(case["requires"].get("harness", []))
    for step in case["steps"]:
        if step.get("kind"):
            need_harness.add(KIND_CAPABILITY[step["kind"]])
        if step.get("burst"):
            need_harness.add("burst")
        if step.get("at_all"):
            need_harness.add("at_all")
        if step.get("segment") or case.get("segments"):
            need_harness.add("segments")
        if (step.get("observe") or {}).get("negative"):
            need_harness.add("negative_observe")
    for check in case["judge"].get("checks", []):
        if check.get("evidence") and check["evidence"] not in EVIDENCE_IMPLEMENTED:
            need_harness.add("evidence_v2")
    missing_harness = sorted(need_harness - HARNESS_IMPLEMENTED)
    if missing_harness:
        return {"state": "blocked_harness", "reasons": missing_harness}
    resources = []
    convs = {case["conversation"]} | {s["conversation"] for s in case["steps"] if s.get("conversation")}
    created = {s.get("conversation", case["conversation"]) for s in case["steps"] if s.get("create_group")}
    for name in sorted(convs):
        conv = reg["conversations"].get(name)
        if conv is None:
            resources.append(f"conversation {name} not registered")
        elif not conv.get("cid") and name not in created:
            resources.append(f"conversation {name} has no cid")
    for role, actor in case["roles"].items():
        info = reg["actors"].get(actor or "")
        if not info or not info.get("profile"):
            resources.append(f"role {role}: actor {actor} not registered")
            continue
        if info.get("kind") == "deap_actor":
            if actor not in reg["employee"].get("open_ids", {}):
                resources.append(f"employee id in {actor}'s view not registered")
            readers = {r for n in convs for r in (reg["conversations"].get(n) or {}).get("readers", [])}
            if not any(r in info.get("open_ids", {}) for r in readers):
                resources.append(f"{actor} id in a human reader's view not registered")
    if resources:
        return {"state": "blocked_resource", "reasons": resources}
    off = {cat: sorted(n for n in case["requires"].get(cat, []) if not cap_on(caps, n))
           for cat in ("release", "ops", "platform")}
    if off["release"]:
        return {"state": "waiting_release", "reasons": off["release"]}
    if off["ops"]:
        return {"state": "waiting_ops", "reasons": off["ops"]}
    vacuous = [c for c in case["judge"].get("checks", []) if c.get("requires") and not requires_ok(c["requires"], caps)]
    partial = bool(off["platform"] or vacuous)
    return {"state": "runnable_partial" if partial else "runnable",
            "reasons": off["platform"] + ([f"{len(vacuous)} check(s) vacuous"] if vacuous else [])}


def dry_run(paths: list[Path] | None, caps: dict[str, dict[str, bool]], reg: dict[str, Any] | None = None) -> dict[str, Any]:
    reg = reg if reg is not None else registry()
    rows, suite_errors = [], []
    seen_ids: set[str] = set()
    for path in paths or [V2_DIR / name for name in SUITE_FILES]:
        spec = load_suite(path)
        suite_errors.extend(validate_suite(spec, caps))
        for case in spec["cases"]:
            if case.get("id") in seen_ids:
                suite_errors.append(f"duplicate case id {case.get('id')}")
            seen_ids.add(case.get("id"))
            res = classify(case, spec, caps, reg)
            rows.append({"id": case["id"], "suite": spec["suite"], "conversation": case["conversation"],
                         "authored_status": (case.get("status") or {}).get("now"), **res})
    counts: dict[str, int] = {}
    for r in rows:
        counts[r["state"]] = counts.get(r["state"], 0) + 1
    return {"checked_at": now().isoformat(timespec="seconds"), "total": len(rows), "counts": counts,
            "suite_errors": suite_errors, "capabilities": caps, "cases": rows}


def in_run_window(window: dict[str, str] | None, at: _dt.datetime | None = None) -> bool:
    if not window:
        return True
    at = (at or now()).astimezone(TZ)
    hhmm = at.strftime("%H:%M")
    return window["from"] <= hhmm < window["to"]


def sync_known_gaps(paths: list[Path] | None = None) -> dict[str, str]:
    """cases/v2/known_gaps.json mirrors every case's known_gap field (ratchet rule unchanged)."""
    gaps = {case["id"]: case["known_gap"] for _, case in load_all(paths) if case.get("known_gap")}
    write_json(V2_DIR / "known_gaps.json", {
        "schema": "el2e.known_gaps.v1",
        "note": "Generated from cases/v2/*.json known_gap fields by `e2e.py v2 sync-gaps`. A gap that passes must be removed from its case in the same change.",
        "gaps": gaps})
    return gaps
