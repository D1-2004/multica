---
name: multica-autopilots
description: "Use when creating, updating, inspecting, triggering, or debugging a Multica autopilot (scheduled, webhook, or manual)."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Multica Autopilots

## Quick start

Autopilots are durable automations. Read before mutating:

```bash
multica autopilot list --output json
multica autopilot get <autopilot-id> --output json
multica autopilot runs <autopilot-id> --output json
```

Do not run `trigger`, `delete`, `trigger-delete`, or `trigger-rotate-url` to test. Those are real side effects.

## Core model

An autopilot is not an agent. It is a rule that dispatches work to an agent, or to a squad's leader agent.

The chain is: trigger fires (`schedule`, `webhook`, or `manual`) -> `autopilot_run` row -> `execution_mode` decides output -> assignee readiness check -> issue/task execution -> run status sync. Webhooks have a durable admission step in front: HTTP ingress stores a queued `webhook_delivery`, synchronously creates or reuses its idempotent run, and returns `200` with `status=accepted|skipped` plus `run_id`; a database-leased worker then resumes accepted runs and owns recoverable issue/task dispatch.

Execution modes:

- `create_issue` creates a Multica issue, making the run visible as issue state.
- `run_only` creates an agent task directly. No issue is created; any durable
  report location has to come from other task context or instructions.

`issue-title-template` supports exactly two variables: `{{date}}` and `{{date_yesterday}}`. Do not invent `{{trigger_id}}`, `{{branch}}`, or other variables. Both render `YYYY-MM-DD` in the triggering schedule's timezone (runs with no schedule trigger — manual and webhook — render UTC). `{{date}}` is the day the run itself fires, not the day it reports on: an autopilot that fires after midnight to summarize the previous day must title itself with `{{date_yesterday}}`.

## CLI

```bash
multica autopilot list --output json
multica autopilot get <autopilot-id> --output json
multica autopilot create --title "<title>" --description "<task prompt>" --agent <agent-name-or-id> --mode create_issue|run_only --output json
multica autopilot update <autopilot-id> --status active|paused --output json
multica autopilot runs <autopilot-id> --output json
multica autopilot trigger-add <autopilot-id> --kind schedule --cron "0 9 * * *" --timezone Asia/Shanghai --output json
multica autopilot trigger-add <autopilot-id> --kind webhook --label "ci" --output json
multica autopilot trigger <autopilot-id> --output json
multica autopilot trigger-rotate-url <autopilot-id> <trigger-id> --yes --output json
```

Use `trigger` only when the user explicitly asks for a manual run. Use `trigger-rotate-url` only when rotating a webhook URL; the old URL stops being valid.

Webhook trigger output can include a URL/token. Do not paste webhook tokens or signing material into comments, logs, docs, or PRs. Redact secrets.

## Debugging

For "why didn't it run":

1. `multica autopilot get <id> --output json` — status, mode, assignee, triggers.
2. `multica autopilot runs <id> --output json` — run status and failure reason.
3. If assigned to a squad, inspect the squad: `multica squad get <squad-id> --output json`; execution goes to the leader.
4. Inspect the target agent/runtime: `multica agent get <agent-id> --output json` and `multica runtime list --output json`.
5. For webhooks, inspect delivery status: `queued` means the worker has not completed dispatch; `failed` carries the worker error. A provider retry with the same `X-GitHub-Delivery` / `Idempotency-Key` reuses the original delivery.
6. For `create_issue`, inspect the created issue if the run records one.

## Side effects

These mutate durable state or start work: `create`, `update`, `delete`, trigger add/update/delete/rotate, `trigger`, and webhook calls to `/api/webhooks/autopilots/{token}`.

More source-backed details: `references/autopilots-source-map.md`.

## Agent event triggers

`agent update <id> --event-trigger-enabled[=false]` changes the default-off
Agent event-trigger setting, also available under Digital Employee → Conversation
& follow-up in Agent configuration. It does not change digital-employee bindings or
subscription scope. The first adapter is observed DingTalk group messages.
Changes synchronize to Router within about five seconds when Router is available;
failed syncs retry automatically. Message receipt is not task completion.

The deterministic scheduler collects for 4 seconds of quiet, at most 12 seconds
from the first pending event, and starts tasks at least 30 seconds apart for the
same source/conversation. These are dispatch eligibility times, not a model reply
SLA. A busy conversation keeps its next batch pending until the current task
finishes. Each batch contains at most 100 events; overflow remains pending.

Execution reuses a platform-managed `run_only` Autopilot. Its runs remain readable
through `autopilot runs`, but direct editing, deletion, manual triggering or adding
schedule/webhook triggers is rejected. Configure behavior through Agent instructions
and skills; configure admission through the Agent toggle. No coordinator, issue,
VIP classifier, or business-specific alert rule is added by this mechanism.

The inbox persists admitted content and message IDs before acknowledging delivery.
Duplicates do not move deadlines. Only successful tasks consume their frozen batch.
Failed tasks retain the same batch/run and retry up to three scheduler attempts;
cancellation or exhausted attempts pauses that conversation, retaining its events.
Operators can inspect `GET /api/agents/{id}/event-batches` and explicitly retry with
`POST /api/agents/{id}/event-batches/{batchId}/retry` after fixing the cause and enabling
the Agent setting. Workspace and Agent management permissions apply.

Disabling stops new admission/dispatch, preserves pending events, and allows an
already-started task to finish. Re-enabling resumes pending work. Consumed inbox
payloads are cleared after seven days; deduplication IDs and normal Autopilot/task
execution history remain. Pending/failed content remains until handled or workspace
deletion. This is not a general chat-history archive.
