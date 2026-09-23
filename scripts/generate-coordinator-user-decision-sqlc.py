#!/usr/bin/env python3
"""Regenerate Coordinator setting queries without unrelated fork API changes.

The checkout contains hand-maintained generated compatibility files. A full
sqlc run currently duplicates those declarations. Schema-only migration 271
also precedes its fork-owned table creation (9025); defer it for sqlc analysis
only. This script does not run or change any database migration.
"""

import argparse
import json
import os
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
    queries = ["agent_dingtalk_response.sql", "inbound_coordinator.sql"]
    with tempfile.TemporaryDirectory(prefix="coordinator-user-decision-sqlc-") as tmp:
        def relative(path):
            return os.path.relpath(path, tmp)
        config = {"version": "2", "sql": [{
            "engine": "postgresql",
            "queries": [relative(root / "pkg/db/queries" / name) for name in queries],
            "schema": [relative(path) for path in schemas],
            "gen": {"go": {"package": "db", "out": "out", "sql_package": "pgx/v5",
                           "emit_json_tags": True, "emit_empty_slices": True,
                           "omit_unused_structs": True}},
        }]}
        cfg = Path(tmp) / "sqlc.json"
        cfg.write_text(json.dumps(config))
        subprocess.run([args.sqlc, "generate", "-f", str(cfg)], check=True)
        outputs = [(name + ".go", name + ".go") for name in queries]
        for source, target in outputs:
            content = (Path(tmp) / "out" / source).read_bytes()
            destination = root / "pkg/db/generated" / target
            if args.check:
                if not destination.exists() or destination.read_bytes() != content:
                    raise SystemExit(f"stale generated coordinator query: {target}")
            else:
                destination.write_bytes(content)


if __name__ == "__main__":
    main()
