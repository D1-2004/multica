# Context capabilities: scene and personal connectors and skills

Status: v1 contract (2026-09-29), plus official app OAuth connections
(2026-09-30, `docs/internal-mcp-connectors.md` "Official apps"), plus the
configuration architecture for 智能体 / 场域 / 个人 (2026-09-30, migrations
9413-9417, see §1.1 and §1.2), plus the 连接器 tab redesign with the
connected-apps API and manager access to the configure page (2026-09-30, no
migration, see §1.1, §5 and §6), plus 1:1 chat = person, per-scope custom MCP
servers and the scene page parallel to the 连接器 tab (2026-09-30, migrations
9418-9419, see §1.1, §1.2, §5 and §6). Modelled on Claude Tag (Claude in Slack):
admins own a library, channels and people opt in, and the effective toolset of
one run depends on where the message came from and who sent it.

## 1. Layers

A claimed task's capabilities are the union of three layers. Scene and personal
layers only ever ADD capabilities; they never remove a global one.

| Layer | Key | Who edits | Where | Stored in |
| --- | --- | --- | --- | --- |
| Global (智能体) | agent | workspace admin / agent manager | web: agent detail → 配置 → 能力 → 连接器 (official apps: 「对所有用户启用」; Aone FaaS grants) / Skills (agent skills) | `internal_connector_agent`, `agent_skill` (existing) |
| Offer catalog | agent | agent manager | web: agent detail → 配置 → 能力 → 连接器 (official app dialog switch 「允许群聊、个人连接自己的账号」; Aone FaaS row switch 「群聊/个人」) / Skills, section 「允许在场域 / 个人中开启」 | `context_capability_binding` (`scope_type='offer'`) |
| Scene (场域: 群聊) | agent + org_id + openConversationId | members of that DingTalk group; agent managers from the web and from the configure page | web and mobile `/dingtalk/configure` tab 「本会话」; web agent detail → 场域 → scene → 配置 | `context_capability_binding` (`scope_type='scene'`), custom MCP servers in `context_scope_mcp_config`, prompt in `agent_scene_config` |
| Personal (个人), also every 1:1 chat (单聊) scene | agent + org_id + staffId | that DingTalk person; agent managers for bindings and custom MCP servers of a 1:1 chat (not accounts) | web and mobile `/dingtalk/configure` tab 「我的」, or the 1:1 chat's scene there and on the web | `context_capability_binding` (`scope_type='person'`), custom MCP servers in `context_scope_mcp_config`; a 1:1 chat's prompt stays on the scene in `agent_scene_config` |

- Resources are library items only: `resource_type='connector'` (an
  `internal_connector` row) or `resource_type='skill'` (a workspace `skill`
  row). End users never enter URLs; they pick from the agent's offer catalog.
- A scene or personal binding is effective only while the same resource is in
  the agent's enabled offer catalog. Removing an offer disables every scene and
  personal use at once (the check runs at claim time and on every connector call).
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
- **场域 (scene; the per-scene side).** Only DingTalk IM scenes: a group
  chat or a 1:1 chat. Scene identity everywhere is (agent, platform
  `dingtalk`, org_id = the agent's `agent_dingtalk_identity.org_id`,
  scene_key = openConversationId); kind is `dm` for positively 1:1
  conversation types (`contextcap.IsDirectConversationType`), else `group`.
  Every scene has its own scene prompt (场域提示词), written only by the
  agent-manage set (workspace owner/admin or the agent owner) from the web;
  later the agent may maintain it itself.
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
  - The web scene page (agent detail → 场域 → scene → 配置) is parallel to
    the 连接器 tab: the scene prompt, then (for a 1:1 chat) the person it is
    bound to, then 「MCP」 (the offered Aone FaaS connectors, each switched
    on for this scene with 「在本场域启用」 and, when it takes a token,
    设置令牌 / 移除令牌 for the scene's own token, plus the scene's own
    「自定义 MCP 服务器」) and 「连接应用」 (the offered official apps, each
    in a dialog: 「在本场域启用」 and the scene's own account). Account
    actions use the configure page's credential and connect routes and are
    offered only when the detail's `can_connect` is true; otherwise the
    page says the person connects on the configure page.
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
lists the agent's scenes; a scene opens 入站记录 (its Coordinator transcript,
via `inbound_session_id`), 记忆 (its `scene_memory` row, loaded by
`memory_id`) and 配置 (scene prompt, scene connectors and skills). Switching
scene, sub-tab or view while the scene prompt has unsaved edits asks before
discarding them, like the pane's own tabs. Under the scene list, 其他记录
opens the full inbound conversation list (the old 入站会话 view) and the full
scene memory list, so conversations the scene list does not cover (no
openConversationId, another DingTalk org or robot endpoint) and memory rows
of an earlier DingTalk binding stay reachable. On the configure page a 1:1
chat scene is labelled 「单聊 · {title}」 and edits its person's
configuration; a manager who is not that person sees 「由本人连接」 instead of
account actions.

### 1.2 配置 vs 生效 (stored vs applied at runtime)

This round builds the configuration architecture only. Runtime resolution
(`ScopeFromTaskContext`, claim injection, the connector relay, instruction
composition) is unchanged.

| Setting | Stored | Applied at runtime |
| --- | --- | --- |
| Agent global connectors and skills | yes | yes |
| Offer catalog | yes | yes (gates scene and personal bindings) |
| Group scene connectors, skills, credentials | yes | yes |
| Personal connectors, skills, credentials | yes | yes, in every run the person triggers, including group runs (see below) |
| 1:1 chat connectors, skills, credentials (= its person's, §1.1) | yes, in the person scope | yes, as the personal layer: a 1:1 run carries its person (§2) and no scene layer |
| Rows an earlier release stored on a 1:1 chat's own scene key | yes | no, and no longer shown either (not on the scene pages, not in connected-apps usage) |
| Custom MCP servers of a group or person scope (`context_scope_mcp_config`, a 1:1 chat's are its person's) | yes | no, stored and shown only |
| Scene prompt (`agent_scene_config.prompt`), group and 1:1 | yes | no, stored and shown only |
| `share_in_groups` (「在群聊中由我触发时也可用」) | yes | no: until the runtime reads it, personal bindings still apply in group runs the person triggers regardless of the switch |

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
- org_id: the agent's `agent_dingtalk_identity.org_id` (same source as scene
  memory), or `""` when the agent has no DingTalk identity. Bindings store the
  org_id they were created under and resolution matches it exactly.
- Binding drift: a staffId or openConversationId only means something inside
  the org it was dispatched in. For a bound agent, the dispatch's recorded
  agent org (`external_identity.dws.orgId`) must equal the current org; when
  the dispatch recorded none, the agent must not have been (re)bound
  (`bound_at`) after the task was created. Otherwise the task gets no scene or
  personal layer and cannot mint links, so a queued, running or retried task
  from an earlier binding never reads another org's bindings or credentials.
- Tasks with no dispatch context (web comments, autopilot, plain chat) get only
  the global layer.
- v1 covers the Digital Employee dispatch path (and Coordinator issue tasks that
  carry its context). The robot Stream path is a follow-up.

## 3. Resolution

Claim time (`buildClaimedTaskResponse` → `injectRunnerMCP`) and call time
(`CallInternalConnector`) run the same resolver, so a toggle or revoke applies
to the next tool call of a running task.

Connectors: effective set = global grants ∪ scene/person bindings (deduped by
connector id). Each connector is mounted once as `c<16hex>` via the existing
server relay; secrets never reach the task row, claim payload or sandbox.

Credential selection per connector call, first match wins:

1. personal credential — only when the task's trigger person set one;
2. scene credential — only when the task's scene set one and the connector
   is in the agent's enabled offer catalog (a scene credential serves every
   member's run in the group; offering is the admin's opt-in to that);
3. workspace credential (existing sealed ciphertext or environment fallback).

A bearer or OAuth connector with no available credential in any applicable
layer is not mounted. An expired OAuth credential without a refresh token
does not count as available, so the next layer applies. An official app
connector is also not mounted until its tools have been discovered.
`auth_mode='none'` connectors need no credential. When the scene or
person layers cannot be read (a transient error, or a replica before
migrations 9400+), the task keeps its global connectors with workspace
credentials instead of losing them.

Skills: effective set = `agent_skill` (enabled) ∪ scene/person skill bindings,
deduped by skill id, filtered by `filterAgentSkillsForRuntime`, then built-ins
and the DWS skill as today. `ResolveTaskSkillBundles` accepts agent skills plus,
for a task with a scene or person scope, the requested refs that are enabled
offered skills, so a scene toggle between claim and bundle resolution cannot
fail the task and tasks without a scope load no extra skills.

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
  (`context_config_link`, with `source_task_id`).
- JSAPI group picker (secondary). With a person grant for the agent (a verified
  DingTalk identity), the page signs `dd.config` through
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
  person. Every offer gate applies to managers exactly as to grant holders,
  and the OAuth callback re-checks the manager permission like a grant.
  Manager access is computed per request and never stored as a grant.
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
requires the caller's live grant under the agent's current org: a scene read or
write needs a scene grant for that exact cid, or managing the agent and the cid
being a scene the agent has seen (§5 "Managers"; 404 for an unknown scene); a
person read or write needs a person grant for that exact staffId (managers
included). A request naming a 1:1 chat scene (`scope_type: "scene"` with its
key) acts on that chat's person scope with the §5 "1:1 chat scenes"
authority: 403 `person_only` for a manager's credential write or connect,
409 `dm_person_unknown` for a write when the person is unknown. Bodies are size-limited and reject unknown
fields. `B = {resource_type, resource_id, enabled}`,
`C = {connector_id, hint, updated_at, kind}` (`kind` is `oauth` or `bearer`);
every `bindings` list contains only resources that are currently in the
enabled offer catalog. A personal connector binding also carries
`share_in_groups` (boolean, 「在群聊中由我触发时也可用」, default false);
other bindings omit it. Scene entries `{scope_key, scope_title, source,
expires_at, kind}` carry `kind`: `group` or `dm` (the `agent_scene_config`
kind, else `group`; a 1:1 scene reached through a personal link is always
registered there as `dm`).

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/context-capabilities/links/redeem` | `{token}` → grant (source `agent_link`); returns `{agent_id, workspace_id, scope_type, scope_key, scope_title}` (for a 1:1 personal link with `extra_scene_key` the DM scene grant is added as well, §5); 410 when unknown, malformed, expired or (person) already consumed; 409 when a different account already holds that person's live grant. The link and the grant commit in one transaction |
| GET | `/api/context-capabilities/agents` | `{agents: [{id, name, avatar_url, workspace_id, access, scopes: [{scope_type, scope_key, scope_title, source, expires_at}]}]}`: agents with a live grant for the caller (`access: "grant"`, newest grant first), then the other non-archived user agents the caller manages (`access: "manager"`, `scopes: []`, by name). An agent both granted and managed is listed once with `access: "manager"` and its grants. Older backends send no `access` (read it as `grant`) |
| GET | `/api/context-capabilities/agents/{agentId}` | `{agent, global: {connectors: [{id, name, catalog_slug}], skills: [{id, name, description}]}, offers: {connectors: [{id, name, tools, accepts_credential, credential_required, catalog_slug, auth_mode, accepts_pat, oauth_available, install_url?}], skills}, person: null \| {scope_key, scope_title, source, expires_at, bindings: [B], credentials: [C]}, scenes: [{scope_key, scope_title, source, expires_at, kind}], access, jsapi_available}`. Needs any grant for the agent or managing it (else 403). `access` is `manager` or `grant`. `scenes` lists the granted scenes and, for a manager, every other scene of the agent (newest activity first, at most 1000, `source: "manager"`, `expires_at: ""`, titles and kinds from the admin scene union). `global.connectors` are the agent's granted, enabled connectors with a ready workspace credential (and, for official apps, discovered tools). `catalog_slug` names the official app (`""` for custom connectors). `auth_mode` is `none`, `bearer` or `oauth`. `accepts_credential` means the connector accepts a pasted token: a Bearer connector, or an official app that allows a PAT. `accepts_pat = auth_mode == 'oauth' && accepts_credential`. `credential_required` means the connector uses a credential (Bearer or OAuth) and has no workspace credential. `oauth_available` means the server can run the app's OAuth sign-in (GitHub needs `GITHUB_APP_CLIENT_ID` and `GITHUB_APP_CLIENT_SECRET`; every app needs the connector credential key and an app origin, the other deployment checks of the start endpoint); the page shows 连接 only for a literal `true` (a missing field from an older backend hides it too) and offers the PAT form when `accepts_pat`. `install_url` is the GitHub App installation page (omitted when there is none). `jsapi_available` is true only when H5 signing is configured and the caller has a person grant (the resolve endpoint needs one) |
| GET | `/api/context-capabilities/agents/{agentId}/scenes/{sceneKey}` | `{scene: {scope_key, scope_title, source, expires_at, kind}, scope: null \| {type, key, title}, bindings: [B], credentials: [C], can_connect}` (scene grant or manager required; for a 1:1 chat also its person's grant; a manager without a grant gets `source: "manager"`). `scope` is where the configuration lives: `{type: "scene", key: <cid>}` for a group, `{type: "person", key: <staffId>, title}` for a 1:1 chat, `null` (with empty lists) for a 1:1 chat whose person is unknown. `bindings` and `credentials` are those of `scope` (a person's connector bindings carry `share_in_groups`). `can_connect` is whether the caller may store, remove or connect credentials there (false for a manager on a 1:1 chat). A manager on a 1:1 chat who is not a workspace owner/admin gets that person's credentials with `hint: ""` (the connected state, `kind` and `updated_at` stay), as on the admin scene page and the connected-apps page. `kind` comes from the admin scene union (a key it does not know is `group`). The key may be percent-encoded |
| PUT | `/api/context-capabilities/agents/{agentId}/bindings` | `{scope_type, scope_key, resource_type, resource_id, enabled, share_in_groups?}` → `{binding: B}`; 403 unless the resource is in the enabled offer catalog (enable and disable alike). `share_in_groups` is only accepted for a person scope (`scope_type='person'`, or a 1:1 chat's scene key, which maps to its person) and `resource_type='connector'` (else 400), and only from the person (a manager on a 1:1 chat gets 403 `person_only`); omitted keeps the stored value |
| PUT | `/api/context-capabilities/agents/{agentId}/credentials` | `{scope_type, scope_key, connector_id, bearer}` → `{credential: C}`. The connector must be enabled and offered to the agent, or (person scope only) globally granted to it (else 403). It must accept a pasted token: `auth_mode='bearer'`, or an official app that allows a PAT (GitHub) (else 400). bearer is 1..4096 bytes, with no CR/LF/NUL and no surrounding whitespace; 503 without a credential key. The first credential of an official app also discovers and pins its tools |
| DELETE | `/api/context-capabilities/agents/{agentId}/credentials?scope_type=&scope_key=&connector_id=` | 204, idempotent; allowed after the offer was removed. For an OAuth credential this is "disconnect" (the provider grant is not revoked) |
| POST | `/api/context-capabilities/agents/{agentId}/connections/start` | `{scope_type, scope_key, connector_id, return_to?}` → `{authorize_url}` for connecting an official app account through OAuth, plus the browser binding cookie (the WebView that calls it must also open the URL). The caller needs a live grant for exactly that scope, or, for a group scene, to manage the agent (403; 404 for a manager's unknown scene). A 1:1 chat scene connects its person's account: the state stores the person scope (and, sealed, the requested chat, so the callback re-checks that same request), only the person may start it (403 `person_only` for a manager), and an unknown person answers 409 `dm_person_unknown`. The connector must be an enabled official app that is offered to the agent (scene) or offered or globally granted (person); otherwise 403 with `code: "forbidden"`, including for an unknown connector. Other errors are `{error, code}`: 400 `not_oauth` or `invalid_return_to`; 503 `oauth_unavailable`, `app_origin_missing` or `credential_storage_unavailable`; 502 `provider_unavailable`. The browser returns to `return_to` (default `/dingtalk/configure?agent=<id>`) with `?connected=<slug>` or `?connect_error=<code>`. The callback re-checks the grant and offer, stores the credential for that scope and turns the connector on for it (see `docs/internal-mcp-connectors.md` "Official apps") |
| POST | `/api/context-capabilities/agents/{agentId}/scenes/resolve` | `{chat_id, open_conversation_id?}` → `{scene: {scope_key, scope_title, source, expires_at, kind: "group"}}` (JSAPI path, see §5; group scenes only). 400 without `chat_id` or when `open_conversation_id` differs from the converted one; 403 without a person grant or for a group the agent never served; 503 when chatId conversion is unavailable; 502 when DingTalk rejects the chatId |
| GET | `/api/dingtalk/jsapi-config?url=` | `dd.config` signature `{corp_id, agent_id, time_stamp, nonce_str, signature}` for the page URL without `#fragment`. Any authenticated human (`RequireHumanActor`). The URL must be absolute http(s) on the app origin (`MULTICA_APP_URL` / `FRONTEND_ORIGIN`); without an app origin the endpoint answers 503 rather than signing arbitrary pages. `signature = sha1("jsapi_ticket=<t>&noncestr=<n>&timestamp=<ts>&url=<url>")` in hex, where `<url>` has its query percent-decoded like DingTalk's reference signer and `time_stamp` is Unix seconds. The ticket (`GET {oapi}/get_jsapi_ticket`) is cached in process until 5 minutes before expiry and never returned; the corp access token it is fetched with is redacted from transport errors before they are logged. 503 when `DINGTALK_H5_CORP_ID` / `DINGTALK_H5_AGENT_ID` are unset, no app origin is configured, or the direct client is unavailable |

Admin (workspace routes, human actor, the same permission as editing the
agent's skills: workspace owner/admin or the agent owner). Adding a connector
offer needs a workspace owner/admin, like the connector library and its
grants: an agent owner who is only a member may keep or remove connector
offers, and their GET lists only the connectors already offered.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/agents/{id}/context-capabilities` | `{enabled, library: {connectors: [{id, name, enabled, auth_mode}], skills: [{id, name, description}]}, offers: {connector_ids, skill_ids}, scenes: [{scope_key, scope_title, bindings: [B], credential_count}], persons: [...], configure_url}`; `configure_url = <app origin>/dingtalk/configure?agent=<id>` ("" without an app origin). For an owner/admin the library lists every workspace connector, so saving never drops connector offers. Offers whose connector or skill no longer exists are omitted (and dropped by the next save). `enabled` is always true (clients still handle false from older backends) |
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
agent owner; agent actors get 403). Only that set writes the scene prompt.
`{sceneKey}` is a percent-encoded openConversationId (decoded once, like the
mobile scene route); a malformed key answers 400. A scene is one the agent
has seen: the union of its `scene_memory` rows (under the agent's org, or any
org while it has no DingTalk identity), its inbound Coordinator conversations
(`inbound_coordinator_job` openConversationIds, skipping jobs that recorded
a different agent org), its `agent_scene_config` rows, its scene
bindings and its scene credentials (a group can store a token without turning
anything on; the scene still lists, so a manager can open it and remove the
credential). Kind is `dm` only on positive evidence: the newest Coordinator
job's conversation type is a 1:1 type (`single`, `p2p`, `private`,
`direct`); when that job has no type, or there is no job, the configured kind
(`agent_scene_config`, written by a 1:1 link redemption or a prompt save);
else `group`. `scene_memory.scene_kind` is not used, because the memory
writer records `dm` for every type that is not `group`, empty or unknown
types included. A 1:1 chat's configuration (bindings, credentials, custom
MCP servers) is its person's (§1.1): the detail and the write routes act on
that scope through the same resolver as the mobile routes (the caller manages
the agent; a manager who also holds the person's grant is that person).

`S = {scene_key, kind, title, org_id, last_active_at, inbound_session_id,
inbound_count, memory_id, has_prompt}`:
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
| GET | `/api/agents/{id}/scenes?limit=&offset=` | `{scenes: [S], has_more}`, newest activity first; `limit` 1..200 (default 50), `offset` ≥ 0, else 400 |
| GET | `/api/agents/{id}/scenes/{sceneKey}` | `{scene: S, prompt: {text, updated_at, updated_by_name}, scope: null \| {type, key, title}, bindings: [{resource_type, resource_id, enabled, updated_by_name, updated_at}], offers: {connectors: [{id, name, catalog_slug, auth_mode, accepts_credential, accepts_pat, oauth_available, install_url?, credential: {connected, account}}], skills: [{id, name, description}]}, mcp_config: object \| null, mcp_config_redacted, can_connect}`; 404 for a scene the agent never saw. `prompt.updated_at` / `updated_by_name` are `""` until someone writes a prompt. `scope` is where this page's configuration lives (`scene` for a group, `person` with the staffId and name for a 1:1 chat, `null` for a 1:1 chat whose person is unknown, with empty `bindings`, no credentials and `mcp_config: null`); `bindings`, the connectors' `credential` and `mcp_config` are those of `scope`. `bindings` lists only offered resources; `offers.connectors` lists offered connectors that are enabled in the library, with the configure page's flag rules (`accepts_credential`, `accepts_pat`, `oauth_available`, `install_url`, §6 Mobile). `credential.connected` means a credential is stored for the connector in `scope`; `account` is its hint (`@login`, `OAuth`, `••••abcd`), for a person's scope only to workspace admins and that person (`""` for other agent managers). `mcp_config` is the scope's custom MCP servers (the agent `mcp_config` format), `null` when none; a workspace that always redacts secrets (`always_redact_env`) withholds it (`null`, `mcp_config_redacted: true`) like the agent's own. `can_connect` is whether the caller may store, remove or connect credentials of `scope` through the configure page's credential and connect routes: false for a manager on a 1:1 chat (they are that person's), for an unknown person, and for a session that is not a DingTalk sign-in (those routes need one) |
| PUT | `/api/agents/{id}/scenes/{sceneKey}/prompt` | `{prompt}` (required; trimmed; at most 8000 characters; `""` clears it) → `{prompt: {text, updated_at, updated_by_name}}`. Upserts `agent_scene_config` with the scene's kind and title; the prompt belongs to the scene itself, a 1:1 chat's included; 404 for an unknown scene. Stored only (§1.2) |
| PUT | `/api/agents/{id}/scenes/{sceneKey}/bindings` | `{resource_type, resource_id, enabled}` → `{binding: {resource_type, resource_id, enabled, updated_by_name, updated_at}}`; writes the scene's `scope` (a 1:1 chat's person; 409 `dm_person_unknown` when unknown); offer-gated like the mobile PUT (403 unless offered, enable and disable alike); 404 for an unknown scene |
| PUT | `/api/agents/{id}/scenes/{sceneKey}/mcp-config` | `{mcp_config: object \| null}` (required) → `{mcp_config}`: the scene's custom MCP servers, stored in its `scope` (`context_scope_mcp_config`; a 1:1 chat's person, 409 `dm_person_unknown` when unknown). The value is the agent `mcp_config` format: a JSON object of at most 64 KiB (arrays, strings and other values are 400); `null` or `{}` clears it and deletes the row (response `{"mcp_config": null}`). Managers only (these admin routes); not on the mobile API; 404 for an unknown scene, 400 for a malformed key or body. Stored only (§1.2) |
| GET | `/api/agents/{id}/scene-memory/{memoryId}` | One scene memory row (the scene memory list item shape); same permission as the other scene memory routes; 400 for a malformed id, 404 when the agent has no such row |

The list and the single-scene lookup behind the detail and both PUTs read
the agent's Coordinator jobs through
`inbound_coordinator_job_agent_conversation_idx` (9417, `(agent_id,
BTRIM(command #>> '{event,data,conversation,openConversationId}'))`): the
list scans the agent's jobs only, and a single scene probes its own
conversation. The key filter is a plain predicate so a cached generic plan
still uses the index.

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

## 8. Known limitations (v1)

- Configuration only (§1.2): the scene prompt, custom MCP servers and
  `share_in_groups` are stored and shown but not applied. In particular a
  person's bindings still apply to group runs that person triggers whatever
  the switch says; the next round gates the personal layer in group runs on
  `share_in_groups`, mounts custom MCP servers and composes the scene prompt
  into instructions.
- A 1:1 chat is bound to its person only once the Coordinator has seen that
  person write in it (a job with a sender staffId) or the person redeemed a
  personal link minted there; until then the chat has no configuration.
  Old rows stored on a 1:1 chat's own scene key are ignored, not migrated.
- The admin scene list reads every Coordinator job of the agent (through the
  9417 index, not the whole workspace) and decodes their JSON to group them;
  a single-scene lookup reads only that conversation's jobs. The mobile
  page's scene `kind` lookup reads only the indexed `agent_scene_config`.
- The scene list covers the agent's current DingTalk org only; memory rows
  and conversations of an earlier binding are reachable only through 其他记录
  (the full lists, the memory list capped at the 200 newest rows).
- The Coordinator's routing catalog (`inboundcoord` `FillSkills`) still sees
  agent skills only; scene/personal skills are available to the executing task.
- Robot Stream path tasks get only the global layer.
- Per-user OAuth exists only for the official apps in the catalog; custom
  connectors take pasted Bearer tokens. GitHub App user tokens only see
  repositories where the App is installed.
- A manager's scene list on the configure page is capped at the 1000 most
  recently active scenes; older scenes stay configurable from the web scene
  list and by key. It is read in one statement
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
