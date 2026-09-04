# Runner Built-in Shell MCP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the legacy Local Runner filesystem and Shell tool bundle an always-available MCP server built into the Runner CLI.

**Architecture:** The Runner constructs one effective MCP document at startup from the untouched user entries in `~/.multica/runner/mcp.json` plus a reserved `multica_runner` entry. The existing Runner MCP manager handles that reserved entry in-process with standard MCP JSON-RPC and forwards every other entry to its configured stdio or HTTP server. The backend continues to persist and inject the Runner-reported effective document and routes every reported name through the existing task-scoped relay.

The reserved entry uses `{"type":"builtin","builtin":"shell"}` as an internal
descriptor. It never names or starts a second executable; only the installed
`multica` binary is required.

**Tech Stack:** Go, Cobra CLI, MCP JSON-RPC, Runner WebSocket protocol, PostgreSQL-backed Runner configuration snapshots.

---

### Task 1: Lock the built-in inventory contract

**Files:**
- Modify: `server/cmd/multica/cmd_runner_mcp_test.go`
- Modify: `server/cmd/multica/cmd_runner_mcp.go`

- [x] **Step 1: Write the failing inventory tests**

Add tests asserting that an empty user document still reports `multica_runner`, ordinary user entries and extension fields survive, and a user-defined `multica_runner` entry is rejected.

- [x] **Step 2: Run the inventory tests and verify RED**

Run: `cd server && GOTOOLCHAIN=auto go test ./cmd/multica -run '^TestRunnerMCP(Builtin|Report)' -count=1`

Expected: FAIL because no built-in server is added yet.

- [x] **Step 3: Build the effective Runner MCP document**

Add a reserved built-in server descriptor to the parsed document, include it in inventory summaries and the reported effective config, and calculate the inventory revision from that effective document.

- [x] **Step 4: Run the inventory tests and verify GREEN**

Run: `cd server && GOTOOLCHAIN=auto go test ./cmd/multica -run '^TestRunnerMCP(Builtin|Report)' -count=1`

Expected: PASS.

### Task 2: Implement the in-process MCP server

**Files:**
- Create: `server/cmd/multica/cmd_runner_shell_mcp.go`
- Modify: `server/cmd/multica/cmd_runner_mcp_exec.go`
- Modify: `server/cmd/multica/cmd_runner_mcp_exec_test.go`

- [x] **Step 1: Write failing JSON-RPC tests**

Add real manager tests for `initialize`, `tools/list`, and `tools/call`, including one Shell invocation under a test-created Runner root.

- [x] **Step 2: Run the manager tests and verify RED**

Run: `cd server && GOTOOLCHAIN=auto go test ./cmd/multica -run '^TestRunnerBuiltinShellMCP' -count=1`

Expected: FAIL because the manager does not recognize the built-in transport.

- [x] **Step 3: Implement the minimal MCP handler**

Implement MCP JSON-RPC dispatch in the CLI process. Reuse `runRunnerTool` for file and Shell execution, expose no `machine_id` argument inside the mounted server, and return MCP-compatible tool results and errors.

- [x] **Step 4: Start it with every Runner process**

Pass configured Runner roots into `newRunnerMCPManager` from `runAllRunnerLoops`; the built-in handler is then present before any server connection or inventory report begins.

- [x] **Step 5: Run the manager tests and verify GREEN**

Run: `cd server && GOTOOLCHAIN=auto go test ./cmd/multica -run '^TestRunnerBuiltinShellMCP' -count=1`

Expected: PASS.

### Task 3: Remove the server-owned implementation and document the protocol

**Files:**
- Modify: `server/internal/handler/runner_mcp.go`
- Modify: `server/internal/handler/runner_mcp_unit_test.go`
- Modify: `docs/local-runner-mcp-mounts.md`
- Modify: `docs/superpowers/plans/2026-09-04-unified-sandbox-mcp-relay.md`

- [x] **Step 1: Keep only the compatibility tombstone**

Retain `/api/runner-mcp` as an HTTP 410 compatibility response, but remove its unreachable fixed tool definitions and execution handler. Keep `/api/runner-mcp/mounts/{mountId}/servers/{serverName}` as the only live relay.

- [x] **Step 2: Update focused backend tests**

Delete assertions for the unreachable server-owned tool schema and retain relay timeout, authentication, and routing tests.

- [x] **Step 3: Update maintenance documentation and history**

Document `multica_runner` as an unconditional CLI-owned MCP, its root and OS-user permission semantics, reserved-name behavior, and the reason for moving ownership out of the backend. Append dated history entries to both maintenance documents.

- [ ] **Step 4: Run focused and package verification**

Run:

```bash
cd server
GOTOOLCHAIN=auto go test ./cmd/multica -count=1
GOTOOLCHAIN=auto go test ./internal/handler -run 'RunnerMCP|RunnerCall' -count=1
GOTOOLCHAIN=auto go test ./internal/runnerws ./pkg/runnerprotocol -count=1
```

Expected: PASS.

Status: CLI, daemon, runnerws, protocol, and handler compilation checks pass.
The handler test executable cannot run its target test because the shared test
database cleanup fails first on the existing `agent_owner_id_fkey` fixture.

- [x] **Step 5: Verify the diff without formatting**

Run: `git diff --check && git status --short`

Expected: no whitespace errors; only the planned source, test, and documentation files are modified.
