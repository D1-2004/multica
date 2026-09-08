#!/usr/bin/env python3
"""Generate FC lifecycle queries without replacing fork compatibility APIs.

Like generate-response-sqlc.py, defer schema-only migration 271 until its
fork-owned table exists during sqlc analysis. Database migration order is unchanged.
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
    with tempfile.TemporaryDirectory(prefix="fc-sandbox-sqlc-") as tmp:
        config = {"version": "2", "sql": [{
            "engine": "postgresql",
            "queries": os.path.relpath(root / "pkg/db/queries/fc_e2b_lifecycle.sql", tmp),
            "schema": [os.path.relpath(path, tmp) for path in schemas],
            "gen": {"go": {"package": "db", "out": "out", "sql_package": "pgx/v5",
                           "emit_json_tags": True, "emit_empty_slices": True,
                           "omit_unused_structs": True}},
        }]}
        cfg = Path(tmp) / "sqlc.json"
        cfg.write_text(json.dumps(config))
        subprocess.run([args.sqlc, "generate", "-f", str(cfg)], check=True)
        # The queries reuse existing model types from models.go.
        for source, target in [("fc_e2b_lifecycle.sql.go", "fc_e2b_lifecycle.sql.go")]:
            content = (Path(tmp) / "out" / source).read_bytes()
            destination = root / "pkg/db/generated" / target
            if args.check:
                if not destination.exists() or destination.read_bytes() != content:
                    raise SystemExit(f"stale FC lifecycle query: {target}")
            else:
                destination.write_bytes(content)


if __name__ == "__main__":
    main()
