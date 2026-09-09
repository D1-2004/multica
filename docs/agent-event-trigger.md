# Agent event trigger

Implemented in existing group-awareness CR 36022159, paired with Router CR 35999116.

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

## Deployment

Apply only the new migrations 9200 through 9210 in a separate migration phase before
starting the new application. Do not add migration execution to application startup.
Router uses its own additive 026/027 SQL migration and environment-scoped policies.
Deploy Router first, then Multica. All Agents remain disabled unless explicitly enabled.

Rollback: disable the Agent toggle, wait for Router acknowledgement in
`agent_event_route`, and drain active tasks before reverting application builds.
Keep additive tables to preserve pending events and retry history.

## Validation

`MULTICA_EVENT_TEST_DATABASE_URL` must point to an isolated, migrated PostgreSQL
instance for `go test -race ./internal/service -run '^TestEventTrigger'`.
These tests exercise real SQL deadlines, deduplication, concurrent workers, restart
reconciliation, success-only consumption, failure/retry, and batch overflow.
Handler tests use `DATABASE_URL` and exercise config round trips, malformed input,
self-message protection, and default-off admission. Frontend schema/Digital Employee tests
cover old-server defaults and toggling without any binding mutation.
