---
name: multica-autopilots
description: "Use when creating, updating, inspecting, triggering, or debugging a Multica autopilot (scheduled, webhook, DingTalk message, or manual)."
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

The chain is: trigger fires (`schedule`, `webhook`, `dingtalk_message`, or `manual`) -> `autopilot_run` row -> `execution_mode` decides output -> assignee readiness check -> issue/task execution -> run status sync. Webhooks have a durable admission step in front: HTTP ingress stores a queued `webhook_delivery`, synchronously creates or reuses its idempotent run, and returns `200` with `status=accepted|skipped` plus `run_id`; a database-leased worker then resumes accepted runs and owns recoverable issue/task dispatch.

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

## Proactive conversations

`agent update <id> --event-trigger-enabled[=false]` controls the default-off
“Proactively process all new conversation messages” setting under Digital Employee,
immediately below inbound judging. Enabling it enables inbound judging atomically;
disabling inbound judging disables proactive processing. Existing bindings and
subscription scopes are unchanged. Router synchronization normally takes up to five
seconds plus request latency.

Observed group messages use the normal durable Coordinator window (4 seconds quiet,
12 seconds maximum collection, at most 100 messages). No Autopilot is created, and
there is no extra 30-second task interval or wait for the sandbox to finish before
judging new messages. Configure the employee's behavior through Agent instructions.
Unmentioned messages reach the same Coordinator to decide reply, silence or Issue work.
Authorized additions to a busy Issue are durably queued and combined for its next run.
Read decisions in Coordinator conversations and execution in the associated Issues.
Legacy event Autopilots are retained as history and only drain previously admitted work.

The task-finished follow-up setting still controls automatic completion reports.
Configuration and implementation map to `event_trigger.go`, `agent_event_trigger.go`,
`proactive_conversation.go`, `inbound_coordinator_job.go`, and `coordinator_follow_up.go`.


## DingTalk message automations

This configurable trigger is independent of proactive conversation processing.
Choose an Agent with an active digital employee message binding, then add:

```bash
multica autopilot trigger-add <autopilot-id> --kind dingtalk_message --merge-interval-minutes 5 --output json
multica autopilot trigger-update <autopilot-id> <trigger-id> --merge-interval-minutes 10 --output json
```

The interval is a whole number from 1 to 1440 minutes (default 5). The first
new message admitted after enabling starts a fixed window across the bound
account's subscribed conversations. Later messages do not extend it. Empty
periods produce no run. Own outgoing messages are excluded. Changing interval,
executor, execution mode or enabled state invalidates pending windows; pausing
or removing the binding cancels undispatched work. Already started runs continue.

Each run receives `dingtalk.messages.received` statistics automatically alongside
its instructions: `window_id`, `window_start`, `window_end`,
`merge_interval_minutes`, total `message_count`, `mention_count`,
`conversation_count`, and `conversations` with IDs, names, types, counts and
first/last message timestamps. Bodies are not included; use the bound DWS identity
and conversation IDs to read messages when the runbook requires their content.
The run detail exposes the same statistics in `trigger_payload`. No prompt
placeholder is required. Both `create_issue` and `run_only` are supported.
The former hourly conversation-summary task creator is retired.
