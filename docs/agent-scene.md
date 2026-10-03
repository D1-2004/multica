# Agent work scene (AgentScene, `scene_id`)

Status: current contract (PRI-84 design `docs/plans/2026-10-02/agent-scene-identity.md`,
implemented in PRI-91). Code: `server/internal/scene`. Every change that stores,
reads, sends or reacts to something "per scene" follows this document.

## 1. One meaning, one id

A scene (场域) is **one agent in one tenant org in one scene instance**:

```text
agent + tenant org + scene kind + stable scene instance → one scene_id
```

- `scene_id` is a server-minted UUID stored in `agent_scene`. It is the only
  scene identity anywhere: graph nodes, configuration scope keys, memory rows,
  Coordinator jobs, task context, events and outbound targets carry it.
- The agent is the employee agent that receives and runs the work, never a Tag
  template and never the speaker. The same DingTalk group served by two agents
  is two scenes; their configuration and memory are never shared.
- The tenant org comes from the agent's trusted binding (§3), never from a
  message body, sender or name. The same conversation id under another org is
  another scene; re-binding the agent to another org never carries the old
  org's scenes over.
- Kinds: `group` and `dm` (a DingTalk conversation by its stable
  `openConversationId`), `enterprise` (the tenant org itself, the scene of
  resource events that have no conversation). Anything else is rejected
  (`scene.ErrUnknownKind`); a kind is never guessed.
- **A 1:1 chat is keyed by its conversation, not by a person.** Two 1:1 chats
  with the same person are two scenes; a staffId/UID never creates, finds or
  stands in for a dm scene. The person scope (staffId, or the openDingTalkId
  of a sender a DWS native subscription names only so, context capabilities
  §2) still exists for personal capabilities (context capabilities §1.1) and
  is a separate thing;
  a 1:1 chat's configuration link is its scene link; the one the Host
  appends itself also carries the chat's person (2026-10-03, context
  capabilities §5).
- Renames, membership changes, credential rotation and model/session restarts
  do not change a scene_id. Holding a scene_id grants nothing by itself; every
  read and write still checks workspace, agent, tenant org and the caller's
  rights.

Natural key (`agent_scene`):

| Column | Meaning |
| --- | --- |
| `workspace_id`, `agent_id` | owner (Host-verified) |
| `provider` | `dingtalk` (the business system, not a transport) |
| `tenant_org_id` | the tenant org (§3), non-empty |
| `source_namespace` | `dingtalk.open_conversation_id` for group/dm, `dingtalk.org` for enterprise |
| `scene_kind` | `group` / `dm` / `enterprise` |
| `external_scene_id` | the openConversationId; the org id for enterprise |

Uniqueness is enforced on the key without `scene_kind`
(`agent_scene_locator_idx`): one external id carries one kind, and a kind
mismatch is `scene.ErrKindConflict`, never a second scene. `title` and
`last_active_at` are state, not identity. `kind_source` says where the kind
came from: `observed` (a trusted source stated it) or `migrated` (9510
assigned it without evidence, §8). Only an observation whose kind a trusted
event states (`scene.Observation.KindStated`: an inbound dispatch, a channel
callback, a send to a person) settles a migrated kind, keeping the scene_id;
every other mismatch stays a conflict.

## 2. Storage

| Store | Role |
| --- | --- |
| `agent_scene` | the scene directory: identity, title, last activity. Lists and details read it; nothing "discovers" scenes by unioning other tables. |
| `agent_scene_memory` | Scene Memory state keyed by `scene_id` (revision, cursor, lease, flush). It never mints a second id; APIs address it by `scene_id`. Only `group`/`dm` scenes have memory. |
| `assoc_edge` (`dst_type='scene'`) | graph scene nodes: `dst_id` is the `scene_id`; edge props carry `{scene_id, conversation_id, kind}` for display. |
| `assoc_event.scene_id` | the scene an inbound/outbound event happened in. |
| `context_*` (`scope_type='scene'`) | scene configuration: `scope_key` is the `scene_id`. Configuration links of a group and of a 1:1 chat are minted the same way, as scene links keyed by the conversation's `scene_id` (never by the sender, no staffId needed; `docs/context-capabilities.md` §5). `context_config_link.extra_scene_key` (the 1:1 scene a personal link minted before 2026-10-02 also grants) is a `scene_id`; nothing writes it any more. |
| `inbound_coordinator_job.command.agent_scene` | SceneRef of a persisted dispatch (`inbound_coordinator_job_agent_scene_idx`). |

No FK/cascade (repo rule). Workspace deletion removes `agent_scene` and
`agent_scene_memory` with the workspace.

The retired tables `assoc_scene` and `scene_memory` have **no business reads
or writes** (no dual write, no fallback). They are not dropped and their rows
are not deleted by this change; only workspace deletion still cleans them up.

## 3. Tenant org

`agentTenantOrg` (`server/internal/handler/agent_scene.go`), the same org
context capabilities resolve for a task (docs/context-capabilities.md §2):

1. The org the dispatch recorded for the agent's DWS identity
   (`external_identity.dws.orgId`); a tool call inside a task uses its task's
   recorded org.
2. Else the agent's DingTalk identity org (`agent_dingtalk_identity.org_id`).
3. The org must be one the agent serves now: its identity org, a tenant
   created for it (`agent_tenant`), or, for an agent without an identity,
   the dispatch org itself. Any other org comes from an earlier binding →
   `scene.ErrStaleTenant`; no org at all → `scene.ErrUnresolved`.

An agent with neither (an orgless robot channel) has **no scenes**: its tasks
get no scene layer and no scene memory, only the person layer under org `""`.

## 4. One entry point

`scene.Resolve(ctx, q, owner, locator, observation)` is the only way to turn a
trusted locator into a scene: find → insert (`ON CONFLICT DO NOTHING`) → read
back → kind check → touch (title, `last_active_at` only moves forward).
Concurrent registration returns one id. `scene.Lookup` finds without
registering; `scene.Get` loads by id for an owner; `scene.CheckTenant` is the
use-time fence (§6).

Who may **register** a scene (all through `Resolve`):

| Source | Where | Kind from |
| --- | --- | --- |
| Inbound dispatch (Router, DWS native, Coordinator jobs) | `attachDispatchScene` before admission; the job worker re-resolves a job an older replica admitted without one | conversation type (`single`/`p2p`/… → dm, `group` → group); non-channel domains → the enterprise scene |
| Channel engine conversation (robot channel) | `AssociateChannelConversation` | chat type |
| Agent tool send to a person (`dws chat message send --user`, with the conversation id from its receipt or `query-send-status`) | `bindAssocOutboundFromTools` | dm: the send itself proves a 1:1 chat with that person. A receipt without a conversation id binds nothing; a person's earlier chat is never looked up in its place |
| Explicit assoc bind (HTTP/MCP `assoc_bind`) | `conversationSceneNode(register=true)` | the caller's `kind` (required for a new conversation) |

Everything else only **looks up** (`register=false`): recall, events, the
Coordinator's `SceneLookup`, the JSAPI group picker (groups only), tool sends
into a conversation id, and verified sandbox deliveries
(`BindVerifiedDingTalkSend`) outside the dispatch's own scene, whose receipt
does not say the kind. An unseen conversation there has no scene: recall
returns nothing, bind is skipped (`assoc_outbound_bind_skipped`,
`scene_unresolved`), `waiting_on` is recorded as unresolved.

## 5. SceneRef v1

`scene.Ref{scene_id}` is the one reference a record or event carries:

- `DispatchCommand.AgentScene` (json `agent_scene`, set by the Host, never
  accepted from Router input) and the persisted Coordinator job command;
- task context key `agent_scene` (`protocol.AgentSceneContextKey`), read by
  context capabilities, the task-finished loop and the outbound path;
- `inboundcoord.Turn.SceneID`, `dingtalkresponse.ActionInput.SceneID`,
  assoc `Event.SceneID` / `Query.SceneID` / `SceneNode`;
- scene routines (`context_scope_routine.scene_id`) and their runs, which
  carry `agent_scene` plus the frozen `scene_routine` binding and no inbound
  message (`docs/context-capabilities.md` §9);
- the config-qwen-tag-scene scene token (`auth.SceneTokenClaims.SceneID`),
  which binds one task's MCP server to its scene (§10 there).

Provider calls read the external locator back from the directory
(`agent_scene.external_scene_id`); a scene_id is never sent as a DingTalk
conversation id.

## 6. Use-time fences

A persisted SceneRef is used only while the scene is still the agent's and
belongs to the org its event happened in, and the agent still serves that
org (`agentTenantOrg`, §3; `fencedScene` / `fenceSceneRef`). A job or
task admitted before the agent was re-bound to another org reads no scene
state of the old org:

- Coordinator job claim re-checks the job's SceneRef (and resolves one for a
  job an older replica admitted without it); a failing ref is dropped.
- `dispatchScene` (memory marks, reset, reply routes, associations) and the
  task-finished loop's envelope ref go through the same fence.
- Task claim: the scene layer applies only if `contextcap.GetScene` finds the
  scene among the agent's conversation scenes in the task's org and that org
  is still the agent's current one; otherwise the task keeps its org and
  person layers without one.
- Scene Memory DWS history read: `scene.CheckTenant(scene, identity org)`;
  a mismatch fails closed (`route_inactive`).
- Admin and mobile scene routes resolve `{scene_id}` within the requested
  tenant org; a scene of another org or agent is 404.
- A managed DingTalk reply targets the dispatch scene's conversation and
  kind from the directory. Without a scene it only quote-replies into the
  event's own conversation; with nothing to quote no managed route is
  registered (no send to the sender chosen by the event type). The
  Coordinator's chat type of an unknown conversation is `unknown`, not `p2p`.
- Inside a task, a `scene_id` a tool names (assoc recall) passes the same
  fence for the task's org.

## 7. API surface

- Every scene object has `scene_id`. Where a response historically had
  `scene_key` or `memory_id`, they now carry the same `scene_id`;
  `conversation_id` is the openConversationId, for display and lookups only.
- Path keys are scene ids: `/tenants/{orgId}/context/scene/{scene_id}`,
  `/scene-memory/{scene_id}`, `/context-capabilities/agents/{id}/scenes/{scene_id}`.
  A conversation id there is 400; an unknown id 404.
- Agent-facing tools keep accepting the conversation id the model sees
  (`conversation_id`) and resolve it through the directory; outputs include
  `scene_id`. `GET /api/assoc/recall` also takes `scene_id`; a scene_id sent as
  `conversation_id` (a client passing a `scene_key`) names that scene. A
  DingTalk conversation id is never a UUID, so the two cannot be confused.
- The admin scene list (`GET /tenants/{orgId}/groups`) lists groups and 1:1
  chats; `groups_only=true` restricts it to groups.

## 8. Migration and rollout

- `9500`–`9509` create the directory, the memory state table, their
  CONCURRENTLY indexes, the job command scene index and `assoc_event.scene_id`.
- `9510` registers every conversation that stored scene configuration had a
  tenant org and conversation id for, and re-keys those rows
  (`scope_key`, `extra_scene_key`) to the scene_id. A dm is registered as dm
  only on positive evidence (a personal link's extra scene, or a stored dm
  kind); otherwise group. Rows without an org or with a malformed key are left
  untouched and no longer apply. Re-running it changes nothing.
- `9511` adds `kind_source`; `9512` marks the scenes stored configuration
  names that no inbound Coordinator job carries as `migrated` (association
  events prove no kind), then settles each one whose trusted records for the
  same workspace, agent, tenant org and conversation all name one kind (the
  retired Scene Memory kind, inbound job conversation types). The rest are
  settled by their next trusted inbound event (§1). 9512 re-evaluates, so a
  database that ran an earlier 9511 with org-blind evidence is corrected.
- Scene connector credentials are sealed with their scope key, so
  `ReconcileSceneCredentials` reseals them under the scene_id at server start:
  per row, compare-and-swap on the old key and ciphertext (an older replica's
  newer write wins and moves on a later start); a scene that already holds a
  credential under its scene_id keeps it. Until it ran, a credential under a
  conversation id is simply not found.
- Old Scene Memory and old graph scene nodes (conversation ids) are not
  migrated: memory rebuilds from new trusted inbound messages, and legacy
  conversation-id nodes are ignored.
- Rolling window: an old replica keeps writing the retired tables, which the
  new code ignores; a Coordinator job an old replica admitted is resolved at
  claim time from its persisted command (§4).

## 9. Adding an event source or a kind

Future events (calendar, approvals, documents, other providers) attach to this
definition instead of inventing their own scene keys:

1. Build a typed `scene.Locator` from trusted data only (a provider-issued id
   in a verified namespace, the tenant org from §3). Add a namespace constant
   when a provider issues a new kind of id; a mapping between two locators of
   one scene needs explicit evidence and still returns the one scene_id.
2. Resolve it once at admission with `scene.Resolve` and carry `scene.Ref` on
   the persisted event/job and in task context (`agent_scene`). Resource
   events without a conversation use `scene.DingTalkEnterprise(org)`.
3. Downstream code reads the scene by id (`scene.Get` / `dispatchScene`) and
   applies the use-time fence before reading private state or sending.
4. A new kind is a new constant, a widened `agent_scene_kind_check` in a new
   fork migration, and a kind mapping in the locator builder; unknown kinds
   stay rejected.
