# Unified Sandbox MCP Relay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the `multica mcp` CLI discovery/call path from Agent tasks and expose every effective MCP Server through the Agent runtime's native MCP tool surface, using one loopback HTTP relay inside a sandbox only for backend-hosted and Local Runner sources.

**Architecture:** Multica stores every backend-hosted MCP definition and the exact raw MCP configuration most recently reported by each Local Runner. Task claim always merges all backend-hosted definitions, every configured Server from the selected Runner, the Agent's direct `mcp_config`, and its task overlay; machine online state and MCP initialization are not claim-time filters. A separate route map—not marker fields added to MCP entries—tells the sandbox which server names must be projected to the single loopback relay. Agent-configured and task-overlay MCP Servers remain direct native runtime configuration.

**Tech Stack:** Go 1.26.1, Chi, sqlc, PostgreSQL 17, MCP Streamable HTTP, gorilla/websocket, OpenCode `OPENCODE_CONFIG_CONTENT`, ACP `mcpServers`, Vitest/Go tests, SLS structured logs.

---

## Contract and boundaries

### Authoritative records

1. The backend-hosted MCP registry/dispatcher is the source of truth for Multica-hosted Servers; every registered definition is included in every task config, without a health probe.
2. `agent.mcp_config` remains the source of truth for MCP Servers explicitly configured on an Agent.
3. `agent_task_queue.runtime_mcp_overlay` remains the short-lived source of truth for task-scoped connected-app MCP Servers; its secrets are cleared on terminal task state.
4. A Local Runner's source file remains `~/.multica/runner/mcp.json`, and the Runner reports its complete raw document without field removal, normalization or redaction. Multica stores the exact bytes and their SHA-256 revision on `runner_machine`; disconnect never clears them.
5. `agent_runner_binding` records which machine an Agent selected. Binding is the only claim-time Runner eligibility check; machine online state is not consulted while composing MCP config.
6. `task_mcp_mount` records each relayed Multica/Runner server selected for a task. It stores the source revision and routing IDs. The raw Runner document is referenced from `runner_machine`, while the exact merged task config is delivered in the claim payload.
7. Structured logs still exclude raw configuration and business payloads. "No redaction" applies to storage and task delivery, not to writing credentials into logs.

### Mount record

```text
task_mcp_mount
  id                uuid primary key
  task_id           uuid not null
  workspace_id      uuid not null
  agent_id          uuid not null
  effective_name    varchar(128) not null
  source_kind       varchar(32) not null  # multica | runner
  source_name       varchar(128) not null
  source_ref        jsonb not null        # only safe IDs, never connection material
  config_revision   text not null
  tool_count        integer null
  catalog_hash      text null
  last_error_code   varchar(64) null
  expires_at        timestamptz not null
  created_at        timestamptz not null
  updated_at        timestamptz not null
```

Allowed `source_ref` shapes:

```json
{"kind":"multica","server_name":"multica"}
{"kind":"runner","binding_id":"<uuid>","machine_id":"<uuid>","server_name":"source-name"}
```

### Effective names and conflicts

- Reserve every backend-hosted Server name; today that includes `multica`.
- Preserve configured names when they are unique.
- Preserve Runner names when they are unique.
- Do not rename, normalize, prefix or silently drop colliding names. Fail task claim with `mcp_server_name_conflict` and report both sources so the configuration owner can resolve it.
- Record both `effective_name` and `source_name`; for a valid task they are identical.

### One relay, multiple logical MCP Servers

Only managed MCP entries injected into a sandbox use the relay:

```json
{
  "mcpServers": {
    "multica": {
      "type": "http",
      "url": "http://127.0.0.1:${MULTICA_RELAY_PORT}/mcp/<opaque-mount-id>"
    },
    "llm-wiki": {
      "type": "http",
      "url": "http://127.0.0.1:${MULTICA_RELAY_PORT}/mcp/<opaque-mount-id>"
    }
  }
}
```

The claim carries two separate values:

```json
{
  "mcp_config": {"mcpServers": {"multica": {}, "llm-wiki": {}, "agent-direct": {}}},
  "mcp_relay_mounts": {
    "multica": "<opaque-mount-id>",
    "llm-wiki": "<opaque-mount-id>"
  }
}
```

`mcp_config` contains the exact stored server entries. `mcp_relay_mounts` is routing metadata outside that document. Inside the sandbox, the daemon creates the runtime-facing projection: only names present in `mcp_relay_mounts` have their transport replaced by `type=http` plus the loopback mount URL. This transport substitution is unavoidable because a Runner's local stdio command or localhost URL cannot execute inside the sandbox; the original claim document remains unchanged. Names not present in the route map are passed through unchanged. There is one physical relay listener, not one aggregated MCP catalog.

### Call path

```text
Agent runtime native MCP client
  -> source_kind=multica/runner
     -> 127.0.0.1:$MULTICA_RELAY_PORT/mcp/{mount_id}
     -> sandbox relay adds task identity and forwards bytes
     -> /api/task-mcp/mounts/{mount_id}
        -> source_kind=multica: existing Multica MCP business dispatcher
        -> source_kind=runner: existing runner_call -> Runner WebSocket -> local MCP
  -> source_kind=agent/task_overlay/runtime
     -> runtime native direct HTTP/SSE connection or local stdio child process
  <- each logical server preserves its own JSON-RPC response, structuredContent, isError and session headers
```

### Failure semantics

- A selected Runner is mounted from its last reported full config even when disconnected. Calls fail with `runner_offline`; claim never suppresses its Servers because of connection state.
- A Runner config revision change fails the old mount with `runner_mcp_configuration_changed`; the next task receives the newly reported raw config.
- Agent-configured, task-overlay and runtime-native MCP failures are reported by the native runtime client; the Multica relay does not reinterpret them.
- Failure to load persisted configuration or persist the mount manifest fails task claim and requeues the task. It must never start the Agent with a partial config.
- Do not probe machine presence, open a Runner call, or invoke MCP `initialize`/`tools/list` while claiming. Connection and protocol errors surface only when the runtime initializes or calls that Server.

### Observability and data minimization

Emit these structured events:

```text
mcp_mount_manifest_created task_id agent_id runtime_id mount_count source_counts manifest_hash
mcp_mount_injected task_id mount_id effective_name source_kind config_revision
mcp_server_initialized task_id mount_id effective_name protocol_version tool_count catalog_hash duration_ms
mcp_tool_called task_id mount_id effective_name tool_name call_id duration_ms status error_code
mcp_mount_call_failed task_id mount_id effective_name source_kind error_code
```

Never log MCP request arguments, results, URL, headers, command, environment, task token, pairing token or session ID. Persisting and delivering the exact Runner config is intentional, but it must not be printed. `runner_call` may hold a request/result only while dispatching; terminal completion must clear the payload and retain only call metadata and error code.

## File map

- Create `server/internal/handler/task_mcp_mount.go`: build and persist the task-scoped mount manifest, resolve names, and render opaque mount entries.
- Create `server/internal/handler/task_mcp_relay.go`: authenticate an opaque managed mount and dispatch JSON-RPC only to Multica or Runner.
- Create `server/internal/handler/task_mcp_mount_test.go`: builder, exact-config, collision and offline-independent tests.
- Create `server/internal/handler/task_mcp_relay_test.go`: transport, authorization, routing, session-header and failure tests.
- Create `server/pkg/mcpprotocol/mount.go`: shared mount source and route-map types.
- Modify `server/pkg/runnerprotocol/messages.go`: report the complete raw Runner MCP document and its revision rather than only a sanitized inventory.
- Modify `server/cmd/multica/cmd_runner.go` and `server/cmd/multica/cmd_runner_mcp.go`: read, validate and report the exact bytes of `~/.multica/runner/mcp.json` without removing or rewriting fields.
- Modify `server/internal/handler/runner_mcp.go`: retain Runner execution only; remove task-config injection from this file.
- Modify `server/internal/handler/multica_mcp.go`: extract a reusable authenticated JSON-RPC dispatcher without changing `/api/mcp` client compatibility.
- Modify `server/internal/handler/daemon.go`: build one complete manifest before returning a claim and attach the rendered config.
- Modify `server/internal/daemon/runner_mcp.go`: rename/generalize Runner-only rebasing into managed mount rebasing.
- Modify `server/internal/daemon/daemon.go`: pass the complete effective MCP config to every supported runtime.
- Modify `server/pkg/agent/opencode.go` and `server/pkg/agent/opencode_mcp.go`: keep native inline injection and add manifest-safe diagnostics.
- Modify ACP/Pi/Codex adapters under `server/pkg/agent/`: verify the same effective config reaches every supported runtime.
- Modify `server/cmd/server/router.go`: add the task-mount relay route.
- Delete `server/cmd/multica/cmd_mcp.go` and `server/cmd/multica/cmd_mcp_test.go`.
- Modify `server/cmd/multica/main.go`: remove the `mcp` command registration.
- Modify `server/internal/daemon/execenv/runtime_config_sections.go`: remove CLI-based MCP discovery instructions and state that native runtime tools are authoritative.
- Modify `docs/multica-mcp-client-setup.md`: remove sandbox CLI instructions while retaining direct external MCP client setup.
- Create migrations `9129` through `9135` and update `server/pkg/db/queries/runner.sql` plus a new `server/pkg/db/queries/task_mcp_mount.sql`.

### Task 1: Pin the mount-manifest contract

**Files:**
- Create: `server/pkg/mcpprotocol/mount.go`
- Create: `server/internal/handler/task_mcp_mount_test.go`
- Create: `server/internal/handler/task_mcp_mount.go`

- [ ] **Step 1: Write failing tests for exact source preservation, route metadata and conflict rejection**

```go
func TestBuildTaskMCPMountsIncludesEverySource(t *testing.T) {
    sources := taskMCPSources{
        Agent: []mcpSource{{Name: "docs"}},
        TaskOverlay: []mcpSource{{Name: "composio"}},
        Runner: []mcpSource{{Name: "llm-wiki", SourceID: "27255ce7"}},
    }
    mounts, err := buildTaskMCPMounts(sources)
    require.NoError(t, err)
    require.Equal(t, []string{"multica", "docs", "composio", "llm-wiki"}, mountNames(mounts))
}

func TestBuildTaskMCPMountsRejectsNameCollisionWithoutRenaming(t *testing.T) {
    sources := taskMCPSources{
        Agent: []mcpSource{{Name: "wiki"}},
        Runner: []mcpSource{{Name: "wiki", SourceID: "27255ce7"}},
    }
    _, err := buildTaskMCPMounts(sources)
    require.ErrorContains(t, err, "mcp_server_name_conflict")
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/handler -run 'TestBuildTaskMCPMounts' -count=1`

Expected: FAIL because `buildTaskMCPMounts` does not exist.

- [ ] **Step 3: Implement the safe manifest types and deterministic builder**

The builder must always add every backend-hosted Multica MCP, merge the exact Agent/task entries, and add every server entry from the selected Runner's persisted raw document without checking online state. It returns the merged raw config plus a separate `mcp_relay_mounts` map containing only Multica and Runner names. Add byte/semantic preservation tests covering unknown extension fields, headers, environment, command arrays and URLs. Reject collisions instead of changing names.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/handler ./pkg/mcpprotocol -run 'TaskMCPMount|BuildTaskMCPMounts' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the contract**

```bash
git add server/pkg/mcpprotocol/mount.go server/internal/handler/task_mcp_mount.go server/internal/handler/task_mcp_mount_test.go
git commit -m "feat(mcp): 定义任务级挂载清单"
```

### Task 2: Persist the exact Runner config and task mounts

**Files:**
- Create: `server/migrations/9129_runner_mcp_config.up.sql`
- Create: `server/migrations/9129_runner_mcp_config.down.sql`
- Create: `server/migrations/9130_task_mcp_mount.up.sql`
- Create: `server/migrations/9130_task_mcp_mount.down.sql`
- Create: `server/migrations/9131_task_mcp_mount_id_idx.up.sql`
- Create: `server/migrations/9131_task_mcp_mount_id_idx.down.sql`
- Create: `server/migrations/9132_task_mcp_mount_primary_key.up.sql`
- Create: `server/migrations/9132_task_mcp_mount_primary_key.down.sql`
- Create: `server/migrations/9133_task_mcp_mount_task_idx.up.sql`
- Create: `server/migrations/9133_task_mcp_mount_task_idx.down.sql`
- Create: `server/migrations/9134_task_mcp_mount_name_idx.up.sql`
- Create: `server/migrations/9134_task_mcp_mount_name_idx.down.sql`
- Create: `server/migrations/9135_runner_call_payload_cleanup.up.sql`
- Create: `server/migrations/9135_runner_call_payload_cleanup.down.sql`
- Create: `server/pkg/db/queries/task_mcp_mount.sql`
- Modify: `server/pkg/db/queries/runner.sql`
- Modify: `server/pkg/runnerprotocol/messages.go`
- Modify: `server/cmd/multica/cmd_runner.go`
- Modify: `server/cmd/multica/cmd_runner_mcp.go`
- Test: matching Runner protocol and command tests

- [ ] **Step 1: Add migration contract tests**

Assert that no migration contains `REFERENCES`, every explicit index uses `CREATE INDEX CONCURRENTLY` or `CREATE UNIQUE INDEX CONCURRENTLY`, the primary-key constraint is attached with `USING INDEX` after its unique index is built concurrently, `source_kind` only permits `multica|runner`, and `source_ref` is constrained to a JSON object. Assert Runner config storage uses `BYTEA` plus a revision hash so JSON fields and bytes are not rewritten by PostgreSQL JSONB normalization.

- [ ] **Step 2: Run migration tests and verify RED**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/migrations -run 'TaskMCPMount|RunnerMCPConfig' -count=1`

Expected: FAIL because migrations `9129` through `9135` do not exist.

- [ ] **Step 3: Change Runner reporting from inventory to exact raw config**

Define a versioned Runner message containing `config_revision` plus a `json.RawMessage`/byte payload. The CLI reads the source file bytes, validates only that it is valid MCP JSON and within the size limit, computes SHA-256 over the unmodified bytes, and sends those same bytes. It must not canonicalize JSON, remove environment/header/command fields, classify secrets, or replace localhost URLs. An empty valid document replaces the previously stored config; disconnect and heartbeat expiry do not clear it.

- [ ] **Step 4: Add the schema and sqlc queries**

Add `mcp_config BYTEA`, `mcp_config_revision TEXT` and `mcp_config_updated_at` to `runner_machine`; remove the binding-level allowlist semantics from `enabled_mcp_servers`. Create `task_mcp_mount` without foreign keys or an inline primary key, build the ID unique index concurrently, and then attach the primary-key constraint with `USING INDEX`. Add concurrent task/name indexes and queries to atomically replace a machine's raw config, insert all Multica/Runner task mounts, list mounts by task, load one active mount by ID/task/workspace/agent, update call failure state, and delete expired mounts. Make terminal Runner call updates clear raw `arguments` and `result` after delivery while preserving metadata.

- [ ] **Step 5: Regenerate sqlc and run database tests**

Run: `make sqlc`

Run: `cd server && GOTOOLCHAIN=auto go test ./pkg/db/generated ./internal/migrations -run 'TaskMCPMount|RunnerMCPConfig|RunnerCall' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit persistence changes**

```bash
git add server/migrations server/pkg/db/queries server/pkg/db/generated server/internal/migrations server/pkg/runnerprotocol server/cmd/multica
git commit -m "feat(mcp): 持久化任务挂载元数据"
```

### Task 3: Build the complete mount set at task claim

**Files:**
- Modify: `server/internal/handler/task_mcp_mount.go`
- Modify: `server/internal/handler/daemon.go`
- Modify: `server/internal/handler/runner_mcp.go`
- Test: `server/internal/handler/task_mcp_mount_test.go`
- Test: `server/internal/handler/runner_mcp_unit_test.go`

- [ ] **Step 1: Add failing claim tests**

Cover these exact outcomes:

```text
no configured MCP + no Runner config -> every Multica-hosted MCP
Agent MCP + task overlay + selected Runner config -> all entries included
offline selected Runner with stored config -> every stored Runner entry included
selected Runner with no reported config -> no invented Runner entries
source-name collision -> claim fails; no source is renamed or dropped
manifest persistence failure -> task requeued, no partial claim returned
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/handler -run 'TaskClaim.*MCP|TaskMCPMount|RunnerMCP' -count=1`

Expected: FAIL because claim still performs Runner-only overlay injection.

- [ ] **Step 3: Replace source-specific overlay injection with one builder**

At claim, resolve the Agent config, task overlay, every backend-hosted Multica MCP, the selected Runner's stored raw config and runtime-native entries in one function. Do not read RunnerHub/Tair connection state. Persist Multica/Runner mount rows in one transaction before returning the claim. Return the exact merged source config plus the separate name-to-mount-ID route map. Preserve Agent, task-overlay and runtime-native entries unchanged so their URL or stdio command is consumed directly by the runtime.

- [ ] **Step 4: Remove online state from mount composition**

Do not call `runnerBindingOnline`, RunnerHub or the inventory cache while composing task MCP. A selected binding always contributes the complete raw config stored on `runner_machine`. Disconnect and heartbeat handlers must never clear that config. Runner connection state is checked only when a relayed request is executed, producing `runner_offline` at that point.

- [ ] **Step 5: Run focused handler tests and commit**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/handler -run 'TaskClaim.*MCP|TaskMCPMount|RunnerMCP' -count=1`

Expected: PASS.

```bash
git add server/internal/handler server/pkg/db/generated
git commit -m "feat(mcp): 在任务领取时统一挂载"
```

### Task 4: Implement the single task-MCP relay endpoint

**Files:**
- Create: `server/internal/handler/task_mcp_relay.go`
- Create: `server/internal/handler/task_mcp_relay_test.go`
- Modify: `server/internal/handler/multica_mcp.go`
- Modify: `server/internal/handler/runner_mcp.go`
- Modify: `server/cmd/server/router.go`

- [ ] **Step 1: Write failing transport and authorization tests**

Test POST/GET/DELETE forwarding, task-token ownership, workspace/agent scoping, expired mount rejection, body-size limits, protocol-version preservation, `Mcp-Session-Id` request/response preservation, notification 202 behavior and JSON-RPC error preservation.

- [ ] **Step 2: Run the tests and verify RED**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/handler -run 'TaskMCPRelay' -count=1`

Expected: FAIL because `/api/task-mcp/mounts/{mountId}` is not registered.

- [ ] **Step 3: Extract the Multica MCP dispatcher**

Keep `/api/mcp` working for external MCP clients, but move its authenticated request switch into a reusable internal function. The new task relay invokes that function for `source_kind=multica`; it must not make an HTTP loop back into the same server.

- [ ] **Step 4: Route Runner and configured MCP sources**

For Runner mounts, validate the task's stored config revision and call the existing `callRunnerMCP`. Do not test Runner online state until this request is executed. Reject `agent`, `task_overlay` and `runtime` mounts at the relay even if a caller guesses their mount ID; these sources never use the endpoint. Runtime-native HTTP/SSE and stdio handling continues through the existing runtime adapters.

- [ ] **Step 5: Add the route and verify GREEN**

Register authenticated methods at `/api/task-mcp/mounts/{mountId}`. Run:

`cd server && GOTOOLCHAIN=auto go test ./internal/handler ./cmd/server -run 'TaskMCPRelay|MulticaMCP|RunnerMountedMCP' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit the relay**

```bash
git add server/internal/handler server/cmd/server/router.go
git commit -m "feat(mcp): 统一任务级转发入口"
```

### Task 5: Inject the complete effective config through native runtime MCP support

**Files:**
- Rename: `server/internal/daemon/runner_mcp.go` to `server/internal/daemon/managed_mcp.go`
- Modify: `server/internal/daemon/daemon.go`
- Modify: `server/pkg/agent/opencode.go`
- Modify: `server/pkg/agent/opencode_mcp.go`
- Modify: `server/pkg/agent/hermes.go`
- Modify: `server/pkg/agent/pi_mcp.go`
- Modify: `server/pkg/agent/codex.go`
- Test: matching `*_mcp_test.go` and backend tests

- [ ] **Step 1: Write failing generic-rebase tests**

Given an unchanged source config plus a route map containing the Multica and Runner server names, assert only those named entries receive a runtime-facing loopback transport. Assert no private marker is added to any MCP entry and the Agent-configured entry is unchanged. Also assert every non-transport extension field on a relayed entry is preserved.

- [ ] **Step 2: Run daemon tests and verify RED**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/daemon -run 'ManagedMCP|RunnerMCP' -count=1`

Expected: FAIL because the current rebasing function is Runner-specific.

- [ ] **Step 3: Generalize rebasing and native injection**

Rename `rebaseManagedRunnerMCP` to `rebaseManagedMCPMounts` and make it consume only the separate name-to-mount-ID route map. It must not infer relay behavior from URL, command, header, environment or any field inside the source config. For names in the map, preserve the original entry in the claim and create the runtime-facing transport projection to the loopback relay; for all other names, pass the entry through unchanged. OpenCode receives the resulting set through `OPENCODE_CONFIG_CONTENT`; ACP runtimes receive it in `session/new`/`session/load`; Pi receives its 0600 temporary runtime-format projection; Codex receives its managed TOML block. Do not write OpenCode's dynamic entries into `~/.config/opencode/opencode.json`.

- [ ] **Step 4: Add runtime-specific catalog tests**

For every supported runtime, assert `multica`, `llm-wiki` and `runner-canary` appear as native MCP Servers and no URL/token/header is printed to logs. Add a regression assertion that reading the disk OpenCode config is not part of MCP availability detection.

- [ ] **Step 5: Run runtime tests and commit**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/daemon ./pkg/agent -run 'ManagedMCP|OpenCode.*MCP|Hermes.*MCP|Pi.*MCP|Codex.*MCP' -count=1`

Expected: PASS.

```bash
git add server/internal/daemon server/pkg/agent
git commit -m "feat(runtime): 注入统一原生MCP挂载"
```

### Task 6: Remove the MCP CLI and misleading Agent instructions

**Files:**
- Delete: `server/cmd/multica/cmd_mcp.go`
- Delete: `server/cmd/multica/cmd_mcp_test.go`
- Modify: `server/cmd/multica/main.go`
- Modify: `server/internal/daemon/execenv/runtime_config_sections.go`
- Modify: `server/internal/daemon/execenv/execenv_test.go`
- Modify: `server/internal/daemon/execenv/runtime_config_kind_test.go`
- Modify: `docs/multica-mcp-client-setup.md`

- [ ] **Step 1: Change tests to reject CLI-based discovery**

Assert `multica mcp` is not registered, generated task instructions contain neither `multica mcp tools` nor `multica mcp call`, and instructions say: "MCP Servers are mounted as native runtime tools for this task; do not inspect disk configuration or use a CLI to infer availability."

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `cd server && GOTOOLCHAIN=auto go test ./cmd/multica ./internal/daemon/execenv -run 'MCP|AvailableCommands' -count=1`

Expected: FAIL while the CLI and old instructions remain.

- [ ] **Step 3: Delete the command and update documentation**

Delete command registration and implementation rather than leaving a compatibility shim. Retain `/api/mcp` and its external-client documentation because it is now the native `multica` mount's backend. Remove only CLI usage and claims that the CLI enumerates the Agent's effective MCP catalog.

- [ ] **Step 4: Run tests and commit**

Run: `cd server && GOTOOLCHAIN=auto go test ./cmd/multica ./internal/daemon/execenv -run 'MCP|AvailableCommands' -count=1`

Expected: PASS.

```bash
git add server/cmd/multica server/internal/daemon/execenv docs/multica-mcp-client-setup.md
git commit -m "refactor(cli): 移除MCP发现调用命令"
```

### Task 7: Add mount and call observability

**Files:**
- Modify: `server/internal/handler/task_mcp_mount.go`
- Modify: `server/internal/handler/task_mcp_relay.go`
- Modify: `server/internal/handler/runner_mcp.go`
- Test: `server/internal/handler/task_mcp_relay_test.go`

- [ ] **Step 1: Add a raw-config log-exclusion test**

Feed exact configurations containing sentinel URL, bearer, environment and argument values. Assert persistence and task delivery retain every sentinel unchanged, while structured logs contain none of them. This is log exclusion, not mutation or redaction of the stored/delivered config.

- [ ] **Step 2: Run the test and verify RED**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/handler -run 'MCP.*Observability|MCP.*LogExclusion' -count=1`

Expected: FAIL because the new event contract is not emitted.

- [ ] **Step 3: Emit lifecycle events and update catalog metadata**

Emit the events listed in the observability section. Observe successful `initialize` and `tools/list` responses at the relay to update `tool_count` and `catalog_hash`; hash canonical tool names and input schemas without storing descriptions or schemas. Record `mcp_tool_called` only after completion so status and latency are authoritative.

- [ ] **Step 4: Run tests and commit**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/handler -run 'MCP.*Observability|MCP.*LogExclusion|TaskMCPRelay' -count=1`

Expected: PASS.

```bash
git add server/internal/handler server/pkg/db/generated
git commit -m "feat(mcp): 增加挂载调用观测"
```

### Task 8: End-to-end verification and rollout

**Files:**
- Modify: `docs/multica-mcp-client-setup.md`
- Modify: this plan's history section with implementation commit IDs after completion

- [ ] **Step 1: Run repository verification**

```bash
pnpm lint
pnpm typecheck
pnpm test
cd server && GOTOOLCHAIN=auto go test ./...
```

Expected: all checks PASS; report any unrelated pre-existing failure separately rather than calling the suite green.

- [ ] **Step 2: Build the server and CLI**

```bash
cd server
GOTOOLCHAIN=auto go build ./cmd/server
GOTOOLCHAIN=auto go build ./cmd/multica
```

Expected: both builds PASS, and `multica --help` contains no `mcp` command.

- [ ] **Step 3: Deploy the Multica branch to pre-release**

Use the repository's `aone-deploy` skill. Confirm the Aone revision equals the branch HEAD, all automated stages succeed, and distinguish the manual pre-release validation gate from deployment completion.

- [ ] **Step 4: Build a Runtime candidate from the repository's own branch**

Use a dedicated `multica-fc-hermes-runtime` branch based on that repository's internal `master`. Pin the exact Multica commit, include both Runtime and Multica commits in the immutable image tag, and create a private candidate Runtime. Do not switch stable templates.

- [ ] **Step 5: Run a real three-source canary**

Create a fresh task whose effective mounts are `multica`, `llm-wiki` and `runner-canary`. Verify from the native Agent tool surface—not disk config and not CLI—that:

```text
multica: tools/list contains the platform tools and one read-only call succeeds
llm-wiki: tools/list is non-empty and one read-only search succeeds
runner-canary: echo_canary succeeds through the Runner
```

Then disconnect the Runner and verify the same task retains the two Runner server names while calls return `runner_offline`. Reconnect and start a new task to verify calls recover.

- [ ] **Step 6: Verify observability without secrets**

Query SLS by each canary task ID. Confirm mount-created, initialized and called events have the expected source kinds, counts and statuses. Confirm no URL, authorization value, MCP arguments or result body appears.

- [ ] **Step 7: Promote only after acceptance**

After the private canary passes, update the selected pre-release Agent to the candidate Runtime. Stable Runtime/template promotion and production deployment remain separate explicit approvals.

## Acceptance criteria

- `multica mcp` does not exist and is absent from generated Agent instructions.
- A direct question such as “你有哪些 MCP” is answered from native runtime tools, without shell commands or disk-config inspection.
- Multica, Agent-configured, connected-app, runtime-native and Runner MCP Servers appear in one effective task configuration.
- Only Multica and Runner entries use the loopback relay origin in a cloud sandbox; direct Agent/task/runtime entries are byte-for-byte unchanged before runtime-specific format translation.
- Each logical MCP Server retains its own MCP session and tool namespace.
- Runner disconnect does not delete the saved selection or last reported exact configuration, and does not change the task's effective MCP names.
- New Runner MCP Servers become available to a new task without rebuilding an image.
- Mount/call logs are sufficient to diagnose source, task, server, latency and failure code while containing no secret or business payload.

## History

| Date | Change | Reason |
| --- | --- | --- |
| 2026-09-04 | Initial plan: remove Agent-facing MCP CLI, introduce an immutable task mount manifest and route every HTTP MCP through one sandbox loopback relay. | The CLI listed only Multica-managed tools while native dynamically mounted Runner tools were available, causing Agents to report false negatives. |
| 2026-09-04 | Restricted the relay to Multica's built-in MCP and Local Runner MCP; Agent-configured, task-overlay and runtime-native MCP remain direct. | The backend must not become an arbitrary MCP upstream proxy, and directly configured MCP already has a native runtime delivery path. |
| 2026-09-04 | Made mount composition configuration-driven: include all backend-hosted definitions and every Server in the selected Runner's last reported raw config without online probing; preserve raw fields and carry relay routing separately. | Availability is a call-time concern, and rewriting, filtering or sanitizing Runner configuration at report/claim time violates the dynamic binding contract. |
