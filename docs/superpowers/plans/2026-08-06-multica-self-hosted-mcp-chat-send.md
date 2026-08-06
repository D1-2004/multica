# Multica Self-hosted MCP Chat Send Implementation Plan

> **For Codex:** Execute this plan task-by-task with tests written before production changes.

**Goal:** Let a running Multica Chat task continue another existing Chat by `session_id` through a Multica-hosted MCP tool, without rebuilding the Agent image.

**Architecture:** The Multica API process exposes a stateless Streamable HTTP MCP endpoint. At task claim time, the server adds that endpoint to the existing canonical `mcpServers` config and authenticates it with the already-minted task-scoped `mat_` token. The MCP handler accepts only task-token actors, validates the authenticated source task and target Chat inside the same workspace, then reuses the existing atomic direct-chat send service. A server-side feature flag controls both discovery and execution during rolling release.

**Tech Stack:** Go, Chi, JSON-RPC 2.0, MCP Streamable HTTP protocol revision `2025-06-18`, pgx/sqlc-backed existing Chat services.

**Boundary:** V1 exposes only `chat_send_message(session_id, content)`. It does not create Chats or Issues, does not add third-party MCP hosting, does not put credentials in URLs, does not add a new database table, and does not require daemon/Agent image changes. No commit, push, or deployment is part of this task.

---

### Task 1: Freeze MCP protocol and config injection behavior

**Files:**
- Create: `server/internal/handler/multica_mcp_test.go`
- Modify: `server/internal/featureflags/keys.go`

- [x] Add failing tests for `initialize`, `tools/list`, unsupported methods, and task-token-only tool calls.
- [x] Add failing tests for canonical `mcpServers.multica` injection with an Authorization header.
- [x] Add failing tests proving the server-side release flag and missing public URL suppress injection.
- [x] Run the focused tests and confirm the expected RED failures.

### Task 2: Implement the stateless self-hosted MCP endpoint

**Files:**
- Create: `server/internal/handler/multica_mcp.go`
- Modify: `server/cmd/server/router.go`

- [x] Implement JSON-RPC request validation and MCP lifecycle methods for protocol revision `2025-06-18`.
- [x] Publish only the `chat_send_message` tool and return structured tool output.
- [x] Return HTTP 405 for unsupported Streamable HTTP methods and validate browser Origin headers.
- [x] Mount `/api/mcp` behind existing authentication and workspace membership middleware.
- [x] Run the protocol-focused tests to GREEN.

### Task 3: Implement task-scoped Chat continuation

**Files:**
- Modify: `server/internal/handler/multica_mcp.go`
- Modify: `server/internal/handler/multica_mcp_test.go`

- [x] Verify the caller is an authenticated `task_token` actor and that the stamped task/Agent/workspace identifiers agree with the database.
- [x] Require an active source Chat task and reject forwarding back into its own Chat session.
- [x] Require the target Chat to be active, owned by the authenticated task owner, invocable by the source human originator, and backed by a non-archived Agent runtime.
- [x] Reuse `TaskService.SendDirectChatMessage` so target task, user message, session touch, and runtime wake-up retain current atomic behavior.
- [x] Broadcast the forwarded message with Agent attribution and return target message/task/trace IDs.
- [x] Run the success and authorization tests to GREEN.

### Task 4: Inject the platform MCP config at claim time

**Files:**
- Modify: `server/internal/handler/daemon.go`
- Modify: `server/internal/handler/multica_mcp.go`
- Modify: `server/internal/handler/multica_mcp_test.go`

- [x] After the `mat_` token is finalized, merge the Multica MCP entry into the task's existing MCP config.
- [x] Apply the same logic to single and batch claim responses.
- [x] Preserve Agent/runtime MCP servers and make the reserved `multica` entry server-owned on collision.
- [x] Gate cloud sandboxes on their existing `mcp` manifest capability and gate all injection on the server release flag.
- [x] Run focused claim/config tests to GREEN.

### Task 5: Document the contract and verify

**Files:**
- Create: `docs/multica-mcp-chat-send.md`

- [x] Document endpoint, auth, tool input/output, authorization rules, release flag, runtime compatibility, and failure behavior.
- [x] Append an audit history entry with the change reason.
- [x] Run focused handler and feature-flag tests.
- [x] Run the relevant Go package tests/build as far as the current repository baseline permits.
- [x] Run `git diff --check` and inspect the final diff for unrelated changes.
