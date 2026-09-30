# Internal MCP connectors

An internal connector is a workspace-owned declaration of one fixed HTTPS MCP
server. It has a name, upstream URL, authentication mode, a discovered tool snapshot,
authorized Agent IDs and an enabled state. Semantica is the first instance,
not a different transport or a special-purpose relay.

This adapts the workspace/Agent grant and audit model from
`feat/faas-mcp-relay` while removing its separate FaaS and platform-to-FaaS
HMAC hop. Multica already authenticates the short-lived, revocable `mat_`
Agent task token and has a shared Tair client. Each call rechecks the active
task, workspace, Agent grant, connector state. The request
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
MCP configuration. `initialize` is handled locally; `tools/list` exposes every upstream tool and
`tools/call` forwards the requested tool with object arguments. No read-only,
annotation, deployment-origin, tool-count or saved-snapshot filter applies.
The stored `allowed_tools` field is retained as discovery metadata for existing
clients, not as an authorization boundary. New upstream tools become visible
on the next list request. Upstream authentication decides permitted operations.
JSON and SSE replies are normalized by
the existing bounded parser (2 MiB, 45 seconds, no redirects). Long calls
flush response headers after authorization to survive the outer 30-second
header deadline. Upstream tool errors retain bounded text so the Agent can
correct its arguments; transport and parse failures remain generic.

Pi composes MCP tool names from the server and upstream tool names, and rejects
overlong names before the Agent starts. The compact server name and a bounded
tool presentation name keep that composite at most 62 bytes. Upstream names
over 38 bytes are exposed under stable `t_<hash>` aliases; tool descriptions
retain the original names, and `tools/call` resolves aliases back to the
original using the live upstream list. Alias collisions in an upstream list are reported as invalid MCP metadata. The relay URL and authorization
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
  sanitized reachability result, all discovered tool names. A valid upstream list, including an empty list,
  is ready; stale discovery metadata does not block connectivity. Failure reasons
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
  or dot-bounded suffix; callers cannot override the target. Pre-release and production use the approved domain families
  `dingtalk.com,alibaba-inc.com`. A
  connector's URL is immutable after creation, keeping its operator-provisioned
  credential bound to that destination. A different URL requires a new
  connector and credential reference. Feature availability remains deployment-configured.
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
tool list from the upstream without filtering annotations or tool names.
An empty list is valid. The snapshot is display metadata only; changes to the
upstream list do not require recreating the connector.

Authentication has two explicit modes: no authentication, which sends no
Authorization header at all, and Bearer, whose value is sealed before database
storage. A URL in the shape `/api/mcp/connect/<access-token>` from any approved
domain is a capability link. Import removes the token from the stored URL and
seals it as a Bearer credential. Only the outgoing request reconstructs the
capability path. The target deployment validates its own credential; the relay
never looks it up in the local credential store. The secret-bearing URL is
never persisted in plaintext, returned, logged or displayed. The page does not
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
