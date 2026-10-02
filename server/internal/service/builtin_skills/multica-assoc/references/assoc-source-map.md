# assoc — source map

Every claim in `SKILL.md` traces to a line below. Re-derive against the current
tree before trusting any line number.

## MCP tools and HTTP

| Fact | Source |
| --- | --- |
| MCP `assoc_recall` | `server/internal/handler/multica_mcp.go` `multicaMCPAssocRecallTool` |
| MCP `assoc_bind` | `server/internal/handler/multica_mcp.go` `multicaMCPAssocBindTool` |
| HTTP recall | `GET /api/assoc/recall` `server/cmd/server/router.go` |
| HTTP events | `GET /api/assoc/events` `server/cmd/server/router.go` |
| HTTP bind | `POST /api/assoc/bind-outbound` task token |
| Bind requires task token | `server/internal/handler/assoc.go` `BindAssocOutbound` |
| Recall since required | `server/internal/assoc/recall.go` `Query.validate` |

## CLI

| Fact | Source |
| --- | --- |
| `multica assoc recall` | `server/cmd/multica/cmd_assoc.go` `runAssocRecall` |
| `multica assoc bind` | `server/cmd/multica/cmd_assoc.go` `runAssocBind` |
| `multica assoc events` | `server/cmd/multica/cmd_assoc.go` `runAssocEvents` |
| `--current-issue` reads `MULTICA_ISSUE_ID` | `server/cmd/multica/cmd_assoc.go` |

## Graph write path

| Fact | Source |
| --- | --- |
| Outbound bind writes outreach / waiting_on / task_scene | `server/internal/assoc/bind.go` `bindOutbound` |
| Inbound Event tags `scene_id` | `server/internal/handler/assoc.go` `recordAssocInboundEvent` |
| Conversation id → scene (lookup; register only with a stated kind) | `server/internal/handler/agent_scene.go` `conversationSceneNode` → `server/internal/scene` `Lookup` / `Resolve` (docs/agent-scene.md) |
| `--kind` has no default; MCP `kind` "never guess" | `server/cmd/multica/cmd_assoc.go`; `server/internal/handler/multica_mcp.go` `assoc_bind` schema |
| Recall by `scene_id` | `server/internal/handler/assoc.go` `recallAssoc` |
| Coordinator `/reset-memory` closes scene edges, unlinks events and resets Coordinator Scene Memory by `scene_id` | `server/internal/handler/agent_dispatch_v2_handler.go` `tryDispatchResetMemory` → `assoc.Service.CloseSceneAssociations`, `scenememory.Store.Reset` |
| Employee standalone `/reset-memory` clears shared scene and only the frozen sender's private namespace, with no LLM; other window messages continue | `server/internal/handler/employee_scene_entry_memory.go` `memoryCommands`; `employee_scene_entry_worker.go` per-receipt outcomes; `service/employeememory/private_reset.go` `ResetPrivateTx` |
| Employee reset journal prevents replay from clearing newer memory; unknown sender leaves memory unchanged | `handler/employee_scene_entry_memory_test.go` real PostgreSQL reset/replay, actor and mixed-window tests |
| Employee management page resets shared scene only | `handler/employee_memory_management.go`; `service/employeememory/management.go` `ResetSceneTx` |
| Issue associate writes spawned_from | `server/internal/assoc/associate.go` |
| Purpose fallback from user message | `server/internal/assoc/purpose.go` `ResolvePurpose` |

## Loop prompt

| Fact | Source |
| --- | --- |
| Dispatch `scene_graph` segment | `server/internal/handler/agent_dispatch_v2.go` `dispatchSceneGraphInstruction` |
| Coordinator identity note | `server/internal/service/inboundcoord/prompt.go` `IdentityNote` |
| Coordinator tool loop | `server/internal/service/inboundcoord/loop.go` `runLoop` |
| Coordinator LLM context logs | `inbound_coordinator_llm_request` / `inbound_coordinator_llm` / `inbound_coordinator_llm_finish`；索引 `conversation_name` / `coord_trace_id` |
| Coordinator SLS 查询 | `scripts/query-coordinator-sls.sh` → Normandy `log list --source sls` project `dt-fde-multica-sls` |
| Coordinator read tools / `finish.actions` plans | `server/internal/service/inboundcoord/tools.go` `AssocTools`; `window_plan.go` validates the read-only plan; Host commits Issue/member-comment effects |
| Bind purpose+intent from the model | Coordinator `finish.actions` uses `start_work` / `continue_work`; Host derives the delegator from source refs, validates structure and submits Issue/Associate effects after semantic review |
| Coordinator recall cards | bounded purpose/intent and real state references; no raw task comments or business conclusions; CLI/MCP still use full `assoc.Result` |
| Event clipped body | `assoc_event.body` via `Event.Body` / `EventRef.Text` |
| Technical work subjects | `server/internal/assoc/purpose.go` structure only; `inboundcoord/policy/finish_check_work.md` semantic review |

| Continuation target review | `inboundcoord/finish_work_target.go` projects the loaded original goal; `finish_check.go` requires target_match consistent with action kind before submission |
