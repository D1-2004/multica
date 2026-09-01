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
| Issue associate writes spawned_from | `server/internal/assoc/associate.go` |
| Purpose fallback from user message | `server/internal/assoc/purpose.go` `ResolvePurpose` |

## Loop prompt

| Fact | Source |
| --- | --- |
| Dispatch `scene_graph` segment | `server/internal/handler/agent_dispatch_v2.go` `dispatchSceneGraphInstruction` |
| Coordinator identity note | `server/internal/service/inboundcoord/prompt.go` `IdentityNote` |
| Coordinator tool loop | `server/internal/service/inboundcoord/loop.go` `runLoop` |
| Coordinator `assoc_recall` / `assoc_bind` / `issue_get` / `issue_comment_*` | `server/internal/service/inboundcoord/tools.go` `AssocTools` |
| Event clipped body | `assoc_event.body` via `Event.Body` / `EventRef.Text` |
