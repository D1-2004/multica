#!/usr/bin/env python3
"""Regenerate GitRepo queries without changing unrelated fork query projections.

Like generate-response-sqlc.py, defer migration 271 for sqlc analysis only.
The Agent model contains JSON-profile projections maintained outside full sqlc
output, so preserve the two unchanged Agent-returning source query projections.
This script never executes database migrations or formats source files.
"""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--sqlc", default=shutil.which("sqlc"))
parser.add_argument("--check", action="store_true")
args = parser.parse_args()
if not args.sqlc:
    parser.error("sqlc is required on PATH or via --sqlc")
root = Path(__file__).resolve().parents[1] / "server"
queries = ["github.sql", "git_connection.sql", "agent_source.sql", "agent_source_preview.sql", "agent_package_source.sql", "workspace_delete.sql"]
schemas = sorted((root / "migrations").glob("*.up.sql"))
deferred = root / "migrations/271_task_completion_canceled_status.up.sql"
if deferred in schemas:
    schemas.remove(deferred)
    schemas.append(deferred)
with tempfile.TemporaryDirectory(prefix="gitrepo-sqlc-") as directory:
    temp = Path(directory)
    config = {"version": "2", "sql": [{"engine": "postgresql", "queries": [os.path.relpath(root / "pkg/db/queries" / name, temp) for name in queries], "schema": [os.path.relpath(path, temp) for path in schemas], "gen": {"go": {"package": "db", "out": "out", "sql_package": "pgx/v5", "emit_json_tags": True, "emit_empty_slices": True, "omit_unused_structs": True}}}]}
    (temp / "sqlc.json").write_text(json.dumps(config))
    subprocess.run([args.sqlc, "generate", "-f", str(temp / "sqlc.json")], check=True)
    outputs = {}
    for query in queries:
        name = query + ".go"
        content = (temp / "out" / name).read_text()
        if query == "agent_source.sql":
            current = (root / "pkg/db/generated" / name).read_text()
            for function in ["applyManagedAgentOwnerChange", "lockManagedAgentOwnerChange"]:
                pattern = r"const " + function + r" =.*?(?=\nconst |\Z)"
                block = re.search(pattern, current, re.S).group(0)
                content = re.sub(pattern, lambda match: block, content, flags=re.S)
        outputs[name] = content
    models = (root / "pkg/db/generated/models.go").read_text()
    generated = (temp / "out/models.go").read_text()
    for model in ["AgentSource", "AgentSourcePreview", "GitConnection"]:
        pattern = r"type " + model + r" struct \{.*?\n\}"
        block = re.search(pattern, generated, re.S).group(0)
        if re.search(pattern, models, re.S):
            models = re.sub(pattern, lambda match: block, models, flags=re.S)
        else:
            models += "\n" + block + "\n"
    models = re.sub(r"type GithubInstallation struct \{.*?\n\}\n", "", models, flags=re.S)
    outputs["models.go"] = models
    for name, content in outputs.items():
        target = root / "pkg/db/generated" / name
        if args.check:
            if target.read_text() != content:
                raise SystemExit("stale generated GitRepo query: " + name)
        else:
            target.write_text(content)
