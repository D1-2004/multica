# Local Runner MCP mounts

Local Runner registration and Agent mounting are separate. Pairing a machine
proves account ownership and gives the daemon a stable machine ID; it does not
mount that machine on an Agent. Mounting a Runner makes every available MCP
server in its current inventory available to new tasks for that Agent.

## Local configuration and inventory

Runner reads user-managed MCP Servers from `~/.multica/runner/mcp.json`:

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

Each user entry must configure exactly one of `command` (stdio) or `url`.
Runner preserves each entry and every extension field as configured. At startup,
the CLI also adds the reserved `local_machine` Server to the effective document.
Users must not define that name in `mcp.json`.

`local_machine` is implemented in-process by the Runner CLI and is available
whenever the Runner process is running. It exposes `list_roots`, `read_file`,
`write_file`, `edit_file`, `list_directory`, `stat`, `glob`, `grep`, `shell`,
`shell_output`, and `shell_kill`. No child MCP process or separate MCP CLI
command is required.

Runner reports the complete effective document over WSS: unchanged user entries
plus the generated built-in descriptor. Multica stores that document and its
SHA-256 revision so task composition does not depend on a live inventory probe.
The document can contain commands, arguments, paths, URLs, environment variables,
headers, and credentials and therefore must never be written to logs.

The generated descriptor is `{"type":"builtin","builtin":"shell"}`. It is
routing metadata, not a command to execute. The sandbox daemon projects it to
the task-scoped HTTP relay, and the Runner CLI handles the relayed request in
the same process.

The inventory message is `runner:mcp_inventory`. Its effective configuration is
persisted on the Runner machine record; disconnecting the machine does not clear
the last successfully reported document.

## Account machine and Agent mount

1. `POST /api/me/runner-pairings` creates account-scoped installation material.
2. Device authorization creates or refreshes `runner_machine`; it does not
   create `agent_runner_binding`.
3. `PUT /api/agents/{agentId}/runner-mount` selects one account-owned machine
   for that Agent. Replacing it revokes the previous mount and expires calls.
4. Every Server in the selected machine's last successfully reported effective
   document is mounted automatically, including `local_machine`. Adding,
   removing, or renaming a user MCP Server takes effect for subsequently claimed
   tasks without changing an Agent or rebuilding a Runtime image.

`agent_runner_binding.enabled_mcp_servers` is retained as a last-known
fingerprint snapshot. It is not a per-Agent allowlist.

## Task injection and relay

Every Server in the selected Runner's persisted effective document is injected
into a new task without probing machine state. Name collisions fail task
composition instead of renaming or silently dropping either source. Agent-owned
MCP entries remain direct and are not routed through the Runner.

Each injected entry keeps its original name and points to:

`POST /api/runner-mcp/mounts/{mountId}/servers/{serverName}`

The endpoint requires an Agent task token and checks its workspace, Agent,
task, and user scope. At call time it checks the exact mount, configuration
fingerprint, and online state. `runner_call` is the durable rendezvous. Runner
verifies the dynamic server name and fingerprint again at execution time.

The sandbox daemon advertises `runner-mcp-mounts-v1`. For each injected server,
it preserves the dynamic server name and path while rewriting only the public
Multica origin to the sandbox localhost relay. The relay then uses the existing
daemon/server channel; no MCP name, command, URL, or secret is baked into the
sandbox image. A stale daemon fails the claim before provider startup instead
of leaving OpenCode to time out against the public URL.

Runner verifies the fingerprint again before using its effective configuration.
The built-in `local_machine` Server executes JSON-RPC in the CLI process. Other
stdio processes are reused per server and JSON-RPC IDs are forwarded unchanged.
Local HTTP servers receive JSON POST requests; `Mcp-Session-Id` is retained per
task/mount/server and JSON or SSE responses are returned transparently.
Cancellation notifications are ordinary JSON-RPC notifications. Transport
context cancellation stops a blocked local stdio process and resets it for the
next call.

File operations and Shell `cwd` must be inside a configured Runner root. Shell
commands execute as the operating-system user running the CLI; roots constrain
the starting directory but are not an operating-system sandbox and do not stop
the command from accessing other paths allowed to that user.

The old backend-owned `/api/runner-mcp` fixed filesystem/Shell bundle remains
disabled. Pairing a machine alone never makes a capability available to a task;
selecting the machine for an Agent mounts the CLI-owned replacement.

## Failure behavior

- A machine remains connected with zero Agent mounts.
- Missing local MCP config reports the built-in Server only. Invalid user config
  does not take the machine offline and retains the built-in Server, while the
  invalid user entries are excluded until the file is corrected.
- Offline state does not delete the saved selection or persisted Server names.
  Calls fail at runtime while the machine is unavailable.
- A fingerprint mismatch is rejected at call time; a new task receives the most
  recently reported effective configuration.
- Multi-replica delivery continues to use the existing WSS relay and durable
  `runner_call` polling; inventory lookup uses local memory then Redis.

## History

- 2026-09-04 — Moved the fixed filesystem/Shell capability from the backend
  into the Runner CLI as the always-present reserved `multica_runner` MCP Server.
  Reason: the capability belongs to the local machine process and must start
  whenever Local Runner starts, while still using the same dynamic task relay as
  every user-mounted Runner MCP Server.

- 2026-09-04 — Persisted and mounted the Runner's complete effective MCP
  document without online-state filtering or field rewriting. Reason: configured
  capabilities must remain selected while a Runner is offline, and connection
  failures should surface when the Agent initializes or calls the Server.

- 2026-09-04 — Renamed the built-in MCP Server from `multica_runner` to
  `local_machine`. Reason: the previous name could be confused with the Multica
  platform MCP; the new name tells the model that Shell and filesystem tools
  execute on the selected local machine.

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
