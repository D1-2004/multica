# Autopilots source map

- `server/cmd/multica/cmd_autopilot.go` registers `list`, `get`, `create`, `update`, `delete`, `trigger`, `runs`, `trigger-add`, `trigger-update`, `trigger-delete`, and `trigger-rotate-url`.
- The CLI maps reads/writes to `/api/autopilots`, `/api/autopilots/{id}`, `/api/autopilots/{id}/trigger`, `/api/autopilots/{id}/runs`, and trigger subroutes.
- `server/internal/service/autopilot.go` has `DispatchAutopilot`, synchronous delivery-idempotent `AdmitAutopilotWebhookDelivery`, and worker-side `DispatchAutopilotForWebhookDelivery`; it creates `autopilot_run` and switches on `execution_mode`.
- `create_issue` calls `dispatchCreateIssue`; `run_only` calls `dispatchRunOnly`.
- Issue-title templates: `SupportedIssueTitleTemplateVariables` (`date`, `date_yesterday`) and `ValidateIssueTitleTemplate` in `server/internal/service/autopilot.go` are the allowlist create/update enforce; `interpolateTemplate` substitutes them from `autopilotRunLocalDay`, which projects the run's trigger instant into the schedule trigger's timezone (`resolveAutopilotTriggerTimezone`, defaulting to UTC when the run has no schedule trigger — manual runs pass an invalid trigger id). `{{date_yesterday}}` steps back one calendar day with `AddDate`, so it stays correct across DST.
- `resolveAutopilotLeader` resolves squad-assigned autopilots to the squad leader.
- `AgentReadiness` blocks archived/runtime-unready agents before enqueue.
- `server/cmd/server/router.go` exposes authenticated `/api/autopilots` routes and unauthenticated webhook ingress `/api/webhooks/autopilots/{token}`.
- `server/internal/handler/autopilot_webhook.go` durably stores public webhook deliveries, synchronously admits an idempotent run for the compatible `200 accepted|skipped` + `run_id` response, and wakes the worker.
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
