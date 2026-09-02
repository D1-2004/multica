---
name: fc-runtime-dev-loop
description: Build a development FC/E2B Runtime image from a multica-fc-hermes-runtime branch and create or switch a Multica candidate Runtime through Token + HTTP API, with Aone CI waiting and read-back verification. Use this skill whenever a developer asks to build, publish, test, select, create, update, or switch a non-stable/candidate FC sandbox Runtime, mentions a Runtime image branch or E2B template, or wants to close the loop from a Runtime code change to a usable Multica sandbox. Do not use it for stable-channel rollout, production release, ASB images, or ordinary application deployment.
compatibility: Requires git, Python 3.11+, a1 CLI authenticated for Code/Aone CI, and a Multica personal access token with candidate Runtime publisher permission.
---

# FC Runtime development loop

Use the bundled `scripts/fc_runtime_dev.py` as the execution engine. It keeps the CI and HTTP contracts deterministic and prevents credentials from appearing in generated shell commands.

Read [references/workflow.md](references/workflow.md) before the first mutation in a session. It defines the two repositories, pipeline identity, API contracts, evidence, and recovery rules.

## Scope

This Skill runs on the developer machine. It does not run inside the FC sandbox.

Use it for this loop:

1. A developer changes `dingtalk-ai-lab/multica-fc-hermes-runtime` on a branch.
2. Aone builds a candidate image and READY E2B Template, then starts a real sandbox smoke test.
3. The script extracts the immutable Template ID.
4. A Multica PAT creates a candidate Runtime or switches an existing candidate Runtime.
5. The script reads the Runtime back and verifies the selected Template ID.

Stable rollout and ASB use different release contracts. Stop and route those requests to their dedicated operator workflow.

## Preconditions

Before triggering CI:

- Work in the authoritative internal Runtime repository: `dingtalk-ai-lab/multica-fc-hermes-runtime`.
- Read that repository's `AGENTS.md` and `CLAUDE.md`.
- Put the change on a dedicated branch, commit it, and push the exact commit. Do not trigger a build for dirty or unpushed work because Aone can only build the remote commit.
- Resolve the Multica source to an immutable 40-character commit when the Runtime change depends on unpublished `dt-fde-multica` code. A branch name is allowed for exploration, but report the commit returned by CI as the build identity.
- Use a branch-specific FC candidate pipeline only. Never alter or manually repurpose the formal `master` pipeline. Pipeline `295064` is the dedicated binding for `codex/fc-runtime-dev-loop-20260903`; for another long-lived branch, add its isolated candidate YAML as required by the Runtime repository and pass both `--pipeline-id` and `--pipeline-path` (or the matching `FC_RUNTIME_CANDIDATE_PIPELINE_*` variables). Aone creates the pipeline and triggers its first run when that YAML is pushed.

For the Multica API, prefer an isolated local profile such as `pre-fde`. The PAT owner must be a workspace admin/owner and a configured stable Runtime publisher; candidate create/switch is intentionally restricted to that allowlist.

## Token handling

Never pass a Token as a command-line flag and never print a profile file.

The script resolves credentials in this order:

1. `MULTICA_TOKEN`, `MULTICA_SERVER_URL`, `MULTICA_WORKSPACE_ID` environment variables.
2. `--profile <name>` from `~/.multica/profiles/<name>/config.json`.
3. The default `~/.multica/config.json` when `--profile default` is used.

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
  --multica-ref <40-char-dt-fde-multica-commit> \
  --runtime-id <candidate-runtime-uuid> \
  --profile pre-fde \
  --dry-run
```

Run the full existing-Runtime loop:

```bash
python3 "$FC_RUNTIME_DEV" cutover \
  --runtime-ref codex/my-runtime-change \
  --multica-ref <40-char-dt-fde-multica-commit> \
  --runtime-id <candidate-runtime-uuid> \
  --profile pre-fde
```

Create an isolated private candidate Runtime instead of switching one:

```bash
python3 "$FC_RUNTIME_DEV" cutover \
  --runtime-ref codex/my-runtime-change \
  --multica-ref <40-char-dt-fde-multica-commit> \
  --create-name "FC candidate - my-runtime-change" \
  --provider hermes \
  --visibility private \
  --profile pre-fde
```

Use the narrower commands when retrying one stage:

```bash
python3 "$FC_RUNTIME_DEV" build --runtime-ref <branch> --multica-ref <commit>
python3 "$FC_RUNTIME_DEV" inspect-build --run-id <aone-run-id> --wait
python3 "$FC_RUNTIME_DEV" switch --template-id <template-id> --runtime-id <uuid> --profile pre-fde
python3 "$FC_RUNTIME_DEV" create --template-id <template-id> --name <name> --provider hermes --profile pre-fde
```

## Completion standard

Report the sanitized JSON result, including:

- Aone run ID and URL
- Runtime branch and exact Runtime commit
- exact Multica ref used by CI
- E2B Template ID, display alias, and provider fingerprint
- mutation (`created` or `switched`)
- Multica Runtime ID and read-back Template ID

Do not report completion when only the CI run was submitted, when the Template is not READY, or when the Runtime mutation response was not read back.

## Failure handling

- `401 invalid token`: refresh the selected Multica profile or export a new PAT, then rerun `doctor`.
- `can_publish=false` or `403`: the PAT user is not in the candidate Runtime publisher allowlist. Do not try another endpoint or stable release path.
- CI failure: retain the run URL and inspect the failed step. Do not call the Multica mutation API.
- Pipeline ID/path mismatch: identify the branch's dedicated candidate pipeline and pass both overrides. Do not fall back to the formal `master` pipeline.
- Template/provider mismatch: choose a provider declared by that Template; do not rewrite metadata.
- target Runtime is stable-managed: create a new candidate Runtime. Never convert the stable row in place.
- API mutation failure after a successful build: reuse `inspect-build --run-id ...` and run `switch` or `create`; rebuilding is unnecessary.
