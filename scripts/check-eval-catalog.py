#!/usr/bin/env python3
"""Check canonical eval definitions and references without running evaluations."""

import argparse
import json
from pathlib import Path, PurePosixPath, PureWindowsPath
import re
import subprocess
import sys
import unicodedata

ROOT = Path(__file__).resolve().parents[1]
CATALOG_PATH = "server/internal/evalcatalog"
OFFICE_PATH = f"{CATALOG_PATH}/office-scenarios.json"
P0_PATH = f"{CATALOG_PATH}/p0-golden.json"
SPEC_PATH = f"{CATALOG_PATH}/spec.json"
ID_PATTERN = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")
P0_IDS = [f"G{i:02}" for i in range(1, 21)]
DEFINITION_FIELDS = ("roles", "verifies", "method")
RESULT_FIELDS = {"status", "pass", "passed", "result", "results", "score", "history", "runId", "traceId", "evidence"}
SENSITIVE_PATTERNS = {
    "live UUID": r"\b[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}\b",
    "live conversation locator": r"\bcid[+A-Za-z0-9/_=-]{12,}",
    "account email": r"\b[\w.+-]+@[\w.-]+\.[A-Za-z]{2,}\b",
    "local account path": r"(?:/Users/|/home/)[^\s\"']+",
    "private key": r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----",
    "credential token": r"\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|LTAI[A-Za-z0-9]{12,})\b",
    "JWT": r"\beyJ[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\.[A-Za-z0-9_-]{12,}\b",
    "credential assignment": r"\b(?:access[_-]?token|refresh[_-]?token|api[_-]?key|password|client[_-]?secret|secret)\s*[:=]\s*[\"']?[^\s\"',;]{8,}",
    "account locator assignment": r"\b(?:staffId|userId|uid|agent_id|workspace_id|tenant_id|openConversationId|openMsgId)\s*[:=]\s*[\"']?[A-Za-z0-9+/=_-]{6,}",
}


def normalized_behavior(item):
    """Ignore titles, role aliases and fixture numbers for exact clone detection."""
    values = item.get("verifies", []) + item.get("method", [])
    text = unicodedata.normalize("NFKC", " ".join(values)).casefold()
    text = re.sub(r"小[甲乙丙丁]", "测试角色", text)
    text = re.sub(r"\d+", "#", text)
    return re.sub(r"\s+", "", text)


class Checker:
    def __init__(self, root=ROOT):
        self.root = root.resolve()
        self.errors = []
        self.source_ids = set()
        self.scenario_ids = set()
        self.category_ids = set()
        self.spec_ids = set()
        self.case_owners = {}
        self.behaviors = {}

    def require(self, condition, message):
        if not condition:
            self.errors.append(message)
        return bool(condition)

    def load(self, relative, required=True):
        path = self.root / relative
        if not path.is_file() and not required:
            return None
        try:
            return json.loads(path.read_text(encoding="utf-8"))
        except (OSError, UnicodeError, json.JSONDecodeError) as exc:
            self.errors.append(f"{relative}: cannot read JSON ({type(exc).__name__})")
            return None

    def text(self, value, label):
        return self.require(isinstance(value, str) and bool(value.strip()), f"{label}: expected nonempty text")

    def strings(self, value, label, allow_empty=False):
        valid = isinstance(value, list) and (allow_empty or bool(value)) and all(isinstance(x, str) and x.strip() for x in value)
        if not self.require(valid, f"{label}: expected {'a' if allow_empty else 'a nonempty'} string list"):
            return []
        self.require(len(value) == len(set(value)), f"{label}: duplicate entries")
        return value

    def scan_text(self, value, label):
        # Scan definitions, never load source contents, credentials or account profiles.
        if isinstance(value, dict):
            for key, child in value.items():
                self.scan_text(child, f"{label}.{key}")
        elif isinstance(value, list):
            for index, child in enumerate(value):
                self.scan_text(child, f"{label}[{index}]")
        elif isinstance(value, str):
            for kind, pattern in SENSITIVE_PATTERNS.items():
                if re.search(pattern, value, re.I):
                    self.errors.append(f"{label}: {kind} is not allowed in definitions")

    def sources(self, item, label):
        for value in self.strings(item.get("sources"), f"{label}.sources"):
            path = PurePosixPath(value)
            valid = (
                not path.is_absolute()
                and not PureWindowsPath(value).is_absolute()
                and ".." not in path.parts
                and not value.startswith("~")
                and "\\" not in value
                and ":" not in value
                and "\x00" not in value
            )
            if not self.require(valid, f"{label}.sources: source must be repo-relative and cannot escape the repository"):
                continue
            name = path.name.lower()
            credential_file = (
                name == ".env"
                or (name.startswith(".env.") and not name.endswith((".example", ".sample", ".template")))
                or path.suffix.lower() in {".pem", ".key", ".p12", ".pfx", ".keystore"}
                or name in {"credentials.json", "credentials", "token.json", "tokens.json"}
                or ".git" in path.parts
            )
            if not self.require(not credential_file, f"{label}.sources: credential or Git metadata file cannot be a definition source"):
                continue
            candidate = self.root / value
            try:
                resolved = candidate.resolve()
                resolved.relative_to(self.root)
                inside = True
            except (OSError, RuntimeError, ValueError):
                inside = False
            if self.require(inside, f"{label}.sources: symlink/path escapes the repository"):
                self.require(candidate.is_file(), f"{label}.sources: file does not exist: {value}")

    def common(self, item, label):
        self.text(item.get("title"), f"{label}.title")
        for field in DEFINITION_FIELDS:
            self.strings(item.get(field), f"{label}.{field}")
        self.sources(item, label)
        self.require(not set(item).intersection(RESULT_FIELDS), f"{label}: definitions cannot contain run results, status, or evidence")
        self.scan_text(item, label)

    def source_pool(self):
        # Legacy files are reference indexes only: no version, shape, UI or coverage gates.
        inventory = self.load(f"{CATALOG_PATH}/suites.json", required=False)
        if isinstance(inventory, dict):
            for suite in inventory.get("suites", []):
                if isinstance(suite, dict):
                    for case in suite.get("cases", []):
                        if isinstance(case, dict) and isinstance(case.get("id"), str):
                            self.source_ids.add(case["id"])
        legacy = self.load(f"{CATALOG_PATH}/golden.json", required=False)
        if isinstance(legacy, dict):
            rows = list(legacy.get("cases", []))
            for group in legacy.get("groups", []):
                if isinstance(group, dict):
                    rows.extend(group.get("cases", []))
            for case in rows:
                if isinstance(case, dict) and isinstance(case.get("id"), str):
                    self.source_ids.add(case["id"])

    def validate(self, office, p0):
        self.source_pool()
        if not self.require(isinstance(office, dict), f"{OFFICE_PATH}: expected an object"):
            return
        self.require(office.get("version") == 1, f"{OFFICE_PATH}: version must be 1")
        categories = office.get("categories")
        if self.require(isinstance(categories, list) and bool(categories), f"{OFFICE_PATH}: missing display categories"):
            for item in categories:
                if not self.require(isinstance(item, dict), "category: expected an object"):
                    continue
                cid = item.get("id")
                if not self.require(isinstance(cid, str) and bool(ID_PATTERN.fullmatch(cid)), "category: invalid stable ID"):
                    continue
                self.require(cid not in self.category_ids, f"{cid}: duplicate category ID")
                self.category_ids.add(cid)
                self.text(item.get("title"), f"{cid}.title")
                self.text(item.get("description"), f"{cid}.description")
                self.require(not set(item).intersection(RESULT_FIELDS), f"{cid}: category cannot contain acceptance results")
                self.scan_text(item, cid)
        scenarios = office.get("scenarios")
        if not self.require(isinstance(scenarios, list) and bool(scenarios), f"{OFFICE_PATH}: missing scenarios"):
            return
        cases = []
        used_categories = set()
        for index, scenario in enumerate(scenarios):
            label = f"scenario[{index}]"
            if not self.require(isinstance(scenario, dict), f"{label}: expected an object"):
                continue
            sid = scenario.get("id")
            if not self.require(isinstance(sid, str) and bool(ID_PATTERN.fullmatch(sid)), f"{label}: invalid stable scenario ID"):
                continue
            self.require(sid not in self.scenario_ids, f"{sid}: duplicate scenario ID")
            self.scenario_ids.add(sid)
            category = scenario.get("categoryRef")
            if self.require(isinstance(category, str) and category in self.category_ids, f"{sid}: unknown categoryRef"):
                used_categories.add(category)
            self.text(scenario.get("title"), f"{sid}.title")
            self.text(scenario.get("description"), f"{sid}.description")
            self.scan_text({"title": scenario.get("title"), "description": scenario.get("description")}, sid)
            rows = scenario.get("cases")
            if not self.require(isinstance(rows, list) and bool(rows), f"{sid}: missing cases"):
                continue
            for row in rows:
                if not self.require(isinstance(row, dict), f"{sid}: case must be an object"):
                    continue
                cid = row.get("id")
                if not self.require(isinstance(cid, str) and bool(ID_PATTERN.fullmatch(cid)), f"{sid}: invalid stable case ID"):
                    continue
                self.require(cid not in self.case_owners, f"{cid}: duplicate case ID")
                self.case_owners[cid] = sid
                cases.append(row)
                self.common(row, cid)
                self.require(row.get("origin") in {"existing", "defined"}, f"{cid}: origin must be existing or defined")
                refs = self.strings(row.get("sourceCases"), f"{cid}.sourceCases", allow_empty=True)
                self.require(row.get("origin") != "existing" or bool(refs), f"{cid}: existing origin needs a sourceCase reference")
                self.require(cid not in refs, f"{cid}: sourceCases cannot cite itself")
                if all(isinstance(row.get(k), list) and all(isinstance(x, str) for x in row[k]) for k in ("verifies", "method")):
                    fingerprint = normalized_behavior(row)
                    if fingerprint:
                        previous = self.behaviors.get(fingerprint)
                        self.require(previous is None, f"{cid}: exact behavior clone of {previous}; define a distinct behavior risk")
                        self.behaviors[fingerprint] = cid
        self.require(len(cases) >= 100, f"{OFFICE_PATH}: at least 100 scenario cases are required; growth is not capped")
        self.require(used_categories == self.category_ids, "display categories cannot be empty")
        self.require(not self.scenario_ids.intersection(self.case_owners), "scenario IDs and case IDs must not collide")
        self.source_ids.update(self.case_owners)
        self.source_ids.update(P0_IDS)
        for case in cases:
            refs = case.get("sourceCases", [])
            if isinstance(refs, list):
                for ref in refs:
                    if isinstance(ref, str):
                        self.require(ref in self.source_ids, f"{case['id']}: unknown sourceCase {ref}")
        if not self.require(isinstance(p0, dict), f"{P0_PATH}: expected an object"):
            return
        self.require(p0.get("version") == 1, f"{P0_PATH}: version must be 1")
        rows = p0.get("cases")
        if not self.require(isinstance(rows, list), f"{P0_PATH}: missing cases"):
            return
        self.require([x.get("id") if isinstance(x, dict) else None for x in rows] == P0_IDS, f"{P0_PATH}: exactly 20 ordered IDs G01-G20 are required")
        for row in rows:
            if not self.require(isinstance(row, dict), "P0 case must be an object"):
                continue
            label = row.get("id", "P0")
            self.common(row, label)
            self.require(row.get("priority") == "P0", f"{label}: priority must be P0")
            scenario_refs = self.strings(row.get("scenarioRefs"), f"{label}.scenarioRefs")
            case_refs = self.strings(row.get("caseRefs"), f"{label}.caseRefs")
            for sid in scenario_refs:
                self.require(sid in self.scenario_ids, f"{label}: unknown scenarioRef {sid}")
            for cid in case_refs:
                owner = self.case_owners.get(cid)
                if self.require(owner is not None, f"{label}: unknown caseRef {cid}"):
                    self.require(owner in scenario_refs, f"{label}: {cid} belongs to {owner}; add its scenarioRef or fix caseRefs")

    def validate_spec(self, spec):
        if not self.require(isinstance(spec, dict), f"{SPEC_PATH}: expected an object"):
            return
        self.require(spec.get("version") == 1, f"{SPEC_PATH}: version must be 1")
        self.text(spec.get("title"), "SPEC.title")
        self.text(spec.get("description"), "SPEC.description")
        self.require(not set(spec).intersection(RESULT_FIELDS), "SPEC: cannot contain acceptance results")
        self.scan_text(spec, "SPEC")
        rows = spec.get("requirements")
        if not self.require(isinstance(rows, list) and bool(rows), "SPEC: missing requirements"):
            return
        covered = set()
        for row in rows:
            if not self.require(isinstance(row, dict), "SPEC requirement: expected an object"):
                continue
            rid = row.get("id")
            if not self.require(isinstance(rid, str) and bool(ID_PATTERN.fullmatch(rid)), "SPEC requirement: invalid stable ID"):
                continue
            self.require(rid not in self.spec_ids, f"{rid}: duplicate SPEC ID")
            self.spec_ids.add(rid)
            self.require(rid not in self.scenario_ids and rid not in self.case_owners, f"{rid}: SPEC ID collides with a scenario or case")
            self.text(row.get("title"), f"{rid}.title")
            self.text(row.get("summary"), f"{rid}.summary")
            self.strings(row.get("requirements"), f"{rid}.requirements")
            self.sources(row, rid)
            self.require(not set(row).intersection(RESULT_FIELDS), f"{rid}: SPEC cannot contain acceptance results")
            for sid in self.strings(row.get("scenarioRefs"), f"{rid}.scenarioRefs"):
                if self.require(sid in self.scenario_ids, f"{rid}: unknown SPEC scenarioRef {sid}"):
                    covered.add(sid)
        self.require(covered == self.scenario_ids, "every office scenario needs a SPEC requirement association")

    def stable_ids(self, base_ref):
        if not base_ref or re.fullmatch(r"0{40,64}", base_ref):
            return
        try:
            result = subprocess.run(["git", "rev-parse", "--verify", "--end-of-options", f"{base_ref}^{{commit}}"], cwd=self.root, stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True, check=True)
            sha = result.stdout.strip()
        except (OSError, subprocess.CalledProcessError):
            self.errors.append("--base-ref must resolve to an available commit")
            return
        baseline = {}
        for relative in (OFFICE_PATH, P0_PATH, SPEC_PATH):
            probe = subprocess.run(["git", "cat-file", "-e", f"{sha}:{relative}"], cwd=self.root, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            if probe.returncode:
                # A verified commit without this module permits initial publication.
                continue
            old = subprocess.run(["git", "show", f"{sha}:{relative}"], cwd=self.root, stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True)
            if not self.require(old.returncode == 0, f"{relative}: cannot read an existing base file"):
                continue
            try:
                baseline[relative] = json.loads(old.stdout)
            except json.JSONDecodeError:
                self.errors.append(f"{relative}: base JSON cannot be interpreted for stable-ID checking")
        if OFFICE_PATH in baseline:
            try:
                for scenario in baseline[OFFICE_PATH]["scenarios"]:
                    sid = scenario["id"]
                    self.require(sid in self.scenario_ids, f"{sid}: stable scenario ID removed; preserve its definition instead of reusing an ID")
                    for case in scenario["cases"]:
                        cid = case["id"]
                        owner = self.case_owners.get(cid)
                        self.require(owner is not None, f"{cid}: stable case ID removed; preserve its behavior definition")
                        self.require(owner is None or owner == sid, f"{cid}: stable case ID moved from {sid} to {owner}; retain ownership or use a controlled migration")
            except (KeyError, TypeError):
                self.errors.append("base office catalog cannot be interpreted for stable-ID checking")
        if P0_PATH in baseline:
            try:
                prior_ids = [case["id"] for case in baseline[P0_PATH]["cases"]]
                self.require(set(prior_ids).issubset(P0_IDS), "base P0 IDs differ from the fixed G01-G20 contract; review migration")
            except (KeyError, TypeError):
                self.errors.append("base P0 catalog cannot be interpreted for stable-ID checking")
        if SPEC_PATH in baseline:
            try:
                for item in baseline[SPEC_PATH]["requirements"]:
                    self.require(item["id"] in self.spec_ids, f"{item['id']}: stable SPEC ID removed; preserve its requirement")
            except (KeyError, TypeError):
                self.errors.append("base SPEC cannot be interpreted for stable-ID checking")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-ref", help="Compare stable IDs against this available Git commit (used by CI).")
    args = parser.parse_args(argv)
    checker = Checker()
    office = checker.load(OFFICE_PATH)
    p0 = checker.load(P0_PATH)
    checker.validate(office, p0)
    checker.validate_spec(checker.load(SPEC_PATH))
    checker.stable_ids(args.base_ref)
    if checker.errors:
        for error in checker.errors:
            safe = error
            for pattern in SENSITIVE_PATTERNS.values():
                safe = re.sub(pattern, "[redacted]", safe, flags=re.I)
            print(safe, file=sys.stderr)
        return 1
    origin = {kind: sum(c.get("origin") == kind for s in office["scenarios"] for c in s["cases"]) for kind in ("existing", "defined")}
    print(f"Definitions valid: {len(checker.spec_ids)} SPEC requirements / {len(checker.category_ids)} categories / 20 P0 Golden / {len(checker.scenario_ids)} scenarios / {len(checker.case_owners)} cases (existing={origin['existing']}, defined={origin['defined']}).")
    print("Structure, references and stable IDs only; no runner, model, DWS or real evaluation executed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
