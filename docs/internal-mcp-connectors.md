# Internal MCP connectors

An internal connector is a workspace-owned declaration of one fixed HTTPS MCP
server. It has a name, upstream URL, credential reference, read-only tool
allowlist, authorized Agent IDs and an enabled state. Semantica is the first
instance, not a different transport or a special-purpose relay.

This adapts the workspace/Agent grant and audit model from
`feat/faas-mcp-relay` while removing its separate FaaS and platform-to-FaaS
HMAC hop. Multica already authenticates the short-lived, revocable `mat_`
Agent task token and has a shared Tair client. Each call rechecks the active
task, workspace, Agent grant, connector state and tool allowlist. The request
goes directly from the Multica server to the configured upstream. It forwards
only the JSON-RPC body, fixed Bearer credential and MCP transport headers,
never the `mat_` token or caller-provided URL. Use the existing managed MCP
loopback route for cloud sandboxes.

The MCP 2025-06-18 tools contract supplies `tools/list` and `tools/call`, with
server-defined tool names and input schemas. This initial implementation is a
stateless JSON-RPC subset of Streamable HTTP: it supports local `initialize`,
`notifications/initialized`, `tools/list` and `tools/call`, but not persistent
MCP sessions, server notifications, resource or prompt methods. Expose an authorized connector as
its own MCP server named `internal-<connector ID>` in the Agent's task-scoped
MCP configuration. `initialize` is handled locally; `tools/list` fetches the
upstream list and returns only the configured allowlist. `tools/call` requires
a tool in that allowlist and an object argument. Only explicitly approved
read-only tools may be listed. This is an operator assertion, not a claim that
MCP annotations alone enforce safety. JSON and SSE replies are normalized by
the existing bounded parser (2 MiB, 45 seconds, no redirects). Long calls
flush response headers after authorization to survive the outer 30-second
header deadline. Upstream tool errors retain bounded text so the Agent can
correct its arguments; transport and parse failures remain generic.

## Configuration and rollout

- The database owns connector metadata, workspace/Agent grants and a
  metadata-only call audit. It never stores the upstream Bearer. The schema
  uses no foreign keys and builds each index concurrently in its own migration.
- `credential_ref` is a deterministic env key
  `MULTICA_INTERNAL_MCP_BEARER_<UUID_WITHOUT_DASHES>`. Its value is injected
  only through the Aone environment trait. `src/main.sh` may load only keys
  with that exact prefix and 32 hexadecimal suffix. The server never returns
  the value. A missing credential keeps the connector unavailable and cannot
  be enabled. A new connector can be added without code changes, but an
  operator must provision its credential and redeploy before enabling it.
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

In `/{workspaceSlug}/internal-connectors`, a human workspace owner/admin creates a
connector, chooses an approved upstream URL, tool allowlist and Agents, and
keeps it off. The page shows the generated credential reference and a clear
“waiting for operator credential” state. After the operator injects that
credential and the next pre-release deployment makes it available, the admin
can enable it. An authorized member then sees which Agent can use the
connector, opens a chat with that Agent and asks it to use the connector's
native MCP tools. The page never asks the member for upstream credentials.

Disabled or unauthorized connectors are absent from task MCP discovery. An
already running task may retain a stale MCP entry until its next claim, but
**each call** rechecks current authorization and state, so disabling or
revoking takes effect for calls immediately. Secret rotation needs the Aone
controlled configuration and a new deployment; no GUI field exposes a secret.

Semantica's current `semantica_mcp_relay` tool remains available during the
migration to avoid breaking active pre-release tasks. Once its workspace
connector has a provisioned credential and the real DingTalk task has called
its native `internal-<id>` endpoint successfully, remove the legacy tool,
legacy env keys and Semantica-only status endpoint in a later reviewed change.
Do not advertise the generic connector as accepted merely because the old
Semantica wrapper still works. Do not publish or merge production in this task.

The protocol decisions follow the official
[MCP tools specification](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)
and [Streamable HTTP transport](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports):
list/call semantics, server-supplied schemas, bounded results, and per-request
authorization remain the deployment's responsibility.
