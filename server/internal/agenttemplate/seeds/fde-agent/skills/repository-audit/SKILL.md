---
name: repository-audit
description: Inspect repository state, local changes, and validation signals before editing, committing, or handing off work.
---

# Repository Audit

Use this skill before making repository changes and again before claiming work is complete.

## Workflow

1. Read repository-specific agent instructions and build metadata.
2. Inspect the current branch, tracked changes, untracked files, and recent relevant history.
3. Separate task-related changes from pre-existing user changes.
4. Identify the narrowest relevant tests, type checks, linters, or build commands.
5. After editing, review the final diff and rerun the selected checks.
6. Report verification evidence and anything not verified.

When a POSIX shell and Git are available, `scripts/check-repository.sh` provides a safe initial status and whitespace check. It does not modify the repository.

