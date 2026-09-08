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
| Inbound Event tags `scene_key` | `server/internal/handler/assoc.go` `recordAssocInboundEvent` |
| `/reset-memory` closes scene edges and unlinks events | `server/internal/handler/agent_dispatch_v2_handler.go` `tryDispatchResetMemory` → `assoc.Service.CloseSceneAssociations` |
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
| Coordinator read tools / `finish.items` plans | `server/internal/service/inboundcoord/tools.go` `AssocTools`; `window_plan.go` validates the read-only plan; Host commits Issue/member-comment effects |
| Bind purpose+intent from the model | Coordinator `assoc_bind` requires recalled `issue_id` plus `purpose`,`intent`,`delegator`. New matter is `finish action=issue` without `issue_id`; server creates Issue then Associate |
| Coordinator recall cards | slim `issue_id` `purpose` `why` `on_this_scene` `last_touched` `last_comment`; CLI/MCP still use full `assoc.Result` |
| Event clipped body | `assoc_event.body` via `Event.Body` / `EventRef.Text` |
