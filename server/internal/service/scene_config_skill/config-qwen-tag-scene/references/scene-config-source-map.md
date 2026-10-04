# config-qwen-tag-scene source map

| Fact | Source |
| --- | --- |
| The MCP server is mounted only for a task with a current group or 1:1 scene, at `/api/scene-config/mcp/{sct_ token}` | `server/internal/handler/scene_config_mcp.go` (`sceneConfigMCPRoute`), `server/internal/handler/runner_mcp.go` (`injectRunnerMCP`) |
| The scene token binds task, agent, workspace and scene; every call also needs the same task's task token and re-resolves the scene | `server/internal/auth/scene_token.go`, `server/internal/handler/scene_config_mcp.go` (`sceneConfigMCPTarget`) |
| Tools take no scene argument | `server/internal/handler/scene_config_mcp.go` (tool definitions) |
| Routine runs are read-only | `server/internal/handler/scene_config_mcp.go` (`routine_run_read_only`), `server/internal/service/scene_routine.go` (`IsSceneRoutineContext`) |
| Routines: dedupe, pause kept, 15-minute floor, Asia/Shanghai default, start/end notices | `server/internal/handler/scene_routines.go` |
| A webhook routine's payload allowlist (`payload_fields`) is set only by managers; chat tool arguments drop it | `server/internal/handler/scene_config_mcp.go` (`sceneConfigRoutineCreate`, `sceneConfigRoutineUpdate`), `server/internal/handler/employee_webhook_origin.go` (`normalizeWebhookPayloadFields`, `selectWebhookPayload`) |
| A webhook routine's signing secret is set only by agent managers on the admin route, never from a chat (no MCP tool); views show only `has_signing_secret` | `server/internal/handler/scene_routines.go` (`setSceneRoutineWebhookSecret`), `server/internal/handler/scene_routines_http.go` (`SetAgentContextRoutineWebhookSecret`) |
| A 1:1 routine created on the configure page sends to the one counterpart named by the scene's Coordinator jobs and Employee messages (`dm_target_unknown`, `dm_target_ambiguous`) | `server/internal/handler/scene_routines.go` (`sceneRoutineDMCounterpart`, `employeeDMSenders`) |
| Remote MCP servers only; reserved names refused | `server/internal/contextcap/mcp_config.go` (`NormalizeRemoteMCPConfig`), `server/internal/handler/context_capabilities_task.go` (`reservedMCPServerName`) |
| Groups and 1:1 chats add, re-point, switch and delete remote servers; omitted fields keep stored values; URLs shown masked; notices name who asked and the address | `server/internal/handler/scene_config_mcp.go` (`sceneConfigMCPUpsert`, `maskMCPServerURL`, `sceneConfigNoticeText`) |
| Routine runs issue no configuration link | `server/internal/handler/scene_config_mcp.go` (`sceneConfigWriteTools`), `server/internal/handler/multica_mcp_context_config.go` (`createContextConfigLink`) |
| Chat runs keep the 15-minute minimum (`routine_run_too_soon`) | `server/internal/handler/scene_config_mcp.go` (`sceneConfigRoutineRun`) |
| `employee_execution` (`run_only` / `employee_decide`): stored on the routine, gated on every replica running the decision reader (`routine_decision_unavailable`), schedule routines only (`invalid_routine` on a webhook), frozen into each occurrence; employee_decide occurrences admit a `routine.decision` wake whose only actions are run_routine (the frozen instructions), reply in this scene, wait_for_next_occurrence and stay_quiet | `server/internal/handler/scene_routines.go` (`normalizeRoutineEmployeeExecution`, `routineDecisionAvailable`), `server/internal/contextcap/routine.go`, `server/internal/service/employee_routine_decision.go`, `server/internal/handler/employee_routine_decision.go` |
| A routine never carries a personal layer | `server/internal/contextcap/scope.go` (`routineScope`), `server/internal/handler/scene_routines.go` (`RoutineRuntimeContext`) |
| Only offered skills and connectors can be switched | `server/internal/contextcap/store.go` (`UpsertBinding`) |
| A group and a 1:1 chat get this scene's configuration link from the executor tools, keyed by its scene_id; only the Host-appended link of a 1:1 chat (Coordinator / Employee capability answer) also carries the chat's person, never an executor's tool result | `server/internal/handler/context_config_link_mint.go` (`mintContextConfigLink`) |
| Account connection uses the host-selected configuration link unchanged, replied as a Markdown link to its `dingtalk_url` | `server/internal/handler/context_config_link_mint.go`, `server/internal/service/inboundcoord/config_link.go` (`ConfigLinkDeepLink`), `server/internal/handler/scene_config_mcp.go` (`scene_connect_link` description), `docs/environment-forwarding.md` |
| The skill is injected at claim and resolved by the same check | `server/internal/handler/daemon.go` (claim and `ResolveTaskSkillBundles`), `server/internal/service/scene_config_skill.go` |
| An explicit, complete request in the run's starting message is the confirmation; adding a remote MCP server or changing its address still waits for confirmation | this skill (step 2), `server/internal/handler/scene_config_mcp.go` (`scene_mcp_server_upsert` description) |
| EmployeeLoop's foreground never changes a scene: it hands routine, prompt, switch and MCP server requests to a Direct task, which reaches this skill and server | `server/internal/handler/employee_scene_capabilities.go` (`employeeForegroundBoundary`, `employeeSceneSelfManagement`), `server/internal/handler/employee_scene_entry_host.go` (`dispatch_task` description) |

## One-shot schedules

- `scene_routine_create` adds `trigger.kind=once`, `run_at` (explicit RFC3339 offset), optional display `timezone`; `scene_routine_update.run_at` reschedules only pending resources.
- `handler/scene_routine_once.go` validates time and serializes mutations with admission; `handler/scene_config_mcp.go` captures trusted source through `service.CaptureRoutineSource`.
- `contextcap/routine_source.go`, `service/employee_routine_source.go`, migrations 10061–10062 retain immutable scoped provenance and selected material without runtime credentials.
- `scheduler/jobs_autopilot.go` plans the absolute once timestamp without cron lateness suppression. `service/employee_routine_task.go` atomically consumes it with occurrence/Task/Run/outbox; old/create replay cannot rearm.
- `handler/employee_routine_origin.go` rechecks source scope at use; the existing delivery outbox remains the sender. Views distinguish consumed admission from last-run outcome.

- `handler/employee_scene_capabilities.go` and `employee_scene_entry_host.go` share the one-shot foreground routing contract: current intent, original message time, and background creation instead of sleep.
- `service/employee_routine_task.go:compileRoutinePacket` distinguishes already-due execution from the historical creation work packet. `handler/employee_run_claim.go` supplies the current result consumer contract: routine plain result versus the current Human structured Direct format.
