# Autopilots source map

- `server/cmd/multica/cmd_autopilot.go` registers `list`, `get`, `create`, `update`, `delete`, `trigger`, `runs`, `trigger-add`, `trigger-update`, `trigger-delete`, and `trigger-rotate-url`.
- The CLI maps reads/writes to `/api/autopilots`, `/api/autopilots/{id}`, `/api/autopilots/{id}/trigger`, `/api/autopilots/{id}/runs`, and trigger subroutes.
- `server/internal/service/autopilot.go` has `DispatchAutopilot`, synchronous delivery-idempotent `AdmitAutopilotWebhookDelivery`, and worker-side `DispatchAutopilotForWebhookDelivery`; it creates `autopilot_run` and switches on `execution_mode`.
- `create_issue` calls `dispatchCreateIssue`; `run_only` calls `dispatchRunOnly`.
- Issue-title templates: `SupportedIssueTitleTemplateVariables` (`date`, `date_yesterday`) and `ValidateIssueTitleTemplate` in `server/internal/service/autopilot.go` are the allowlist create/update enforce; `interpolateTemplate` substitutes them from `autopilotRunLocalDay`, which projects the run's trigger instant into the schedule trigger's timezone (`resolveAutopilotTriggerTimezone`, defaulting to UTC when the run has no schedule trigger — manual runs pass an invalid trigger id). `{{date_yesterday}}` steps back one calendar day with `AddDate`, so it stays correct across DST.
- `resolveAutopilotLeader` resolves squad-assigned autopilots to the squad leader.
- `AgentReadiness` blocks archived/runtime-unready agents before enqueue.
- `server/cmd/server/router.go` exposes authenticated `/api/autopilots` routes and unauthenticated webhook ingress `/api/webhooks/autopilots/{token}`.
- `server/internal/handler/autopilot_webhook.go` durably stores public webhook deliveries, synchronously admits an idempotent run for the compatible `200 accepted|skipped` + `run_id` response, and wakes the worker. It verifies the signature before event identity, answers a reused event id with a different effective payload `409 conflict` (the id stays held by the first authenticated delivery even after it fails; checked under an advisory lock before insert), and builds the admitted envelope from the delivery row.
- `server/internal/handler/employee_webhook_origin.go` holds the endpoint binding (read from PostgreSQL, frozen as `webhook_delivery.source_binding`), the effective payload digest (`source_digest`), the signing secret revision and the frozen-source loader the worker uses for crash-window retries (`source_digest_mismatch`, `binding_changed`); the worker also refuses an admitted, not yet dispatched run whose target changed (`binding_changed`, `scene_unusable`) by settling it skipped.
- `server/internal/handler/webhook_delivery_worker.go` claims queued deliveries with expiring database leases, applies per-trigger dispatch pacing, and resumes admitted runs using `autopilot_run.webhook_delivery_id` so recovery cannot duplicate a run/task.
- `GET /api/autopilots/cron-preview?expr=&tz=` (`server/internal/handler/autopilot_cron_preview.go`) returns `{next_runs}` — the next 3 occurrences as RFC3339 UTC — or 400 with the parser/timezone error. Compute-only (no autopilot is touched), gated by workspace membership; schedule editors use it instead of approximating the next run client-side.
- Write/execute authorization lives in `autopilotWriteByOwnership` / `memberCanWriteAutopilot` / `requireAutopilotWrite` (`server/internal/handler/autopilot.go`): editing, deleting, triggering, replaying deliveries, and managing triggers/webhook secrets require the autopilot's creator, a workspace owner/admin, or an explicit collaborator. Reads (list/get/runs/deliveries) stay open to any workspace member, but `GetAutopilot` redacts `webhook_token`/`webhook_path`/`webhook_url` for callers who lack write access, since the token alone can trigger the autopilot. Creating a new autopilot is still open to any member (they become its creator). This is the autopilot-level View/Write layer; it is independent of, and ANDed with, the private-assignee-agent gate enforced at dispatch time in `shouldSkipDispatch`.
- Explicit write grants ("collaborators") are stored in the `autopilot_collaborator` table (migration 128, members-only, no FK — deleted alongside the autopilot in the delete transaction). Endpoints: `POST /api/autopilots/{id}/collaborators` (body `{user_id}`) and `DELETE /api/autopilots/{id}/collaborators/{userId}`, both gated by the NARROWER `requireAutopilotAccessManagement` (creator or workspace owner/admin only — a granted collaborator keeps write/execute but cannot re-grant or revoke peers, preventing privilege escalation), both returning the updated `{collaborators}` list. `GetAutopilot` embeds the `collaborators` array and stamps two per-caller booleans: `can_write` (gates edit/run/trigger controls) and the narrower `can_manage_access` (gates the "Manage access" entry). The web/desktop "Manage access" UI lives in `packages/views/autopilots/components/manage-access-dialog.tsx`.

## Event-trigger implementation

- `server/internal/service/event_trigger.go`: `SetEnabled`, `Admit`, `ProcessNext`,
  and `SyncRoutes` own configuration, durable inbox, batching and Router policy.
- `server/internal/handler/agent_event_trigger.go`: event adapter, batch inspection,
  and authorized retry; `agent.go` exposes `event_trigger_enabled`.
- `server/internal/handler/autopilot.go`: `requireAutopilotWrite` prevents direct
  mutation/execution of Agent-managed event automations.
- `server/cmd/multica/cmd_agent.go`: `event-trigger-enabled` update flag is Changed-gated.
- `packages/views/agents/components/agent-message-settings.tsx`: Agent event-trigger
  toggle rendered by `tabs/digital-employee-tab.tsx`; identity binding controls are unchanged.
- Read-only verification: `multica agent get <id> --output json`,
  `multica autopilot runs <id> --output json`, and `GET /api/agents/{id}/event-batches`.

## Agent handling-mode configuration

- `server/migrations/9630_agent_coordination_mode.up.sql`: default Coordinator;
  the migration does not enable Agents.
- `server/internal/employeeloopconfig/config.go`: scoped `Load`, row-locked
  `ResolveUpdate`, and same-transaction mode persistence. Omitted mode preserves
  the current owner; the enabled switch stays independent.
- `server/internal/handler/agent.go`, `agent_coordination.go`: existing manage
  authorization, list/detail hydration, `coordination_mode` update, and the
  `EmployeeLoopReady(workspaceID, agentID)` Host callback. Missing/failed readiness
  rejects Employee selection or enable with 409. Mode, response policy and proactive
  settings share one Agent-row transaction.
- `packages/core/api/schemas.ts`: old responses default to Coordinator; unknown
  modes disable ambiguous writes. `agent-message-settings.tsx` and
  `agent-detail-page.tsx` display server-confirmed mode via React Query.
- Verification: `server/internal/handler/agent_coordination_test.go`,
  `packages/core/api/agent-response-schema.test.ts`, and the shared Agent settings
  and detail-page tests. These are configuration/compatibility checks, not full
  EmployeeLoop Runtime or production-scene acceptance.

## Proactive conversation admission

- `server/internal/service/event_trigger.go`: toggle dependency and legacy-only draining.
- `server/internal/handler/agent_event_trigger.go`, `proactive_conversation.go`: Coordinator-owned observed messages enter Coordinator; durable dedup covers the former inbox.
- `server/internal/handler/inbound_coordinator_job.go`: single collection window and persisted decisions.
- `server/internal/service/coordinator_follow_up.go`: busy Issue additions, identity-isolated batching, and actual comment delivery receipts.
- Read-only verification: `GET /api/agents/{id}` and the Agent Coordinator conversations; historical Autopilot runs do not describe new proactive messages.


## DingTalk message automation trigger

- `server/internal/service/autopilot_messages.go`: source-bound metadata admission,
  fixed windows, revision invalidation, counts, and idempotent dispatch/recovery.
- `server/internal/handler/agent_dispatch_message_statistics.go`: authenticated
  metadata adapter, self-message exclusion and retired hourly-summary receipts.
- `server/internal/handler/autopilot.go`, `autopilot_messages.go`: trigger CRUD,
  active binding validation, merge interval and configuration revision changes.
- `server/migrations/9218_dingtalk_message_autopilot.up.sql` through
  `9221_message_event_window.up.sql`: durable windows, dedup and indexes.
- `server/cmd/server/main.go`: message window worker startup.
- `server/cmd/multica/cmd_autopilot.go`: `dingtalk_message` trigger kind and
  `--merge-interval-minutes` create/update flag.
- `packages/views/autopilots/components/message-trigger-section.tsx`: shared
  binding eligibility, interval editor and actual run input example.
- `autopilot-dialog.tsx`, `autopilot-detail-page.tsx` in that directory: trigger
  creation/editing and saved runtime statistics. No proactive setting changes.
- `server/internal/handler/scene_routines.go`, `scene_routines_http.go`,
  `server/internal/contextcap/routine.go`: scene routines (例行任务) — a
  `run_only` autopilot bound to a group or 1:1 chat scene, its run context,
  start and end notices; `requireAutopilotWrite` answers 409
  `managed_by_scene` for them.

## Frozen webhook source release safety

- `server/internal/handler/autopilot_webhook.go`, `employee_webhook_origin.go`: every authenticated source requires the `[webhook-source:1]` live-reader gate, freezes the Host binding and persists the isolated queue.
- `server/migrations/9997_webhook_frozen_queue.up.sql`: exact v1 binding guard also isolates older producers' queued INSERT/binding UPDATE; rollback refuses pending frozen sources. `9998` builds the concurrent queue index and `9999` validates the widened status constraint.
- `server/pkg/db/queries/webhook_delivery.sql`, `handler/webhook_delivery.go`: compatible claim/lease mutations retain isolated status; public status remains queued. `deploymentfence/fence.go` counts frozen leases for draining.
- `docs/webhook-source-release-safety.md`, `handler/webhook_source_release_test.go`: rolling old/new producer/reader evidence, pre-migration leased-work limits and safe rollback.

- Scene-only one-shot `autopilot_trigger.kind=once,run_at` is created through `handler/scene_routines.go` / `scene_config_mcp.go`, not the generic CLI. `scheduler/jobs_autopilot.go` plans its exact instant; `service/employee_routine_task.go` consumes it transactionally with the occurrence and fresh task. Migration 10060 adds the time/shape constraint; 10061–10062 preserve immutable source context.

## Employee Webhook result delivery

- `handler/employee_routine_origin.go::EnqueueRoutineStartNoticeTx`: Employee webhook admission skips a start announcement.
- `service/employee_webhook_task.go::compileWebhookRoutinePacket` and `handler/employee_run_claim.go::employeeRoutineOutputInstruction`: frozen work packet and claim-time business-result guidance; Host owns delivery.
- `handler/scene_routines.go::enqueueRoutineEndNotice`, `employeeWebhookResultText`: frozen Employee automation origin selects a result-only reply; failure/cancel/empty output retain explicit explanation. Routine outbox IDs and terminal savepoints stay unchanged.
- `handler/employee_webhook_task_test.go`, `employee_webhook_notice_test.go`: PG admission/claim/completion/replay and result-format boundaries; `office-hook-result-only-delivery` / G16 defines real IM acceptance, not claimed by local tests.
