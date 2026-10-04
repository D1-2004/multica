# Workspace MCP connections in Settings

Settings → MCP connections manages revocable links bound to the current workspace.
The agent's MCP access page continues to issue its existing agent-only links.
Workspace links never replace or widen an agent credential.

Hosted Agent MCP `describe_agent` reads the authenticated endpoint's public
card independently of its runtime's A2A execution adapters. It still verifies
the current endpoint, workspace, agent and owner binding, the owner's current
workspace membership, and the agent's active (not archived) state. An active
MCP credential remains usable when the A2A publication switch is disabled;
that existing MCP contract is unchanged. Runtime admission remains enforced
when delegating actual work, so a successful profile read proves no execution
capability. This keeps MCP discovery separate from invocation, as specified by
the [MCP tools contract](https://modelcontextprotocol.io/specification/2025-06-18/server/tools).

Workspace MCP is enabled by default in every environment, including production.
The API authentication gate and the frontend public flag use the same default.
Operators can explicitly disable it with `FF_WORKSPACE_MCP_ENDPOINT_ENABLED=false`
(also accepted by the Aone runtime-config allowlist); there is no environment-name
check and no prerequisite Diamond update. Agent-link replacement remains disabled.

The connection uses the existing `workspace_mcp_token` record, SHA-256 credential
hash, scopes, expiry, live membership/role check and revocation. Human workspace
owners/admins issue and revoke connections through the existing token API.
The creation response additionally returns a one-time `url`:
`/api/mcp/workspaces/{workspaceId}/connect/{accessToken}`.
The link adapter accepts only `wmcp_` secrets, overrides ambient cookies/headers,
and delegates to the same authentication and MCP dispatcher as the Bearer route.
Workspace IDs in the URL must match the credential; caller-supplied resource IDs
still pass native workspace authorization. No personal PAT is embedded in links.

The UI shows workspace name, connection name, read/read-write permissions, expiry,
one-time copy controls and a revocation list. Secret values stay in component
memory only and are cleared on workspace changes; list/cache responses carry no
secrets. Errors must not log secret-bearing API responses. Both application and
bundled nginx request logging suppress the link credential. Existing personal
MCP clients continue working, but Settings creates only workspace connections.

Validation focuses on credential precedence, invalid token types, cross-workspace
denial, immediate revocation, UI scope/permission/expiry and OpenCode remote MCP
connection with a URL alone. These tests cover the new authorization boundary,
not a duplicate of every existing tool's business tests.
