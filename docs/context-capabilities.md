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
nodes"), plus the strict edit rights, the 「启用」 switch of prompt
components and custom MCP servers, and prompt / remote MCP editing on the
configure page (2026-10-01, migration 9431, see §5 "Who may change what" and
§6), plus scene identity by `scene_id` (2026-10-02, migration 9510): a scene
scope is keyed by its Agent work scene (`docs/agent-scene.md`), and a 1:1
chat is its own scene again instead of its person's (see §1.1, §2, §5, §6
and §7). Modelled on Claude Tag (Claude in Slack):
admins own a library, channels and people opt in, and the effective toolset of
one run depends on where the message came from and who sent it.

## 1. Layers

A claimed task's context is merged from four layers, outermost first:
global → enterprise (org) → scene (a group or a 1:1 chat) → person (§1.3). For connectors and
skills the scope layers only ever ADD capabilities; they never remove a
global one. Prompt components and custom MCP servers merge by name, nearest
layer wins.

| Layer | Key | Who edits | Where | Stored in |
| --- | --- | --- | --- | --- |
| Global (智能体, 「通用能力」: on for every tenant and scene) | agent | workspace admin / agent manager | web: agent detail → 配置 → 能力 → 连接器 (official apps: 「通用能力」 switch, set by 添加, no shared account needed; Aone FaaS grants, no offer switch) / Skills (section 「通用能力」) | `internal_connector_agent`, `agent_skill` (existing) |
| Offer catalog (「公开给场域」) | agent | agent manager | web: agent detail → 配置 → 能力 → 连接器 (official app dialog switch 「公开给场域」, shown only while the app is not a 通用能力; Aone FaaS row switch 「公开给场域」 on offer-only rows) / Skills, section 「公开给场域」 (skills not assigned to the agent) | `context_capability_binding` (`scope_type='offer'`) |
| Enterprise (企业级, a tenant) | agent + org_id (scope key = org_id) | agent managers, in the web Context Builder; the configure page shows it read-only to everyone who may open the agent there (managers included) | web agent detail → 场域 → tenant → 配置; mobile 场域能力 → 企业能力 (display only) | `agent_tenant` (the tenant), `context_capability_binding` / `context_connector_credential` / `context_scope_mcp_config` / `context_prompt_component` with `scope_type='org'` |
| Scene (场域: 群聊 or 单聊) | agent + org_id + `scene_id` (the Agent work scene, `docs/agent-scene.md`) | whoever may open it: agent managers (web and configure page), members holding the conversation's configure link (a group's or a 1:1 chat's, minted the same way, §5) | web and mobile `/dingtalk/configure` 场域能力 → 当前会话; web agent detail → 场域 → tenant → 群聊和单聊 → 配置 | `context_capability_binding` (`scope_type='scene'`, `scope_key` = scene_id), custom MCP servers in `context_scope_mcp_config`, prompt components in `context_prompt_component` |
| Personal (个人) | agent + org_id + trigger person key (the sender's staffId, which a DWS native subscription event gets by an address book lookup; else `odt:` + the openDingTalkId the agent's account sees, §2) | that DingTalk person only; agent managers only view it | web and mobile `/dingtalk/configure` 场域能力 → 个人能力; web agent detail → 场域 → tenant → 个人 → 配置 | `context_capability_binding` (`scope_type='person'`), custom MCP servers in `context_scope_mcp_config`, prompt components in `context_prompt_component` |

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
    「允许群聊、个人连接自己的账号」 = the offer, and the scenes (groups and
    1:1 chats) and people using it) and 工具 (discovered tools, 允许写操作, 刷新工具). Results of
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
  (§1.3): 企业级 (the tenant's org), 群聊级 / 单聊级 (a scene: a group chat
  or a 1:1 chat) and 个人级 (a person). A scene is an Agent work scene
  (`docs/agent-scene.md`, the source of truth for scene identity): agent +
  tenant org + kind + openConversationId → one `scene_id` in the
  `agent_scene` directory, registered only from trusted data (an admitted
  dispatch, a channel conversation, an agent's send to a person, an
  explicit bind; `docs/agent-scene.md` §4). The scene scope is keyed by
  that id everywhere (`scope_type='scene'`, `scope_key` = scene_id); the
  openConversationId is only the scene's external locator. Kind (`group`
  or `dm`) is the directory's, set when the scene was registered and never
  guessed. Every level has a Context Builder (§1.3): prompt components, MCP
  components and skill components. The old per-scene prompt (场域提示词,
  `agent_scene_config.prompt`) became the prompt component 「场域提示词」
  of the scene (a 1:1 chat's of its then person, migration 9428); its API
  is gone.
  - Every scene, a **group** or a **1:1 chat** (`dm`) alike, has its own
    configuration: scene connectors and skills, scene credentials
    (accounts and tokens connected for that chat), prompt components and
    custom MCP servers. Agent managers change it, from the web and from
    the configure page (§5 "Managers"), and so do members holding a
    group's configure link and the person of a 1:1 chat (whose personal
    link also grants the chat, §5) (§5 "Who may change what"). Two 1:1
    chats with the same person are two scenes. A 1:1 chat is never mapped
    to its counterpart person: the person's own configuration is the
    separate person scope (「我的」), which only that person changes.
  - The web 场域 tab is a tree: tenant (name + OrgId) → 「群聊和单聊」
    (the tenant's scenes) / 「个人」 (people's personal levels). A node's
    配置 is its Context Builder (§1.3, §6 "Context nodes"): 「Prompt」
    (prompt components), 「MCP」 (the offered Aone FaaS connectors switched
    on for the node, their token, and the node's own 「自定义 MCP 服务器」),
    「连接应用」 (the offered official apps: switch and account),
    「Skills」 (offered skills) and 「生效预览」 (the node's effective context
    with layer badges). Account actions use the node's own credential and
    connect routes and are offered only when the node's `can_connect` is
    true (managers for org and scene nodes; only the person for a person
    node).
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
- Page layout (冬翔, 2026-10-02): no page title and no agent header (the
  document title stays 「QwenTag配置」), only two tabs, 场域能力 and 例行任务
  (the old 公开能力 tab is gone; `tab=public` opens 场域能力). The page
  scrolls inside its own shell (the root layout locks the document), so it
  scrolls in the DingTalk desktop side panel too. 场域能力 switches its
  levels with one segmented control above the level, which keeps the full
  width: 企业能力 (whenever the detail carries the enterprise level, i.e.
  for everyone in a tenant), 当前会话 (the bound group or 1:1 chat; a scene
  picker while browsing) and, for a personal link or while browsing,
  个人能力. A single level shows without the control. The level header is
  its name plus one note only when someone else changes it.
- Every level lists four sections, every item on one line (icon, name, one
  status; a row opens its detail): 指令 (its prompt components with their
  switch; 添加, view, edit and delete in a dialog), Skills (the agent's own,
  「默认开启」, then the offered ones with their switch; a row shows the
  description), 连接器和插件 (every app and connector: GitHub, Slack and
  Notion first, then the other official apps in catalog order, custom
  connectors, then catalog apps nobody opened for the agent) and MCP
  服务器 (the level's own remote servers, 添加 and 配置 like connectors).
  A connector row's button is 添加 before it applies at the level and 配置
  after. 添加 needs only the level's switch right (冬翔, 2026-10-02: a
  scene's members configure it; the enterprise level stays with its
  managers): on a catalog app nobody opened for the agent it installs it
  in the workspace, offers it to the agent's scopes and switches it on here
  (`POST …/apps/{slug}`), then opens its settings; an app an admin switched
  off says so. 配置 opens a dialog:
  1 添加 (remove), 2 授权 (OAuth, PAT or Bearer; a row in effect without an
  account says 「未授权」, the enterprise's account counts). At the 当前会话
  level, an app without dynamic registration (Slack, Asana, GitHub) shows
  the chat's own OAuth application right there: the callback URL to
  register in the provider's console, a link to it, and the client ID and
  secret. 冬翔 (2026-10-02): the scene's members save the first one
  (`PUT …/apps/{slug}/oauth-app`), only the agent's managers change it or
  remove it (删除本会话的应用, back to the workspace's), and
  the workspace's and the enterprise's OAuth applications stay in the
  admin console (the page never writes `connector_app`). A chat without its
  own uses the workspace's (「本会话使用工作区的 … OAuth 应用」, with 为本会话单独配置);
  the personal and enterprise levels show no OAuth application form.
  Scene OAuth applications live in `context_connector_app` (one per agent,
  scene and app; the client secret sealed with the connector secret box). A
  connection started at that scene authorizes with it; the code exchange
  and every later refresh of a scene credential use it when the token's
  recorded client ID is the scene application's, so tokens issued earlier
  by the workspace's client keep refreshing with that one. Its tokens are
  never attached to the workspace's authorization instances. Nothing on the page sends people to the
  admin console, except that 企业能力 is display-only (「企业能力由管理员在管理
  后台配置，这里仅展示」: only what applies there, no switches or account
  actions). A stored account stays manageable (revoke) after the connector
  is removed at that level. A chat whose name is unknown is titled by its
  kind (「群聊」/「单聊」). Where an item comes from (the agent's own bundle or
  the offer catalog) is not shown.
- 例行任务: just the list and its add button (no heading or explanation); a
  routine row opens its detail: what it does, the next runs and its run
  history (status, source and times of the newest 30 runs, no run detail;
  a run's output is posted in the chat).

Agent detail IA (shared web and desktop views): top-level sections
概览 | 工作 | 场域 | 配置. 场域 replaces the old 入站会话 and 记忆 sections and
shows the tenant tree; a tenant opens 配置 (its Context Builder) and 设置
(rename, delete); a scene (a group chat or a 1:1 chat) opens 入站记录 (its
Coordinator transcript, via `inbound_session_id`), 记忆 (its Scene Memory,
loaded by its scene_id) and 配置; a person opens 入站记录 and 记忆 of their
1:1 chat (the node's `scene`, when there is one) and 配置 (their personal
level). Under the tree, 其他记录 opens the full inbound conversation list
(the old 入站会话 view) and the full scene memory list, so conversations the
scene list does not cover (no registered scene: no openConversationId,
another DingTalk org or robot endpoint) and Scene Memory of another org's
scenes stay reachable. On the configure page a 1:1 chat scene is labelled
「单聊 · {title}」 and edited like a group: agent managers and its person
(through the chat's configure link) change it (§5 "Who may change what").
A page opened from a 1:1 chat's link opens on that chat (当前会话), and its
「例行任务」 tab lists that chat's routines; a personal link stored before
2026-10-02 still opens the chat beside the person's own settings (个人能力).

### 1.2 配置 vs 生效 (stored vs applied at runtime)

The claim-time Context Builder (§3) applies every layer of a task's tenant
org: `contextcap.LoadLayers` + `contextcap.MergeContext` build the effective
context, and the admin 「生效预览」 runs the same merge.

| Setting | Stored | Applied at runtime |
| --- | --- | --- |
| Agent global connectors and skills | yes | yes |
| Offer catalog | yes | yes (gates org, scene and personal bindings) |
| Enterprise (org) connectors, skills, credentials | yes | yes, for tasks dispatched in that tenant org |
| Scene connectors, skills, credentials (a group or a 1:1 chat) | yes | yes, as the scene layer of runs in that chat, while it is the agent's scene in the task's tenant org (§2); in a 1:1 chat the person's layer applies on top |
| Personal connectors, skills, credentials | yes | yes, in every run the person triggers, including group runs (see below) |
| Rows an earlier release stored on a 1:1 chat's own scene key (an openConversationId) | yes, re-keyed by 9510 to that chat's scene_id (§7) | yes, as that 1:1 chat's own scene configuration |
| Scene rows 9510 could not re-key (no tenant org, or a key that is not a conversation id) | yes | no |
| Prompt components of an org, scene or person scope (`context_prompt_component`) | yes | yes: merged by name, nearest layer wins, appended to the task instructions as one block |
| Custom MCP servers of an org, scene or person scope (`context_scope_mcp_config`) | yes | yes: merged into the agent's `mcp_config` by server name, nearest layer wins |
| A switched-off prompt component (`enabled = false`, 9431) or custom MCP server (`"disabled": true` in its server object) | yes | no: it takes no part in the merge, so it neither applies nor overrides an outer component or server of the same name (runtime and 生效预览 alike); the `disabled` key never reaches the runtime config |
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
  credentials are offer-gated like scene ones. Only agent managers see
  and edit it (web and configure page, §5 "Who may change what").
- **Scenes and people of a tenant** live under its org: its scenes are the
  agent's group and 1:1 chat scenes registered in the `agent_scene`
  directory under that org (§6 "Scenes"); people are 1:1 chat senders
  (Coordinator jobs that recorded no agent org belong to the identity org),
  live person grants and every person scope with stored configuration
  (`contextcap.ListOrgPersons`). A person's 1:1 chat (`dm_scene_key`) is the
  scene of their newest 1:1 Coordinator job, else the 1:1 chat of their
  redeemed personal link; it is a scene of its own, not the person. Orgs
  that scenes or person data mention without a tenant are listed as
  unassigned (`contextcap.ListAgentOrgActivity`); deleting a tenant keeps
  its scene and person data (the org shows as unassigned again) and removes
  only its org scope configuration.
- **Context Builder components** per level: prompt components
  (`context_prompt_component`, 9422-9423: several per scope, each `{name,
  order, text, enabled}`, at most 20, names unique per scope and 1..64
  characters, texts 1..8000 characters; `enabled`, 9431, defaults to true),
  MCP components (offered connectors switched on for the level, plus the
  level's custom MCP servers in the agent `mcp_config` format; a server
  object may carry `"disabled": true`) and skill components (offered skills
  switched on). A switched-off component or server stays stored and takes no
  part in the merge.
- **Effective context** = global → org → scene → person
  (`contextcap.MergeContext`, through the handler's `mergeTaskContext`, the
  one merge the preview and the runtime share, §3): prompt components and
  custom MCP servers merge by name, the
  nearest layer replaces an outer one of the same name (DSH "nearest layer
  wins"; the outer one is kept in the preview with `overridden_by`;
  switched-off components and servers are left out before the merge);
  connectors and skills are a union whose layer is `global` when the agent
  has it globally, else the nearest layer that switches it on. Applied
  prompts are ordered by `order`, then layer (outermost first), then name.
  The preview of an org node is global + org, of a scene node (a group or
  a 1:1 chat) global + org + scene, of a person node global + org + person.
- Runtime helpers: `contextcap.LoadLayers(ctx, db, ws, agent,
  LayerSelection{OrgID, Org, SceneID, PersonKey})` reads the scope layers
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
- Scene: the dispatch's SceneRef, `agent_scene.scene_id` in the task
  context (`protocol.AgentSceneContextKey`, written by the Host, never taken
  from Router input; `docs/agent-scene.md` §5), a group or a 1:1 chat
  alike (`Scope.SceneID`). Nothing is derived from the openConversationId.
  The scene layer applies only while that SceneRef names a conversation
  scene of the agent in the task's tenant org (`contextcap.GetScene` in
  `resolveTaskContextScope`, the use-time fence of `docs/agent-scene.md`
  §6); otherwise (another org, another agent, an enterprise scene, an
  unknown id) the task keeps its org and person layers without one. A 1:1
  run therefore carries its chat's scene layer and, nearer, its person's
  layer.
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
- Person of a DWS native subscription dispatch (2026-10-03, 冬翔: one
  person, one key; staffId first): the event names its sender only by the
  openDingTalkId the receiving DingTalk account sees (no staffId, no uid).
  Measured on 2026-10-03: one account sees the same person under the same
  id in its 1:1 chat and in a group, another account (even in the same org)
  sees another id, and the address book of the receiving account maps it to
  the person's staffId when the person is a member of that org.
  - Before dispatch the server looks the sender's staffId up
    (`nativeSenderStaffID`): first the staffId kept for (org, receiving uid,
    openDingTalkId) in `dws_open_identity_staff` (migration 9787), else the
    receiving account's own address book (`dwsclient.Shared.StaffID` →
    `dws.ContactService.StaffIDOf`: `search_contact_by_key_word` by the
    sender's name, then, in a group, by the member's nick and group nick
    from `list_group_member_by_ids`; only an entry with exactly that
    openDingTalkId counts, a name never decides). A proved staffId is kept
    and dispatched as `sender.staffId` and each message's `senderStaffId`,
    exactly like a Router delivery; it stays out of the native acceptance
    fingerprint. A miss is retried after 10 minutes, a failure after 1
    minute (per process), and the message goes on without a staffId. A
    group that refuses its member list counts as a miss. A kept staffId is
    proved again after 7 days: one the address book no longer proves (the
    person left, the staffId was reassigned) is dropped, while a failed
    lookup keeps it. Events that will not be dispatched (no content, no
    message reference) cost no lookup. Known limit: the address book search
    is ranked and capped, so a common real name in a large org may miss
    until a nick or group nick finds the person; configuration saved under
    a person's `odt:` key while they were unresolved stops applying once
    their staffId is proved (it stays stored and listed, not migrated).
  - The staffId is not always there: a member of another org (an external
    group member, a cross-org 1:1 chat) has no staffId in the tenant org,
    and some accounts (NHI accounts) are not in the address book. Such a
    sender is keyed by `odt:` + `sender.openDingTalkId` (else
    `senderOpenDingTalkId`; the two must agree, the literal `null` is none):
    `contextcap.TriggerPersonKey`. The `odt:` prefix keeps these keys apart
    from staffIds (a staffId that starts with it is refused). An `odt:` key
    is the person with this agent and its bound account; rebinding the
    agent to another account gives them a new one, and the earlier
    configuration is left unused, never applied to someone else.
  - The single-sender rule: a sender named by openDingTalkId without a uid
    (native) is proved only by messages stamped with that openDingTalkId,
    every one including a single one (`singleTriggerPerson`,
    `singleTriggerOpenID`). A Coordinator work item cut from a merged window
    keeps the window's data-level sender, so a group message whose own
    sender is unknown never inherits another speaker's person. A sender
    with a staffId never falls back to the openDingTalkId.
  - The Employee foreground's capability directory
    (`employeeCapabilityPerson`) keys its person layer by the same
    `TriggerPersonKey`. Only capability configuration uses this key; scene
    routines never carry a person (§9).
  - The tenant's people list (`ListOrgPersons`, `ListAgentOrgActivity`)
    reads 1:1 senders by the same rule and translates an openDingTalkId
    through `dws_open_identity_staff`, so a person first seen before their
    staffId was proved is listed once, under the staffId; a sender whose
    staffId is unknown is listed under `odt:<openDingTalkId>`.
  - The FC/E2B scene sandbox reuse buckets by the person key
    (`service/fc_e2b_connection_reuse.go`), so native group speakers get a
    sandbox per person, as Router speakers with a staffId already did.
  - The claim log line (`context builder: claim context`) carries
    `person_key_hash` (first 12 hex digits of its SHA-256) so two runs can
    be matched to one person without logging the key.
- org_id (the tenant): the dispatch's recorded agent org
  (`external_identity.dws.orgId`), else the agent's
  `agent_dingtalk_identity.org_id` (same source as scene memory). A task
  without a recorded org that was created before the agent's current
  binding (`bound_at`) may come from the earlier binding and gets only the
  global layer. An agent without a DingTalk identity (or with an identity
  that names no org), for a task without a recorded org, is orgless and
  keeps org `""`: its implicit tenant, where its configure page and
  personal links write (§5), so the task gets only its person layer under
  org `""`, no org layer and no scene layer (every Agent work scene
  belongs to a tenant org, `docs/agent-scene.md` §3). Bindings store the
  org_id they were created under and resolution matches it exactly.
- Tenant gate: a staffId only means something inside the org it was
  dispatched in (a scene_id belongs to one tenant org by construction), and
  the org, scene and person layers apply
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
  a scene or a single sender (a merged multi-sender run, an unknown
  sender, an event dispatch without a conversation).
- Tasks with no dispatch context (web comments, autopilot, plain chat) get only
  the global layer.
- v1 covers the Digital Employee dispatch path (and Coordinator issue tasks that
  carry its context). The robot Stream path is a follow-up.

## 3. Resolution

Claim time (`buildClaimedTaskResponse` → `injectRunnerMCP`) and call time
(`CallInternalConnector`) run the same resolver, so a toggle or revoke applies
to the next tool call of a running task.

The scope layers of a task are its tenant org's org layer, its scene and
its trigger person (§2), read with `contextcap.LoadLayers` and merged
with the global layer by `contextcap.MergeContext` (§1.3).

Connectors: effective set = global grants ∪ org/scene/person bindings
(deduped by connector id). Each connector is mounted once as `c<16hex>` via
the existing server relay; secrets never reach the task row, claim payload or
sandbox.

Credential selection per connector call, first match wins:

1. personal credential — only when the task's trigger person set one;
2. scene credential — when the task's scene set one. Every connector a task
   may use is granted (通用能力) or offered (公开给场域), and either takes a
   scene account; a scene credential serves every run in that chat (every
   member's, in a group), so only agent managers may connect at scene
   level, for a group and a 1:1 chat alike (§5 "Who may change what"; a
   configure-link holder may not replace the scene's account);
3. org credential — when the task's tenant org set one, under the same rule
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
- A scene's own OAuth application (Slack, Asana, GitHub) lives in
  `context_connector_app` (9740, unique per agent, scene and app in 9741),
  its client secret sealed with the same box. A scene credential whose
  `client_id` is the scene application's is exchanged and refreshed with
  it; any other client ID resolves to the workspace's `connector_app` as
  before. Workspace deletion sweeps the table.
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
  `multica` MCP tool `create_context_config_link` (and `scene_connect_link`
  of config-qwen-tag-scene, §10) mints a token from the trusted task context
  of the current run. It is listed only for task tokens, refuses personal
  access tokens, and requires the calling Agent's own active
  (`dispatched`/`running`) task. It takes no scope. A group's link is a
  scene link bound to (agent, org_id, the conversation's scene_id, title)
  (PRI-98, 冬翔 2026-10-02). The scene_id is the SceneRef the dispatch
  resolved from the conversation's openConversationId (`scene.Resolve`,
  `docs/agent-scene.md` §1, §5) and that still passes the use-time fence
  (§2). A run without such a scene (no SceneRef, another org, an orgless
  agent, an A2A run, manual reruns, routine runs) cannot mint one. Anyone
  who opens a group's link within 30 minutes gets a 30-day grant of that
  scene (it was posted in the conversation, so its readers are the
  conversation's members). The title is the dispatch's conversation title,
  else the scene directory's.
  **A 1:1 chat's Host-appended link also carries the chat's person**
  (冬翔 2026-10-03: 单聊的场域可以拿到个人配置入口, keyed by the person's
  staffId). Only the links the Host appends to a reply itself carry it:
  the Coordinator's capability answer and the Employee foreground's
  (`HostAppended`), which keep the link out of every stored transcript
  (`RedactConfigLinks`, the Employee model journal and tool results). The
  executor tools above always return the scene link: their result lands in
  the run's task messages, which every member who may read the run can
  see. When such a run has one trigger person (§2, `TriggerPersonKey`: the
  staffId a Router delivery carries or a native sender's address book
  lookup proves, else `odt:` + the openDingTalkId) and the dispatch says
  the conversation is a 1:1 chat, the link is stored as a person link
  (scope_type `person`, scope_key the person key, `extra_scene_key` the
  chat's scene_id, title the person's name, `LinkTTLPerson` = 15 minutes).
  Redeeming it grants the chat's scene and, for 365 days, the person's own
  level (个人能力). The first DingTalk account that opens it holds the
  person; that account may open it again while it still holds the person's
  grant (a revoked grant is not restored by reopening), any other account
  gets 410, and while a person's grant is live another account answers 409
  and leaves a link unconsumed. Known limits: redemption proves delivery to
  the 1:1 chat, not the account's identity (no general DingTalk API maps
  the configure page's unionId to a tenant staffId); until it is opened the
  link also sits in the employee account's own DingTalk history, which the
  agent's own DWS tools can read. A 1:1 run without one trigger person (a
  merged window of several speakers) gets the plain scene link. Person
  links stored before 2026-10-02 redeem by the same rules until they
  expire.
  The tool returns (as `structuredContent` and as JSON text)
  `{url, dingtalk_url, scope, scene_kind, expires_at, includes_person?}`
  (`scope` is always `scene`, `scene_kind` the directory kind, `group` or
  `dm`; `includes_person` is never set by the tools, only by the Host's own
  links): `url` is
  `<app origin>/dingtalk/configure?link=<token>` (app origin = `MULTICA_APP_URL`,
  else `FRONTEND_ORIGIN`), `dingtalk_url` is
  `dingtalk://dingtalkclient/page/link?url=<urlencoded url>&pc_slide=true`
  (`inboundcoord.ConfigLinkDeepLink`). Replies carry a link only as a
  Markdown link to `dingtalk_url` (`[配置本群能力](dingtalk_url)`, in a 1:1
  chat `[配置本单聊能力](dingtalk_url)`), never the bare URL; the
  Coordinator's capability answer ends with the same Markdown link.
  Tokens are 32 random bytes (base64url) stored only as SHA-256 hashes
  (`context_config_link`, with `source_task_id`). The Coordinator mints the
  same links for its capability answer (「你有哪些能力」,
  `docs/inbound-coordinator-loop.md` COORD.F04); the link reaches only the
  DingTalk reply, and the Coordinator transcript (`chat_message`, which
  managers and allow-listed members can read, in both the job path and the
  channel engine), the channel engine's stored plan, Issue descriptions
  and the DingTalk history read back into the Coordinator store
  `[configuration link]` in its place (`inboundcoord.RedactConfigLinks`,
  plain and percent-encoded forms). A link the executor mints stays in that
  run's own records: its task messages, its final output and the issue
  comment a comment-triggered run replies with. The DingTalk reply is sent
  from them, the failed-run fallback reply reads the task messages back, and
  the task_finished wrap-up compares the sent text with the result to see
  that the reply already went out; members who can see the run can see the
  link (it expires after 30 minutes, and its grants can be revoked).
- JSAPI group picker (secondary). With a person grant for the agent in the
  page's tenant (a verified DingTalk identity; `org_id` names the tenant),
  the page signs `dd.config` through
  `GET /api/dingtalk/jsapi-config` (`jsApiList: ["biz.chat.chooseConversationByCorpId"]`)
  and calls `biz.chat.chooseConversationByCorpId`; the server converts `chatId`
  with `POST /v1.0/im/chat/{chatId}/convertToOpenConversationId` using the corp
  app token and grants the scene (source `jsapi`, 30 days, keyed by its
  scene_id) only if the agent has a registered group scene for that
  conversation in the page's tenant org (`scene.Lookup` on `agent_scene`,
  kind `group`): a 1:1 chat, a conversation the agent never served there
  and a group of another org are never reached. Both calls need the direct
  DingTalk client (`DINGTALK_CLIENT_ID`/`SECRET` without `DINGTALK_AGENT_BASE_URL`); the
  private-agent client answers "unsupported" and the API returns 503.
  Residual risk: DingTalk offers no general "is this user in this group" API,
  so the server trusts that only group members can obtain a group's chatId.
  `chat_id` is therefore required: an openConversationId is not a secret
  (it outlives membership), so `open_conversation_id` is only cross-checked
  against the converted chatId, never accepted alone. An orgless agent
  (org `""`) has no scenes, so the picker grants nothing there.
- Workspace membership is not required: ordinary group members who talk to the
  digital employee are usually not Multica members. They only ever see offered
  item names, descriptions and tool names, never URLs or credential status of the
  workspace library.
- Managers (no link needed). A caller with the agent-manage permission of the
  admin routes (a member of the agent's workspace who is a workspace
  owner/admin or the agent's owner, as `canManageAgent` defines it) may
  configure every SCENE of the agent from the configure page without a grant:
  the agent is listed with `access: "manager"`, its detail lists all of the
  agent's scenes in the tenant (groups and 1:1 chats, from the scene
  directory of §6 "Scenes", source `manager`, no expiry), and the scene
  read, bindings, credentials, connect, prompt and custom MCP server routes
  accept the manager for any scene of the agent in that tenant org (404 for
  another scene_id). The personal scope still needs the person grant: a
  manager is not that person. Managers also configure the enterprise (org)
  scope of every tenant (bindings, credentials, connect, prompt components,
  custom MCP servers);
  nobody else sees it (the detail's `org` is null for them) and their writes
  answer 403 `{error, code: "manager_only"}`. Every offer gate applies to
  managers exactly as to grant holders, and the OAuth callback re-checks the
  manager permission like a grant. Manager access is computed per request
  and never stored as a grant.
- 1:1 chat scenes (§1.1) follow the scene rules: managers change them
  (bindings, accounts, connects, prompt components, custom MCP servers);
  the person, whose personal link also granted the chat's scene_id, only
  views it, and their writes there answer 403 `{error, code:
  "manager_only"}`. The person's own configuration is their person scope
  (「我的」), which only they change.

### Who may change what

One server function decides every write, on the configure page and on the
admin Context Builder alike: `contextCapScopeRights(scopeType, manages,
self) contextCapRights` in
`server/internal/handler/context_capabilities.go`. `contextCapRights` is
`{Toggle, Connect, EditPrompts, EditMCP, EditRoutines}` (JSON `rights:
{toggle, connect, edit_prompts, edit_mcp, edit_routines}`; routines exist on
scenes only, §9); `scopeType` is the scope (a 1:1 chat is a
`scene`, like a group), `manages` the agent-manage permission (workspace
owner/admin or the agent owner) and `self` whether the caller is the person
of a person scope (their live person grant).

| Level | Agent manager | The person | Anyone else (configure-link holders) |
| --- | --- | --- | --- |
| 企业级 (`org`) | everything | — | nothing, and the configure page does not show the level |
| 群聊级 / 单聊级 (`scene`: a group or a 1:1 chat) | everything, routines included | everything (the person of a 1:1 chat, through the chat's configure link) | everything (configure-link holders) |
| 个人级 (`person`) | view only (configure page and admin Context Builder) | everything | — |

Since 2026-10-02 (冬翔: keep permissions simple until people use the
feature) a scene is changed by whoever may open it, the same rule as
changing it from the conversation (§10): a group's link holders and a 1:1
chat's person edit it like a manager, see its custom MCP servers and connect
its accounts. A manager who also holds the person's grant edits that person
level as the person. `contextCapResolveScope` decides who may read a scope and fills in
`Rights`; `contextCapScopeAllows` refuses a write whose right is missing with
403 `person_only` on a person level and 403 `manager_only` on an org or
scene level. Every write path goes through it: the configure page's bindings
(`toggle`), credentials and OAuth connects (`connect`), prompts
(`edit_prompts`) and custom MCP servers (`edit_mcp`); the admin node
writes of the same four kinds; and `authorizeConnectorOAuthScope`, which
re-checks `connect` at the OAuth start and again at the callback. Revoking a
level's configure-page grants (admin `DELETE .../grants`) is a manager
action outside the table and stays available on person levels. Clients read
`rights` and gate their controls on it; the older `can_edit` (org layer,
admin node: `rights.toggle`) and `can_connect` (scene view, admin node:
`rights.connect`) stay for older clients.

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
org only. `scope_type` may also be `org` (scope key = the org id): only
managers read and write it (anyone else's writes answer 403 `manager_only`).
What the caller may change is `contextCapScopeRights` (§5 "Who may change
what"): a missing right answers 403 `manager_only` on an org or scene
scope and 403 `person_only` on a person scope. The call then
requires the caller's access under that org: a scene read needs a scene
grant for that exact scene_id, or managing the agent and the scene_id being
one of the agent's scenes in that org (§5 "Managers"; 404 for an unknown
scene), and a scene write needs the latter; a scene key that is not a
scene_id (an openConversationId, for example) answers 400; a person read or
write needs a person grant for that exact staffId (managers included). A
1:1 chat is a scene like a group (§5 "1:1 chat scenes"). Bodies are
size-limited and reject unknown fields (an old replica therefore answers 400 to a body carrying `org_id`;
send it only for an org other than the identity org). `B = {resource_type, resource_id, enabled}`,
`C = {connector_id, hint, updated_at, kind}` (`kind` is `oauth` or `bearer`),
`R = {toggle, connect, edit_prompts, edit_mcp}` (what the caller may change
in that scope, §5 "Who may change what"), `P = {id, name, order, text,
enabled}` (the scope's own prompt components, ordered by order, then name)
and `M = {"mcpServers": {...}} | null` (the scope's own custom MCP servers;
null with `mcp_config_redacted: true` when the workspace always redacts
secrets, as on the admin node);
every `bindings` list contains only resources that are currently in the
enabled offer catalog. A personal connector binding also carries
`share_in_groups` (boolean, 「在群聊中由我触发时也可用」, default false);
other bindings omit it. Scene entries `{scope_key, scope_title, source,
expires_at, kind, org_id}` carry the scene_id as `scope_key`, `kind`:
`group` or `dm` (the scene directory's, `group` for an id it does not
know) and the tenant org they live in.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/context-capabilities/links/redeem` | `{token}` → grant (source `agent_link`); returns `{agent_id, workspace_id, scope_type, scope_key, scope_title, org_id, extra_scene_id}` (`org_id` is the org the link was minted in; pass it on the next calls when it is not the identity org; a group's link grants that group's scene; a 1:1 chat's link is a person link with `extra_scene_key` (§5): it grants the chat's scene_id and the person, and `extra_scene_id` names that chat so the page opens it rather than another 1:1 chat a manager sees; `extra_scene_id` is `""` for any other link); 410 when unknown, malformed, expired or (person link) already consumed by another account, or by this account after its person grant was revoked (otherwise its own account may open it again); 409 when a different account already holds that person's live grant. The link and the grant commit in one transaction |
| GET | `/api/context-capabilities/agents` | `{agents: [{id, name, avatar_url, workspace_id, access, scopes: [{scope_type, scope_key, scope_title, source, expires_at, org_id}]}]}`: agents with a live grant for the caller under one of the agent's tenant orgs (`access: "grant"`, newest grant first; grants under an org that is not a tenant are left out), then the other non-archived user agents the caller manages (`access: "manager"`, `scopes: []`, by name). An agent both granted and managed is listed once with `access: "manager"` and its grants. Older backends send no `access` (read it as `grant`) |
| GET | `/api/context-capabilities/agents/{agentId}?org_id=` | `{agent, global: {connectors: [{id, name, catalog_slug}], skills: [{id, name, description}]}, offers: {connectors: [{id, name, tools, accepts_credential, credential_required, catalog_slug, auth_mode, accepts_pat, oauth_available, install_url?}], skills}, apps: [{slug, name}], person: null \| {scope_key, scope_title, source, expires_at, bindings: [B], credentials: [C], rights: R, prompts: [P], mcp_config: M, mcp_config_redacted}, scenes: [{scope_key, scope_title, source, expires_at, kind, org_id}], access, jsapi_available, tenant: null \| {org_id, name, source}, tenants: [{org_id, name, source}], org: null \| {scope_key, scope_title, bindings: [B], credentials: [C], rights: R, can_edit, prompts: [P], mcp_config: M, mcp_config_redacted}}` for one tenant org: `org_id`, else the identity org, else (a caller who does not manage the agent and holds no grant there) the org of the caller's newest grant. `tenant` is that org (null for an agent without a DingTalk identity when none was named), `tenants` the orgs the caller may switch to (every tenant for a manager, else those holding a grant of the caller), `org` its enterprise layer for every caller (rights only for a manager; for anyone else credential hints are blank, the MCP document is withheld, switched-off prompts and the accounts of connectors that are neither offered nor the agent's own are left out; null without a tenant); `can_edit` is `rights.toggle`, kept for older clients. `person.rights` is the person's own (everything). Needs any grant for the agent in that org or managing it (else 403). `access` is `manager` or `grant`. `scenes` lists the granted scenes and, for a manager, every other scene of the agent in that org, groups and 1:1 chats (newest activity first, at most 1000, `source: "manager"`, `expires_at: ""`, titles and kinds from the scene directory; none for an orgless agent). `global.connectors` are the agent's granted, enabled connectors with a ready workspace credential (and, for official apps, discovered tools). `catalog_slug` names the official app (`""` for custom connectors). `auth_mode` is `none`, `bearer` or `oauth`. `accepts_credential` means the connector accepts a pasted token: a Bearer connector, or an official app that allows a PAT. `accepts_pat = auth_mode == 'oauth' && accepts_credential`. `credential_required` means the connector uses a credential (Bearer or OAuth) and has no workspace credential. `oauth_available` means the server can run the app's OAuth sign-in (GitHub needs `GITHUB_APP_CLIENT_ID` and `GITHUB_APP_CLIENT_SECRET`; every app needs the connector credential key and an app origin, the other deployment checks of the start endpoint); the page shows 连接 only for a literal `true` (a missing field from an older backend hides it too) and offers the PAT form when `accepts_pat`. `install_url` is the GitHub App installation page (omitted when there is none). `apps` is the official app catalog in catalog order, opened for the agent or not (the page lists every supported app), each with `setup` (`automatic`: dynamic registration or a deployment client; `oauth_app`: an OAuth application is needed first, the workspace's or a scene's own; `unsupported`) and `ready` (the workspace can start a sign-in now). `jsapi_available` is true only when H5 signing is configured and the caller has a person grant (the resolve endpoint needs one) |
| GET | `/api/context-capabilities/agents/{agentId}/scenes/{sceneKey}?org_id=` | `{scene: {scope_key, scope_title, source, expires_at, kind, org_id}, scope: {type, key, title}, bindings: [B], credentials: [C], rights: R, can_connect, prompts: [P], mcp_config: M, mcp_config_redacted}` for one scene, a group or a 1:1 chat: `{sceneKey}` is its scene_id (a conversation id is 400). Scene grant or manager required; a manager without a grant gets `source: "manager"`. `scope` is the scene itself (`{type: "scene", key: <scene_id>, title}`); `bindings`, `credentials`, `prompts` and `mcp_config` are the scene's. `rights` is what the caller may change there (everything for a manager, nothing for a link holder: a group member, or the person of a 1:1 chat); `can_connect` is `rights.connect`, kept for older clients. A caller without `rights.connect` gets the credentials with `hint: ""` (the connected state, `kind` and `updated_at` stay). `kind` comes from the scene directory (an id it does not know is `group`). `scene_oauth_apps` lists the apps (catalog slugs) the scene signs in to with its own OAuth application (`[]` when none) |
| PUT | `/api/context-capabilities/agents/{agentId}/bindings` | `{scope_type, scope_key, org_id?, resource_type, resource_id, enabled, share_in_groups?}` → `{binding: B}` (`scope_type` `org`, `scene` or `person`; needs `rights.toggle`); 403 unless the resource is in the enabled offer catalog (enable and disable alike). `share_in_groups` is only accepted for a person scope (`scope_type='person'`; a scene, 1:1 chats included, answers 400) and `resource_type='connector'` (else 400), and only from the person (a manager gets 403 `person_only`, like any of their person-scope writes); omitted keeps the stored value |
| PUT | `/api/context-capabilities/agents/{agentId}/credentials` | `{scope_type, scope_key, org_id?, connector_id, bearer}` → `{credential: C}` (needs `rights.connect`; an `org` credential, like a scene credential, needs an offered or granted connector). The connector must be enabled and offered to the agent, or (person scope only) globally granted to it (else 403). It must accept a pasted token: `auth_mode='bearer'`, or an official app that allows a PAT (GitHub) (else 400). bearer is 1..4096 bytes, with no CR/LF/NUL and no surrounding whitespace; 503 without a credential key. The first credential of an official app also discovers and pins its tools |
| DELETE | `/api/context-capabilities/agents/{agentId}/credentials?scope_type=&scope_key=&connector_id=&org_id=` | 204, idempotent; needs `rights.connect`; allowed after the offer was removed. For an OAuth credential this is "disconnect" (the provider grant is not revoked) |
| GET | `/api/context-capabilities/agents/{agentId}/github-installations?scope_type=&scope_key=&org_id=&connector_id=` | needs `rights.connect`. GitHub only. `{connected, installations: [{id, account_login, account_type, repository_selection, settings_url}], error?, truncated?}`. No credential → `connected: false`. GitHub 401 → `error: "reconnect"`; 403 (a personal access token) → `error: "not_github_app_token"`. The token is not returned. One user token covers every installation listed |
| POST | `/api/context-capabilities/agents/{agentId}/connections/start` | `{scope_type, scope_key, org_id?, connector_id, return_to?}` → `{authorize_url}` (an `org` scope needs a manager; the state stores the tenant org and the callback re-checks that it is still a tenant) for connecting an official app account through OAuth, plus the browser binding cookie (the WebView that calls it must also open the URL). The caller needs `rights.connect` there: managing the agent for an org or scene scope, a group or a 1:1 chat alike (403 `manager_only` for a link holder, the person of a 1:1 chat included; 404 for a manager's unknown scene), being the person for a person scope. The connector must be an enabled official app that is offered to the agent (scene) or offered or globally granted (person); otherwise 403 with `code: "forbidden"`, including for an unknown connector. Other errors are `{error, code}`: 400 `not_oauth` or `invalid_return_to`; 503 `oauth_unavailable`, `app_origin_missing` or `credential_storage_unavailable`; 502 `provider_unavailable`. The browser returns to `return_to` (default `/dingtalk/configure?agent=<id>`) with `?connected=<slug>` or `?connect_error=<code>`. The callback re-checks the grant and offer, stores the credential for that scope and turns the connector on for it (see `docs/internal-mcp-connectors.md` "Official apps") |
| PUT | `/api/context-capabilities/agents/{agentId}/prompts` | `{scope_type, scope_key, org_id?, prompts: [{name, order, text, enabled?}]}` → `{prompts: [{id, name, order, text, enabled, updated_by_name, updated_at}]}`: replaces the scope's prompt components, like the admin node PUT and with its validation (at most 20; names trimmed, 1..64 characters, unique, 400 `duplicate_prompt_name`; texts trimmed, 1..8000 characters, 400 `invalid_prompts`; `enabled` defaults to true; `[]` clears them). 403 without `rights.edit_prompts` |
| PUT | `/api/context-capabilities/agents/{agentId}/mcp-config` | `{scope_type, scope_key, org_id?, mcp_config: {mcpServers: {...}} \| null}` → `{mcp_config}`: replaces the scope's custom MCP servers. Remote servers only (`contextcap.NormalizeRemoteMCPConfig`): the document holds only `mcpServers`; each server is an object with a `url` (http or https, with a host) and otherwise only `type` (`http`, `sse` or `streamable-http`), `headers` (header name → string, no CR/LF/NUL) and `disabled` (boolean); `command`, `args`, `env`, `cwd` and any other key, names that are empty, longer than 64 characters or padded, and the reserved names `multica` and `c<16 hex>` answer 400 `invalid_mcp_config` with a reason. `null`, `{}` and `{"mcpServers": {}}` clear it. At most 64 KiB. 403 without `rights.edit_mcp`. While the workspace redacts (`mcp_config_redacted`), the page cannot see the stored servers and a PUT still replaces them all |
| POST | `/api/context-capabilities/agents/{agentId}/apps/{slug}` | `{scope_type, scope_key, org_id?}` → `{connector_id, default_on}`. Adds a catalog app at a level: an app the agent already owns (`default_on`, nothing changes) or offers is switched on; another one is installed in the workspace (its catalog connector, when missing) and offered to the agent's scopes first. Needs only the level's switch right (403 otherwise: `manager_only` at the enterprise level), so a scene's link holders add apps here although the admin console's offers need a workspace admin; 409 `app_disabled` for an app an admin switched off; 404 for an unknown app. |
| GET, PUT, DELETE | `/api/context-capabilities/agents/{agentId}/apps/{slug}/oauth-app` | A scene's own OAuth application of an app without dynamic registration (Slack, Asana, GitHub). GET `?scope_type=scene&scope_key=<scene_id>&org_id=` → `{slug, name, fields: [{key, optional, file}], docs_url, callback_url, saved, client_id, client_secret_set, workspace_ready, ready, can_edit}`: `fields` are `client_id` and `client_secret`; `callback_url` is the production callback (pre-release forwards it back); `saved` says the scene has its own; `workspace_ready` that the workspace's would serve without it; `ready` that a sign-in can start at the scene now; `can_edit` that the caller may save now (the first one with the scene's `rights.connect`, a saved one only as the agent's manager). Secrets never come back. PUT `{scope_type: "scene", scope_key, org_id?, client_id, client_secret?}` saves the first one (400 `oauth_app_field_required` without both values) or, for the agent's managers, changes it (an omitted secret keeps the stored one only while the client ID stays: a new client ID needs its secret, else 400 `oauth_app_field_required`); DELETE with the GET query removes it, so the scene signs in with the workspace's again (the agent's managers only, else 403 `oauth_app_locked`; 204 also when none was saved; accounts it authorized ask to reconnect at their next refresh); anyone else changing a saved one, or saving after someone else just did, gets 403/409 `oauth_app_locked`. Only scenes: any other `scope_type` is 400 `oauth_app_scene_only` (the workspace's and the enterprise's OAuth applications are the admin console's). Scene read rights (GET) or connect rights (PUT) are required like the other scene routes; 404 for an app that needs none |
| POST | `/api/context-capabilities/agents/{agentId}/scenes/resolve?org_id=` | `{chat_id, open_conversation_id?}` → `{scene: {scope_key, scope_title, source, expires_at, kind: "group", org_id}}` (JSAPI path, see §5; registered group scenes only, `scope_key` = the group's scene_id) in the tenant `org_id` names (default the identity org): the person grant must be in that tenant and the scene grant is stored under it. 400 without `chat_id` or when `open_conversation_id` differs from the converted one; 403 without a person grant in that tenant or when the agent has no registered group scene for that conversation there (a 1:1 chat, a group of another org, an orgless agent); 404 `tenant_not_found`; 503 when chatId conversion is unavailable; 502 when DingTalk rejects the chatId |
| GET | `/api/dingtalk/jsapi-config?url=` | `dd.config` signature `{corp_id, agent_id, time_stamp, nonce_str, signature}` for the page URL without `#fragment`. Any authenticated human (`RequireHumanActor`). The URL must be absolute http(s) on the app origin (`MULTICA_APP_URL` / `FRONTEND_ORIGIN`); without an app origin the endpoint answers 503 rather than signing arbitrary pages. `signature = sha1("jsapi_ticket=<t>&noncestr=<n>&timestamp=<ts>&url=<url>")` in hex, where `<url>` has its query percent-decoded like DingTalk's reference signer and `time_stamp` is Unix seconds. The ticket (`GET {oapi}/get_jsapi_ticket`) is cached in process until 5 minutes before expiry and never returned; the corp access token it is fetched with is redacted from transport errors before they are logged. 503 when `DINGTALK_H5_CORP_ID` / `DINGTALK_H5_AGENT_ID` are unset, no app origin is configured, or the direct client is unavailable |

Admin (workspace routes, human actor, the same permission as editing the
agent's skills: workspace owner/admin or the agent owner). Adding a connector
offer here needs a workspace owner/admin, like the connector library and its
grants (the configure page's 添加 of a catalog app is the exception: a level's
switch right suffices, see `POST …/apps/{slug}` above): an agent owner who is only a member may keep or remove connector
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
  counted). Scenes are groups and 1:1 chats alike, each counted once by
  its scene_id; a 1:1 chat's account and switches count as that scene,
  not as its person.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/agents/{id}/connected-apps` | `{apps: [A], can_admin}`; `can_admin` is true for a workspace owner/admin |
| GET | `/api/agents/{id}/connected-apps/{slug}` | `A` plus `scenes: [{scene_id, scene_key, title, kind, enabled, connected, account}]` (`scene_key` carries the same scene_id), `persons: [{scope_key, title, enabled, connected, account, share_in_groups}]`, `tool_list: [{name, read_only, allowed}]` and `can_admin`; 404 for a slug not in the catalog. `scenes` and `persons` list only the scopes that count in `usage` (enabled or connected); `account` is the scope credential's hint, for a person only to workspace admins (`""` for other agent managers, like the shared account's hint). Scene titles and kinds come from the scene directory (newest activity first; a key the directory does not know is listed last, as a group); person titles are the binding's snapshot or the newest grant title (the DingTalk display name), people sorted by title |
| DELETE | `/api/workspaces/{ws}/internal-connectors/{connectorId}/credential` | Workspace owner/admin (`RequireWorkspaceMCPHumanIssuer` plus the admin role): removes the workspace's stored credential of a connector, i.e. disconnects an official app's shared account. 204, idempotent; 404 for an unknown connector, 400 for a malformed id. The provider grant is not revoked and an environment credential (`MULTICA_INTERNAL_MCP_BEARER_<id>`) is not affected; a concurrent OAuth refresh holds the row lock, so it cannot restore the credential |

### Scenes (admin)

Same routes group and permission (human actor, workspace owner/admin or the
agent owner; agent actors get 403). A scene is an Agent work scene
(`docs/agent-scene.md`) and its key in these routes is its scene_id; an
openConversationId or another malformed key answers 400. The scenes of an
org are the agent's group and 1:1 chat scenes in the `agent_scene`
directory under that tenant org (`contextcap.ListAgentScenes`); nothing is
discovered by unioning memory, Coordinator jobs, configuration or bindings.
Kind is the directory's (`group` or `dm`, set when the scene was
registered, `docs/agent-scene.md` §4). An orgless agent (org `""`) has no
scenes. A 1:1 chat's configuration (bindings, credentials, prompt
components, custom MCP servers) is its own, like a group's: the Context
Builder routes act on the scene through the same resolver as the mobile
routes.

`S = {scene_id, scene_key, conversation_id, kind, title, org_id,
last_active_at, inbound_session_id, inbound_count, memory_id, has_memory,
has_prompt}` (`has_prompt`: the scene has prompt components). `scene_key`
carries the same scene_id for older clients; `conversation_id` is the
scene's openConversationId, for display only. `title` is the directory's
(the group title or the 1:1 counterpart), else the locating line of its
Scene Memory.
`inbound_session_id` is the newest Coordinator chat session of the scene
(jobs whose command carries its SceneRef; open its transcript with
`GET /api/agents/{id}/coordinator-conversations/{inbound_session_id}/messages`)
or `""`; `inbound_count` counts the sessions that transcript shows (the
messages endpoint's anchor partition: same endpoint namespace, source
platform and source type as that session); sessions of the same
conversation under another endpoint namespace or source stay reachable in
the full inbound conversation list. `memory_id` is the scene_id when the
scene has Scene Memory (`has_memory`), else `""` (open it with
`GET /api/agents/{id}/scene-memory/{scene_id}`, which also serves scenes
beyond the 200 newest that the list endpoint returns); `last_active_at` is
the directory's last activity. The list carries no binding counts; a
scene's bindings are in its detail.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/agents/{id}/scene-memory/{sceneId}` | The Scene Memory of one scene by its scene_id (the scene memory list item shape, `id`, `scene_id` and `scene_key` = the scene_id, `conversation_id` for display); same permission as the other scene memory routes; 400 for a malformed id, 404 when the agent has no memory for that scene |

The agent-wide scene list (`GET /api/agents/{id}/scenes`) and the scene
detail, prompt, bindings and custom MCP server routes of
`/api/agents/{id}/scenes/{sceneKey}` are gone: the per-tenant `groups` and
`persons` lists and the Context Builder routes below replace them. The
lists and the single-scene lookup behind the context nodes and the task
claim read the directory (`agent_scene_agent_active_idx`, 9503) and, per
scene, its Coordinator jobs through `inbound_coordinator_job_agent_scene_idx`
(9507, `(agent_id, command #>> '{agent_scene,scene_id}')`).

### Tenants (admin)

Same routes group and permission. `T = {org_id, name, source, group_count,
person_count}`: `source` is `identity` for the agent's DingTalk identity org
(listed first, with or without a row) or `created`; `group_count` counts the
org's group scenes in the directory (1:1 chats left out) and `person_count`
its people (§1.3). Errors carry `{error, code}`.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/agents/{id}/tenants` | `{tenants: [T], unassigned_orgs: [{org_id, group_count, person_count}]}`; `unassigned_orgs` are the non-empty orgs that scenes or person data mention without a tenant, by org id |
| POST | `/api/agents/{id}/tenants` | `{org_id, name}` → 201 `{tenant: T}`. `org_id` 1..64 of `[A-Za-z0-9_-]` (400 `invalid_org_id`), `name` trimmed, 1..64 characters (400 `invalid_name`); 409 `tenant_exists` for a tenant row or the identity org |
| PATCH | `/api/agents/{id}/tenants/{orgId}` | `{name}` → `{tenant: T}`; renaming the identity tenant stores a row for it; 404 `tenant_not_found`, 400 `invalid_name` |
| DELETE | `/api/agents/{id}/tenants/{orgId}` | 204: removes the tenant row and its org-scope configuration (bindings, credentials, prompt components, custom MCP servers, pending org OAuth connects) in one transaction; group and person data stays and the org shows as unassigned. 409 `identity_tenant` for the identity org, 404 `tenant_not_found` |
| GET | `/api/agents/{id}/tenants/{orgId}/groups?limit=&offset=&groups_only=` | `{scenes: [S], has_more}`: the tenant's scenes, groups and 1:1 chats (`groups_only=true`: groups only), newest activity first; `limit` 1..200 (default 50), `offset` ≥ 0, else 400 |
| GET | `/api/agents/{id}/tenants/{orgId}/persons` | `{persons: [{staff_id, title, dm_scene_key, last_active_at}]}`: the tenant's people, newest activity first. `title` is the newest 1:1 sender display name, else the person grant title, else the binding title; `dm_scene_key` is the scene_id of their 1:1 chat with the agent (§1.3; `""` when unknown), a scene node of its own, not the person's configuration |

### Context nodes (admin)

A node of the 场域 tree is `(orgId, scopeType, scopeKey)` under
`/api/agents/{id}/tenants/{orgId}/context/{scopeType}/{scopeKey}`:
`scopeType` `org` (`scopeKey` = `orgId`, else 400), `scene` (`scopeKey` = a
scene_id of the agent in that org, a group or a 1:1 chat; 400 for a key
that is not a scene_id, 404 for another scene) or `person` (a person of
that org, §1.3; 404 otherwise). Same routes group and
permission (workspace owner/admin or agent owner); what the caller may
change is `contextCapScopeRights` (§5 "Who may change what"): managers
change org and scene scopes, while a person scope is only that person's (a
live person grant of the caller): a manager reads it and every write there
answers 403 `person_only`. Unknown tenant: 404 `tenant_not_found`.

`N = {scope: {type, org_id, key, title}, scene: null | S, prompts:
[P], connectors: [{id, name, catalog_slug, auth_mode, accepts_credential,
accepts_pat, oauth_available, install_url?, credential: {connected,
account}, enabled}], skills: [{id, name, description, enabled}],
mcp_config: object | null, mcp_config_redacted, rights: {toggle, connect,
edit_prompts, edit_mcp}, can_edit, can_connect, effective:
{prompts: [{name, text, layer, overridden_by?}], connectors: [{id, name,
layer}], skills: [{id, name, layer}], mcp_servers: [{name, layer,
overridden_by?}]}}` with `P = {id, name, order, text, enabled,
updated_by_name, updated_at}`:

- `scope` is the node's own scope (the org, the scene, or the person);
  `scene` is the scene of a scene node or the person's 1:1 chat scene
  (null for org nodes and people without one), for 入站记录 and 记忆.
- `connectors` and `skills` are the offer catalog (connectors enabled in the
  library, with the configure page's flag rules); `enabled` is this scope's
  binding, `credential` this scope's stored credential (`account` is its
  hint; for a person scope only to workspace admins and that person).
- `mcp_config` is the scope's custom MCP servers (null when none; withheld
  with `mcp_config_redacted: true` when the workspace always redacts
  secrets).
- `rights`: what the caller may change in `scope` (§5 "Who may change
  what"); `can_edit` is
  `rights.toggle` and `can_connect` is `rights.connect`, kept for older
  clients.
- `effective` is `contextcap.MergeContext` over global + org (+ scene for a
  scene node, + person for a person node): `layer` is `global`, `org`,
  `scene` or `person`; `overridden_by` names the nearer layer whose
  component of the same name replaces this one (omitted when it applies).
  Connectors switched off in the library and deleted skills are left out,
  and so are switched-off prompt components and custom MCP servers.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `.../context/{scopeType}/{scopeKey}` | `N` |
| PUT | `.../context/{scopeType}/{scopeKey}/bindings` | `{resource_type, resource_id, enabled}` → `{binding: {resource_type, resource_id, enabled, updated_by_name, updated_at}}`; `rights.toggle`; offer-gated (403 unless offered, enable and disable alike) |
| PUT | `.../context/{scopeType}/{scopeKey}/prompts` | `{prompts: [{name, order, text, enabled?}]}` (required) replaces the scope's prompt components → `{prompts: [P]}` ordered by order, then name; `rights.edit_prompts`. At most 20; names trimmed, 1..64 characters, unique (400 `duplicate_prompt_name`); texts trimmed, 1..8000 characters, no NUL (400 `invalid_prompts`); `enabled` defaults to true; `[]` clears them. A component whose name stays keeps its id, and its update stamp when neither order, text nor `enabled` changed |
| PUT | `.../context/{scopeType}/{scopeKey}/mcp-config` | `{mcp_config: object \| null}` (required) → `{mcp_config}`; `rights.edit_mcp`: the agent `mcp_config` format, a JSON object of at most 64 KiB (local command servers allowed here, unlike the configure page); a server object may carry `"disabled": true`; `null` or `{}` clears it |
| PUT | `.../context/{scopeType}/{scopeKey}/credentials` | `{connector_id, bearer}` → `{credential: C}`; `rights.connect`; the mobile PUT's connector rules (an org or scene credential needs an offered connector) |
| DELETE | `.../context/{scopeType}/{scopeKey}/credentials?connector_id=` | 204, idempotent |
| GET | `.../context/{scopeType}/{scopeKey}/github-installations?connector_id=` | `rights.connect`; the configure-page GitHub installation list, for an admin context node |
| POST | `.../context/{scopeType}/{scopeKey}/connections/start` | `{connector_id, return_to?}` → `{authorize_url}` plus the browser binding cookie; the mobile start's rules and errors; the state stores the tenant org |
| DELETE | `.../context/{scopeType}/{scopeKey}/grants` | Managers revoke every configure-page grant of a scene or person scope → `{revoked: n}` (a 1:1 chat node revokes the chat's view grants; the person grant stays). A person scope also loses the 1:1 chat grants its personal links gave the same accounts, under the redemption lock (`contextcap.RevokeGrants`), so the person can redeem a new personal link (no more 409); the scope's configuration stays. 400 for an org node (no grants). The web Context Builder shows it as 配置页访问 → 撤销访问 on scene and person levels |

## 7. Rollout and gating

- Always on: there is no feature flag for the scene and personal layers.
  Connectors are always on as well; they still need a credential key and
  the host allowlist (`docs/internal-mcp-connectors.md`, which also covers
  the production rolling window: the previous binary defaulted connectors
  off there).
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
  that release's binary ignored (since 9510 such rows are that 1:1 chat's
  own configuration again, §1.2), so repeat it after the rollout. A connect
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
  into its person's scope, resolved as that release mapped a 1:1 chat to
  its person, since removed: an org-less job belongs to the identity org
  and only live grants count; such components stay in the person scope
  after 9510; skipped
  while the person is unknown; when two 1:1 chats resolve to one person the
  most recently edited prompt wins; `ON CONFLICT DO NOTHING`, so a replay
  keeps edited components). All are idempotent; the down migrations drop
  org rows before narrowing a check. During the rollout an old replica
  answers 404 on `/api/agents/{id}/tenants*` (the 场域 tree stays empty until
  the rollout completes), still serves the removed `/api/agents/{id}/scenes*`
  routes (a scene prompt it saves lands in `agent_scene_config.prompt`,
  which the new binary no longer reads, so re-enter it as a component after
  the rollout) and rejects a mobile body carrying `org_id` (400, unknown
  field). Its binding queries name `scene` and `person` explicitly, but its
  credential usage queries do not: until the rollout completes (or after a
  rollback) the connected-apps page it serves lists enterprise (org)
  credentials as usage rows with `scope_type: "org"` (display only; it
  never resolves or sends them).
- Stored configuration that becomes applied (9428 and the claim-time
  builder): scene prompts and per-scope custom MCP servers were saved
  under a "configuration only, not applied" contract (9413, 9418). After
  this release they are applied: a 1:1 chat's prompt becomes its person's
  component, and person-level custom MCP servers (possibly with tokens in
  their headers) also apply to group runs that person triggers alone (§8,
  `share_in_groups`). Before deploying, list them and tell their owners or
  clear them:
  `SELECT workspace_id, agent_id, org_id, scene_key FROM agent_scene_config
  WHERE scene_kind = 'dm' AND BTRIM(prompt) <> '';` and
  `SELECT workspace_id, agent_id, scope_type, org_id, scope_key FROM
  context_scope_mcp_config WHERE scope_type IN ('person', 'scene');`.
- 9429-9430 add plain performance indexes concurrently, each alone in its
  file (no pre-migration hook: an invalid leftover only costs speed):
  `context_config_link (agent_id, extra_scene_key)` for the 1:1 chat person
  lookup of that release (gone since scene ids), and
  `context_config_grant (agent_id, scope_type, org_id,
  scope_key)` for the personal-scope holder check and grant revokes.
- Workspace deletion: an old replica deletes a workspace without sweeping
  the tables this release adds (no FKs), so during the window or after a
  rollback their rows can outlive the workspace. After the rollout, remove
  them once per table, for example `DELETE FROM context_prompt_component
  WHERE workspace_id NOT IN (SELECT id FROM workspace);` for
  `context_capability_binding`, `context_connector_credential`,
  `context_config_grant`, `context_config_link`, `agent_scene_config`,
  `context_scope_mcp_config`, `agent_tenant`, `context_prompt_component`,
  `connector_oauth_client` and `connector_oauth_state`.
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

- Strict rights, switches and configure-page editing (9431):
  `context_prompt_component.enabled boolean NOT NULL DEFAULT true`
  (`ADD COLUMN IF NOT EXISTS`, no index, existing rows stay enabled; the
  down migration drops it). During the rollout an old replica applies a
  switched-off prompt component (it does not read the column; its prompt
  PUT keeps the stored switch), merges a switched-off custom MCP server and
  passes its `disabled` key to the runtime, rejects an admin prompts PUT
  carrying `enabled` (400, unknown field), answers 404 or 405 on the two new
  configure-page PUT routes, still returns the org layer to grant holders,
  and still applies the earlier rights: group link holders can toggle and
  connect the group, managers can write a person's bindings, prompt
  components and custom MCP servers. These close once every replica runs
  this binary; switch things off again after the rollout if a test in the
  window depended on it.
- Scene ids (9510; `docs/agent-scene.md` §8): 9510 registers in
  `agent_scene` every conversation for which stored scene configuration
  (bindings, credentials, grants, links, custom MCP servers, prompt
  components, `agent_scene_config`) had a tenant org and an
  openConversationId, and re-keys those rows (`scope_key`, and a personal
  link's `extra_scene_key`) to its scene_id. A conversation is registered
  as `dm` only on positive evidence (a personal link's extra scene, or a
  stored `dm` kind), else as `group`. Rows without an org or with a
  malformed key are left untouched and no longer apply (§1.2); re-running
  it changes nothing. Scene credentials seal their scope key, so
  `ReconcileSceneCredentials` reseals them under the scene_id at every
  server start (idempotent, monotonic); until it ran, a credential still
  under a conversation id is not found. Person rows are not touched:
  configuration a 1:1 chat stored in its person's scope while a 1:1 chat
  was its person (9418 until 9510) stays that person's and is not moved
  into the chat's scene. During the rollout an old replica still keys
  scenes by openConversationId: it finds no configuration under re-keyed
  scenes (a task it claims gets no scene layer), answers 400 to a scene_id
  key, and a scene write it serves lands under the conversation id, which
  the new binary does not read (repeat it after the rollout; a credential
  stored that way is re-keyed at the next server start when its
  conversation has a scene). A task an old replica dispatched carries no
  SceneRef and gets no scene layer on any replica.

## 8. Known limitations (v1)

- `share_in_groups` is stored and shown but not applied (§1.2): a person's
  whole layer (bindings, prompt components and custom MCP servers) still
  applies to group runs that person triggers alone, whatever the switch
  says, and the group sees the run's output. Do not put a person's own token
  into a person-level custom MCP server's headers; connect the account
  instead. The next round gates the personal layer in group runs on the
  switch.
- An orgless agent (no DingTalk identity and no recorded dispatch org) has
  no scenes, so its tasks get no scene layer (`docs/agent-scene.md` §3);
  only its person configuration under org `""` applies (§2). Org `""` is
  not a tenant row: the web 场域 tree does not list it; it is configured on
  the mobile page through personal links only.
- The configure page edits prompt components and remote custom MCP servers
  (url, type, headers, switch); servers that run a local command are added
  on the web only (agent detail → 场域). While the workspace always redacts
  secrets, the page does not see the stored servers and a save replaces
  them all.
- A scene exists only once it was registered from trusted data
  (`docs/agent-scene.md` §4); a conversation the agent has not seen has no
  scene, so it cannot be configured or granted yet. Scene configuration
  9510 could not re-key (no tenant org, a malformed key) no longer applies,
  and configuration a 1:1 chat stored in its person's scope stays that
  person's (§7).
- A tenant's scene list reads the `agent_scene` directory and, per listed
  scene, its newest Coordinator session and session count (through the
  9507 index); its people and the tenant person counts still read every
  1:1 Coordinator job of the agent and decode their JSON. The mobile page's
  scene `kind` comes from the directory.
- Scenes and people are listed per tenant; scenes, memory and
  conversations of an org that is not a tenant are reachable through its
  unassigned org (create a tenant for it) or through 其他记录 (the full
  lists, the memory list capped at the 200 newest rows).
- A tenant's people list is not paged; it holds every person the agent has
  seen there.
- The connected-apps usage counts and lists cover scenes (groups and 1:1
  chats) and people of the identity org; enterprise (org) credentials and
  switches, and the scenes and people of other tenants, are not counted
  there (the 场域 tree shows them per tenant). The offer switches' 「在用」
  confirmation does count
  enterprise switches (as scenes, from the admin view's `orgs`).
- Each relay tool call re-resolves the scope (tenants, the scene fence,
  prompt components, custom MCP servers, bindings, credentials), about
  three times the queries of the scene-only resolver; tenant lists read
  the agent's 1:1 Coordinator jobs once per request. Fine at today's
  volumes; cache per task if it shows.
- The Coordinator's routing catalog (`inboundcoord` `FillSkills`) still sees
  agent skills only; scene/personal skills are available to the executing task.
- Robot Stream path tasks get only the global layer.
- Per-user OAuth exists only for the official apps in the catalog; custom
  connectors take pasted Bearer tokens. GitHub App user tokens only see
  repositories where the App is installed. The configure page's Connect
  GitHub button opens `https://github.com/apps/<GITHUB_APP_SLUG>/installations/new?state=`.
  The callback accepts `code`, `installation_id` and `setup_action`. A code
  from the install page is exchanged without PKCE. A post-install redirect
  without a code continues into user authorization when no credential is
  stored yet (the state is not consumed), or returns to the configure page
  when a credential is already stored. The page lists each covered account
  or organization as all repositories or selected repositories, and offers
  add-account and change-repositories. The list refreshes when the window
  regains focus.
- A manager's scene list on the configure page is capped at the 1000 most
  recently active scenes of the selected tenant; older scenes stay
  configurable from the web 场域 tree and by scene_id. It is read in one
  statement (`contextcap.ListAllAgentScenes`: one read of the scene
  directory, one snapshot), and the page's scene writes (bindings, credentials) refresh
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
  the recipient holds the scope until a manager revokes it (Context Builder
  → 配置页访问 → 撤销访问, §6 "Context nodes"); the person then redeems a
  new link. Verifying the redeeming DingTalk account against the sender
  (unionId ↔ staffId through the corp app, or a code the person sends back
  to the agent) is the follow-up.
- The run that posts an executor-minted link keeps it in its delivery copy
  (final output, or the reply comment of a comment-triggered run): members
  who can read that Issue can see a link minted by a run tied to it. Task
  messages, transcripts and Issue descriptions hold the placeholder. A
  person link is single use and lives 15 minutes, and a leaked one can be
  revoked as above.
- Renaming the identity tenant stores an `agent_tenant` row for that org. If
  the agent is later bound to another DingTalk org, the old org stays a
  (created) tenant with its configuration until it is deleted from the 场域
  tree; tasks that recorded the old org then keep its layers.
- Deleting a tenant and a concurrent enterprise-level write (bindings,
  credential, prompts, custom MCP servers) of that org are not serialized:
  the write can commit after the delete and leave org rows that come back
  if the tenant is created again. Re-check the enterprise level after
  re-creating a tenant.
- A dynamic client registration is reused while its redirect URI and client
  name match; an expired or deleted client (`invalid_client`) is not
  re-registered automatically. A credential whose client is gone, a revoked
  PAT and a revoked token without a refresh token stay stored and keep
  winning person > scene > org > workspace until someone disconnects them
  (the relay answers "reconnect" instead of falling back to the next layer).
- Configuration links are never purged; expired rows only cost space (the
  lookups are indexed, 9429-9430).
- Personal scopes are keyed by `staffId` under the agent's org. The dispatch
  carries no sender corp id, so v1 relies on the dispatcher reporting
  `sender.staffId` only for members of the agent's org (DingTalk robot
  callbacks omit `senderStaffId` for external members). If an external
  sender's own-org staffId ever arrives, it could collide with an internal
  person's; keying person scopes by a globally unique id (unionId) is the
  follow-up.

## 9. Scene routines (例行任务)

A routine is work the agent does in one Agent work scene — a group or a 1:1
chat (`docs/agent-scene.md`) — on a cron schedule or when a webhook request
arrives. There are no org-level or person-level routines.

- **Storage.** A routine is a `run_only` autopilot assigned to the agent plus a
  `context_scope_routine` row (migrations 9520–9522) binding it to its
  `scene_id`, tenant org and kind, with the 1:1 counterpart's
  `openDingTalkId` frozen at creation for delivery (the `person_staff_id`
  column is no longer written or read). The configure pages read the
  counterpart from server-written facts of that scene only: the sender of
  its newest Coordinator job and the senders of the newest 20 user
  messages the EmployeeLoop admitted there (an agent in employee mode has
  no Coordinator jobs): a message's own `senderOpenDingTalkId`, else the
  envelope sender of a single-message window (DWS native messages carry the
  address-book staffId on the message). They must name one person; otherwise creation
  answers `dm_target_ambiguous` (none yet: `dm_target_unknown`). A routine
  created from the chat itself uses that task's own sender. The autopilot
  keeps the trigger, schedule and run history; scene-managed autopilots
  answer 409 `managed_by_scene` on the autopilot routes.
- **Runs carry the scene.** Every run (cron, webhook, run now) carries
  `agent_scene` plus the frozen `scene_routine` binding
  (`protocol.SceneRoutineContextKey`) and no inbound message.
  `ScopeFromTaskContext` reads it as the scene layer in the routine's org.
  A routine never carries a personal layer, in a group or a 1:1 chat,
  whoever created or changed it (冬翔, 2026-10-02): it runs with the
  scene's capabilities only, and a `person_staff_id` left in an older run
  context is ignored. The scene is fenced when the
  run is created (`scene.CheckTenant`): a routine of an org the agent left is
  recorded as skipped, never run elsewhere. Reruns get no layers.
- **Notices.** The Host posts a start notice when the run's task is queued
  and an end notice from the task's terminal transaction, under a savepoint
  so a failed notice never aborts the transition (the clipped final
  output, the failure reason, or 已取消). Terminal paths without a
  completion transaction (cancel, the stale-task sweeper, a runtime that
  fails to start) post it from the task event that `SyncRunFromTask`
  handles, unless the end notice exists or another attempt of the run is
  still active. Both go through `dingtalkresponse` routine
  notices: no Router callback, request ids `routine:<run_id>:start|end`, no
  @ in a group, the frozen counterpart in a 1:1 chat. The run's prompt asks
  the agent not to post the result itself. Completion is not routed through
  the Coordinator; the EmployeeLoop will take it over as a completion event.
- **Webhook runs of an employee-mode agent.** When the agent is in employee
  mode and every live replica advertises the EmployeeLoop marker, the
  webhook ingress freezes `dispatch=employee_direct` in the delivery's
  binding and the delivery worker runs it as one EmployeeTask Direct
  execution (`service.DispatchEmployeeWebhookRoutine`, receipt
  `employee_webhook_occurrence`, source `scene.routine.webhook`, event id
  `delivery/<id>`), through the same admission core as schedule and run-now
  occurrences. The delivery's own AutopilotRun is used (no planned_at, no
  overlap rule); a retry replays the receipt. The packet carries only the
  webhook's payload allowlist (`trigger.payload_fields`: JSON pointers into
  the envelope, default `/event` and `/eventPayload`, frozen at acceptance,
  at most 32 KiB selected, else the occurrence fails) as untrusted data.
  The routine's notices stay the only sender. A delivery frozen for this
  path never switches producer; while the marker is missing it waits.
  `payload_fields` is set on the routine create/PATCH body by managers,
  never from a chat.
- **Rules.** Title and instructions are required; a schedule is a
  five-field cron in an IANA timezone (default Asia/Shanghai) and runs at
  most every 15 minutes. A routine with the same purpose (word set of the
  title), trigger kind, cron and timezone as an existing routine of the
  scene is updated instead of duplicated, and keeps its paused or running
  state. A webhook URL is shown in full only in the create and rotate
  responses. A webhook routine may also require an HMAC signature
  (`X-Hub-Signature-256` over the raw body): agent managers set or clear
  its signing secret (16–256 printable characters) on a route of its own;
  the secret is never returned or logged, views show only
  `trigger.has_signing_secret`, and a chat cannot set it (no MCP tool).
- **Employee-mode agents.** For an agent in employee mode, every occurrence
  is admitted as an independent EmployeeTask with a frozen receipt
  (`employee_routine_occurrence`) instead of the run_only prompt; the start
  and end notices stay the only sender (`docs/employee-loop.md`). The
  routine's `employee_execution` (migration 9820, default `run_only`) picks
  how: `run_only` dispatches the frozen instructions as one Direct Run;
  `employee_decide` admits one `routine.decision` task wake whose model may
  only run the frozen instructions (`run_routine`, then the usual notices),
  reply once in the scene, `wait_for_next_occurrence` or stay quiet, within
  the three-request budget. A decision without a Run completes the
  AutopilotRun with `result.employee_decision` and closes its Task without a
  Run. Choosing `employee_decide` is refused with
  `routine_decision_unavailable` until every live replica runs the decision
  reader, and with `invalid_routine` on a webhook routine (a delivery
  always runs its instructions). While the previous occurrence is undecided
  or its Run is still active, the next one is recorded as `skipped_overlap`.
  A schedule slot whose planned time fell while the routine was paused is
  recorded as skipped even when the routine is resumed before the
  dispatcher's five-minute lateness window closes.
- **Who may change them.** `rights.edit_routines` follows the scene rights:
  agent managers (configure page and admin Context Builder). From a
  conversation, the config-qwen-tag-scene tools (§10) act for the scene
  itself.
- **API.** Configure page: `GET|POST /api/context-capabilities/agents/{agentId}/routines`
  (`scene_id`, `org_id`), `PATCH|DELETE …/routines/{id}`,
  `POST …/routines/{id}/run`, `POST …/routines/{id}/rotate-webhook`,
  `GET …/routines/{id}/runs` (`{runs: [{id, status, source, failure_reason?,
  created_at, completed_at}]}`, newest 30; reading needs only access to the
  scene). Admin:
  the same under `/api/agents/{id}/tenants/{orgId}/context/scene/{scene_id}/routines`,
  plus `PUT …/routines/{id}/webhook-signing-secret` (`{signing_secret}`,
  `""` clears; admin only). Create and PATCH accept `employee_execution`
  (`run_only` | `employee_decide`); views return it.
  Errors carry codes: `invalid_routine`, `routine_requires_dingtalk_identity`, `routine_decision_unavailable`,
  `dm_target_unknown`, `dm_target_ambiguous`, `agent_runtime_required`, `routine_duplicate`,
  `routine_paused`, `scene_kind_without_routines`, `routine_gone` (the
  autopilot was archived or lost its trigger outside the scene API; delete
  the routine, or create it again, which replaces the stale row).

## 10. Scene configuration from a conversation (config-qwen-tag-scene)

A task whose run belongs to a group or 1:1 chat scene gets the
`config-qwen-tag-scene` skill and a managed MCP server of the same name; a
task without a current scene (A2A, rerun, no dispatch, not a tenant, an
earlier binding, an unknown scene) gets neither.

- **Binding.** The claim issues a scene token (`auth.IssueSceneToken`: HMAC
  keyed from `JWT_SECRET`, binding workspace, agent, task and scene_id, 24h)
  into the server's route path `/api/scene-config/mcp/{sct_…}`; the
  Authorization stays the task's task token. Each call verifies the token,
  requires its task, agent and workspace to be the caller's, requires the
  task to be running or dispatched, and re-resolves the task's scene, which
  must still be the token's. The path segment is redacted in the access log
  and the sandbox relay log.
- **Tools** (no scene argument): `scene_config_get`, `scene_prompt_upsert`,
  `scene_prompt_delete`, `scene_mcp_server_upsert` (remote servers only;
  `multica`, `config-qwen-tag-scene` and `c<16 hex>` reserved; fields left
  out keep their stored values),
  `scene_mcp_server_delete`, `scene_capability_set` (offered items only),
  `scene_connect_link` (accounts are never connected in chat; an optional
  `tab` — `scope` or `routines` — opens that page tab (`public`, the
  removed tab, opens the default one), as does
  `tab` on the multica `create_context_config_link` tool: the URL becomes
  `/dingtalk/configure?link=<token>&tab=<tab>`, the tab after the token so
  link redaction still covers the token),
  `scene_routine_list|create|update|delete|run`.
- **Guards.**
  - A routine run is read-only (`routine_run_read_only`): its input may come
    from a webhook. It also issues no configuration link, from
    `scene_connect_link` or the multica `create_context_config_link` tool.
  - Remote MCP servers can be added, re-pointed, switched and deleted from
    a group or a 1:1 chat (冬翔, 2026-10-02, knowing the chat cannot tell
    whether the requester manages the agent). The guards: `http(s)` URLs
    only, header values and URL secrets masked everywhere, a change notice
    in the scene naming who asked (the dispatch sender) and the masked
    address, switching off or deleting any time, and no change from a
    routine run. No manager approval card yet.
  - `scene_routine_run` from the chat keeps the 15-minute minimum after the
    routine's previous run (`routine_run_too_soon`).
  - Refusals (`routine_run_read_only`, `routine_run_too_soon`,
    `task_not_active`, …) are ordinary tool results
    `{"ok": false, "refused": <code>, "message": …}`, not `isError`: the
    sandbox MCP bridge (pi-mcp-extension) replaces an `isError` result's text
    with a generic line, so the agent could not relay why. Only unexpected
    failures stay `isError`.
  - Every write posts a Host change notice into the scene. Header values,
    URL user info and query strings, and full webhook URLs never enter the
    conversation.
- **Skill.** Added at claim when the task has a current scene
  (`taskHasConfigScene`); bundle resolution serves the static skill to any
  task whose claim listed it, so a failing scene lookup cannot fail the
  resolve. A workspace skill of the same name gives way.
