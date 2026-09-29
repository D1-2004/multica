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
MCP sessions, server notifications, resource or prompt methods. Expose an authorized connector as
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
  and the feature off.
- The existing Tair client atomically increments task/Agent/workspace rate
  counters with TTL, namespaced by environment and connector ID. A missing
  client or rejected counter fails closed. Audit records IDs, method, tool,
  outcome; never request/response content or credentials.
- Diamond `dt-fde-multica-runtime.json` / `DEFAULT_GROUP` controls
  `features.internal_mcp_connectors`. It defaults on only in explicitly
  identified pre-release environments, off in production/unknown; an explicit
  false wins immediately. Release the schema before adding that JSON field to
  Diamond because older binaries reject unknown keys.

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
