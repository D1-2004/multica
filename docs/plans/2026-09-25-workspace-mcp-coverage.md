# Workspace MCP CLI coverage and rollout

`POST /api/mcp/workspaces/{workspaceId}` exposes fixed server tools. This inventory was checked against the installed `multica --help` tree on 2026-09-25. Some installed CLI commands target server features that are absent from this `origin/develop` checkout; those are recorded below instead of being advertised as implemented.

The registry currently declares 123 tools. A declaration is a fixed API method and path, never a client-supplied URL or shell command. Every call is reauthenticated as the bound member and passes the existing route, workspace gate, business handler, and activity audit. `tools/list` and `tools/call` both intersect token scopes (`read`, `write`, `manage`) with the current member role. The request and response envelope uses MCP Streamable HTTP. The local integration test has exercised token issuance, `tools/list`, issue creation/read/assignment, run history, runtime listing, cross-workspace rejection, and revocation. Other registry entries still need individual pre-release checks.

| Installed CLI command | MCP tools / current status | Constraint or next step |
| --- | --- | --- |
| `issue list/search/get/create/update/status/assign/children` | `issue_*` mapped | Create, status, and assign have field schemas and route through existing issue handlers. Full create/update flag parity needs schema and case verification. |
| `issue comment list/add/update/delete/resolve/unresolve` | `issue_comment_*` mapped | Native comment ownership and trigger rules apply. |
| `issue runs/run-messages/timeline/usage/pull-requests/rerun/cancel-task` | `issue_*` mapped | `issue_run_status` reads the run history, including terminal states. Run message visibility stays with the native task handler. |
| `issue label/metadata/property/subscriber/reorder` | Write and list routes mapped where present | Metadata get can be read from metadata list; property list from issue detail. Targeted subscriber add/remove requires route parity review. |
| `issue wakeup *` | Not exposed | Installed CLI has wakeup subcommands, but this checkout has no issue wakeup HTTP handlers. Add native API and authorization first. |
| `agent list/get/create/update/archive/restore/tasks/skills` | `agent_*` mapped | Agent owner/native member checks remain in the handlers. `agent copy` has no dedicated route in this checkout; create from a redacted export is a future typed workflow. |
| `agent env/avatar/mcp` | Not exposed | Env values and avatar uploads need secret-safe or binary MCP resources. Installed CLI's workspace MCP library commands have no corresponding HTTP API in this checkout. |
| `runtime list/activity/usage/rename/update/delete/profile` | `runtime_*` mapped except profile local path overrides | `runtime_update` maps to the CLI update request; `runtime_config_update` maps to PATCH. `profile set-path/unset-path` stay on the local machine. |
| `workspace get/member list/update` | `workspace_*` mapped | Admin and owner role gates run on the URL workspace. `workspace create/list/switch` are account or local-context operations and are excluded. |
| `workspace mcp add/list/remove/update` | Not exposed | Installed CLI has these commands, but the target source branch has no workspace MCP library handler. New workspace endpoint discovery is `workspace_mcp`, a separate operation. |
| `project list/get/create/update/status/delete/resource *` | Project and resource routes mapped | Status uses native project update. |
| `squad list/get/create/update/delete/member */activity` | Mapped | Member operations use native routes and role checks. |
| `autopilot list/get/create/update/delete/runs/trigger/trigger-*` | Mapped | `trigger-list` uses autopilot get. Webhook token rotation is a manage operation with native audit. |
| `skill list/get/create/update/delete/files/label`, `label *`, `property *` | Most mapped | Installed `skill search` searches installable remote skills; `skill_search_workspace` is explicitly local to the workspace. Import/refresh can download remote code and need a separate validated resource policy. |
| `repo list/add/remove/checkout` | Not exposed | Registry CRUD API is absent in this source branch; checkout is a local file operation. |
| `attachment upload/download` | Not exposed | Requires binary MCP resource streaming and explicit size/retention policy. Metadata read is available through issue/skill routes. |
| `chat history/thread` | Not exposed | These CLI commands use the current conversation context; a workspace token has no ambient conversation. Add explicit session ID and native visibility checks before exposing. |
| `agent delegate_task/get_issue/continue_issue/list_artifacts/describe_agent` | `agent_*` workspace aliases mapped | `agent_delegate_task` creates an issue assigned to an agent; native issue/comment permissions apply. |
| `agent get_task/read_artifact` | Partial | Run history/messages cover status and text, but old connection-scoped artifact semantics and binary read need explicit resource types. |
| `auth/login/config/setup/daemon/update/version/user profile` | Excluded | Account and local-machine operations do not belong to a fixed workspace business endpoint. |

## Credentials and switches

An owner or admin can `POST /api/workspaces/{id}/mcp-tokens/` with `name`, `scopes`, optional `expires_at` (up to 30 days), and either the issuer’s own `subject_user_id` or `service_name` for an independent identity. `service_name` creates an independent member identity; the default role is member. Only an owner may request `service_role=admin`. The response shows `wmcp_` secret once; list returns metadata only. `POST /api/workspaces/{id}/mcp-tokens/{tokenId}/revoke` invalidates it immediately. The database stores only SHA-256 hashes. Auth checks membership, role, expiry, and revocation on every request and has no DTA owner-elevation path. The token cannot call normal REST APIs directly or masquerade as a task token.

A tool takes `payload` only when its native route reads a request body: `issue_cancel_task` and `autopilot_trigger` take path arguments only, and `issue_rerun` accepts an optional `payload` (`task_id`). Routes that answer with an empty body (every DELETE tool, HTTP 204) return the text content `{"ok":true,"status":204}`, so MCP text content always carries a `text` string.

Diamond keys `workspace_mcp_endpoint_enabled` and `workspace_mcp_replace_agent_links` default on for pre-release environment labels and off for production. The first controls token authentication, issuance, discovery, and tool calls. The second changes new A2A config disclosure to `workspace_mcp_url` and clears `mcp_url`; old agent MCP URLs and credentials continue to work. `GET /api/workspaces/{id}/mcp` provides the current workspace endpoint. Both flags can be turned off to roll back new disclosure and calls. PostgreSQL token/audit state and the Diamond snapshot are shared across replicas.

## Remaining verification

- Verify each mapped command and native role denial with a client connected to pre-release. Registry entries with a generic `payload` object rely on the native handler's request validation; add exact field schemas for full tool-type parity.
- Confirm old `/api/mcp`, `/api/mcp/agents/{publicAgentId}`, and `/api/mcp/connect/{accessToken}` clients still work against the deployed revision.
- Use a real agent MCP client with a header token to create and assign an issue, read its run ID/status, and list runtimes; save redacted `tools/list` and `tools/call` returns with timestamps, subject ID, and issue/run IDs.
