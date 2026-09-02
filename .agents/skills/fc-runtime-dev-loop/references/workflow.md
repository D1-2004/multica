# FC Runtime candidate workflow contract

## Authorities

| Concern | Authority |
| --- | --- |
| Runtime source | `dingtalk-ai-lab/multica-fc-hermes-runtime` on internal Code |
| Dedicated candidate build | Aone CI pipeline `295064` |
| Dedicated pipeline YAML | `.aoneci/runtime-fc-runtime-dev-loop-candidate.yaml` |
| Multica API | selected development/pre-release Multica server |
| Runtime identity | E2B Template ID, not display alias |

The verified pipeline ID is an environment binding, not source truth. It belongs to the Runtime branch `codex/fc-runtime-dev-loop-20260903`. `doctor` checks that the ID still points to the expected repository and candidate YAML before any build.

For another long-lived Runtime branch, follow that repository's rule: add a separate candidate YAML that listens only to the branch and push it. Aone automatically creates the pipeline and starts its first `PUSH` run. Then pass the resulting `--pipeline-id` and `--pipeline-path`. The same values can be supplied as `FC_RUNTIME_CANDIDATE_PIPELINE_ID` and `FC_RUNTIME_CANDIDATE_PIPELINE_PATH`. Keeping ID and path as a checked pair prevents an agent from silently repurposing the formal `master` pipeline or somebody else's branch pipeline.

## Build state machine

```text
submitted -> RUNNING/WAITING/PENDING -> SUCCESS
                                  \-> FAILED/CANCELED/SKIPPED
```

Only `SUCCESS` advances. The build step must yield all of:

```text
template_id: <immutable E2B template ID>
runtime_commit: <40 lowercase hex>
provider_fingerprint: <16 lowercase hex>
display_alias: <human-readable alias>
```

The script additionally verifies `runtime_commit == Aone run.commit`. This prevents a stale log or a build from another revision from being cut over.

## Aone calls

For the dedicated development-loop pipeline, the deterministic sequence is:

```bash
a1 ci pipeline get 295064 --repo dingtalk-ai-lab/multica-fc-hermes-runtime -f json
a1 ci pipeline run 295064 --repo dingtalk-ai-lab/multica-fc-hermes-runtime --branch codex/fc-runtime-dev-loop-20260903 --param multica_ref=<ref> -f json
a1 ci run get <run-id> --repo dingtalk-ai-lab/multica-fc-hermes-runtime -f json
a1 ci run log <run-id> --repo dingtalk-ai-lab/multica-fc-hermes-runtime --job build-publish-and-verify --step build-and-verify-e2b-template -f json
```

The script invokes each `a1` command as a separate subprocess and parses JSON. It never scrapes terminal tables.

## Multica HTTP calls

Every call carries:

```text
Authorization: Bearer <PAT>
X-Workspace-ID: <workspace UUID>
Content-Type: application/json     # mutation only
```

The Token is loaded from the process environment or a local CLI profile. It is never an argument.

Preflight:

```text
GET /api/runtimes/fc-e2b/stable-channel
GET /api/runtimes/fc-e2b/templates
GET /api/runtimes
```

`stable-channel.can_publish` must be true. The name reflects the shared publisher permission; this workflow does not modify the stable channel.

Switch an existing candidate Runtime:

```http
PATCH /api/runtimes/{runtime_id}/fc-e2b-template
{"template_id":"<template-id>"}
```

Create a private candidate Runtime:

```http
POST /api/runtimes/fc-e2b
{
  "name": "<name>",
  "template_id": "<template-id>",
  "template_channel": "candidate",
  "provider": "hermes",
  "visibility": "private"
}
```

The API rejects a candidate Template for a provider it does not declare. It also rejects switching a stable-managed Runtime; that separation protects stable rollout ownership.

## Read-back evidence

After mutation, fetch `GET /api/runtimes` again and locate the Runtime UUID. Completion requires:

```text
runtime.runtime_mode == cloud
runtime.provider == requested provider
runtime.metadata.sandbox_backend == aliyun_fc
runtime.metadata.template_channel == candidate
runtime.metadata.template_id == built template_id
```

The switch endpoint invalidates warm sandboxes for that Runtime. A later task creates a new sandbox from the selected Template; the workflow does not claim that an Agent task has run unless a separate task smoke is executed.

## Recovery matrix

| Failure | Safe recovery |
| --- | --- |
| unpushed Runtime change | commit and push the branch, then trigger build |
| CI failed before Template | fix branch and build new commit |
| CI succeeded, API failed | reuse run ID; inspect-build then switch/create |
| PAT invalid | refresh profile; no rebuild |
| publisher permission missing | update server allowlist through the normal config/deploy path; no bypass |
| existing Runtime is stable | create a separate candidate Runtime |
| wrong provider | use a provider declared in Template metadata or rebuild the image contract |
