#!/usr/bin/env python3
"""Generate steer queries and refresh unchanged-signature fork query constants.

Full sqlc generation rewrites hand-maintained compatibility APIs in this fork.
Generate only the steer additions and the four unchanged-shape statements they
extend. No database migration is run or modified.
"""
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
        parser.error("sqlc is required on PATH or via --sqlc")
    server = Path(__file__).resolve().parents[1] / "server"
    source = (server / "pkg/db/queries/agent.sql").read_text()
    names = ["CancelAgentTask", "ListAgentPendingTasks", "ClaimAgentTask", "ClaimAgentTaskByID"]
    with tempfile.TemporaryDirectory(prefix="task-steer-sqlc-", dir=os.environ.get("MULTICA_TASK_TMPDIR")) as tmp:
        scratch = Path(tmp)
        statements = []
        for name in names:
            match = re.search(r"-- name: " + name + r" :[^\n]+\n.*?(?=\n-- name: |\Z)", source, re.S)
            if not match:
                raise SystemExit("missing query " + name)
            statements.append(match.group(0))
        (scratch / "agent_steer.sql").write_text("\n".join(statements))
        relative = lambda path: os.path.relpath(path, tmp)
        config = {"version": "2", "sql": [{
            "engine": "postgresql",
            "queries": ["agent_steer.sql", relative(server / "pkg/db/queries/task_steer.sql")],
            "schema": [relative(p) for p in sorted((server / "migrations").glob("*.up.sql"))],
            "gen": {"go": {"package": "db", "out": "out", "sql_package": "pgx/v5",
                           "emit_json_tags": True, "emit_empty_slices": True, "omit_unused_structs": True}},
        }]}
        cfg = scratch / "sqlc.json"
        cfg.write_text(json.dumps(config))
        subprocess.run([args.sqlc, "generate", "-f", str(cfg)], check=True)
        destination = server / "pkg/db/generated/task_steer.sql.go"
        generated = (scratch / "out/task_steer.sql.go").read_bytes()
        old_path = server / "pkg/db/generated/agent.sql.go"
        old = old_path.read_text()
        refreshed = (scratch / "out/agent_steer.sql.go").read_text()
        for name in names:
            pattern = r"const \w+ = `-- name: " + name + r" :[^\n]+\n.*?`"
            match = re.search(pattern, refreshed, re.S)
            if not match:
                raise SystemExit("missing generated query " + name)
            old = re.sub(pattern, lambda _: match.group(0), old, count=1, flags=re.S)
        if args.check:
            if destination.read_bytes() != generated or old_path.read_text() != old:
                raise SystemExit("stale generated steer queries")
        else:
            destination.write_bytes(generated)
            old_path.write_text(old)


if __name__ == "__main__":
    main()
