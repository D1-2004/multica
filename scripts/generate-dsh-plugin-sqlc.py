#!/usr/bin/env python3
"""Regenerate DSH plugin queries while preserving unrelated fork-generated APIs.

The checkout contains hand-maintained generated compatibility files. A full
sqlc run currently duplicates those declarations. Schema-only migration 271
also precedes its fork-owned table creation (9025); defer it for sqlc analysis
only. This script does not run or change any database migration.
"""

import argparse
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sqlc", default=shutil.which("sqlc"))
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if not args.sqlc:
        parser.error("sqlc is required on PATH or via --sqlc")
    root = Path(__file__).resolve().parents[1] / "server"
    schemas = sorted((root / "migrations").glob("*.up.sql"))
    deferred = root / "migrations/271_task_completion_canceled_status.up.sql"
    if deferred in schemas:
        schemas.remove(deferred)
        schemas.append(deferred)
    queries = ["dsh_plugin.sql"]
    with tempfile.TemporaryDirectory(prefix="dsh-plugin-sqlc-") as tmp:
        def relative(path):
            return os.path.relpath(path, tmp)
        config = {"version": "2", "sql": [{
            "engine": "postgresql",
            "queries": [relative(root / "pkg/db/queries" / name) for name in queries],
            "schema": [relative(path) for path in schemas],
            "gen": {"go": {"package": "db", "out": "out", "sql_package": "pgx/v5",
                           "emit_json_tags": True, "emit_empty_slices": True,
                           "omit_unused_structs": False}},
        }]}
        cfg = Path(tmp) / "sqlc.json"
        cfg.write_text(json.dumps(config))
        subprocess.run([args.sqlc, "generate", "-f", str(cfg)], check=True)
        outputs = [(name + ".go", name + ".go") for name in queries]
        generated_models = (Path(tmp) / "out/models.go").read_text()
        model_path = root / "pkg/db/generated/models.go"
        existing_models = model_path.read_text()
        for name in ("AgentDshPlugin",):
            pattern = rf"type {name} struct \{{.*?\n\}}"
            match = re.search(pattern, generated_models, re.S)
            if not match:
                raise SystemExit(f"missing generated model: {name}")
            existing_models, count = re.subn(pattern, lambda _: match.group(), existing_models, flags=re.S)
            if count != 1:
                raise SystemExit(f"unexpected model count: {name}: {count}")
        if args.check:
            if model_path.read_text() != existing_models:
                raise SystemExit("stale DSH plugin models")
        else:
            model_path.write_text(existing_models)
        for source, target in outputs:
            content = (Path(tmp) / "out" / source).read_bytes()
            destination = root / "pkg/db/generated" / target
            if args.check:
                if not destination.exists() or destination.read_bytes() != content:
                    raise SystemExit(f"stale generated DSH plugin query: {target}")
            else:
                destination.write_bytes(content)


if __name__ == "__main__":
    main()
