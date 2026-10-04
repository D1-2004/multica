#!/usr/bin/env python3
"""Validate the reviewed inventory and its source-case coverage without executing evals."""

import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[1]
CATALOG = ROOT / "server/internal/evalcatalog"
STATUS = {"defined", "planned", "historical", "needs-migration"}
errors = []


def require(condition, message):
    if not condition:
        errors.append(message)


def strings(value):
    return isinstance(value, list) and bool(value) and all(isinstance(s, str) and s.strip() for s in value)


def sources(item, label):
    require(strings(item.get("sources")), f"{label}: missing sources")
    for source in item.get("sources", []):
        path = Path(source)
        require(not path.is_absolute() and ".." not in path.parts, f"{label}: source must be repo-relative: {source}")
        require((ROOT / path).is_file(), f"{label}: source does not exist: {source}")


def unique(items, label):
    seen = set()
    for item in items:
        name = item.get("id", "")
        require(bool(re.fullmatch(r"[a-z0-9]+(?:-[a-z0-9]+)*", name)), f"{label}: invalid id {name!r}")
        require(name not in seen, f"{label}: duplicate id {name}")
        seen.add(name)
    return seen


def check_coverage(path, expected, cases):
    actual = [c["sourceCase"] for c in cases if path in c["sources"]]
    require(set(actual) == set(expected), f"{path}: source coverage missing={sorted(set(expected) - set(actual))}, extra={sorted(set(actual) - set(expected))}")
    require(len(actual) == len(set(actual)), f"{path}: duplicate source cases")


def main():
    inventory = json.loads((CATALOG / "suites.json").read_text())
    environment = json.loads((CATALOG / "environment.json").read_text())
    ids = {}
    required = {
        "profiles": ("title", "purpose", "proves", "limits"),
        "identities": ("title", "grant", "scope", "proof", "limits"),
        "tools": ("name", "phase", "purpose", "access", "readback"),
    }
    for collection, fields in required.items():
        items = environment[collection]
        ids[collection] = unique(items, collection)
        for item in items:
            label = item["id"]
            for field in fields:
                require(isinstance(item.get(field), str) and item[field].strip(), f"{label}: missing {field}")
            sources(item, label)
            if collection == "profiles":
                for field in ("components", "gates"):
                    require(strings(item.get(field)), f"{label}: missing {field}")
    for collection, fields in {"stages": ("title", "artifact", "requirement"), "gates": ("title", "rule")}.items():
        require(bool(environment.get(collection)), f"missing {collection}")
        for item in environment.get(collection, []):
            for field in fields:
                require(isinstance(item.get(field), str) and item[field].strip(), f"{collection}: missing {field}")

    suites = inventory["suites"]
    unique(suites, "suites")
    cases = [case for suite in suites for case in suite["cases"]]
    unique(cases, "cases")
    refs = {"environmentIds": "profiles", "identityIds": "identities", "toolIds": "tools"}
    for item in suites + cases:
        label = item["id"]
        require(item.get("status") in STATUS, f"{label}: invalid status")
        sources(item, label)
        for field, collection in refs.items():
            require(strings(item.get(field)), f"{label}: missing {field}")
            for ref in item.get(field, []):
                require(ref in ids[collection], f"{label}: unknown {field} {ref}")
        fields = ("project", "title", "description") if "cases" in item else ("title", "module", "why", "sourceCase")
        for field in fields:
            require(isinstance(item.get(field), str) and item[field].strip(), f"{label}: missing {field}")
        if "cases" in item:
            require(bool(item["cases"]), f"{label}: empty suite")
        else:
            for field in ("method", "verify", "evidence", "boundaries"):
                require(strings(item.get(field)), f"{label}: missing {field}")
            require(isinstance(item.get("history"), str), f"{label}: history must be explicit")
            require(item["status"] != "historical" or bool(item.get("history")), f"{label}: historical case needs dated explanation")

    for spec in sorted((ROOT / "e2e").glob("*.spec.ts")):
        titles = re.findall(r'\btest\s*\(\s*"([^"\n]+)"', spec.read_text())
        require(bool(titles), f"{spec.name}: no test titles found; update coverage parser")
        check_coverage(spec.relative_to(ROOT).as_posix(), titles, cases)
    for name in ("assoc-scene-eval-suite", "coordinator-collect-e2e", "coordinator-progressive-smoke"):
        path = f"docs/evals/{name}.json"
        source = json.loads((ROOT / path).read_text())
        check_coverage(path, [c["id"] for c in source["cases"]], cases)
    for name in ("2026-09-08-coordinator-policy-v2-full", "20260910T033123Z-generic-proactive-gate"):
        path = f"docs/evals/results/{name}.json"
        source = json.loads((ROOT / path).read_text())
        rows = source if isinstance(source, list) else source.get("results", source.get("cases", []))
        expected = [c.get("id", c.get("case_id", c.get("case"))) for c in rows]
        require(bool(expected) and all(expected), f"{path}: source shape changed; update coverage parser")
        check_coverage(path, expected, cases)

    plan = "docs/plans/2026-10-03/employee-loop-backend-delivery/08-integration-and-release.md"
    expected = []
    for cell in re.findall(r"^\| ([A-Z]+-[A-Z0-9/-]+) \|", (ROOT / plan).read_text(), re.M):
        first, *rest = cell.split("/")
        expected += [first] + [suffix if "-" in suffix else first.split("-")[0] + "-" + suffix for suffix in rest]
    check_coverage(plan, expected, cases)
    require(bool(expected), "Employee acceptance table has no parsed case IDs; review source format")

    mcp = "scripts/agent-mcp-deep-e2e.mjs"
    segments = sorted(set(re.findall(r"\breport\.([A-Za-z]+)(?:\.|\s*=)", (ROOT / mcp).read_text())))
    require(bool(segments), "MCP report has no parsed sections; review source format")
    check_coverage(mcp, segments, cases)

    golden = json.loads((CATALOG / "golden.json").read_text())
    groups = golden.get("groups", [])
    require(golden.get("version") == 1 and len(groups) == 4, "golden catalog must define version 1 and four groups")
    golden_cases = [case for group in groups for case in group.get("cases", [])]
    require([c.get("id") for c in golden_cases] == [f"G{i:02}" for i in range(1, 21)], "golden definitions must be ordered G01 through G20")
    source_ids = {c["id"] for c in cases}
    for level, group in enumerate(groups, start=1):
        require(group.get("level") == level and len(group.get("cases", [])) == 5, f"golden complexity {level}: expected five cases in order")
        for case in group.get("cases", []):
            label = case["id"]
            for field in ("title", "module", "definition"):
                require(isinstance(case.get(field), str) and case[field].strip(), f"{label}: missing {field}")
            for field in ("setup", "actors", "grants", "steps", "verify", "evidence", "tools", "cleanup", "sourceCases"):
                require(strings(case.get(field)), f"{label}: missing {field}")
            sources(case, label)
            for ref in case.get("sourceCases", []):
                require(ref in source_ids, f"{label}: unknown source case {ref}")
            require(not set(case).intersection({"why", "history", "status"}), f"{label}: golden cases only define scenarios")

    office = json.loads((CATALOG / "office-scenarios.json").read_text())["scenarios"]
    office_cases = [c for scenario in office for c in scenario["cases"]]
    office_ids = unique(office_cases, "office cases")
    require(len(office_cases) >= 100, "office catalog must contain at least 100 definitions")
    for case in office_cases:
        for field in ("roles", "verifies", "method"):
            require(strings(case.get(field)), f"{case['id']}: missing {field}")
        sources(case, case["id"])
        require(case.get("origin") in {"existing", "defined"}, f"{case['id']}: invalid definition origin")
    p0 = json.loads((CATALOG / "p0-golden.json").read_text())["cases"]
    require([c["id"] for c in p0] == [f"G{i:02}" for i in range(1, 21)], "P0 must be ordered G01-G20")
    scenario_ids = {s["id"] for s in office}
    for case in p0:
        for field in ("roles", "verifies", "method", "caseRefs", "scenarioRefs"):
            require(strings(case.get(field)), f"{case['id']}: missing {field}")
        require(set(case["caseRefs"]).issubset(office_ids), f"{case['id']}: unknown office case")
        require(set(case["scenarioRefs"]).issubset(scenario_ids), f"{case['id']}: unknown scenario")
        sources(case, case["id"])

    metadata = (CATALOG / "suites.json").read_text() + (CATALOG / "environment.json").read_text() + (CATALOG / "golden.json").read_text()
    require(not re.search(r"\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b|cid[A-Za-z0-9+/=]{15,}", metadata), "live object identifier found in public metadata")
    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    print(f"P0 + office definitions valid: 20 Golden / {len(office_cases)} office cases; source inventory: {len(suites)} suites / {len(cases)} cases; all references covered.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
