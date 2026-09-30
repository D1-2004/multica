# Internal MCP connectors

An internal connector is a workspace-owned declaration of one fixed HTTPS MCP
server. It has a name, upstream URL, authentication mode, a pinned tool set,
authorized Agent IDs and an enabled state. Semantica is the first instance,
not a different transport or a special-purpose relay.

This adapts the workspace/Agent grant and audit model from
`feat/faas-mcp-relay` while removing its separate FaaS and platform-to-FaaS
HMAC hop. Multica already authenticates the short-lived, revocable `mat_`
Agent task token and has a shared Tair client. Each call rechecks the active
task, workspace, Agent grant, connector state and tool allowlist. The request
goes directly from the Multica server to the configured upstream. It forwards
only the JSON-RPC body, an optional fixed Bearer credential and MCP transport
headers, never the `mat_` token or caller-provided URL. Use the existing
managed MCP loopback route for cloud sandboxes.

The MCP 2025-06-18 tools contract supplies `tools/list` and `tools/call`, with
server-defined tool names and input schemas. This initial implementation is a
stateless JSON-RPC subset of Streamable HTTP: it supports local `initialize`,
`notifications/initialized`, `tools/list` and `tools/call`, but not persistent
MCP sessions, server notifications, resource or prompt methods towards the
Agent. (Towards an official app's upstream the server does keep MCP sessions;
see "Official apps" below.) Expose an authorized connector as
its own MCP server with a stable compact name (`c` plus the first 16 UUID hex
digits) in the Agent's task-scoped
MCP configuration. `initialize` is handled locally; `tools/list` fetches the
upstream list and returns only the pinned allowlist. `tools/call` requires
a tool in that allowlist and an object argument. The simplified administrator
flow pins tools that the approved upstream marks `readOnlyHint=true`; legacy
API callers may still supply an explicit tool list. The trusted upstream and
workspace/Agent grant remain the authority boundary: MCP annotations are
hints, not a guarantee of read-only behavior. JSON and SSE replies are normalized by
the existing bounded parser (2 MiB, 45 seconds, no redirects). Long calls
flush response headers after authorization to survive the outer 30-second
header deadline. Upstream tool errors retain bounded text so the Agent can
correct its arguments; transport and parse failures remain generic.

Pi composes MCP tool names from the server and upstream tool names, and rejects
overlong names before the Agent starts. The compact server name and a bounded
tool presentation name keep that composite at most 62 bytes. Upstream names
over 38 bytes are exposed under stable `t_<hash>` aliases; tool descriptions
retain the original names, and `tools/call` resolves aliases back to the
original only after the configured allowlist check. Alias collisions in one
connector are rejected at configuration time. The relay URL and authorization
continue to use the full connector UUID; shortening the display name does not
shorten the security identifier.

## Configuration and rollout

- The database owns connector metadata, workspace/Agent grants and a
  metadata-only call audit. When the admin uses the GUI to set a Bearer, it
  stores only AES-GCM ciphertext, never plaintext. The schema
  uses no foreign keys and builds each index concurrently in its own migration.
- Bearer connectors retain a deterministic environment fallback key
  `MULTICA_INTERNAL_MCP_BEARER_<UUID_WITHOUT_DASHES>`. Its value is injected
  only through the Aone environment trait. `src/main.sh` may load only keys
  with that exact prefix and 32 hexadecimal suffix. The server never returns
  the value. A missing credential keeps a Bearer connector unavailable and
  unable to be enabled. No-auth connectors do not read this key and send no
  Authorization header. A new Bearer connector can be added without code
  changes through encrypted workspace storage; operator-managed Bearer
  credentials still require provisioning and deployment.
- Alternatively a human workspace owner/admin can set or rotate the Bearer in
  the management page. The server seals it before writing workspace-scoped
  ciphertext. The default key source is a dedicated
  `MULTICA_INTERNAL_MCP_SECRET_KEY` (base64-encoded 32-byte key) provisioned
  through a secret-backed environment channel. Where an existing `env-vars`
  trait cannot mark values secret, operators can explicitly set the
  **non-secret** `MULTICA_INTERNAL_MCP_KEY_SOURCE=jwt-derived`. The server then
  derives a distinct AES key with HMAC-SHA256 over the shared `JWT_SECRET`
  (at least 32 bytes) and a fixed connector-specific context label. This
  follows the repository's existing domain-separated key derivation pattern;
  it does not persist another master secret in the trait. All replicas must
  select the same source and share the same root secret. Rotating that root
  invalidates stored connector credentials until re-encrypted. A stored
  credential takes precedence over the
  legacy environment reference; unreadable ciphertext fails closed. Admin
  responses expose readiness and source but never the value. The dedicated
  key option remains for deployments with supported secret injection.
- The admin connectivity check sends a bounded `tools/list` request to the
  connector's immutable URL with its selected authentication mode. It returns a
  sanitized reachability result, the discovered allowlisted tool names, and
  any configured names not found in the bounded list. Reachable without all
  configured tools is a warning, never a ready/green result. Failure reasons
  identify safe categories (credential rejected, timeout, HTTP status or MCP
  parse failure) without returning upstream bodies, credentials or URLs;
  it does not call a tool, grant an Agent, or expose the Bearer or raw response.
  Because `tools/list` is read-only, an administrator test or initial discovery
  may retry once after a network timeout under a total bounded deadline. A
  credential rejection, invalid MCP response, or upstream tool error is not
  retried. The metadata-only log records the attempt count and safe error
  class; task `tools/call` is never automatically retried.
- `MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES` is an operator-set list of
  approved upstream DNS names/suffixes. URLs must be fixed HTTPS origins plus
  path, with no userinfo, query or fragment. Host validation uses exact match
  or dot-bounded suffix; callers cannot override the target. Operators should
  configure exact upstream hostnames in pre-release and production. A
  connector's URL is immutable after creation, keeping its operator-provisioned
  credential bound to that destination. A different URL requires a new
  connector and credential reference. Production starts with an empty allowlist
  and the feature off. Official app (catalog) connectors are the only
  exception: they bypass this allowlist for exactly their catalog MCP URL
  (see "Official apps").
- The existing Tair client atomically increments task/Agent/workspace rate
  counters with TTL, namespaced by environment and connector ID. A missing
  client or rejected counter fails closed. Audit records IDs, method, tool,
  outcome; never request/response content or credentials.
- Connectors are always on; there is no feature flag. Diamond
  `features.internal_mcp_connectors` is still accepted by the strict runtime
  config parser so existing configs keep loading, but it is ignored. A
  deployment without a credential key or host allowlist still fails closed
  (credentials cannot be stored, upstream URLs fail validation).

## User flow

In `/{workspaceSlug}/internal-connectors`, a human workspace owner/admin enters a
name, an approved HTTPS MCP URL and the Agents that may use it. The creation
form does not ask the admin to transcribe tool names. It discovers the bounded
tool list from the upstream and pins only tools explicitly marked with MCP
`annotations.readOnlyHint=true` in the existing server-side allowlist. Later
changes to the upstream tool list do not silently grant new tools. A connector
with no such tools cannot be created through this simplified flow. Annotations
are untrusted hints, so approved hostnames and administrator trust in the
upstream remain necessary; the annotation alone is not a security guarantee.

Authentication has two explicit modes: no authentication, which sends no
Authorization header at all, and Bearer, whose value is sealed before database
storage. A URL generated by this deployment in the shape
`/api/mcp/connect/<access-token>` is a capability link, not an ordinary
upstream address. On import, the server validates the token against its own
credential store, resolves its immutable public Agent ID, stores the canonical
`/api/mcp/agents/<id>` URL, and seals the token as a Bearer. It never saves,
returns, logs or displays the secret-bearing path. URLs from other deployments
are not silently reinterpreted as local capability links. The page does not
show an empty Bearer input for no-auth connectors; an existing Bearer is only
changed through an explicit rotation action.

The connector is created disabled. The workspace's **Aone FaaS connector**
page has one job: add and manage connectors. It shows current state and an
enable/disable action directly on each card; the management dialog presents
the same state as a clear action rather than an unlabeled checkbox. It does
not duplicate Agent chat or test conversations below the management list.
The admin confirms connectivity and enables the connector for selected
Agents. An Agent's MCP configuration page lists usable workspace-assigned
connectors. Members use the Agent's existing chat surface to invoke native
MCP tools. No upstream secret is placed in the Agent configuration, task
prompt or member-facing page.

Disabled or unauthorized connectors are absent from task MCP discovery. An
already running task may retain a stale MCP entry until its next claim, but
**each call** rechecks current authorization and state, so disabling or
revoking takes effect for calls immediately. GUI-managed credential rotation
takes effect on the next call; operator-managed environment rotation needs a
new deployment. No API returns a saved credential.

## Scene and personal layers

A task's connectors are the Agent's
global grants plus connectors in the Agent's offer catalog that the task's
DingTalk group (scene) or trigger person enabled on the mobile page
`/dingtalk/configure` (see `docs/context-capabilities.md`). An enabled
connector therefore no longer needs an Agent grant to be saved, and a Bearer
connector may be enabled without a workspace credential (people and groups
bring their own token; until one applies, the connector is not mounted).
The connector list reports this as `credential_optional` (every Bearer
connector), and the admin connector page then allows "Enable"
without a workspace credential and labels the connector as using group or
personal credentials. Only workspace
owners/admins can add a connector to an Agent's offer catalog. Claim-time
injection and every relay call run the same task-aware resolver, so a
toggle, offer removal or credential revoke applies to the next call; if the
scene/person layers cannot be read, the task keeps its global connectors.
For a Bearer connector the relay sends the first usable credential of
person > scene > workspace, where a scene credential only applies to an
offered connector. Scene and personal credentials are sealed with the same
connector key, bound to their full scope, and write-only (responses carry a
`••••` hint). The call audit and its log line add `binding_layer`
(`global`/`scene`/`person`) and `credential_layer`
(`none`/`workspace`/`scene`/`person`).

## Official apps (catalog connectors)

Besides custom (Aone FaaS) connectors, a workspace can add an official remote
MCP server from a static catalog (`server/internal/connectorcatalog`) with
one click, ChatGPT-connector style. The catalog fixes, per app, the slug,
name, MCP URL, how people authorize it and the exact hosts the server may
contact for it:

| Slug | MCP URL | Authorization |
| --- | --- | --- |
| `github` | `https://api.githubcopilot.com/mcp/` | the deployment's GitHub App (`oauth_github_app`); a Personal Access Token is also accepted |
| `notion` | `https://mcp.notion.com/mcp` | MCP OAuth with dynamic client registration and S256 PKCE (`oauth_dcr`) |
| `linear` | `https://mcp.linear.app/mcp` | `oauth_dcr` |
| `atlassian` | `https://mcp.atlassian.com/v1/mcp` | `oauth_dcr` (authorization server metadata at the MCP origin) |
| `sentry` | `https://mcp.sentry.dev/mcp` | `oauth_dcr` |
| `asana` | `https://mcp.asana.com/mcp` | `oauth_dcr` |
| `figma` | `https://mcp.figma.com/mcp` | `oauth_dcr` (confidential client) |
| `stripe` | `https://mcp.stripe.com/` | `oauth_dcr` |

A catalog connector is an ordinary `internal_connector` row with
`catalog_slug` set, `auth_mode='oauth'`, `upstream_url` equal to the catalog
URL (immutable, like every connector URL) and `enabled=true`. The partial
unique index on `(workspace_id, catalog_slug)` and an advisory lock keep one
connector per app and workspace. Grants, offers, the scene and personal
layers, rate limits, audit and the relay (`POST
/api/internal-connectors/{id}/mcp`) work as for custom connectors, so
secrets still never reach the sandbox. `validateConnectorURL` accepts a
catalog template URL without the host allowlist. The custom connector create
path refuses those URLs, so only the server writes them.

Where it is managed (2026-09-30): official apps are the agent's global
configuration, so they are added and managed in the agent's connector tab
(agent detail → 配置 → 能力 → 连接器, DetailTab `mcp_config`). 「添加连接器」
→ 官方应用 calls `POST /connector-catalog/{slug}` (idempotent; creates the
workspace catalog connector when it is missing) and then grants that
connector to the agent through the connector update. The shared-account
connect, GitHub PAT, tool refresh and 允许写操作 controls live on that tab
too. The workspace page `/{workspaceSlug}/internal-connectors` lists only
custom (Aone FaaS) connectors; it no longer shows the official app gallery.

### Connecting accounts

There are three credential layers. They resolve person > scene (offered
only) > workspace, exactly like Bearer connectors
(`docs/context-capabilities.md` §3):

- **Workspace shared account** (optional, global layer): an owner/admin
  clicks "connect shared account" on the agent's connector tab on the web
  (the desktop app sends them there, see "Browser binding" below).
- **Personal** and **group (scene)** accounts: people click 连接 on the
  mobile page `/dingtalk/configure`. Connecting also turns the connector on
  for that scope when it is offered.
- **GitHub Personal Access Token**: for apps with `allows_pat`, the existing
  write-only Bearer path accepts a pasted token in every layer (admin `PUT
  .../credential`, mobile `PUT .../credentials`).

The OAuth flow works like this:

1. The start endpoint checks authority: the admin role, or the caller's live
   grant plus the offer rule.
2. It prepares the provider and stores a single-use state: 32 random bytes
   behind the `mcpc.` prefix, stored only as a SHA-256 hash in
   `connector_oauth_state` and valid for 10 minutes. The row also holds the
   validated `return_to` and a sealed payload: the PKCE verifier, the OAuth
   client the authorize URL was built for, and the browser binding below.
3. It returns the provider's authorize URL and sets the state's browser
   binding cookie.
4. The callback consumes the state atomically, checks the browser binding,
   and re-checks the initiating user's authority.
5. It exchanges the code (PKCE S256) with the client the connect was started
   with, reads the account label (GitHub `GET /user` → `login`), seals the
   credential for that scope, and runs the first tool discovery when the
   connector has no tools yet.
6. It sends a 302 to `return_to` with `?connected=<slug>` or
   `?connect_error=<code>`. The codes are `invalid_state`,
   `browser_mismatch`, `access_denied` (only when the provider answered
   `error=access_denied`), `provider_error` (any other provider error),
   `forbidden`, `connector_unavailable`, `exchange_failed` and
   `store_failed`.

An unknown, expired or replayed state has no trusted destination, so the
callback answers 400 with a small HTML page (the DingTalk WebView shows the
response directly) that links back to `/dingtalk/configure` and the app.
`return_to` must be a path or an absolute URL on the app origin
(`MULTICA_APP_URL` / `FRONTEND_ORIGIN`). The default is
`/dingtalk/configure?agent=<id>` for scene and person scopes and
`/<workspace slug>/internal-connectors` for the workspace. The web client
passes `return_to=/<workspace slug>/agents/<agent id>?view=mcp_config` for
a workspace connect, so the admin lands back on the agent's connector tab.

**Browser binding.** A state completes only in the browser that started it.
Without this, anyone allowed to start a connect (for example any holder of a
personal grant) could send the provider authorize URL to a colleague; a
provider that skips its consent screen for an already authorized app (the
GitHub App usually is) would send the colleague's code straight back, and the
colleague's account would be stored in the sender's scope. The start response
therefore sets an HttpOnly, `SameSite=Lax` cookie `multica_mcpc_<16 hex of
the state hash>` holding a random nonce, scoped to the callback path
(`/api/connector-oauth/callback` or `/api/github/authorize`) and living 10
minutes; the sealed state keeps only the nonce's SHA-256. The callback reads
and clears that cookie and refuses a missing or different nonce
(`browser_mismatch`, constant-time comparison) before exchanging anything.
The state is burned either way. The page that starts a connect must be
served from the callback's origin (DCR apps: `MULTICA_APP_URL`, else
`FRONTEND_ORIGIN`; GitHub: `FRONTEND_ORIGIN`), so keep `MULTICA_APP_URL`
equal to `FRONTEND_ORIGIN`.

Every start binds this way, for the workspace scope and the mobile scopes
alike; there is no link that binds whichever browser opens it (an earlier
desktop "begin" link did, which let an admin hand it to someone who then
authorized their own account into the admin's workspace; it was removed and
`/api/connector-oauth/begin` is no longer routed). The desktop app's API
responses land in its own cookie jar, not in the system browser that signs
in, so the desktop never calls the start endpoint: "connect shared account"
opens `<daemon_app_url>/<workspace slug>/agents/<agent id>?view=mcp_config`
(the agent's connector tab on the web, with `daemon_app_url` from
`/api/config`) in the system browser and tells the admin to finish
connecting there. When the server
publishes no app URL, it asks the admin to open the web version instead. The
desktop list refetches on focus, so the new account shows up on return.

- **DCR apps** discover the protected resource and authorization server
  metadata and register one client per connector (RFC 7591). The
  registration is stored sealed in `connector_oauth_client` and reused; it
  is replaced only when the redirect URI changes. They redirect to
  `<app origin>/api/connector-oauth/callback`. That route is public (no
  Multica session; the hashed single-use state and its browser binding
  cookie are the proof) and is rate-limited per IP
  (`RATE_LIMIT_CONNECTOR_OAUTH_CALLBACK`, default 60 per minute, only
  enforced with `REDIS_URL`). The same limit covers the GitHub callback
  requests that carry a `mcpc.` state; other
  `/api/github/authorize` requests (the install flow) are unchanged.
- **Replaced registrations are kept.** Changing the app origin changes the
  redirect URI, so the next connect registers a new client (or promotes a
  kept earlier registration for that URI). The replaced registration stays
  in the sealed row (`previous`, newest first, at most 8), and every OAuth
  credential records the client that issued it (`oauth.client_id`): a
  refresh always uses that client. Overwriting it would make every existing
  connection's next refresh fail with `invalid_grant` and delete it. A token
  whose client is no longer known is not refreshed and not deleted; the
  Agent is told to ask the user to reconnect.
- **GitHub** has no dynamic registration. It reuses the deployment's GitHub
  App (`GITHUB_APP_CLIENT_ID` / `GITHUB_APP_CLIENT_SECRET`; OAuth is offered
  only when both are set) and its registered callback
  `<FRONTEND_ORIGIN>/api/github/authorize`. `GitHubAuthorizeCallback`
  hands states with the `mcpc.` prefix to the connector callback before its
  install-cookie check; every other state keeps the GitHub App install flow.
  **The GitHub App's callback URL must stay `/api/github/authorize`.**
  GitHub App user tokens only see repositories where the App is installed,
  so the UI links `https://github.com/apps/<GITHUB_APP_SLUG>/installations/new`
  (`install_url`, omitted when `GITHUB_APP_SLUG` is unset).

### Credentials, refresh and sessions

- OAuth credentials use the same `InternalConnectorSecretBox` payloads as
  Bearer credentials: workspace ciphertext on `internal_connector`, scene and
  personal rows in `context_connector_credential`. The payload gains an
  optional `oauth` object `{access_token, refresh_token, expires_at,
  token_type, scope, account, client_id}`, and `bearer` always mirrors the current
  access token, so an older binary could still use an unexpired token. Every
  binding check after `Open` is unchanged. Hints show `@<account>` or
  `OAuth`, never token material. Scene and personal credential views carry
  `kind` (`oauth` or `bearer`); the admin list carries `credential_account`.
- Refresh happens when a token expires within 60 seconds, and after any
  upstream 401. It runs under a row lock (`SELECT … FOR UPDATE` in its own
  transaction; re-open, refresh only if still stale, reseal, commit), so
  concurrent relay calls on all replicas refresh once. An upstream 401 causes
  one forced refresh and one retry, because a 401 proves the call did not
  run. A pasted PAT is never retried. A transient refresh failure keeps
  using a token that is still valid. The locked section (lock wait, token
  request, reseal, commit; bounded to 30 seconds) runs detached from the
  relay request: providers that rotate refresh tokens invalidate the old one
  as soon as they answer, so an answer must be stored even when the call
  that triggered it was cancelled.
- `invalid_grant` deletes the credential (a workspace credential is
  cleared). The UI then shows it as not connected, and the Agent gets a tool
  error asking the user to reconnect (audit outcome `reconnect_required`). An
  expired OAuth credential without a refresh token is skipped during
  resolution, so the next layer applies.
- Upstream calls use a session-aware Streamable HTTP client
  (`pkg/remotemcp/session.go`). It sends the request as is. When the server
  answers HTTP 400 or 404 (no session, an unknown or expired one; servers on
  the MCP SDK's session-map pattern answer 400 for an unknown id), it drops
  any cached session, runs `initialize` (protocol `2025-06-18`, clientInfo
  `multica`) and `notifications/initialized`, sends `MCP-Protocol-Version`,
  and retries once; those answers prove the request did not run, so this
  holds for `tools/call` too. A JSON-RPC error mentioning the session or
  initialization triggers the same only for methods without side effects
  (`tools/list`, `resources/list`, `prompts/list`, `ping`): a `tools/call`
  that ran and then failed can report such text, and it is never replayed.
  `Mcp-Session-Id` is cached in process for 10 minutes per (connector id,
  SHA-256 of the access token). JSON-RPC request ids are unique per process,
  because every relay call builds its own client while calls with the same
  credential share a cached session, and session servers route responses by
  id. JSON and SSE replies are parsed. Sessions are a per-replica
  optimization, never shared state.
- Egress: catalog connectors use a proxy-aware client
  (`http.ProxyFromEnvironment`) that checks the app's catalog hosts on every
  request and follows no redirects. Direct dials must resolve to public
  addresses. Behind a proxy the resolved-IP check cannot apply to the proxy
  hop, so the fixed host list is the boundary there. Custom connectors keep
  the proxy-less internal client.

### Tool pinning

A catalog connector starts with no tools, and until its tools are discovered
it is not mounted: claim, relay and the mobile global list skip it. Discovery
runs with a connected account:

- when the first credential is stored in any layer (OAuth callback or a
  pasted PAT);
- on the admin "refresh tools" action, which uses the workspace account, or
  else the most recently updated usable scene or personal account. A
  credential that cannot list the tools (a revoked PAT, a failed refresh) is
  skipped for the next one; when accounts exist but none works, the action
  answers 502 `discovery_failed`.

The server stores the snapshot (`discovered_tools`, at most 256
`{name, read_only}` entries) and pins `allowed_tools` to the tools marked
`readOnlyHint`. When the admin enables writes (`write_enabled`) it pins all
tools, read-only first. Either way it keeps at most 64, and the
presented-name collision rules apply. Toggling writes re-pins from the
snapshot. The server ignores `allowed_tools` sent by a client for a catalog
connector.

### Admin API

These are workspace owner/admin routes with the same guard as the rest of
the connector library (`RequireWorkspaceMCPHumanIssuer`):

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/workspaces/{id}/connector-catalog` | `{apps: [{slug, name, mcp_url, auth_kind, allows_pat, oauth_available, connector_id, install_url?}]}`; `connector_id` is null until the app is added |
| POST | `/api/workspaces/{id}/connector-catalog/{slug}` | `{connector: <connector>}`; 201 when created, 200 when it already existed, 404 for an unknown slug |
| POST | `/api/workspaces/{id}/internal-connectors/{connectorId}/oauth/start` | optional body `{return_to?}` → `{authorize_url}` for the workspace shared account, plus the browser binding cookie. Unknown body fields (including the removed `external_browser`) are 400 `invalid request body`. Errors are `{error, code}`: 403 `forbidden`; 400 `not_oauth` or `invalid_return_to`; 503 `oauth_unavailable`, `app_origin_missing` or `credential_storage_unavailable`; 502 `provider_unavailable` |
| POST | `/api/workspaces/{id}/internal-connectors/{connectorId}/tools/refresh` | `{discovered, allowed_tools}`; 409 `no_connected_account`, 502 `discovery_failed`, 400 `not_official_app` |
| PATCH | `/api/workspaces/{id}/internal-connectors/{connectorId}` | also accepts `write_enabled` (catalog connectors only, 400 otherwise) |
| GET | `/api/workspaces/{id}/internal-connectors` | items add `catalog_slug`, `write_enabled`, `discovered_tool_count` and `credential_account` (`@login`, `OAuth` or `""`) |

The mobile start endpoint and the new detail fields are in
`docs/context-capabilities.md` §6.

### Rollout

- Run migrations 9409–9412 before the new binary serves traffic (the Aone
  start script does this). They are idempotent and add no foreign keys, and
  the unique catalog index is built concurrently in its own file.
- During a rolling deploy, an old binary does not serve the new routes
  (404) and its credentials PUT refuses a GitHub PAT (400). What keeps it
  from mounting catalog connectors is its host allowlist, not its auth mode
  check: its claim injection and relay validate connectors without the auth
  mode, and it treats `auth_mode='oauth'` like `bearer`. As long as no
  catalog host is covered by `MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES`,
  it rejects the catalog URLs, never mounts catalog connectors and answers
  503 on their relay calls. Keep the catalog hosts out of that allowlist
  until every replica runs the new binary; otherwise an old replica calls a
  globally granted catalog connector directly with the sealed `bearer`,
  without the proxy, refresh or sessions, and fails once the token expires.
  Custom connectors are unaffected. The window closes once every replica
  runs the new binary.
- Every replica needs the same connector credential key, `GITHUB_APP_*`
  variables and app origin, because a state, registration or credential
  created on one replica is completed or refreshed on another.

Semantica's current `semantica_mcp_relay` tool remains available during the
migration to avoid breaking active pre-release tasks. Once its workspace
connector has a provisioned credential and the real DingTalk task has called
its compact native MCP endpoint successfully, remove the legacy tool,
legacy env keys and Semantica-only status endpoint in a later reviewed change.
Do not advertise the generic connector as accepted merely because the old
Semantica wrapper still works. Do not publish or merge production in this task.

The protocol decisions follow the official
[MCP tools specification](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)
and [Streamable HTTP transport](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports):
list/call semantics, server-supplied schemas, bounded results, and per-request
authorization remain the deployment's responsibility.
