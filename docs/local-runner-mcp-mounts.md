# Local Runner MCP mounts

Local Runner registration and Agent mounting are separate. Pairing a machine
proves account ownership and gives the daemon a stable machine ID; it does not
mount that machine on an Agent. Mounting a Runner makes every available MCP
server in its current inventory available to new tasks for that Agent.

## Local configuration and inventory

Runner reads `~/.multica/runner/mcp.json`:

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"],
      "env": { "LOCAL_SECRET": "value-kept-on-the-machine" }
    },
    "browser": {
      "url": "http://127.0.0.1:8931/mcp",
      "headers": { "Authorization": "Bearer value-kept-on-the-machine" }
    }
  }
}
```

Each entry must configure exactly one of `command` (stdio) or `url`. HTTP URLs
are restricted to localhost or a loopback IP. Runner sends only name,
transport, availability, and a SHA-256 configuration fingerprint over WSS.
Commands, arguments, paths, URLs, environment variables, headers, and secrets
never leave the machine.

The inventory message is `runner:mcp_inventory`. It is held by the owning WSS
replica and mirrored to Redis with the Runner online TTL so an API replica can
render settings or dispatch a task without persisting secret-bearing config.

## Account machine and Agent mount

1. `POST /api/me/runner-pairings` creates account-scoped installation material.
2. Device authorization creates or refreshes `runner_machine`; it does not
   create `agent_runner_binding`.
3. `PUT /api/agents/{agentId}/runner-mount` selects one account-owned machine
   for that Agent. Replacing it revokes the previous mount and expires calls.
4. Every inventory entry reported as available is mounted automatically. Adding,
   removing, or renaming a local MCP server takes effect for subsequently
   claimed tasks without changing an Agent or rebuilding a Runtime image.

`agent_runner_binding.enabled_mcp_servers` is retained as a last-known
fingerprint snapshot for transient inventory-cache misses. It is not a
per-Agent allowlist when a live inventory is present.

## Task injection and relay

Every currently available live inventory entry is injected into a new task. An
Agent's ordinary remote MCP entry wins on a name collision and remains direct.

Each injected entry keeps its original name and points to:

`POST /api/runner-mcp/mounts/{mountId}/servers/{serverName}`

The endpoint requires an Agent task token and checks its workspace, Agent,
task, and user scope. It then checks the exact mount, last inventory snapshot,
current inventory fingerprint, availability, and online state. `runner_call`
is the durable rendezvous. Runner verifies the dynamic server name and
fingerprint again at execution time.

The sandbox daemon advertises `runner-mcp-mounts-v1`. For each injected server,
it preserves the dynamic server name and path while rewriting only the public
Multica origin to the sandbox localhost relay. The relay then uses the existing
daemon/server channel; no MCP name, command, URL, or secret is baked into the
sandbox image. A stale daemon fails the claim before provider startup instead
of leaving OpenCode to time out against the public URL.

Runner verifies the fingerprint again before using its local configuration.
Stdio processes are reused per server and JSON-RPC IDs are forwarded unchanged.
Local HTTP servers receive JSON POST requests; `Mcp-Session-Id` is retained per
task/mount/server and JSON or SSE responses are returned transparently.
Cancellation notifications are ordinary JSON-RPC notifications. Transport
context cancellation stops a blocked local stdio process and resets it for the
next call.

The old `/api/runner-mcp` fixed filesystem/shell bundle is disabled. Pairing a
machine alone never makes a capability available to a task; selecting the
machine for an Agent does.

## Failure behavior

- A machine remains connected with zero Agent mounts.
- Missing or invalid local MCP config reports an empty inventory and does not
  take the machine offline.
- Offline, absent, unavailable, or fingerprint-mismatched servers are omitted
  from new task configuration and rejected at call time.
- Multi-replica delivery continues to use the existing WSS relay and durable
  `runner_call` polling; inventory lookup uses local memory then Redis.

## History

- 2026-09-03 — Changed Agent mounts to expose every available MCP from the
  selected Runner dynamically and added `runner-mcp-mounts-v1` executor
  negotiation. Reason: per-server switches duplicated the Runner's local
  configuration, and stale sandbox daemons could leave dynamic MCP URLs pointed
  at the public service until OpenCode timed out.

- 2026-09-03 — Split account pairing from Agent mounting and introduced
  fingerprint-bound multi-MCP inventory, explicit enablement, transparent task
  relay, and local stdio/HTTP execution. Reason: machine ownership and Agent
  capability authorization had been coupled, which implicitly exposed a fixed
  tool bundle and could not safely represent multiple secret-bearing local MCP
  configurations.
