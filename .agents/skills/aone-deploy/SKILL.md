---
name: aone-deploy
description: Deploy this fork to the Aone pre-release environment, read runtime logs, safely update the env-vars trait, and diagnose failed builds or deploys. Use when asked to deploy, redeploy, check deployment status, read server logs, add or change environment variables, fix Aquaman YAML or env.value type errors, or investigate why pre-release is broken.
---

# Aone Deploy & Operations

Operate this fork's Aone pre-release deployment. Root `CLAUDE.md` owns migration,
transaction and multi-replica invariants; this skill owns operation procedures.

## Choose the operation

A status/log/config read does not imply a CR, `reenter`, configuration writes or
another deployment. Employee/Tag work first uses
`docs/development-delivery.md` and its domain contract for acceptance, environment,
milestones and handoff boundary. Review can
finish with deployment facts explicitly unverified; claiming a deployed or
running result still requires actual release/live-replica evidence.

Read only the relevant section of [operations](references/operations.md):

| Need | Section |
| --- | --- |
| Deploy, retry, resolve release conflict | Deploy; Pipelines |
| Read startup/runtime logs | Read runtime logs |
| Change environment variables | Runtime config; Safe read-modify-write |
| Diagnose failed stage | Diagnose a failed deploy |
| Schema-breaking upstream sync | Upstream-sync deployment fence; Database; Upstream sync |

Load the exact a1 CLI contract for the selected action, not every operation.
For env writes, read serialization/rollback rules before any mutation; for
schema/fence work, read root Aone Fork rules and the fence section first.

## Shared constraints

- Internal requests run with six proxy variables removed in that process:
  `unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy`.
- Credentials and expanded trait/error values stay out of output and Git.
  Environment trait values use JSON string literals; preserve unrelated values
  and rollback on failed replacement.
- Shared pre-release has one agreed publisher/Apply owner. Independent reads do
  not wait for that publisher; writes respect the active release/test window.
- Use the exact run ID and its frozen release/source, not the newest SUCCESS or
  sorted release branch. Confirm each live backend's current startup and fence/
  required markers. Save one manifest for the unchanged wave; refresh after a
  new deploy, restart or relevant config change.
- Observe with bounded waits and backoff. Read timeout is missing evidence,
  not permission to trigger another CI/release. At a user deadline, report stage
  and remaining uncertainty; do not silently extend the scope.
- Pipeline66 pre-release is distinct from production. Closing its verification
  gate requires the current scoped acceptance, not an empty ACK or local build.
- Pre-release migrations run through the packaged migrator. Never manually run
  them from local CLI or pod shell. Risky PolarDB permission checks use rollback
  rehearsal under the procedure, never a committed manual migration.

Report the operation result, exact version/run (when applicable), current
observation limits and restoration. Source review, build, deployment and real
user effect are separate milestones.
