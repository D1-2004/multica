# Context capabilities: scene and personal connectors and skills

Status: v1 contract (2026-09-29), plus official app OAuth connections
(2026-09-30, `docs/internal-mcp-connectors.md` "Official apps"), plus the
configuration architecture for 智能体 / 场域 / 个人 (2026-09-30, migrations
9413-9417, see §1.1 and §1.2), plus the 连接器 tab redesign with the
connected-apps API and manager access to the configure page (2026-09-30, no
migration, see §1.1, §5 and §6), plus 1:1 chat = person, per-scope custom MCP
servers and the scene page parallel to the 连接器 tab (2026-09-30, migrations
9418-9419, see §1.1, §1.2, §5 and §6), plus tenants, the enterprise (org)
level and the Context Builder of prompt, MCP and skill components
(2026-10-01, migrations 9420-9428, see §1.3, §6 "Tenants" and "Context
nodes"). Modelled on Claude Tag (Claude in Slack):
admins own a library, channels and people opt in, and the effective toolset of
one run depends on where the message came from and who sent it.

## 1. Layers

A claimed task's context is merged from four layers, outermost first:
global → enterprise (org) → group scene → person (§1.3). For connectors and
skills the scope layers only ever ADD capabilities; they never remove a
global one. Prompt components and custom MCP servers merge by name, nearest
layer wins.

| Layer | Key | Who edits | Where | Stored in |
| --- | --- | --- | --- | --- |
| Global (智能体) | agent | workspace admin / agent manager | web: agent detail → 配置 → 能力 → 连接器 (official apps: 「对所有用户启用」; Aone FaaS grants) / Skills (agent skills) | `internal_connector_agent`, `agent_skill` (existing) |
| Offer catalog | agent | agent manager | web: agent detail → 配置 → 能力 → 连接器 (official app dialog switch 「允许群聊、个人连接自己的账号」; Aone FaaS row switch 「群聊/个人」) / Skills, section 「允许在场域 / 个人中开启」 | `context_capability_binding` (`scope_type='offer'`) |
| Enterprise (企业级, a tenant) | agent + org_id (scope key = org_id) | agent managers (web and configure page); people with a live grant under that org read it on the configure page | web agent detail → 场域 → tenant → 配置; mobile 「企业」 | `agent_tenant` (the tenant), `context_capability_binding` / `context_connector_credential` / `context_scope_mcp_config` / `context_prompt_component` with `scope_type='org'` |
| Scene (场域: 群聊) | agent + org_id + openConversationId | members of that DingTalk group; agent managers from the web and from the configure page | web and mobile `/dingtalk/configure` tab 「本会话」; web agent detail → 场域 → tenant → 群聊 → 配置 | `context_capability_binding` (`scope_type='scene'`), custom MCP servers in `context_scope_mcp_config`, prompt components in `context_prompt_component` |
| Personal (个人), also every 1:1 chat (单聊) scene | agent + org_id + staffId | that DingTalk person; agent managers for bindings, prompt components and custom MCP servers (not accounts) | web and mobile `/dingtalk/configure` tab 「我的」, or the 1:1 chat's scene there; web agent detail → 场域 → tenant → 个人 → 配置 | `context_capability_binding` (`scope_type='person'`), custom MCP servers in `context_scope_mcp_config`, prompt components in `context_prompt_component` |

- Resources are library items only: `resource_type='connector'` (an
  `internal_connector` row) or `resource_type='skill'` (a workspace `skill`
  row). End users never enter URLs; they pick from the agent's offer catalog.
- An org, scene or personal binding is effective only while the same resource
  is in the agent's enabled offer catalog. Removing an offer disables every
  org, scene and personal use at once (the check runs at claim time and on
  every connector call).
- The connector library switch (`internal_connector.enabled`) stays the global
  kill switch for every layer.

### 1.1 Configuration layers and where they are managed

- **智能体 (agent, global; the Shared side).** The agent's connector tab (web
  agent detail → 配置 → 能力 → 连接器, DetailTab `mcp_config`) holds
  everything connector related for the agent, for every user and every
  scene, in two blocks (web and desktop share the view; its only note is
  「对所有用户、所有场域生效」, and each app is configured in a dialog):
  - **「MCP（由 Multica 管理）」**: MCP servers Multica manages for this
    agent, for every user and every scene. Sub-section 「Aone FaaS 连接器」
    lists the Aone FaaS connectors granted to the agent, plus the Aone FaaS
    connectors that are only offered, so an offer made elsewhere stays
    visible and removable. A row shows its name, one status (工作区已停用,
    else 仅群聊 / 个人开启 for an offered-only row, else 缺少凭证 for a
    granted row without a usable workspace credential, else 已启用) and one
    line of host · tool count · auth mode. An offered-only row never reads
    缺少凭证: groups and people bring their own token there (person →
    scene → workspace). Each row has 移除
    (a trash icon; confirm; revokes the agent's grant and its offer, so the
    row really goes away), a per-row switch labelled 「群聊/个人」 (accessible
    name 「允许群聊 / 个人单独开启」: the connector's offer; workspace admins
    only; turning off an offer in use asks first)
    and the block has 「添加 Aone FaaS 连接器」 from the workspace library
    (catalog connectors excluded), plus a link to the library page
    `/{slug}/internal-connectors`. The library and grants stay
    workspace-admin only. Everyone else sees one read-only list with a hint:
    the Aone FaaS connectors granted to the agent from the member-visible
    list (`GET .../internal-connectors/available`, official apps left out by
    `catalog_slug`), plus, for an agent manager who can read the offer
    catalog, the offered-only ones. Sub-section 「自定义 MCP 服务器」 is the agent's own
    `mcp_config` (managed servers, runtime-inherited servers, Runner), shown
    only when the runtime reads `mcp_config`.
  - **「连接应用」**: one compact tile per official app of the catalog
    (GitHub, Notion, Linear, Atlassian, Sentry, Asana, Figma, Stripe):
    logo, name and one status built only from
    `GET /api/agents/{id}/connected-apps` (§6). A tile opens the app's
    configuration dialog (`app=<slug>`, deep-linkable; closing it drops the
    parameter). Its header carries the status and, for an added app,
    从智能体移除 (revoke grant and offer; the workspace catalog connector
    stays). Under it, an admin sees the one step still missing, if any:
    添加, or 在工作区启用 for a connector switched off in the workspace.
    Then three short sections: 共享账号 (the workspace shared account:
    OAuth connect when `oauth_available`, GitHub PAT with one line saying
    the server has no OAuth for the app, replace, disconnect, the GitHub App
    install link; switch 「对所有用户启用」 = the agent grant, which needs a
    usable shared account), 群聊和个人 (switch
    「允许群聊、个人连接自己的账号」 = the offer, and the groups and people
    using it) and 工具 (discovered tools, 允许写操作, 刷新工具). Results of
    actions are toasts; a non-admin reads the dialog with one short
    admin-only line. 添加 (workspace admins only) creates the
    workspace catalog connector when it is missing and then offers the app
    to the agent (允许群聊、个人连接自己的账号), which is what makes it
    `added`; it never grants it, because 对所有用户启用 needs a usable
    shared account first. The block ends with the link to the configure
    page (`configure_url`). Status details: the runtime mounts an app only
    with at least one allowed tool, so a granted app with a connected
    shared account but `tools.allowed == 0` (or an offered app with some
    account connected) reads 还没有可用工具 in warning tone, and a missing
    tool list says 尚未发现工具，请刷新工具 once any account is connected
    (连接账号后发现工具 only before). A shared account from the deployment
    environment has no 断开. The shared account, the tools and 允许写操作
    belong to the workspace connector, and the page says they affect every
    agent that uses the app. A shared-account sign-in result
    (`connected` / `connect_error`) shows as a toast and reopens that
    app's dialog. The app list and dialog refetch whenever the
    window regains focus (`refetchOnWindowFocus: "always"`; the shared
    staleTime is Infinity), so a connect made in the system browser
    (desktop) or on a phone shows on return.
  The Skills tab keeps its 「允许在场域 / 个人中开启」 skill section.
- **场域 (scene; the per-scene side).** Three levels under each tenant
  (§1.3): 企业级 (the tenant's org), 群聊级 (a group chat) and 个人及单聊级
  (a person; a 1:1 chat is its person). Scenes are DingTalk IM scenes
  only: a group chat or a 1:1 chat. Scene identity everywhere is (agent,
  platform `dingtalk`, org_id = the tenant org, scene_key =
  openConversationId); kind is `dm` for positively 1:1 conversation types
  (`contextcap.IsDirectConversationType`), else `group`. Every level has a
  Context Builder (§1.3): prompt components, MCP components and skill
  components. The old per-scene prompt (场域提示词,
  `agent_scene_config.prompt`) became the prompt component 「场域提示词」
  of the scene (a 1:1 chat's of its person, migration 9428); its API is
  gone.
  - A **group** scene has its own configuration: scene connectors and
    skills (toggled by members on the configure page, or by the same admin
    set from the web and from the configure page, §5 "Managers"), scene
    credentials (accounts and tokens a group connects) and custom MCP
    servers.
  - A **1:1 chat** (`dm`) scene's configuration IS its counterpart
    person's (单聊绑定到人): its bindings, credentials and custom MCP servers
    read and write that person's scope, the same scope as 「我的」 on the
    configure page. The person is `contextcap.DirectScenePerson`: the
    sender staffId of the newest inbound Coordinator job of the conversation
    that names one (`command` `event.data.sender.staffId`, the dispatch
    sender the task scope reads, skipping jobs recorded under another agent
    org), else the staffId of a live person grant redeemed from a personal
    link minted in that chat (`context_config_link.extra_scene_key`); the
    title is the sender's display name or the grant title. When neither
    exists the scene reads with `scope: null` and no configuration, and
    writes answer 409 `dm_person_unknown`. `contextCapResolveScope` is the
    one place that maps a scene request to this effective scope, on every
    mobile and admin scene route and in the OAuth connect. Rows an earlier
    release stored on a 1:1 chat's own scene key (scene bindings,
    credentials) are ignored: neither shown nor applied.
  - The web 场域 tab is a tree: tenant (name + OrgId) → 「群聊」 /
    「个人」. A node's 配置 is its Context Builder (§1.3, §6 "Context
    nodes"): 「Prompt」 (prompt components), 「MCP」 (the offered Aone FaaS
    connectors switched on for the node, their token, and the node's own
    「自定义 MCP 服务器」), 「连接应用」 (the offered official apps: switch
    and account), 「Skills」 (offered skills) and 「生效预览」 (the node's
    effective context with layer badges). Account actions use the node's
    own credential and connect routes and are offered only when the node's
    `can_connect` is true (managers for org and group nodes; only the
    person for a person node).
- **个人 (person).** A person's own connectors and skills. They are not
  carried into a group run by default: the person turns on
  「在群聊中由我触发时也可用」 per connector (`share_in_groups`, default off).
  This round only stores the switch (§1.2); the configure page says so next
  to it, and its 我的 hint says personal items currently also apply when
  the person @s the agent in a group.
- The configure page `/dingtalk/configure` works on phones (DingTalk
  WebView) and desktop browsers alike. The agent tab links to it as
  `configure_url` (`/dingtalk/configure?agent=<id>`); a manager of the agent
  who opens that link configures all of the agent's scenes there (§5).

Agent detail IA (shared web and desktop views): top-level sections
概览 | 工作 | 场域 | 配置. 场域 replaces the old 入站会话 and 记忆 sections and
shows the tenant tree; a tenant opens 配置 (its Context Builder) and 设置
(rename, delete); a group opens 入站记录 (its Coordinator transcript, via
`inbound_session_id`), 记忆 (its `scene_memory` row, loaded by
`memory_id`) and 配置; a person opens 入站记录 and 记忆 of their 1:1 chat
(the node's `scene`, when there is one) and 配置. Under the tree, 其他记录
opens the full inbound conversation list (the old 入站会话 view) and the full
scene memory list, so conversations the scene list does not cover (no
openConversationId, another DingTalk org or robot endpoint) and memory rows
of an earlier DingTalk binding stay reachable. On the configure page a 1:1
chat scene is labelled 「单聊 · {title}」 and edits its person's
configuration; a manager who is not that person sees 「由本人连接」 instead of
account actions.

### 1.2 配置 vs 生效 (stored vs applied at runtime)

The claim-time Context Builder (§3) applies every layer of a task's tenant
org: `contextcap.LoadLayers` + `contextcap.MergeContext` build the effective
context, and the admin 「生效预览」 runs the same merge.

| Setting | Stored | Applied at runtime |
| --- | --- | --- |
| Agent global connectors and skills | yes | yes |
| Offer catalog | yes | yes (gates org, scene and personal bindings) |
| Enterprise (org) connectors, skills, credentials | yes | yes, for tasks dispatched in that tenant org |
| Group scene connectors, skills, credentials | yes | yes |
| Personal connectors, skills, credentials | yes | yes, in every run the person triggers, including group runs (see below) |
| 1:1 chat connectors, skills, credentials (= its person's, §1.1) | yes, in the person scope | yes, as the personal layer: a 1:1 run carries its person (§2) and no scene layer |
| Rows an earlier release stored on a 1:1 chat's own scene key | yes | no, and no longer shown either (not on the scene pages, not in connected-apps usage) |
| Prompt components of an org, group or person scope (`context_prompt_component`) | yes | yes: merged by name, nearest layer wins, appended to the task instructions as one block |
| Custom MCP servers of an org, group or person scope (`context_scope_mcp_config`) | yes | yes: merged into the agent's `mcp_config` by server name, nearest layer wins |
| Old scene prompt (`agent_scene_config.prompt`) | kept for the rolling window | no (migrated into the 「场域提示词」 component, §1.1) |
| `share_in_groups` (「在群聊中由我触发时也可用」) | yes | no: until the runtime reads it, the whole personal layer (connectors and skills, and also prompt components and custom MCP servers) still applies in group runs the person triggers alone, regardless of the switch (§8) |

### 1.3 Tenants and the Context Builder

An agent is reused by many enterprises. Each enterprise is a **tenant**: a
DingTalk org the agent serves, created explicitly with a name and its OrgId
(`agent_tenant`, 9420; OrgId `^[A-Za-z0-9_-]{1,64}$`, name 1..64
characters). The agent's DingTalk identity org
(`agent_dingtalk_identity.org_id`, named by its `organization_name`) is a
tenant without a row (source `identity`); a row for it only renames it, and
it cannot be deleted. `contextcap.AgentTenants(ctx, db, workspaceID,
agentID)` is the only definition of "the agent's tenants" (identity first,
then created tenants by name). The agent's global connectors and skills
(the 连接器 and Skills tabs) are common to all tenants.

- **Org scope.** `scope_type='org'` with `scope_key = org_id` in
  `context_capability_binding`, `context_connector_credential`,
  `context_scope_mcp_config`, `context_prompt_component` and
  `connector_oauth_state` (9424-9427 widen the checks). Org bindings and
  credentials are offer-gated like scene ones. Agent managers edit it
  (web and configure page); anyone with a live grant under that org reads
  it on the configure page.
- **Groups and people of a tenant** live under its org: group scenes are the
  scene union of §6 "Scenes" for that org (Coordinator jobs that recorded no
  agent org belong to the identity org); people are 1:1 chat senders, person
  grants (and the 1:1 chat of a personal link) and every person scope with
  stored configuration (`contextcap.ListOrgPersons`). Orgs that scene or
  person data mentions without a tenant are listed as unassigned
  (`contextcap.ListAgentOrgActivity`); deleting a tenant keeps its group and
  person data (the org shows as unassigned again) and removes only its org
  scope configuration.
- **Context Builder components** per level: prompt components
  (`context_prompt_component`, 9422-9423: several per scope, each `{name,
  order, text}`, at most 20, names unique per scope and 1..64 characters,
  texts 1..8000 characters), MCP components (offered connectors switched on
  for the level, plus the level's custom MCP servers in the agent
  `mcp_config` format) and skill components (offered skills switched on).
- **Effective context** = global → org → group → person
  (`contextcap.MergeContext`, through the handler's `mergeTaskContext`, the
  one merge the preview and the runtime share, §3): prompt components and
  custom MCP servers merge by name, the
  nearest layer replaces an outer one of the same name (DSH "nearest layer
  wins"; the outer one is kept in the preview with `overridden_by`);
  connectors and skills are a union whose layer is `global` when the agent
  has it globally, else the nearest layer that switches it on. Applied
  prompts are ordered by `order`, then layer (outermost first), then name.
  The preview of an org node is global + org, of a group node global + org
  + group, of a person node global + org + person.
- Runtime helpers: `contextcap.LoadLayers(ctx, db, ws, agent,
  LayerSelection{OrgID, Org, SceneKey, PersonKey})` reads the scope layers
  (enabled bindings of offered resources, prompt components, custom MCP
  servers), `contextcap.LoadGlobalLayer` the agent's own (preview only),
  `contextcap.LayerCredentials` the layers' credentials (person > scene >
  org > workspace), `EffectiveContext.PromptBlock()` renders the applied
  prompts under 「## 场域上下文」 and `contextcap.MergeMCPConfig` layers the
  applied scope servers onto the agent's `mcp_config`.

## 2. Scene and trigger person of a task

`contextcap.ScopeFromTaskContext(task.Context)` derives the scope from the
server-written DingTalk dispatch context. Nothing is read from the prompt.

- A2A-origin tasks (`service.IsA2ATaskOrigin`) get no scene or personal layer.
- Manual reruns get no scene or personal layer: `rerunDispatchContext` marks
  the copied context `replayed_dispatch_context: true`, and a task with
  `rerun_of_task_id` is excluded as well. The member who reran the task, not
  the DingTalk sender, triggered it, so it can neither use that sender's
  personal/scene connectors and credentials nor mint a configuration link.
- Scene: `dispatch_event_data.conversation.openConversationId` when
  `conversation.type == "group"` and the id passes the `cid…` validator
  (`dingtalkOpenConversationID`). 1:1 chats have no runtime scene layer:
  their configuration is their person's (§1.1), which the person layer
  below already applies. `Scope.DirectSceneKey` records a positively 1:1
  conversation's openConversationId only so a personal link can also grant
  that DM scene (§5); `HasScene`/`SceneKey` stay group-only.
- Person: `dispatch_event_data.sender.staffId`, only when the run positively
  comes from that one person: with no messages (event dispatches) the sender
  is the actor; exactly one message must not name anyone else
  (`senderStaffId`, `senderUid`, `senderOpenDingTalkId`); two or more
  messages, or a coalesced Coordinator follow-up
  (`coordinator_follow_up_comment_ids` with more than one entry), need every
  message to carry `senderStaffId == sender.staffId`. A message without a
  staffId proves nothing, because merged windows and coalesced follow-ups keep
  only one data-level sender. So one person's credentials never serve a batch
  that may contain someone else's message. Coordinator item tasks already
  rebind `sender` per work item.
- org_id (the tenant): the dispatch's recorded agent org
  (`external_identity.dws.orgId`), else the agent's
  `agent_dingtalk_identity.org_id` (same source as scene memory). A task
  without a recorded org that was created before the agent's current
  binding (`bound_at`) may come from the earlier binding and gets only the
  global layer. An agent without a DingTalk identity (or with an identity
  that names no org), for a task without a recorded org, keeps org `""`:
  its implicit tenant, where its configure page, links and JSAPI grants
  write (§5), so the task gets its group and person layers under org `""`
  and no org layer. Bindings store the org_id they were created under and
  resolution matches it exactly.
- Tenant gate: a staffId or openConversationId only means something inside
  the org it was dispatched in, and the org, scene and person layers apply
  only while that org is one of the agent's tenants
  (`contextcap.AgentTenants`, §1.3; org `""` of an agent without an
  identity, above, counts as its tenant). A task from any other org (a deleted
  tenant, an org nobody created a tenant for, an earlier binding) gets only
  the global layer and cannot mint links, so it never reads another org's
  bindings or credentials. This replaces the earlier "dispatched under
  another binding" drop. The handler's `resolveTaskContextScope` is the one
  place this runs (`taskContextScope` for links); `contextcap.Scope` then
  carries `Dispatched` and the tenant `OrgID`.
- Org layer: every dispatched task of a tenant org carries it, also without
  a group scene or a single sender (a merged multi-sender run, an unknown
  sender, an event dispatch without a conversation).
- Tasks with no dispatch context (web comments, autopilot, plain chat) get only
  the global layer.
- v1 covers the Digital Employee dispatch path (and Coordinator issue tasks that
  carry its context). The robot Stream path is a follow-up.

## 3. Resolution

Claim time (`buildClaimedTaskResponse` → `injectRunnerMCP`) and call time
(`CallInternalConnector`) run the same resolver, so a toggle or revoke applies
to the next tool call of a running task.

The scope layers of a task are its tenant org's org layer, its group scene
and its trigger person (§2), read with `contextcap.LoadLayers` and merged
with the global layer by `contextcap.MergeContext` (§1.3).

Connectors: effective set = global grants ∪ org/scene/person bindings
(deduped by connector id). Each connector is mounted once as `c<16hex>` via
the existing server relay; secrets never reach the task row, claim payload or
sandbox.

Credential selection per connector call, first match wins:

1. personal credential — only when the task's trigger person set one;
2. scene credential — only when the task's scene set one and the connector
   is in the agent's enabled offer catalog (a scene credential serves every
   member's run in the group; offering is the admin's opt-in to that);
3. org credential — only when the task's tenant org set one and, like a
   scene credential, the connector is in the enabled offer catalog
   (`contextcap.LayerCredentials` returns person, scene, org in that order);
4. workspace credential (existing sealed ciphertext or environment fallback).

A bearer or OAuth connector with no available credential in any applicable
layer is not mounted. An expired OAuth credential without a refresh token
does not count as available, so the next layer applies. An official app
connector is also not mounted until its tools have been discovered.
`auth_mode='none'` connectors need no credential. When the org, scene or
person layers cannot be read (a transient error, or a replica before
migrations 9400+), the task keeps its global connectors with workspace
credentials instead of losing them.

Skills: effective set = `agent_skill` (enabled) ∪ org/scene/person skill
bindings, deduped by skill id, filtered by `filterAgentSkillsForRuntime`, then
built-ins and the DWS skill as today. `ResolveTaskSkillBundles` accepts agent
skills plus, for a task with a scope, the requested refs that are enabled
offered skills, so a toggle between claim and bundle resolution cannot fail
the task and tasks without a scope load no extra skills.

Prompt components: the applied ones (nearest layer wins by name) are
appended to the task instructions as one block
(`EffectiveContext.PromptBlock`, headed 「## 场域上下文」), after every other
instruction layer (squad briefing, OKR catalog, dispatch brief). Custom MCP
servers: the applied scope servers are merged into the agent's `mcp_config`
(after the per-task Composio overlay) by server name
(`contextcap.MergeMCPConfig`, a scope server replaces an agent server of the
same name) before the runtime's capability gates. A Pi runtime without
the `mcp` capability cannot mount any of them: the claim leaves the scope
servers out (logged as `mcp_servers_unmounted`) and the task runs without
them, instead of being cancelled; the agent's own servers still meet the
gate. A scope server named like a managed server of the claim (`multica`, or
a connector server `c<16 hex>`) is left out, because the managed merge would
refuse the claim; the preview leaves it out too. A scope server named like a
server of one of the agent's Runner MCP bindings is left out at
`injectRunnerMCP` (warning `custom MCP servers share a Runner MCP server
name`), so the Runner mount keeps its name and the claim never fails and
requeues on `mcp_server_name_conflict`; the preview, which does not read the
Runner inventory, still lists it. A task without a scope is unchanged, byte
for byte.

Where it runs (`server/internal/handler/context_capabilities_task.go`):
`taskEffectiveContext` builds the effective context of a task (scope,
`LoadLayers`, `mergeTaskContext` = `contextcap.MergeContext` minus the
reserved servers above, which the admin 生效预览 calls too). The claim
(`buildClaimedTaskResponse`) builds it once over the agent's `mcp_config`
and uses it for the MCP servers, the skills and the prompt block; the
connector resolver (`authorizedTaskConnectors`, at claim in
`injectRunnerMCP` and on every relay call) builds it over the global
grants. Each claim of a dispatched task logs one line,
`context builder: claim context`, keyed by `task_id`: `org_id`, `skipped`
(why no layer applies: `a2a` and `no_dispatch` do not log, else `rerun`,
`not_tenant`, `earlier_binding`, `lookup_failed`), `layers`, the applied
prompt components and custom MCP servers as `layer:name`, the overridden
ones as `layer:name>nearer`, `mcp_servers_reserved`,
`mcp_servers_unmounted`, `invalid_mcp_layers` and the counts of skills and
connectors the scope layers switch on. Names
only, never prompt text, server configuration or credentials.

## 4. Credentials

- Scene and personal credentials live in `context_connector_credential`, sealed
  with `InternalConnectorSecretBox` (same key domain as workspace connector
  credentials on every replica). The sealed JSON binds
  `{workspace_id, agent_id, connector_id, scope_type, org_id, scope_key, bearer}`
  and every field is re-checked after `Open`, because secretbox has no AAD.
  A credential stored by an OAuth connect adds an `oauth` object
  (`{access_token, refresh_token, expires_at, token_type, scope, account,
  client_id}`), and `bearer` mirrors the current access token. `client_id`
  is the OAuth client that issued the tokens; refreshes use it.
- Write-only API: responses expose `hint` and `updated_at`, never the secret.
  A pasted token's hint is its last 4 characters, prefixed with `••••`; an
  OAuth credential's hint is `@<account>` (for example the GitHub login) or
  `OAuth`. `kind` is `oauth` or `bearer`.
- Two kinds of connector hold scene or personal credentials:
  - `auth_mode='bearer'` connectors take a pasted token.
  - Official app connectors (`auth_mode='oauth'`, see
    `docs/internal-mcp-connectors.md` "Official apps") take an account
    connected through the provider's OAuth consent. GitHub also takes a pasted
    Personal Access Token.
- OAuth tokens are refreshed under a row lock shortly before they expire and
  after an upstream 401, detached from the triggering call so a rotated
  refresh token is never lost. A refresh token the provider rejects
  (`invalid_grant`) deletes the credential; the page then shows the connector
  as not connected, and the Agent is told to ask the user to reconnect.
- An OAuth connect completes only in the browser that started it (a binding
  cookie set by the start response; see `docs/internal-mcp-connectors.md`
  "Browser binding"), so a forwarded sign-in link cannot store someone
  else's account in the sender's scope.

## 5. Proving who may configure what

The mobile page signs in with the existing DingTalk OAuth flow (a normal
`auth_method=dingtalk` Multica session). Authority comes from grants:
`context_config_grant(user_id, agent_id, org_id, scope_type, scope_key)`.

- Agent-issued link (primary, works without JSAPI signing). The built-in
  `multica` MCP tool `create_context_config_link` mints a token from the
  trusted task context of the current run. It is listed only for task tokens,
  refuses personal access tokens, and requires the
  calling Agent's own active (`dispatched`/`running`) task. Input
  `{"scope": "scene" | "person"}` is optional; the default is `person` when the
  dispatch conversation type is positively 1:1 (`single`, `p2p`, `private`,
  `direct`, the dispatcher's DM allow-list), otherwise `scene`. Manual reruns
  cannot mint links (§2):
  - in a group chat → a scene link bound to (agent, org_id, cid, title). Anyone
    who opens it within 30 minutes gets a 30-day scene grant (it was posted in
    the group, so its readers are group members);
  - in a 1:1 chat → a personal link bound to (agent, org_id, sender staffId).
    Single use, 15 minutes, 365-day person grant. A 1:1 chat is also a scene:
    when the dispatch carries the DM's openConversationId, the link stores it
    as `extra_scene_key` and redemption also grants that DM scene for the
    same 365 days (only the person takes part in it) and registers it as a
    `dm` scene in `agent_scene_config` (empty prompt), in the same
    transaction. The first account to
    redeem a person's link holds that personal scope: while its grant is live,
    a later personal link for the same person redeemed by a different account
    answers 409 and stays unconsumed, so a forwarded or leaked link cannot take
    over (redemptions for one person serialize on an advisory lock). Personal links are only
    minted in a positively 1:1 conversation, never in a group or a
    conversation of empty/unknown type (the tool tells the Agent to ask the
    user to 私聊 it), because everyone there could open them, nor for a merged
    multi-sender run.
  The tool returns (as `structuredContent` and as JSON text)
  `{url, dingtalk_url, scope, expires_at}`: `url` is
  `<app origin>/dingtalk/configure?link=<token>` (app origin = `MULTICA_APP_URL`,
  else `FRONTEND_ORIGIN`), `dingtalk_url` is
  `dingtalk://dingtalkclient/page/link?url=<urlencoded url>&pc_slide=true`.
  Tokens are 32 random bytes (base64url) stored only as SHA-256 hashes
  (`context_config_link`, with `source_task_id`). The Coordinator mints the
  same links for its capability answer (「你有哪些能力」,
  `docs/inbound-coordinator-loop.md` COORD.F04); the link reaches only the
  DingTalk reply, and the Coordinator transcript (`chat_message`, which
  managers and allow-listed members can read) stores `[configuration link]`
  in its place.
- JSAPI group picker (secondary). With a person grant for the agent in the
  page's tenant (a verified DingTalk identity; `org_id` names the tenant),
  the page signs `dd.config` through
  `GET /api/dingtalk/jsapi-config` (`jsApiList: ["biz.chat.chooseConversationByCorpId"]`)
  and calls `biz.chat.chooseConversationByCorpId`; the server converts `chatId`
  with `POST /v1.0/im/chat/{chatId}/convertToOpenConversationId` using the corp
  app token and grants the scene (source `jsapi`, 30 days) only if it is a
  known group scene of this agent under its current org (`scene_memory`,
  platform `dingtalk`, kind `group`). Both calls need the direct DingTalk
  client (`DINGTALK_CLIENT_ID`/`SECRET` without `DINGTALK_AGENT_BASE_URL`); the
  private-agent client answers "unsupported" and the API returns 503.
  Residual risk: DingTalk offers no general "is this user in this group" API,
  so the server trusts that only group members can obtain a group's chatId.
  `chat_id` is therefore required: an openConversationId is not a secret
  (every scene-grant holder sees it as `scope_key`, and it outlives
  membership), so `open_conversation_id` is only cross-checked against the
  converted chatId, never accepted alone. For an agent without
  `agent_dingtalk_identity` (org `""`), the `scene_memory` check matches the
  agent's group scenes of any org, because scene memory records them under
  the dispatch's DWS org.
- Workspace membership is not required: ordinary group members who talk to the
  digital employee are usually not Multica members. They only ever see offered
  item names, descriptions and tool names, never URLs or credential status of the
  workspace library.
- Managers (no link needed). A caller with the agent-manage permission of the
  admin routes (a member of the agent's workspace who is a workspace
  owner/admin or the agent's owner, as `canManageAgent` defines it) may
  configure every SCENE of the agent from the configure page without a grant:
  the agent is listed with `access: "manager"`, its detail lists all of the
  agent's scenes (group and 1:1, the admin scene union of §6 "Scenes", source
  `manager`, no expiry), and the scene read, bindings, credentials and connect
  routes accept the manager for any scene the agent has seen (404 for another
  key). The personal scope still needs the person grant: a manager is not that
  person. Managers also configure the enterprise (org) scope of every tenant
  (bindings, credentials, connect); a caller with any live grant under a
  tenant org reads that org's scope read-only (writes answer 403 `{error,
  code: "manager_only"}`). Every offer gate applies to managers exactly as
  to grant holders, and the OAuth callback re-checks the manager permission
  like a grant. Manager access is computed per request and never stored as
  a grant.
- 1:1 chat scenes (§1.1): the effective scope is the person's. The caller's
  live person grant for that staffId or live scene grant for the 1:1 chat's
  key (a DM link grants both) gives full access, accounts included. A
  manager may read it and write its bindings and custom MCP servers, but not
  store, remove or connect an account there (403 `{error, code:
  "person_only"}`): a manager cannot connect someone else's account. For a
  1:1 chat whose person is unknown, writes answer 409 `{error, code:
  "dm_person_unknown"}` and reads return the scene with `scope: null`.

## 6. API

Mobile (requires an `auth_method=dingtalk` human session; `RequireDingTalkHumanActor`;
not workspace-scoped). Every
agent-scoped call loads the agent (must exist, not archived, a user agent) and
works in one tenant org of the agent (§1.3): the request's optional `org_id`
(query parameter on GETs and DELETE, body field on PUT/POST; an org scope
without one works in the org its key names), else the agent's identity org;
an `org_id` that is not a tenant answers 404 `{error, code:
"tenant_not_found"}` to a caller who manages the agent or holds a live grant
for it, and the same 403 a tenant org would give to anyone else, so the
routes do not tell which org ids are tenants. Grants, scenes and person scopes are read under that
org only. `scope_type` may also be `org` (scope key = the org id): managers
write it, holders of any live grant under that org read it (writes 403
`manager_only`). The call then
requires the caller's live grant under that org: a scene read or
write needs a scene grant for that exact cid, or managing the agent and the cid
being a scene the agent has seen (§5 "Managers"; 404 for an unknown scene); a
person read or write needs a person grant for that exact staffId (managers
included). A request naming a 1:1 chat scene (`scope_type: "scene"` with its
key) acts on that chat's person scope with the §5 "1:1 chat scenes"
authority: 403 `person_only` for a manager's credential write or connect,
409 `dm_person_unknown` for a write when the person is unknown. Bodies are size-limited and reject unknown
fields (an old replica therefore answers 400 to a body carrying `org_id`;
send it only for an org other than the identity org). `B = {resource_type, resource_id, enabled}`,
`C = {connector_id, hint, updated_at, kind}` (`kind` is `oauth` or `bearer`);
every `bindings` list contains only resources that are currently in the
enabled offer catalog. A personal connector binding also carries
`share_in_groups` (boolean, 「在群聊中由我触发时也可用」, default false);
other bindings omit it. Scene entries `{scope_key, scope_title, source,
expires_at, kind, org_id}` carry `kind`: `group` or `dm` (the `agent_scene_config`
kind, else `group`; a 1:1 scene reached through a personal link is always
registered there as `dm`) and the tenant org they live in.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/context-capabilities/links/redeem` | `{token}` → grant (source `agent_link`); returns `{agent_id, workspace_id, scope_type, scope_key, scope_title, org_id}` (`org_id` is the org the link was minted in; pass it on the next calls when it is not the identity org; for a 1:1 personal link with `extra_scene_key` the DM scene grant is added as well, §5); 410 when unknown, malformed, expired or (person) already consumed; 409 when a different account already holds that person's live grant. The link and the grant commit in one transaction |
| GET | `/api/context-capabilities/agents` | `{agents: [{id, name, avatar_url, workspace_id, access, scopes: [{scope_type, scope_key, scope_title, source, expires_at, org_id}]}]}`: agents with a live grant for the caller under one of the agent's tenant orgs (`access: "grant"`, newest grant first; grants under an org that is not a tenant are left out), then the other non-archived user agents the caller manages (`access: "manager"`, `scopes: []`, by name). An agent both granted and managed is listed once with `access: "manager"` and its grants. Older backends send no `access` (read it as `grant`) |
| GET | `/api/context-capabilities/agents/{agentId}?org_id=` | `{agent, global: {connectors: [{id, name, catalog_slug}], skills: [{id, name, description}]}, offers: {connectors: [{id, name, tools, accepts_credential, credential_required, catalog_slug, auth_mode, accepts_pat, oauth_available, install_url?}], skills}, person: null \| {scope_key, scope_title, source, expires_at, bindings: [B], credentials: [C]}, scenes: [{scope_key, scope_title, source, expires_at, kind, org_id}], access, jsapi_available, tenant: null \| {org_id, name, source}, tenants: [{org_id, name, source}], org: null \| {scope_key, scope_title, bindings: [B], credentials: [C], can_edit}}` for one tenant org: `org_id`, else the identity org, else (a caller who does not manage the agent and holds no grant there) the org of the caller's newest grant. `tenant` is that org (null for an agent without a DingTalk identity when none was named), `tenants` the orgs the caller may switch to (every tenant for a manager, else those holding a grant of the caller), `org` its enterprise layer (null when the caller may not read it); `can_edit` is true for a manager, and a reader gets credential hints blanked. Needs any grant for the agent in that org or managing it (else 403). `access` is `manager` or `grant`. `scenes` lists the granted scenes and, for a manager, every other scene of the agent (newest activity first, at most 1000, `source: "manager"`, `expires_at: ""`, titles and kinds from the admin scene union). `global.connectors` are the agent's granted, enabled connectors with a ready workspace credential (and, for official apps, discovered tools). `catalog_slug` names the official app (`""` for custom connectors). `auth_mode` is `none`, `bearer` or `oauth`. `accepts_credential` means the connector accepts a pasted token: a Bearer connector, or an official app that allows a PAT. `accepts_pat = auth_mode == 'oauth' && accepts_credential`. `credential_required` means the connector uses a credential (Bearer or OAuth) and has no workspace credential. `oauth_available` means the server can run the app's OAuth sign-in (GitHub needs `GITHUB_APP_CLIENT_ID` and `GITHUB_APP_CLIENT_SECRET`; every app needs the connector credential key and an app origin, the other deployment checks of the start endpoint); the page shows 连接 only for a literal `true` (a missing field from an older backend hides it too) and offers the PAT form when `accepts_pat`. `install_url` is the GitHub App installation page (omitted when there is none). `jsapi_available` is true only when H5 signing is configured and the caller has a person grant (the resolve endpoint needs one) |
| GET | `/api/context-capabilities/agents/{agentId}/scenes/{sceneKey}?org_id=` | `{scene: {scope_key, scope_title, source, expires_at, kind, org_id}, scope: null \| {type, key, title}, bindings: [B], credentials: [C], can_connect}` (scene grant or manager required; for a 1:1 chat also its person's grant; a manager without a grant gets `source: "manager"`). `scope` is where the configuration lives: `{type: "scene", key: <cid>}` for a group, `{type: "person", key: <staffId>, title}` for a 1:1 chat, `null` (with empty lists) for a 1:1 chat whose person is unknown. `bindings` and `credentials` are those of `scope` (a person's connector bindings carry `share_in_groups`). `can_connect` is whether the caller may store, remove or connect credentials there (false for a manager on a 1:1 chat). A manager on a 1:1 chat who is not a workspace owner/admin gets that person's credentials with `hint: ""` (the connected state, `kind` and `updated_at` stay), as on the admin scene page and the connected-apps page. `kind` comes from the admin scene union (a key it does not know is `group`). The key may be percent-encoded |
| PUT | `/api/context-capabilities/agents/{agentId}/bindings` | `{scope_type, scope_key, org_id?, resource_type, resource_id, enabled, share_in_groups?}` → `{binding: B}` (`scope_type` `org`, `scene` or `person`; an `org` write needs a manager); 403 unless the resource is in the enabled offer catalog (enable and disable alike). `share_in_groups` is only accepted for a person scope (`scope_type='person'`, or a 1:1 chat's scene key, which maps to its person) and `resource_type='connector'` (else 400), and only from the person (a manager on a 1:1 chat gets 403 `person_only`); omitted keeps the stored value |
| PUT | `/api/context-capabilities/agents/{agentId}/credentials` | `{scope_type, scope_key, org_id?, connector_id, bearer}` → `{credential: C}` (an `org` credential needs a manager and, like a scene credential, an offered connector). The connector must be enabled and offered to the agent, or (person scope only) globally granted to it (else 403). It must accept a pasted token: `auth_mode='bearer'`, or an official app that allows a PAT (GitHub) (else 400). bearer is 1..4096 bytes, with no CR/LF/NUL and no surrounding whitespace; 503 without a credential key. The first credential of an official app also discovers and pins its tools |
| DELETE | `/api/context-capabilities/agents/{agentId}/credentials?scope_type=&scope_key=&connector_id=&org_id=` | 204, idempotent; allowed after the offer was removed. For an OAuth credential this is "disconnect" (the provider grant is not revoked) |
| POST | `/api/context-capabilities/agents/{agentId}/connections/start` | `{scope_type, scope_key, org_id?, connector_id, return_to?}` → `{authorize_url}` (an `org` scope needs a manager; the state stores the tenant org and the callback re-checks that it is still a tenant) for connecting an official app account through OAuth, plus the browser binding cookie (the WebView that calls it must also open the URL). The caller needs a live grant for exactly that scope, or, for a group scene, to manage the agent (403; 404 for a manager's unknown scene). A 1:1 chat scene connects its person's account: the state stores the person scope (and, sealed, the requested chat, so the callback re-checks that same request), only the person may start it (403 `person_only` for a manager), and an unknown person answers 409 `dm_person_unknown`. The connector must be an enabled official app that is offered to the agent (scene) or offered or globally granted (person); otherwise 403 with `code: "forbidden"`, including for an unknown connector. Other errors are `{error, code}`: 400 `not_oauth` or `invalid_return_to`; 503 `oauth_unavailable`, `app_origin_missing` or `credential_storage_unavailable`; 502 `provider_unavailable`. The browser returns to `return_to` (default `/dingtalk/configure?agent=<id>`) with `?connected=<slug>` or `?connect_error=<code>`. The callback re-checks the grant and offer, stores the credential for that scope and turns the connector on for it (see `docs/internal-mcp-connectors.md` "Official apps") |
| POST | `/api/context-capabilities/agents/{agentId}/scenes/resolve?org_id=` | `{chat_id, open_conversation_id?}` → `{scene: {scope_key, scope_title, source, expires_at, kind: "group", org_id}}` (JSAPI path, see §5; group scenes only) in the tenant `org_id` names (default the identity org): the person grant must be in that tenant and the scene grant is stored under it. 400 without `chat_id` or when `open_conversation_id` differs from the converted one; 403 without a person grant in that tenant or for a group the agent never served there; 404 `tenant_not_found`; 503 when chatId conversion is unavailable; 502 when DingTalk rejects the chatId |
| GET | `/api/dingtalk/jsapi-config?url=` | `dd.config` signature `{corp_id, agent_id, time_stamp, nonce_str, signature}` for the page URL without `#fragment`. Any authenticated human (`RequireHumanActor`). The URL must be absolute http(s) on the app origin (`MULTICA_APP_URL` / `FRONTEND_ORIGIN`); without an app origin the endpoint answers 503 rather than signing arbitrary pages. `signature = sha1("jsapi_ticket=<t>&noncestr=<n>&timestamp=<ts>&url=<url>")` in hex, where `<url>` has its query percent-decoded like DingTalk's reference signer and `time_stamp` is Unix seconds. The ticket (`GET {oapi}/get_jsapi_ticket`) is cached in process until 5 minutes before expiry and never returned; the corp access token it is fetched with is redacted from transport errors before they are logged. 503 when `DINGTALK_H5_CORP_ID` / `DINGTALK_H5_AGENT_ID` are unset, no app origin is configured, or the direct client is unavailable |

Admin (workspace routes, human actor, the same permission as editing the
agent's skills: workspace owner/admin or the agent owner). Adding a connector
offer needs a workspace owner/admin, like the connector library and its
grants: an agent owner who is only a member may keep or remove connector
offers, and their GET lists only the connectors already offered.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/agents/{id}/context-capabilities` | `{enabled, library: {connectors: [{id, name, enabled, auth_mode}], skills: [{id, name, description}]}, offers: {connector_ids, skill_ids}, orgs: [{scope_key, scope_title, bindings: [B], credential_count}], scenes: [...], persons: [...], configure_url}` (`orgs` are the enterprise levels with configuration, `scope_key` = the org id; an older backend omits it); `configure_url = <app origin>/dingtalk/configure?agent=<id>` ("" without an app origin). For an owner/admin the library lists every workspace connector, so saving never drops connector offers. Offers whose connector or skill no longer exists are omitted (and dropped by the next save). `enabled` is always true (clients still handle false from older backends) |
| PUT | `/api/agents/{id}/context-capabilities/offers` | `{connector_ids, skill_ids}` (both required, ≤ 256 each) replaces the catalog in one transaction and returns the GET body; 400 for an id outside the workspace library or a skill another agent's Git source manages (the `SetAgentSkills` rule); 403 when a non-admin adds a connector offer |

### Connected apps (admin)

Same routes group and permission (human actor, workspace owner/admin or the
agent owner). Read-only views for the 连接应用 block of the agent's 连接器
tab (§1.1); every action uses an existing route: add the app
(`POST /api/workspaces/{ws}/connector-catalog/{slug}`, then the offers PUT
below), grant or revoke it
for the agent and 允许写操作 (`PATCH .../internal-connectors/{connectorId}`
with `agent_ids` / `write_enabled`), offer it (`PUT
/api/agents/{id}/context-capabilities/offers`), connect the shared account
(`POST .../internal-connectors/{connectorId}/oauth/start`, or a GitHub PAT
with `PUT .../credential`), disconnect it (`DELETE .../credential`, below)
and refresh tools (`POST .../tools/refresh`). Those workspace routes stay
workspace-admin only; `can_admin` tells the page whether the caller is one.

`A = {slug, name, auth_kind, oauth_available, allows_pat, install_url,
connector_id, added, enabled_in_workspace, global_enabled, offered,
write_enabled, tools: {discovered, allowed, read_only}, shared_account:
{connected, account, source}, usage: {scenes_enabled, scenes_connected,
persons_enabled, persons_connected}}`, one per catalog app in catalog order:

- `auth_kind` is `oauth_dcr` or `oauth_github_app`; `oauth_available` is the
  mobile field's rule (the app's OAuth is configured, and the deployment has
  a connector credential key and an app origin), so 连接 is never shown when
  the start endpoint would refuse it for configuration reasons; `allows_pat`
  means a Personal Access Token can be saved on this deployment (the app
  accepts one, GitHub, and the connector credential key is configured, which
  `PUT .../credential` requires), so the PAT form is never offered when the
  save would answer 503; `install_url` is the GitHub App installation page
  or `""`. There is no `description`; the page supplies its own copy.
- `connector_id` is the workspace catalog connector, `null` until the app is
  added to the workspace. `added` means the app is on this agent: the
  connector exists and the agent grants (`global_enabled`) or offers
  (`offered`) it. A connector that exists but is neither granted nor offered
  (just added, or removed from this agent) has `added: false` and a
  `connector_id`, so the page shows its controls. `enabled_in_workspace` is
  the library switch (`internal_connector.enabled`, false without a
  connector). The connector library is workspace-admin only (as in the
  offer admin view): for an agent owner who is not a workspace admin, an
  app whose connector is neither granted nor offered here reads like an app
  not in the workspace (`connector_id: null`, no tools, no shared account;
  the detail has no `tool_list` or usage).
- `tools`: `discovered` counts the last discovery snapshot, `read_only` the
  discovered tools marked read-only, `allowed` the pinned tools.
- `shared_account.connected` is true only while the workspace credential is
  usable (a pasted token, or an OAuth token that is unexpired or
  refreshable: the `credential_ready` rule); `account` is its hint (`@login`
  or `OAuth` for an OAuth account, `••••abcd` for a pasted token, `""` when
  not connected, and always `""` for a caller who is not a workspace admin,
  like the library's `credential_account`). `source` is `workspace` for the
  stored workspace credential and `environment` for the operator's
  `MULTICA_INTERNAL_MCP_BEARER_<id>` fallback, which serves runs (so it
  counts as connected) but has no hint and cannot be removed by
  `DELETE .../credential` (the page offers no 断开 for it); `""` when not
  connected.
- `usage` counts the agent's scene and person scopes under its current
  DingTalk org: `*_connected` = a stored credential exists for the app in
  that scope (OAuth account or pasted token), whether or not its switch is
  on; `*_enabled` = an enabled binding while the app is offered (a binding
  of an app that is not offered is stored but never applies, so it is not
  counted). Rows stored on a 1:1 chat's own scene key (the admin scene
  union says `dm`) are skipped: a 1:1 chat's configuration is its person's
  (§1.1) and the runtime ignores those rows. A 1:1 chat's account and
  switches show up as its person.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/agents/{id}/connected-apps` | `{apps: [A], can_admin}`; `can_admin` is true for a workspace owner/admin |
| GET | `/api/agents/{id}/connected-apps/{slug}` | `A` plus `scenes: [{scene_key, title, kind, enabled, connected, account}]`, `persons: [{scope_key, title, enabled, connected, account, share_in_groups}]`, `tool_list: [{name, read_only, allowed}]` and `can_admin`; 404 for a slug not in the catalog. `scenes` and `persons` list only the scopes that count in `usage` (enabled or connected); `account` is the scope credential's hint, for a person only to workspace admins (`""` for other agent managers, like the shared account's hint). Scene titles and kinds come from the admin scene union (newest activity first, unknown keys last); person titles are the binding's snapshot or the newest grant title (the DingTalk display name), people sorted by title |
| DELETE | `/api/workspaces/{ws}/internal-connectors/{connectorId}/credential` | Workspace owner/admin (`RequireWorkspaceMCPHumanIssuer` plus the admin role): removes the workspace's stored credential of a connector, i.e. disconnects an official app's shared account. 204, idempotent; 404 for an unknown connector, 400 for a malformed id. The provider grant is not revoked and an environment credential (`MULTICA_INTERNAL_MCP_BEARER_<id>`) is not affected; a concurrent OAuth refresh holds the row lock, so it cannot restore the credential |

### Scenes (admin)

Same routes group and permission (human actor, workspace owner/admin or the
agent owner; agent actors get 403). Scene keys are percent-encoded
openConversationIds (decoded once, like the mobile scene route); a malformed
key answers 400. A scene of an org is one the agent has seen there: the
union of its `scene_memory` rows (under that org, or any org for the org
"" of an agent without a DingTalk identity), its inbound Coordinator
conversations (`inbound_coordinator_job` openConversationIds recorded under
that agent org; a job that recorded none belongs to the identity org), its
`agent_scene_config` rows, its scene bindings, scene credentials and scene
prompt components (a group can store a token without turning anything on;
the scene still lists, so a manager can open it and remove the credential).
Kind is `dm` only on positive evidence: the newest Coordinator job's
conversation type is a 1:1 type (`single`, `p2p`, `private`, `direct`); when
that job has no type, or there is no job, the configured kind
(`agent_scene_config`, written by a 1:1 link redemption); else `group`.
`scene_memory.scene_kind` is not used, because the memory writer records
`dm` for every type that is not `group`, empty or unknown types included. A
1:1 chat's configuration (bindings, credentials, prompt components, custom
MCP servers) is its person's (§1.1): the Context Builder routes act on that
scope through the same resolver as the mobile routes (the caller manages the
agent; a manager who also holds the person's grant is that person).

`S = {scene_key, kind, title, org_id, last_active_at, inbound_session_id,
inbound_count, memory_id, has_prompt}` (`has_prompt`: the scene has prompt
components):
`inbound_session_id` is the newest Coordinator chat session of the
conversation (open its transcript with
`GET /api/agents/{id}/coordinator-conversations/{inbound_session_id}/messages`)
or `""`; `inbound_count` counts the sessions that transcript shows (the
messages endpoint's anchor partition: same endpoint namespace, source
platform and source type as that session); sessions of the same
conversation under another endpoint namespace or source stay reachable in
the full inbound conversation list. `memory_id` is the `scene_memory` row id
(open it with `GET /api/agents/{id}/scene-memory/{memory_id}`, which also
serves rows beyond the 200 newest that the list endpoint returns) or `""`;
`last_active_at` is the newest update across the sources. The list carries
no binding counts; a scene's bindings are in its detail.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/agents/{id}/scene-memory/{memoryId}` | One scene memory row (the scene memory list item shape); same permission as the other scene memory routes; 400 for a malformed id, 404 when the agent has no such row |

The agent-wide scene list (`GET /api/agents/{id}/scenes`) and the scene
detail, prompt, bindings and custom MCP server routes of
`/api/agents/{id}/scenes/{sceneKey}` are gone: the per-tenant `groups` and
`persons` lists and the Context Builder routes below replace them (a 1:1
chat is reached through its person, or by its key as a scene node). The
lists and the single-scene lookup behind the context nodes read the agent's Coordinator jobs through
`inbound_coordinator_job_agent_conversation_idx` (9417, `(agent_id,
BTRIM(command #>> '{event,data,conversation,openConversationId}'))`): the
list scans the agent's jobs only, and a single scene probes its own
conversation. The key filter is a plain predicate so a cached generic plan
still uses the index.

### Tenants (admin)

Same routes group and permission. `T = {org_id, name, source, group_count,
person_count}`: `source` is `identity` for the agent's DingTalk identity org
(listed first, with or without a row) or `created`; `group_count` counts the
org's group scenes and `person_count` its people (§1.3), from every scene
and person source. Errors carry `{error, code}`.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/agents/{id}/tenants` | `{tenants: [T], unassigned_orgs: [{org_id, group_count, person_count}]}`; `unassigned_orgs` are the non-empty orgs scene or person data mentions without a tenant, by org id |
| POST | `/api/agents/{id}/tenants` | `{org_id, name}` → 201 `{tenant: T}`. `org_id` 1..64 of `[A-Za-z0-9_-]` (400 `invalid_org_id`), `name` trimmed, 1..64 characters (400 `invalid_name`); 409 `tenant_exists` for a tenant row or the identity org |
| PATCH | `/api/agents/{id}/tenants/{orgId}` | `{name}` → `{tenant: T}`; renaming the identity tenant stores a row for it; 404 `tenant_not_found`, 400 `invalid_name` |
| DELETE | `/api/agents/{id}/tenants/{orgId}` | 204: removes the tenant row and its org-scope configuration (bindings, credentials, prompt components, custom MCP servers, pending org OAuth connects) in one transaction; group and person data stays and the org shows as unassigned. 409 `identity_tenant` for the identity org, 404 `tenant_not_found` |
| GET | `/api/agents/{id}/tenants/{orgId}/groups?limit=&offset=` | `{scenes: [S], has_more}`: the tenant's group scenes (1:1 chats left out), newest activity first; `limit` 1..200 (default 50), `offset` ≥ 0, else 400 |
| GET | `/api/agents/{id}/tenants/{orgId}/persons` | `{persons: [{staff_id, title, dm_scene_key, last_active_at}]}`: the tenant's people, newest activity first. `title` is the newest 1:1 sender display name, else the person grant title, else the binding title; `dm_scene_key` their 1:1 chat with the agent (`""` when unknown) |

### Context nodes (admin)

A node of the 场域 tree is `(orgId, scopeType, scopeKey)` under
`/api/agents/{id}/tenants/{orgId}/context/{scopeType}/{scopeKey}`:
`scopeType` `org` (`scopeKey` = `orgId`, else 400), `scene` (a scene of that
org, 404 otherwise; a 1:1 chat's key maps to its person, `scope: null` and
409 `dm_person_unknown` on writes while its person is unknown) or `person`
(a person of that org, §1.3; 404 otherwise). Same routes group and
permission (workspace owner/admin or agent owner); managers write the org,
group and person scopes' bindings, prompt components and custom MCP
servers; credentials and connects of an org or group scope are managers',
of a person scope only that person's (a live person grant of the caller;
403 `person_only` otherwise). Unknown tenant: 404 `tenant_not_found`.

`N = {scope: null | {type, org_id, key, title}, scene: null | S, prompts:
[P], connectors: [{id, name, catalog_slug, auth_mode, accepts_credential,
accepts_pat, oauth_available, install_url?, credential: {connected,
account}, enabled}], skills: [{id, name, description, enabled}],
mcp_config: object | null, mcp_config_redacted, can_connect, effective:
{prompts: [{name, text, layer, overridden_by?}], connectors: [{id, name,
layer}], skills: [{id, name, layer}], mcp_servers: [{name, layer,
overridden_by?}]}}` with `P = {id, name, order, text, updated_by_name,
updated_at}`:

- `scope` is where the node's configuration lives (the org, the group, or
  the person; a 1:1 chat's person); `scene` is the group of a scene node or
  the person's 1:1 chat (null for org nodes and people without one), for
  入站记录 and 记忆.
- `connectors` and `skills` are the offer catalog (connectors enabled in the
  library, with the configure page's flag rules); `enabled` is this scope's
  binding, `credential` this scope's stored credential (`account` is its
  hint; for a person scope only to workspace admins and that person).
- `mcp_config` is the scope's custom MCP servers (null when none; withheld
  with `mcp_config_redacted: true` when the workspace always redacts
  secrets).
- `can_connect`: the caller may store, remove or connect credentials of
  `scope` (managers for org and group scopes; only the person for a
  person scope).
- `effective` is `contextcap.MergeContext` over global + org (+ group for a
  scene node, + person for a person node): `layer` is `global`, `org`,
  `scene` or `person`; `overridden_by` names the nearer layer whose
  component of the same name replaces this one (omitted when it applies).
  Connectors switched off in the library and deleted skills are left out.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `.../context/{scopeType}/{scopeKey}` | `N` |
| PUT | `.../context/{scopeType}/{scopeKey}/bindings` | `{resource_type, resource_id, enabled}` → `{binding: {resource_type, resource_id, enabled, updated_by_name, updated_at}}`; offer-gated (403 unless offered, enable and disable alike) |
| PUT | `.../context/{scopeType}/{scopeKey}/prompts` | `{prompts: [{name, order, text}]}` (required) replaces the scope's prompt components → `{prompts: [P]}` ordered by order, then name. At most 20; names trimmed, 1..64 characters, unique (400 `duplicate_prompt_name`); texts trimmed, 1..8000 characters, no NUL (400 `invalid_prompts`); `[]` clears them. A component whose name stays keeps its id, and its update stamp when neither order nor text changed |
| PUT | `.../context/{scopeType}/{scopeKey}/mcp-config` | `{mcp_config: object \| null}` (required) → `{mcp_config}`: the agent `mcp_config` format, a JSON object of at most 64 KiB; `null` or `{}` clears it |
| PUT | `.../context/{scopeType}/{scopeKey}/credentials` | `{connector_id, bearer}` → `{credential: C}`; the mobile PUT's connector rules (an org or group credential needs an offered connector) |
| DELETE | `.../context/{scopeType}/{scopeKey}/credentials?connector_id=` | 204, idempotent |
| POST | `.../context/{scopeType}/{scopeKey}/connections/start` | `{connector_id, return_to?}` → `{authorize_url}` plus the browser binding cookie; the mobile start's rules and errors; the state stores the tenant org |

## 7. Rollout and gating

- Always on: there is no feature flag for the scene and personal layers.
  Connectors are always on as well; they still need a credential key and
  the host allowlist (`docs/internal-mcp-connectors.md`).
- New env: `DINGTALK_H5_CORP_ID`, `DINGTALK_H5_AGENT_ID` (JSAPI signing only;
  whitelisted in `src/main.sh`, documented in `.env.example`). The JSAPI path
  also needs the direct DingTalk corp client; the agent-issued link flow needs
  none of these, only an app origin (`MULTICA_APP_URL` or `FRONTEND_ORIGIN`).
- Rolling deploy: an old replica serving `CallInternalConnector` only knows
  global grants, so a scene-only connector call can get 403 during the window,
  and a globally granted connector call it serves uses the workspace
  credential even when the trigger person or group set their own.
  Likewise, a task claimed on a new replica can carry scene/personal skill
  refs; if the daemon's `ResolveTaskSkillBundles` call reaches an old replica,
  it only accepts agent skills and answers 404 "skill bundle not found", which
  fails that task. This window exists in every rollout of this release
  (there is no flag to hold it back); it closes once every replica runs this
  binary. Manual reruns created by an old replica
  without `rerun_of_task_id` (issue-level reruns) carry no replay marker and
  can still inherit layers until the rollout completes.
- Official apps (9409+): an old replica answers 404 on the new routes and
  its credentials PUT still refuses a GitHub PAT (400). It does not check
  `auth_mode` when mounting or relaying (it treats `oauth` like `bearer`);
  only its host allowlist keeps it from calling catalog URLs, so keep the
  catalog hosts out of `MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES` during
  the rollout (`docs/internal-mcp-connectors.md` "Rollout"). A sealed OAuth
  credential keeps `bearer` equal to the access token, so the payload stays
  readable by the old binary's opener. The GitHub connect needs the GitHub
  App's callback to stay `/api/github/authorize`.
- Tables are fork-owned (9400+), created with `IF NOT EXISTS`, no FKs, every
  index `CONCURRENTLY` in its own file, and registered in the workspace deletion
  manifest. Skill deletion (manual and managed-agent source sync) sweeps
  `resource_type='skill'` bindings.
- Configuration architecture (9413-9416): `agent_scene_config` (9413) with
  its unique `(agent_id, platform, org_id, scene_key)` index (9414, an
  `ON CONFLICT` arbiter with an invalid-index pre-migration hook),
  `context_capability_binding.share_in_groups` (9415) and
  `context_config_link.extra_scene_key` (9416), all additive and
  idempotent; `agent_scene_config` is swept on workspace deletion. During
  the rollout an old replica answers 404 on `/api/agents/{id}/scenes*`,
  omits `share_in_groups` and scene `kind` (clients should read a missing
  kind as `group` and a missing `share_in_groups` as false), rejects a bindings PUT
  that carries `share_in_groups` (400, unknown field), keeps the stored
  `share_in_groups` when it writes a binding, and redeems a 1:1 link into the
  person grant only (no DM scene grant; mint a new link after the rollout).
- 9417 adds `inbound_coordinator_job_agent_conversation_idx` concurrently in
  its own file (a plain performance index, no pre-migration hook: an invalid
  leftover only costs speed). An old replica does not serve
  `GET /api/agents/{id}/scene-memory/{memoryId}` (405); the scene detail shows
  its memory load error until the rollout completes.
- Connected apps and manager access (no migration): an old replica answers
  404 on `/api/agents/{id}/connected-apps*` and 405 on `DELETE
  .../internal-connectors/{connectorId}/credential`, lists only granted
  agents on the configure page (no `access`), refuses a manager without a
  grant (403) and reports `oauth_available` without the credential key and
  app origin checks. Clients treat a missing `access` as `grant`; a
  manager's configure page can show 「暂无可配置的智能体」 when served by such
  a replica until the rollout completes.
- 1:1 chat = person and custom MCP servers (9418-9419):
  `context_scope_mcp_config` (9418, `CREATE TABLE IF NOT EXISTS`, no FKs,
  swept on workspace deletion like the other context tables) and its unique
  `(agent_id, scope_type, org_id, scope_key)` index (9419, alone in its file,
  `CONCURRENTLY`, an `ON CONFLICT` arbiter with the invalid-index
  pre-migration hook). Both are additive and idempotent. During the rollout
  an old replica answers 404 on `PUT .../scenes/{sceneKey}/mcp-config`,
  omits `scope`, `mcp_config`, `can_connect` and the connector credential
  fields from the scene detail (clients read a missing `scope` as the scene
  itself and a missing `can_connect` as false), and still treats a 1:1 chat
  as its own scene: a write it serves lands on the chat's scene key, which
  the new binary ignores (§1.2), so repeat it after the rollout. A connect
  started on a new replica for a 1:1 chat stores the person scope; if an old
  replica serves its callback, it re-checks the person grant, so a manager
  can never complete one.
- Tenants, the org scope and prompt components (9420-9428): `agent_tenant`
  (9420) and `context_prompt_component` (9422), `CREATE TABLE IF NOT
  EXISTS`, no FKs, swept on workspace deletion; their unique indexes
  `(agent_id, org_id)` (9421) and `(agent_id, scope_type, org_id, scope_key,
  name)` (9423), each alone in its file, `CONCURRENTLY`, `ON CONFLICT`
  arbiters with the invalid-index pre-migration hook; 9424-9427 widen the
  `scope_type` checks of `context_capability_binding`,
  `context_connector_credential`, `context_scope_mcp_config` and
  `connector_oauth_state` to `org` (`DROP CONSTRAINT IF EXISTS` + `ADD
  CONSTRAINT`, every existing row passes); 9428 copies every non-empty
  `agent_scene_config.prompt` into a 「场域提示词」 component (a 1:1 chat's
  into its person's scope, skipped while the person is unknown; `ON
  CONFLICT DO NOTHING`, so a replay keeps edited components). All are
  idempotent; the down migrations drop org rows before narrowing a check.
  During the rollout an old replica answers 404 on `/api/agents/{id}/tenants*`
  (the 场域 tree stays empty until the rollout completes), still serves the
  removed `/api/agents/{id}/scenes*` routes (a scene prompt it saves lands in
  `agent_scene_config.prompt`, which the new binary no longer reads, so
  re-enter it as a component after the rollout), ignores org rows (its
  queries name `scene` and `person` explicitly) and rejects a mobile body
  carrying `org_id` (400, unknown field).
- Claim-time Context Builder (§3, same release, no further migration):
  during the rollout a task claimed on an old replica gets no org layer, no
  prompt components and no custom MCP servers of any scope, and its group
  and personal layers only under the identity org (the old binding drop).
  A relay call an old replica serves resolves without the org layer: an
  org-only connector answers 403 and an org credential is not used. A skill
  ref an org layer added at a new replica's claim is refused (404, which
  fails the task) when `ResolveTaskSkillBundles` reaches an old replica and
  the task has no group or single sender, as scene skills were in the first
  rollout of §1.

## 8. Known limitations (v1)

- `share_in_groups` is stored and shown but not applied (§1.2): a person's
  whole layer (bindings, prompt components and custom MCP servers) still
  applies to group runs that person triggers alone, whatever the switch
  says, and the group sees the run's output. Do not put a person's own token
  into a person-level custom MCP server's headers; connect the account
  instead. The next round gates the personal layer in group runs on the
  switch.
- An agent without a DingTalk identity has its scene and person
  configuration under org `""` (§2), which is not a tenant row: the web 场域
  tree does not list it; it is configured on the mobile page (links and the
  group picker) only.
- The mobile configure page shows the enterprise (org) layer's connectors
  and skills only; prompt components and custom MCP servers of every level
  are edited on the web (agent detail → 场域).
- A 1:1 chat is bound to its person only once the Coordinator has seen that
  person write in it (a job with a sender staffId) or the person redeemed a
  personal link minted there; until then the chat has no configuration.
  Old rows stored on a 1:1 chat's own scene key are ignored, not migrated.
- A tenant's group list, its people and the tenant counts read every
  Coordinator job of the agent (through the 9417 index, not the whole
  workspace) and decode their JSON to group them; a single-scene lookup
  reads only that conversation's jobs. The mobile page's scene `kind` lookup
  reads only the indexed `agent_scene_config`.
- Groups and people are listed per tenant; memory rows and conversations of
  an org that is not a tenant are reachable through its unassigned org
  (create a tenant for it) or through 其他记录 (the full lists, the memory
  list capped at the 200 newest rows).
- A tenant's people list is not paged; it holds every person the agent has
  seen there.
- The connected-apps usage counts and lists cover group scenes and people of
  the identity org; enterprise (org) credentials and switches, and the
  groups and people of other tenants, are not counted there (the 场域 tree
  shows them per tenant). The offer switches' 「在用」 confirmation does count
  enterprise switches (as scenes, from the admin view's `orgs`).
- Each relay tool call re-resolves the scope (tenants, prompt components,
  custom MCP servers, bindings, credentials), about three times the queries
  of the scene-only resolver; tenant lists read the agent's Coordinator jobs
  twice per request. Fine at today's volumes; cache per task if it shows.
- The Coordinator's routing catalog (`inboundcoord` `FillSkills`) still sees
  agent skills only; scene/personal skills are available to the executing task.
- Robot Stream path tasks get only the global layer.
- Per-user OAuth exists only for the official apps in the catalog; custom
  connectors take pasted Bearer tokens. GitHub App user tokens only see
  repositories where the App is installed.
- A manager's scene list on the configure page is capped at the 1000 most
  recently active scenes of the selected tenant; older scenes stay
  configurable from the web 场域 tree and by key. It is read in one statement
  (`contextcap.ListAllAgentScenes`: one scan of the agent's history, one
  snapshot), and the page's scene writes (bindings, credentials) refresh
  only that scene, not the agent detail; person writes refresh the agent
  detail, which holds the personal scope. The page takes the manager state
  (hint, empty states, 我的 note) from the detail's `access`, not from the
  agent list.
- The mobile page offers 连接 only for offered connectors. The server also
  accepts a person-scope connect for a connector that is only globally
  granted, but the page does not show one; the agent's app dialog says so when
  an app has no shared account.
- Provider sign-in runs in the page's own browser (the DingTalk WebView),
  because the connect is bound to it. Providers whose Google sign-in refuses
  embedded WebViews need another sign-in method there or a PAT (GitHub).
- Disconnecting an OAuth account deletes the stored credential but does not
  revoke the grant at the provider.
- Cross-org (external) groups: the openConversationId in the agent's org can
  differ from the one JSAPI returns in the user's org; use the agent-issued link.
- A personal link proves delivery to the person's 1:1 chat, not the identity
  of whoever opens it first. If the person forwards it before redeeming it,
  the recipient holds the scope until an admin clears the grant. Verifying the
  redeeming DingTalk account against the sender (unionId ↔ staffId through
  the corp app, or a code the person sends back to the agent) is the
  follow-up.
- Personal scopes are keyed by `staffId` under the agent's org. The dispatch
  carries no sender corp id, so v1 relies on the dispatcher reporting
  `sender.staffId` only for members of the agent's org (DingTalk robot
  callbacks omit `senderStaffId` for external members). If an external
  sender's own-org staffId ever arrives, it could collide with an internal
  person's; keying person scopes by a globally unique id (unionId) is the
  follow-up.
