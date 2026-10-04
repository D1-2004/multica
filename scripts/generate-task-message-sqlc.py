#!/usr/bin/env python3
"""Regenerate transcript queries without rewriting fork compatibility models."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sqlc", default=shutil.which("sqlc"))
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if not args.sqlc:
        parser.error("sqlc is required")
    server = Path(__file__).resolve().parents[1] / "server"
    with tempfile.TemporaryDirectory(prefix="task-message-sqlc-", dir=os.environ.get("MULTICA_TASK_TMPDIR")) as tmp:
        scratch = Path(tmp)
        relative = lambda p: os.path.relpath(p, tmp)
        config = {"version": "2", "sql": [{
            "engine": "postgresql", "queries": relative(server / "pkg/db/queries/task_message.sql"),
            "schema": [relative(p) for p in sorted((server / "migrations").glob("*.up.sql"))],
            "gen": {"go": {"package": "db", "out": "out", "sql_package": "pgx/v5",
                           "emit_json_tags": True, "emit_empty_slices": True, "omit_unused_structs": True}},
        }]}
        path = scratch / "sqlc.json"
        path.write_text(json.dumps(config))
        subprocess.run([args.sqlc, "generate", "-f", str(path)], check=True)
        query = (scratch / "out/task_message.sql.go").read_bytes()
        model = re.search(r"type TaskMessage struct \{.*?\n\}", (scratch / "out/models.go").read_text(), re.S).group(0)
        destination = server / "pkg/db/generated/task_message.sql.go"
        models_path = server / "pkg/db/generated/models.go"
        models = models_path.read_text()
        models = re.sub(r"type TaskMessage struct \{.*?\n\}", lambda _: model, models, count=1, flags=re.S)
        if args.check:
            if destination.read_bytes() != query or models_path.read_text() != models:
                raise SystemExit("stale generated task message queries/model")
        else:
            destination.write_bytes(query)
            models_path.write_text(models)


if __name__ == "__main__":
    main()
