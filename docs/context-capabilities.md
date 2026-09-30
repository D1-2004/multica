# Context capabilities: scene and personal connectors and skills

Status: v1 contract (2026-09-29), plus official app OAuth connections
(2026-09-30, `docs/internal-mcp-connectors.md` "Official apps"). Modelled on
Claude Tag (Claude in Slack):
admins own a library, channels and people opt in, and the effective toolset of
one run depends on where the message came from and who sent it.

## 1. Layers

A claimed task's capabilities are the union of three layers. Scene and personal
layers only ever ADD capabilities; they never remove a global one.

| Layer | Key | Who edits | Where | Stored in |
| --- | --- | --- | --- | --- |
| Global | agent | workspace admin / agent manager | web: connector library, skills library, agent detail | `internal_connector_agent`, `agent_skill` (existing) |
| Offer catalog | agent | agent manager | web: agent detail → "Context capabilities" tab | `context_capability_binding` (`scope_type='offer'`) |
| Scene (群场域) | agent + org_id + openConversationId | members of that DingTalk group | mobile H5 `/dingtalk/configure` | `context_capability_binding` (`scope_type='scene'`) |
| Personal (个人) | agent + org_id + staffId | that DingTalk person | mobile H5 `/dingtalk/configure` | `context_capability_binding` (`scope_type='person'`) |

- Resources are library items only: `resource_type='connector'` (an
  `internal_connector` row) or `resource_type='skill'` (a workspace `skill`
  row). End users never enter URLs; they pick from the agent's offer catalog.
- A scene or personal binding is effective only while the same resource is in
  the agent's enabled offer catalog. Removing an offer disables every scene and
  personal use at once (the check runs at claim time and on every connector call).
- The connector library switch (`internal_connector.enabled`) stays the global
  kill switch for every layer.

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
  (`dingtalkOpenConversationID`). 1:1 chats have no scene layer.
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
    Single use, 15 minutes, 365-day person grant. The first account to
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

## 6. API

Mobile (requires an `auth_method=dingtalk` human session; `RequireDingTalkHumanActor`;
not workspace-scoped). Every
agent-scoped call loads the agent (must exist, not archived, a user agent) and
requires the caller's live grant under the agent's current org: a scene read or
write needs a scene grant for that exact cid, a person read or write needs a
person grant for that exact staffId. Bodies are size-limited and reject unknown
fields. `B = {resource_type, resource_id, enabled}`,
`C = {connector_id, hint, updated_at, kind}` (`kind` is `oauth` or `bearer`);
every `bindings` list contains only resources that are currently in the
enabled offer catalog.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/context-capabilities/links/redeem` | `{token}` → grant (source `agent_link`); returns `{agent_id, workspace_id, scope_type, scope_key, scope_title}`; 410 when unknown, malformed, expired or (person) already consumed; 409 when a different account already holds that person's live grant. The link and the grant commit in one transaction |
| GET | `/api/context-capabilities/agents` | `{agents: [{id, name, avatar_url, workspace_id, scopes: [{scope_type, scope_key, scope_title, source, expires_at}]}]}`: agents with a live grant for the caller |
| GET | `/api/context-capabilities/agents/{agentId}` | `{agent, global: {connectors: [{id, name, catalog_slug}], skills: [{id, name, description}]}, offers: {connectors: [{id, name, tools, accepts_credential, credential_required, catalog_slug, auth_mode, accepts_pat, oauth_available, install_url?}], skills}, person: null \| {scope_key, scope_title, source, expires_at, bindings: [B], credentials: [C]}, scenes: [{scope_key, scope_title, source, expires_at}], jsapi_available}`. Needs any grant for the agent (else 403). `global.connectors` are the agent's granted, enabled connectors with a ready workspace credential (and, for official apps, discovered tools). `catalog_slug` names the official app (`""` for custom connectors). `auth_mode` is `none`, `bearer` or `oauth`. `accepts_credential` means the connector accepts a pasted token: a Bearer connector, or an official app that allows a PAT. `accepts_pat = auth_mode == 'oauth' && accepts_credential`. `credential_required` means the connector uses a credential (Bearer or OAuth) and has no workspace credential. `oauth_available` means the server can run the app's OAuth sign-in (GitHub needs `GITHUB_APP_CLIENT_ID` and `GITHUB_APP_CLIENT_SECRET`); the page hides 连接 when it is false and offers the PAT form when `accepts_pat`, and treats a missing field (older backend) as true. `install_url` is the GitHub App installation page (omitted when there is none). `jsapi_available` is true only when H5 signing is configured and the caller has a person grant (the resolve endpoint needs one) |
| GET | `/api/context-capabilities/agents/{agentId}/scenes/{sceneKey}` | `{scene: {scope_key, scope_title, source, expires_at}, bindings: [B], credentials: [C]}` (scene grant required). The key may be percent-encoded |
| PUT | `/api/context-capabilities/agents/{agentId}/bindings` | `{scope_type, scope_key, resource_type, resource_id, enabled}` → `{binding: B}`; 403 unless the resource is in the enabled offer catalog (enable and disable alike) |
| PUT | `/api/context-capabilities/agents/{agentId}/credentials` | `{scope_type, scope_key, connector_id, bearer}` → `{credential: C}`. The connector must be enabled and offered to the agent, or (person scope only) globally granted to it (else 403). It must accept a pasted token: `auth_mode='bearer'`, or an official app that allows a PAT (GitHub) (else 400). bearer is 1..4096 bytes, with no CR/LF/NUL and no surrounding whitespace; 503 without a credential key. The first credential of an official app also discovers and pins its tools |
| DELETE | `/api/context-capabilities/agents/{agentId}/credentials?scope_type=&scope_key=&connector_id=` | 204, idempotent; allowed after the offer was removed. For an OAuth credential this is "disconnect" (the provider grant is not revoked) |
| POST | `/api/context-capabilities/agents/{agentId}/connections/start` | `{scope_type, scope_key, connector_id, return_to?}` → `{authorize_url}` for connecting an official app account through OAuth, plus the browser binding cookie (the WebView that calls it must also open the URL). The caller needs a live grant for exactly that scope (403). The connector must be an enabled official app that is offered to the agent (scene) or offered or globally granted (person); otherwise 403 with `code: "forbidden"`, including for an unknown connector. Other errors are `{error, code}`: 400 `not_oauth` or `invalid_return_to`; 503 `oauth_unavailable`, `app_origin_missing` or `credential_storage_unavailable`; 502 `provider_unavailable`. The browser returns to `return_to` (default `/dingtalk/configure?agent=<id>`) with `?connected=<slug>` or `?connect_error=<code>`. The callback re-checks the grant and offer, stores the credential for that scope and turns the connector on for it (see `docs/internal-mcp-connectors.md` "Official apps") |
| POST | `/api/context-capabilities/agents/{agentId}/scenes/resolve` | `{chat_id, open_conversation_id?}` → `{scene: {scope_key, scope_title, source, expires_at}}` (JSAPI path, see §5). 400 without `chat_id` or when `open_conversation_id` differs from the converted one; 403 without a person grant or for a group the agent never served; 503 when chatId conversion is unavailable; 502 when DingTalk rejects the chatId |
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

## 8. Known limitations (v1)

- The Coordinator's routing catalog (`inboundcoord` `FillSkills`) still sees
  agent skills only; scene/personal skills are available to the executing task.
- Robot Stream path tasks get only the global layer.
- Per-user OAuth exists only for the official apps in the catalog; custom
  connectors take pasted Bearer tokens. GitHub App user tokens only see
  repositories where the App is installed.
- The mobile page offers 连接 only for offered connectors. The server also
  accepts a person-scope connect for a connector that is only globally
  granted, but the page does not show one; the admin gallery says so when an
  app has no shared account.
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
