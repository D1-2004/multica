---
name: fc-runtime-dev-loop
description: Build and validate development FC/E2B Runtime images, configure candidate Multica Runtimes through Token + HTTP API, and verify Daemon compatibility on both FC run-once sandboxes and persistent local devices. Use whenever a developer changes Runtime image code, Daemon/client/task protocol code, asks for a candidate FC sandbox, wants to create/switch/rollback a non-stable Runtime, or asks whether a Daemon change still works on real computers. Require immutable provenance, real task canaries, and the local/FC rolling compatibility matrix when the touched code demands them. Do not use for stable-channel rollout, production release, ASB images, or unrelated application deployment.
compatibility: Requires git, Python 3.11+, a1 CLI authenticated for Code/Aone CI, and a Multica personal access token with candidate Runtime publisher permission.
---

# FC Runtime development loop

Use `scripts/fc_runtime_dev.py` for candidate build/configuration and `scripts/daemon_compat.py` for released-vs-candidate binary comparison and live Daemon evidence. They keep contracts deterministic and keep credentials out of generated commands.

Read [references/workflow.md](references/workflow.md) and [references/common-cases.md](references/common-cases.md) before the first mutation. If any Daemon, task client, provider adapter, execenv, or `/api/daemon/*` code changed, also read [references/daemon-compatibility.md](references/daemon-compatibility.md).

## Scope

This Skill runs on the developer machine. It does not run inside the FC sandbox.

The FC configuration loop is:

1. A developer changes `dingtalk-ai-lab/multica-fc-hermes-runtime` on a branch.
2. Aone builds a candidate image and READY E2B Template, then starts a real sandbox smoke test.
3. The script extracts the immutable Template ID.
4. A Multica PAT creates a candidate Runtime or switches an existing candidate Runtime.
5. The script reads the Runtime back and verifies every cloud/backend/channel/provider/status/visibility/Template invariant.
6. A private canary Agent runs a real Multica task on the exact Runtime.

FC and local execution share `handleTask/runTask`, but their lifecycle differs. FC starts a short-lived `daemon run-once` inside a server-managed sandbox for one exact task; it does not register or heartbeat. A real device runs persistent `daemon start`, registers `runtime_mode=local` rows across every Workspace visible to its PAT, connects WS/HTTP heartbeats, batch-claims tasks, updates, and deregisters on stop. FC success never substitutes for a local-device smoke.

Stable rollout and ASB use different release contracts. Stop and route those requests to their dedicated operator workflow.

## Preconditions

Before triggering CI:

- Work in the authoritative internal Runtime repository: `dingtalk-ai-lab/multica-fc-hermes-runtime`.
- Read that repository's `AGENTS.md` and `CLAUDE.md`.
- Put the change on a dedicated branch, commit it, and push the exact commit. Do not trigger a build for dirty or unpushed work because Aone can only build the remote commit.
- Resolve both repositories to immutable 40-character commits. The helper rejects a branch/tag as `--multica-ref` and requires `--runtime-commit`; the pipeline must emit both commits independently.
- Use a branch-specific FC candidate pipeline only. Never alter or manually repurpose the formal `master` pipeline. Pipeline `295064` is the dedicated binding for `codex/fc-runtime-dev-loop-20260903`; for another long-lived branch, add its isolated candidate YAML as required by the Runtime repository and pass both `--pipeline-id` and `--pipeline-path` (or the matching `FC_RUNTIME_CANDIDATE_PIPELINE_*` variables). Aone creates the pipeline and triggers its first run when that YAML is pushed.

For the Multica API, prefer an isolated local profile such as `pre-fde`. Candidate create requires member + publisher; switch requires owner/admin + publisher. A read-only doctor proves authentication/catalog/publisher signals, not final mutation authority—the endpoint remains authoritative.

## Token handling

Never pass a Token as a command-line flag and never print a profile file.

The script resolves endpoint/workspace in this order:

1. explicit `--server-url` / `--workspace-id`;
2. the selected profile;
3. ambient variables only when the profile has no value.

Conflicting profile and ambient endpoint/workspace values fail closed. `MULTICA_TOKEN` may supply a secret without putting it on the command line. Remote HTTP, URL userinfo/path/query/fragment, and every redirect are rejected so a Bearer token cannot cross origins.

For a fresh Token, use an interactive prompt so it does not enter shell history:

```bash
read -r -s MULTICA_TOKEN
export MULTICA_TOKEN
```

Or refresh the isolated CLI profile:

```bash
multica --profile pre-fde login
```

## Execution

Set the helper path once without changing common system variables:

```bash
FC_RUNTIME_DEV=.agents/skills/fc-runtime-dev-loop/scripts/fc_runtime_dev.py
```

When the Runtime ref is not the verified bootstrap branch, bind the branch's own candidate pipeline before running the examples:

```bash
FC_RUNTIME_CANDIDATE_PIPELINE_ID=<dedicated-pipeline-id>
FC_RUNTIME_CANDIDATE_PIPELINE_PATH=.aoneci/<branch-candidate-yaml>
export FC_RUNTIME_CANDIDATE_PIPELINE_ID FC_RUNTIME_CANDIDATE_PIPELINE_PATH
```

Start with the read-only doctor. It validates the Aone candidate pipeline, Multica authentication, publisher permission, and Template catalog access:

```bash
python3 "$FC_RUNTIME_DEV" doctor --profile pre-fde
```

Preview the exact build and cutover plan without triggering CI or changing a Runtime:

```bash
python3 "$FC_RUNTIME_DEV" cutover \
  --runtime-ref codex/my-runtime-change \
  --runtime-commit <40-char-runtime-commit> \
  --multica-ref <40-char-dt-fde-multica-commit> \
  --runtime-id <candidate-runtime-uuid> \
  --profile pre-fde \
  --dry-run
```

Run the full existing-Runtime loop:

```bash
python3 "$FC_RUNTIME_DEV" cutover \
  --runtime-ref codex/my-runtime-change \
  --runtime-commit <40-char-runtime-commit> \
  --multica-ref <40-char-dt-fde-multica-commit> \
  --runtime-id <candidate-runtime-uuid> \
  --profile pre-fde
```

Create an isolated private candidate Runtime instead of switching one:

```bash
python3 "$FC_RUNTIME_DEV" cutover \
  --runtime-ref codex/my-runtime-change \
  --runtime-commit <40-char-runtime-commit> \
  --multica-ref <40-char-dt-fde-multica-commit> \
  --create-name "FC candidate - my-runtime-change" \
  --provider hermes \
  --visibility private \
  --profile pre-fde
```

Use the narrower commands when retrying one stage:

```bash
python3 "$FC_RUNTIME_DEV" build --runtime-ref <branch> --runtime-commit <commit> --multica-ref <commit>
python3 "$FC_RUNTIME_DEV" inspect-build --run-id <aone-run-id> --runtime-ref <branch> --runtime-commit <commit> --multica-ref <commit> --wait
python3 "$FC_RUNTIME_DEV" switch --template-id <template-id> --runtime-id <uuid> --profile pre-fde
python3 "$FC_RUNTIME_DEV" create --template-id <template-id> --name <name> --provider hermes --profile pre-fde
python3 "$FC_RUNTIME_DEV" create --template-id <template-id> --name <same-name> --provider hermes --profile pre-fde --reconcile-only
```

Use a unique create name as the operation key. The initial call reuses one exact matching Runtime and reconciles a lost response when the row is already visible. If the response is lost and no row is visible, normal retry is forbidden: rerun with `--reconcile-only`, which never POSTs, until the row appears or the server provides authoritative proof that the original request did not commit. Ambiguous or mismatched rows always stop.

## Daemon compatibility path

Compare the installed release and candidate before starting a test device:

```bash
DAEMON_COMPAT=.agents/skills/fc-runtime-dev-loop/scripts/daemon_compat.py
python3 "$DAEMON_COMPAT" compare \
  --baseline-bin "$(command -v multica)" \
  --candidate-bin /absolute/path/to/candidate-multica
```

For changes that touch persistent lifecycle or server daemon protocol, follow the live device ledger in `references/daemon-compatibility.md`: unique daemon ID, dedicated/explicit PAT scope, restricted provider PATH, registration + WS heartbeat read-back, exact task marker, graceful stop, strict cleanup. Do not assume a named profile limits registration to `profile.workspace_id`; it does not.

## Completion standard

Report the sanitized JSON result, including:

- Aone run ID and URL
- Runtime branch and exact Runtime commit
- exact Multica/Daemon commit emitted by CI
- E2B Template ID, display alias, and provider fingerprint
- mutation (`created`, `reused`, `reconciled_after_transport_error`, or `switched`) and previous Template for a switch
- Multica Runtime ID and read-back Template ID
- FC task canary ID/runtime/marker when FC orchestration is in scope
- persistent local Daemon surface/live/task/stop evidence when Daemon lifecycle is in scope

The helper deliberately returns `runtime_configured=true, complete=false` after create/switch. Do not report the whole workflow complete until the real task canary passes. Use the vocabulary in `common-cases.md` to report exactly which gates passed.

## Failure handling

- `401 invalid token`: refresh the selected Multica profile or export a new PAT, then rerun `doctor`.
- `can_publish=false` or `403`: the PAT user is not in the candidate Runtime publisher allowlist. Do not try another endpoint or stable release path.
- CI failure: retain the run URL and inspect the failed step. Do not call the Multica mutation API.
- Pipeline ID/path mismatch: identify the branch's dedicated candidate pipeline and pass both overrides. Do not fall back to the formal `master` pipeline.
- Template/provider mismatch: choose a provider declared by that Template; do not rewrite metadata.
- target Runtime is stable-managed: create a new candidate Runtime. Never convert the stable row in place.
- API mutation failure after a successful build: reuse `inspect-build` with the expected branch and both commits, then run `switch` or `create`; rebuilding is unnecessary.
- FC task canary failure after switch: restore `previous_template_id` and read it back before reporting rollback.
- Local Daemon surface regression: keep functional and compatibility results separate. A task completing does not approve a removed provider or CLI flag.
- Local smoke cleanup 409: stop. Never use cascade on an identity that drifted or has a non-test Agent binding.
