---
name: multica-creating-agents
description: "Use when creating, inspecting, or debugging a Multica agent definition via the `multica agent` CLI or POST /api/agents. Not for assigning issues to agents that already exist, and not for runtime task prompts."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Creating Multica agents

This is the contract for Multica's agent-creation path: what the create entry
points accept, what the server validates and rejects, how each field is
persisted, and which fields the daemon actually reads at claim time. It is
not a parameter manual — it states source-traced facts, and every claim is
backed by `file:line` in `references/creating-agents-source-map.md`.

## Quick start (read-only inspection)

These commands read state and have no side effects:

```bash
multica agent get <agent-id> --output json      # full persisted agent record
multica agent okr list <agent-ref> --output json # objectives, label ids, and measured spend
multica agent skills list <agent-id> --output json   # current skill bindings
multica agent env get <agent-id> --output json  # plaintext env (agent owner or ws owner/admin; agents denied)
```

An agent can also be **unbound**: `runtime_id` is `NULL` (served as `""` with
`runtime_bound: false`) after its runtime was deleted, which unbinds instead of
deleting its agents (MUL-5559). An unbound agent keeps everything it owns and
stays editable, but no trigger path will run it — they all refuse with
`agent_runtime_required` — until `agent update <id> --runtime-id <runtime-id>` binds
it again. Unbound is orthogonal to archived.

`agent get` returns the persisted agent including `runtime_id`, `model`,
`thinking_level`, `service_tier`, `custom_args`, `has_custom_env`,
`custom_env_key_count`, and `skills`. It never returns plaintext `custom_env`.

`agent okr list` returns the whole objective tree as
`{"usage_available": ..., "okrs": [...]}`. Every objective and key result
includes its stable materialized `label_id`, its `agent_okr` row `id`, and its
authored `position`. `spend` is present only when `usage_available=true`; a
false availability bit is a read failure, not measured zero. Spend is the
current Issue-label combination cost and may include collaborating agents on
the same labeled Issue.

## Core model

An agent is a workspace-scoped row (table `agent`). Creation is a single
`POST /api/agents` (`multica agent create`). At task claim time the daemon
re-reads the agent row and assembles the runtime payload — so the persisted
fields, not the create-time output, are what the agent runs on.

Definition fields serve different consumers:

- `description` is a catalog summary. It is stored and shown in listings; the
  daemon does NOT inject it into the agent's runtime prompt. Treat it as
  human-facing metadata only. Capped at 255 Unicode code points.
- `instructions` is the runtime behavior contract. The daemon reads it at
  claim time and ships it to the provider as the agent's durable instructions.
  Persona, responsibilities, boundaries, output and escalation rules go here,
  not in `description`.
- `coordinator_contract` is a short, explicit routing contract for the server
  Coordinator. It may narrow the platform action set; it cannot add tools or
  permit direct business answers. Full workflow instructions stay on the executor.

## CLI / API entry points

Minimum create call (`--name` and `--runtime-id` are both required):

```bash
multica agent create --name <name> --runtime-id <runtime-id> \
  --description "<short catalog summary>" \
  --instructions "<runtime behavior contract>" \
  --output json
```

`runAgentCreate` builds a JSON body and posts it to `/api/agents`. It only
adds a key when its flag was provided — `description`/`instructions` on a
non-empty value, the rest (`runtime-config`, `custom-args`, `model`,
`thinking-level`, `service-tier`, `visibility`, …) on the flag being `Changed`
— so omitted flags fall through to server defaults rather than sending empty
strings. `--max-concurrent-tasks` is validated as 1–50 before the request is
sent.

The HTTP body (`CreateAgentRequest`) accepts: `name`, `description`,
`instructions`, `coordinator_contract`, `avatar_url`, `runtime_id`, `runtime_config`, `custom_env`,
`custom_args`, `model`, `thinking_level`, `service_tier`, `visibility`,
`max_concurrent_tasks`, `mcp_config`, `skill_ids`.

## Bounded Coordinator contract

Use `--coordinator-contract-file <path>` (or `--coordinator-contract '<json>'`)
on `agent create`, `agent update`, or `agent copy`. The file contains one object:

```json
{"version":1,"scope":"Product support","must_delegate":["Product evidence checks"],"constraints":["Draft only until approved"],"clarify_when":["Missing recipient or message body"]}
```

The canonical JSON object, including keys and the Host's source hash, must fit
1600 Unicode code points. Unsupported versions, unknown fields, blank scope,
empty list entries and oversized objects are rejected, never truncated.
`instructions` remains the executor's complete job contract.

The Host adds `source_instructions_sha256` when it is omitted. A supplied hash
is preserved as a version reference; it is not an authorization credential.
Copies therefore retain stale contracts without silently recertifying them.
After reviewing a contract against changed instructions, explicitly republish
its authored object with that hash omitted to bind the new instruction version.
`agent get` exposes `coordinator_contract_state` as `loaded`, `not_configured`,
`stale`, or `unavailable`; only `loaded` can supply the current short contract.

On update, omission preserves the contract; `--coordinator-contract null`
clears it atomically. Updating only `instructions` keeps the previous contract
and source hash, so the state becomes `stale`. Missing, stale or invalid
contracts never mean "no restrictions". Git-backed and local-package Agent contracts are managed
with their source definitions and reject direct API edits, just like instructions.
Portable `agent.json` stores `coordinator_contract` at the root beside the
instructions reference. Preview and export retain it, including its source hash;
a malformed contract is rejected at the schema/preview boundary.

## Copying an agent

`multica agent copy <source-agent-id>` forks an existing agent's portable
configuration into a brand-new agent, leaving the source untouched. It is the
CLI/headless equivalent of the web "Duplicate" action. No dedicated server API
is involved: `runAgentCopy` reads the source with `GET /api/agents/<id>`, then
POSTs a `CreateAgentRequest` — passing the source's skill ids in `skill_ids` so
the bindings attach in the SAME create transaction (unlike `agent create`, which
binds nothing). The mutation is therefore a single atomic create.

```bash
multica agent copy <source-agent-id> --name "My Agent (copy)"   # same runtime
multica agent copy <source-agent-id> --runtime-id <target> --model <model>  # cross-runtime fork
```

- Copied by default, each overridable with the matching flag: `name` (suffixed
  `" (copy)"`), `description`, `instructions`, `coordinator_contract`, avatar, `custom_args`,
  `max_concurrent_tasks`, invocation permission (`permission_mode` +
  allow-list), and assigned workspace skills.
- A copied `max_concurrent_tasks` is included only when the source value is
  within 1–50. Historical out-of-range values are omitted so the new agent
  receives the server default (`6`); an explicit out-of-range
  `--max-concurrent-tasks` override is rejected before any API request.
- Runtime-specific fields (`model`, `thinking_level`, `service_tier`) are copied
  ONLY when the target runtime is unchanged. `--runtime-id` selecting a
  different runtime drops them and REQUIRES `--model` (pass `--model ""` to
  accept the target runtime default), mirroring the web Duplicate clearing model
  on a runtime switch.
- Never copied: `custom_env`, `mcp_config`, `runtime_config` (secret /
  machine-local; redacted or masked on read anyway). Supply fresh values with
  the same secret-safe flags as `agent create` (`--custom-env*`, `--mcp-config*`,
  `--runtime-config`), or with `agent env set` after the copy exists.
- `--no-skills` skips copying the source's skill bindings.

After creation, an administrator can start and inspect DingTalk bot setup with:

```bash
multica dingtalk install begin --agent-id <agent-id> --output json
multica dingtalk install status <session-id> --output json
```

`begin` creates a QR-code installation session. `status` is a single read of
that session (`pending`, `success`, or `error`); the CLI does not poll forever.
Add `--allow-unbound` to `begin` when external users or customers should use
the Agent without binding a Multica account.

## Field contracts

| Field | Persisted as | Validated? | Consumed by |
|---|---|---|---|
| `name` | `agent.name` | required, 400 if empty | listings, runtime payload |
| `description` | `agent.description` | 400 if > 255 code points | catalog/listing only — NOT the runtime prompt |
| `instructions` | `agent.instructions` | none | daemon → provider at claim time |
| `avatar_url` | `agent.avatar_url` | none; an explicit non-empty value is preserved, while omitted/empty creates a random `emoji:<glyph>` avatar | catalog/listing UI only — NOT the runtime prompt |
| `runtime_id` | `agent.runtime_id` (nullable) | required at create (400) + must resolve to a runtime in this workspace | selects runtime/provider; `NULL` means unbound — see below |
| `model` | `agent.model` (nullable) | none beyond runtime support | daemon reads; empty = runtime default |
| `thinking_level` | `agent.thinking_level` (nullable) | provider-level enum/safe-token gate; unknown literal → 400. Pi accepts only `off|minimal|low|medium|high|xhigh|max`, then the daemon checks the selected model's RPC-discovered subset. ACP runtimes that advertise an effort selector in `session/new` (currently `reasonix`) take the safe-token path and are checked against the discovered catalog by the daemon; that catalog covers only the model the discovery session was on, so other models show no picker until per-model probing exists. A runtime with no reasoning control (e.g. `hermes`) rejects EVERY non-empty value and says so — that 400 is a capability answer, not a bad token | daemon; empty = runtime default |
| `service_tier` | `agent.service_tier` (nullable) | Codex-only safe token; other providers reject; exact model/tier pair checked by daemon | daemon → Codex app-server; empty = local Codex config |
| `custom_args` | `agent.custom_args` (JSON array) | JSON shape checked CLI-side; server stores as-is | daemon (extra CLI switches); defaults to `[]` |
| `runtime_config` | `agent.runtime_config` (JSON) | JSON shape checked CLI-side; server stores as-is | runtime-specific config; defaults to `{}` |
| `custom_env` | `agent.custom_env` (JSON object) | — | daemon (process env); see Env & secrets |
| `mcp_config` | `agent.mcp_config` (raw JSON) | CLI checks it is a JSON object or `null`; server stores as-is. At create, literal `null` is dropped (no-op); at update, `null` clears the column | daemon → provider (provider-specific MCP handling); redacted on read |
| `visibility` | `agent.visibility` | — | access control; defaults to `private`; gates who can read/route a private agent (e.g. a private squad leader) — NOT the runtime prompt |
| `max_concurrent_tasks` | `agent.max_concurrent_tasks` | integer from 1 through 50; out-of-range values return 400 | scheduler task cap; defaults to `6` |

Defaults when omitted or explicitly `null`: `max_concurrent_tasks` → `6`.
Other defaults when omitted: `runtime_config` → `{}`, `custom_env` → `{}`,
`custom_args` → `[]`, `avatar_url` → a random `emoji:<glyph>`, `visibility` →
`private`
(all materialized server-side before the insert). `custom_args`/`runtime_config`
are typed `[]string`/`any` and marshaled as-is — the JSON-shape rejection
happens in the CLI, not the create handler.

The 1–50 concurrency range applies consistently to manual create, update, and
the create-from-template HTTP path. On create paths, an omitted field defaults
to 6 while an explicitly supplied 0 is rejected; on update, omission preserves
the current value. The CLI performs the same range check before sending create
or update requests.

`thinking_level` is validated only at the provider level: fixed-vocabulary
providers reject an unrecognized literal, while dynamic-vocabulary providers
such as Codex/OpenCode accept a syntactically safe token. Pi's provider-level
vocabulary is fixed (`off|minimal|low|medium|high|xhigh|max`), but its exact
supported subset is model-specific and discovered from the local Pi RPC model
catalog. A value unsupported for the chosen model is NOT rejected here — the
daemon checks its local model catalog at execution time, logs a warning, and
omits the incompatible override.

Set it from the CLI with `--thinking-level` on `agent create` and `agent
update`, mirroring `--model`: the flag is a thin pass-through to the top-level
`thinking_level` field, and on update an empty string (`--thinking-level ""`)
clears it back to the runtime default. The CLI deliberately does not enumerate
the valid levels — they are runtime/model-specific (Claude currently uses
`low|medium|high|xhigh|max`; Pi uses
`off|minimal|low|medium|high|xhigh|max`; Codex values are discovered from the
runtime's model catalog). It forwards the token, the server applies the
provider's fixed-enum or safe-token gate, and the daemon performs the exact
model/level check. A runtime whose provider has no thinking concept rejects any
non-empty value with a 400.

`service_tier` is the matching first-class Codex speed control. Set it with
`--service-tier <catalog-id>` on create/update; use `--service-tier ""` on
update to clear it. The runtime model catalog owns both availability and
display copy (currently `priority`, shown as Fast). The server accepts safe
future Codex catalog IDs, while the daemon verifies the exact model/tier pair
before execution and omits a stale incompatible override. Agents without an
explicit model fail closed because the effective config.toml model is unknown.

### model vs custom_args

`model` is a first-class persisted column the daemon reads directly.
`custom_args` are raw provider CLI args. The CLI help notes that some providers
(codex app-server, openclaw) reject `--model` inside `custom_args` — but that is
documented CLI guidance, not a server-enforced invariant; nothing in the create
handler inspects `custom_args` for a model flag. Pi is stricter at invocation
time: `--thinking` in `custom_args` is filtered because the first-class
`thinking_level` field owns that flag and must be the only source of its value.

## Env & secrets

`custom_env` is secret material. The CLI offers three input channels; two keep
secrets out of shell history and the process list:

```bash
multica agent create --name <name> --runtime-id <runtime-id> --custom-env-stdin --output json
multica agent create --name <name> --runtime-id <runtime-id> --custom-env-file <0600-json> --output json
```

`--custom-env-stdin` reads the JSON object from stdin; `--custom-env-file`
reads it from a file (suggested mode 0600). The third channel,
`--custom-env <json>`, puts the value on the command line where shell history
and `ps` can see it — avoid it for real secrets.

Read-side facts (these are the wrong assumptions to avoid):

- At claim time, blocklist-checked names and values are injected into the
  provider process. Hermes additionally writes those exact names (never the
  values) into its task-local `tools.env_passthrough`, because Hermes otherwise
  removes credential-like variables from Python and terminal tool subprocesses.
- Agent resources never expose plaintext `custom_env`. `agent
  list/get/create/update` and WS events return only `has_custom_env` (bool) and
  `custom_env_key_count` (int).
- Reading plaintext values requires the dedicated `GET /api/agents/{id}/env`
  endpoint (`multica agent env get`). It is gated to the **agent's own human
  owner** or a workspace **owner/admin**, and **agent actors are denied**
  regardless of the backing member's role — a running agent cannot read another
  agent's secrets, not even one its own human owns.
- Writing values after creation does NOT go through `agent update`. The generic
  update handler rejects any `custom_env` field with a 400 ("use PUT
  /api/agents/{id}/env"). Plaintext env writes are handled by
  `PUT /api/agents/{id}/env` (`multica agent env set`), which carries the same
  gate and writes an audit row.

### mcp_config

`mcp_config` is the agent's MCP server configuration (a JSON object such as
`{"mcpServers": {…}}`). It is also secret material — MCP entries routinely embed
API tokens — and offers the same three input channels as `custom_env`, on BOTH
`agent create` and `agent update`:

```bash
multica agent create --name <name> --runtime-id <runtime-id> --mcp-config-file <0600-json> --output json
multica agent update <agent-id> --mcp-config-stdin --output json
multica agent update <agent-id> --mcp-config 'null'   # clears the config
```

`--mcp-config-stdin` / `--mcp-config-file` keep the value out of shell history
and `ps`; the inline `--mcp-config <json>` does not. The CLI requires a JSON
**object** or the literal `null`; a top-level array or primitive is rejected
client-side, and empty stdin/file input errors rather than silently clearing.

Two ways `mcp_config` differs from `custom_env`:

- **It IS settable through `agent update`.** Unlike `custom_env`, `mcp_config`
  has no dedicated audited endpoint — the generic `PUT /api/agents/{id}` accepts
  it. Tri-state per the raw request body: field omitted → no change; `null` →
  clear; object → replace.
- **It is serialized on read, but redacted.** `agent get`/`list` return
  `mcp_config` only to callers allowed to view agent secrets; otherwise the
  field is `null` and `mcp_config_redacted` is `true`. Agent actors never see
  it, and a workspace may force redaction for everyone.

Provider support is not uniform: Qwen Code accepts a managed `mcp_config` through a daemon-owned 0600 temporary JSON file passed with `--mcp-config`; it is removed when the run exits. Leave the field unset (`null`) to inherit Qwen Code native settings.

## Skill binding

Creating an agent does NOT bind any workspace skill — binding is a separate
call after the agent exists. Two distinct verbs:

- `add` is additive — it merges the given ids with existing bindings
  (`POST /api/agents/{id}/skills/add`).
- `set` is replace-all — it overwrites the entire binding list with exactly
  the given ids (`PUT /api/agents/{id}/skills`); `--skill-ids ''` clears all.

```bash
multica agent skills add <agent-id> --skill-ids <skill-id> --output json
multica agent skills list <agent-id> --output json
```

At claim time the daemon starts with workspace-bound skills compatible with the
task's exact Runtime, then appends platform built-ins. A cloud-only dependency
belongs in `skill.config.execution.required_runtime_capabilities`; a
backend-only workflow uses `required_sandbox_backends`. Every capability must be
advertised and the actual backend must be listed, or the skill is omitted from
full and ref-based claims. The backend comes from the task's persisted Runtime
start attempt and must still match Runtime metadata. Local runtimes keep their
machine-owned skill availability. `LoadAgentSkills` loads each bound skill's
content plus supporting files; built-ins are embedded at compile time and loaded
from `SKILL.md` plus sibling files. Capability belongs in a bound skill, not in
`instructions`.

## Side effects needing approval

Read-only (safe): `agent get`, `agent okr list`, `agent skills list`, `agent env get`.

State-changing (require an explicit instruction — do not run speculatively):

- `multica agent create` — inserts a new agent row.
- `multica agent copy` — inserts a new agent row (a fork of an existing agent);
  the source is left untouched.
- `multica agent skills add` / `set` — mutate bindings (`set` is destructive:
  it drops bindings not in the new list).
- `multica agent env set` — overwrites the full `custom_env` map and writes an
  audit row.
- `multica agent transfer-owner <id> --to-id <user-id>` — `PUT /api/agents/{id}/owner`.
  The current owner, or a workspace owner/admin, reassigns `agent.owner_id` to a
  current human workspace member. Not part of `agent update`. A2A/MCP export
  stops until the new owner re-opens it; DingTalk identity and `custom_env` stay
  on the agent.

## Common wrong assumptions

- "`description` is the prompt." It is not — only `instructions` reaches the
  runtime. A rich description with empty instructions yields a named shell with
  no operating contract.
- "Create binds the agent's skills." It does not; bind explicitly afterward.
- "`agent update` can rotate env." It cannot — it 400s on `custom_env`; use the
  env endpoint.
- "`agent update --owner-id` changes the owner." It does not; use
  `agent transfer-owner`.
- "`mcp_config` behaves like `custom_env` on update." It does not — `mcp_config`
  IS settable via `agent update` (`--mcp-config`), with `--mcp-config null` to
  clear; only `custom_env` is gated behind the dedicated env endpoint.
- "`agent get` shows env values." It shows only `has_custom_env` and
  `custom_env_key_count`.
- "An invalid `thinking_level`/`model` combo is caught at create." Only an
  unknown provider-level literal is — model-specific gaps fail at run time.
- "`set` and `add` are interchangeable for skills." `set` replaces all
  bindings; using it when you meant `add` silently removes capabilities.

## References

The canonical package is a directory containing `agent.json`, the downloaded
`agent.schema.json`, its instructions Markdown file, and declared skill directories
with `SKILL.md` and supporting files. Use `multica.agent/v2` for the complete
configuration contract. Download the authoritative Schema with `GET /api/agent-schema`;
the server always validates with its embedded copy before reading referenced files.
A bundle is the parsed manifest and files, with a canonical content hash. It is
not a DTA CLI build artifact; do not require `dta bundle` or `dingtalk-agent.json`.

ZIP files may place `agent.json` at the archive root or inside a common enclosing
directory added when compressing an Agent folder or downloading a repository.
All package files must stay together beneath that directory. The ZIP adapter
strips enclosing directories and ignores `__MACOSX`, `.DS_Store`, and `._*`
metadata after validating original paths, file types, and size limits. This does
not change logical skill paths, scope/ID, or the bundle hash. A root manifest takes
precedence over nested examples; ambiguous packages return candidate paths and
repacking guidance. Git imports still require `agent.json` at repository root.

Workspace owners/admins can upload a ZIP through
`POST /api/workspaces/{id}/agent-packages/preview` (`application/zip`, or one
multipart `file`, maximum 40 MiB), or acquire the same directory from GitHub via
`POST /api/workspaces/{id}/github/agent-preview`. Git reads a pinned commit using
the selected workspace GitHub App installation, not the Agent's execution identity.
Both produce an immutable, actor-bound `preview_id` valid for 30 minutes.

`requirements.binding_declarations` lists the resource paths and JSON declarations
present in this parsed package, including explicit null or empty-list requests.
ZIP, Git and Builder previews use the same parser result. This metadata contains
portable references, not destination credentials. The form shows these declarations
only after preview succeeds and clears them when the selected file or Git ref changes.
Previously imported resource bindings remain in a separate, initially collapsed
section below the publication form; they do not describe the pending package.

After the user reviews the instructions, configuration and skill files, both
creation methods confirm through `POST /api/workspaces/{id}/agent-packages` with
`preview_id`, the destination `runtime_id`, and optional `name` / `description`.
Supply `secrets` by alias through the import form; never put real values in a chat.
`requirements.deferred_bindings` lists external identity, bot, runner, plugin or
non-portable access choices that need separate destination setup. The user must
explicitly acknowledge these in `deferred_bindings`; they are never silently
copied by UUID. Deferred member-based access creates a private Agent. Runtime
requirements and disabled runtime skills are checked against the chosen runtime.
The Agent, configuration, OKRs, A2A policies, exclusive workspace skills and files
are written in one transaction. Duplicate confirmation returns the same Agent.
Package OKRs retain authored text separately and allocate independent labels per Agent.
Repeat imports are allowed; updates reuse only the current Agent's exclusive labels. A2A policies do not include credentials or mint new tokens.

Git creation records the repository URL, workspace GitHub installation, selected
ref and resolved commit. Creation and publication accept branches, tags and
commit SHAs. Use `refs/heads/<branch>` or `refs/tags/<tag>` to distinguish
same-named branches and tags. The repository/source branches endpoints also
return `tags`; an omitted creation ref uses the repository default branch.

For an existing Git Agent, preview `POST /api/agents/{id}/source/preview` with
`{"ref":"refs/heads/<branch>"}`, `{"ref":"refs/tags/<tag>"}` or a commit SHA,
and review `git_changes` and `configuration_changes`.
Clicking Preview changes opens the successful result directly in a global dialog
with file navigation and a read-only side-by-side diff. Git and configuration
changes are separate tabs; close the dialog to confirm publication on the page.
Unchanged lines can be expanded. A Git
file without preview text shows metadata only; missing text is not an empty file.
Confirm with `POST /api/agents/{id}/source/sync`, passing the `preview_id` and any
explicit secret/binding choices. Omitted configuration stays unmanaged, while
explicit false/empty/null values are applied. Expired or stale previews require
previewing again. Skills stay exclusive, editable and deletable. Publication
preserves the selected runtime. v1 preserves the instance name and description;
v2 publishes the manifest name and its description when declared.

`GET /api/agents/{id}/source/publications` lists successful publications, newest
first, with the author, timestamp, ref, immutable commit SHA and rollback origin.
Follow `next_cursor` as `?before=<publication ID>` for older entries. Agent
managers can preview a rollback through the same source preview endpoint with
`{"publication_id":"<history ID>"}` instead of `ref`. Require the response's
`rollback_of` to match that ID, review the global diff, and confirm its
`preview_id` through `source/sync`. This creates another publication without
rewriting Git. Current GitHub repository access is still required, but a moved
or deleted ref does not affect the saved snapshot.

New publications save the complete portable configuration after import in the
same transaction. Rollback can clear later configuration and recreate removed
exclusive skills; shared skills still require their existing scope/ID and edit
permissions. Historical records without `has_configuration_snapshot` only
restore their original package declarations and warn that omitted fields retain
their current values. Runtime credentials and authenticated account bindings are
never restored from history; portable declarations use the normal destination
binding and secret checks.

`GET /api/agents/{id}/export` downloads a ZIP of the current platform definition,
including disabled skills and supporting files. Secrets become references;
runtime history, credentials and platform system instructions are excluded.

The AI Builder emits a complete `<agent_package>` object containing `manifest`
and `files`, and an `agent_draft` summary for older clients. The server-owned
prompt includes the current Schema. Preserve every current manifest field and
referenced file while revising a package. Do not put credentials or real account
bindings in the response. The UI calls `POST /api/workspaces/{id}/agent-packages/prepare`,
reviews the validated snapshot, and confirms through the same package create API.
`GET /api/workspaces/{id}/agent-packages/{previewId}/download` downloads that
caller's validated package, suitable for ZIP upload or a Git repository.

An existing local-package or manually created Agent can upload a ZIP to
`POST /api/agents/{id}/source/preview` using `application/zip` or multipart `file`.
Review the complete configuration diff, then confirm through `source/sync`.
This updates the existing Agent, preserves its environment bindings, and creates
local source provenance only when no source existed. Git Agents publish from
Git revisions: new ZIP preview/confirmation requests return 409. Replaying an
already applied ZIP confirmation remains idempotent. Source-managed skills are
replaced; other assigned skills remain.

When revising an exported v2 package for publication back to the same Agent,
preserve each skill's `scope` (`{"type":"workspace","id":"<workspace UUID>"}`)
and `skill_id`. They identify the existing assigned skill; `path` only locates
its files. An unchanged skill is reused without rewriting its content/files;
changes are reviewed as a diff and update the same skill ID. Shared skills retain
their original ownership and bindings, and edits require the skill creator or a
workspace owner/admin. Missing or detached local identities fail instead of
creating replacements. Older platform exports recover an already attached local
skill from `workspace-skills/<skill-id>`. Templates without identities and new
Agent creation retain their existing copy semantics. Do not invent identities.

Import errors retain full validator messages, all issues and the JSON Schema
DetailedOutput tree, with `schema_url: /api/agent-schema`. File and JSON errors
identify the offending path or byte location. The UI keeps details visible;
fix the package and preview it again before confirmation.

`references/creating-agents-source-map.md` maps every contract above to its
`file:line` on the current tree, the runtime effect, and a safe read-only
verification command.

## Protocol history

- 2026-09-08: Added repository URL creation and preview-ID source confirmation.
  Replaced unreviewed source sync with fixed-commit Git/configuration previews,
  and allowed ordinary source skill edits/deletion while retaining exclusivity.

- 2026-09-08: Unified local ZIP and Git package creation around agent.json, added
  v2 configuration materialization and explicit destination choices, and documented
  the Builder draft/package boundary. Reason: the package protocol is authoritative;
  importing must not depend on a separately built DTA artifact or drop configuration.

## Proactive conversations

`agent update <id> --event-trigger-enabled[=false]` controls the default-off
“Proactively process all new conversation messages” setting under Digital Employee,
immediately below inbound judging. Enabling it enables inbound judging atomically;
disabling inbound judging disables proactive processing. Existing bindings and
subscription scopes are unchanged. Router synchronization normally takes up to five
seconds plus request latency.

Observed group messages use the normal durable Coordinator window (4 seconds quiet,
12 seconds maximum collection, at most 100 messages). No Autopilot is created, and
there is no extra 30-second task interval or wait for the sandbox to finish before
judging new messages. Configure the employee's behavior through Agent instructions.
Unmentioned messages reach the same Coordinator to decide reply, silence or Issue work.
Authorized additions to a busy Issue are durably queued and combined for its next run.
Read decisions in Coordinator conversations and execution in the associated Issues.
Legacy event Autopilots are retained as history and only drain previously admitted work.

The task-finished follow-up setting still controls automatic completion reports.
Configuration and implementation map to `event_trigger.go`, `agent_event_trigger.go`,
`proactive_conversation.go`, `inbound_coordinator_job.go`, and `coordinator_follow_up.go`.

- 2026-09-10: Added ZIP publication, complete Builder packages and downloads,
  independently owned OKR labels, and complete schema diagnostics. Reason: allow
  package-driven updates and repeat imports without losing configuration or error context.

- 2026-09-11: Preserve skill scope/ID during exported-package updates and recover
  attached skill IDs from older platform export paths. Reason: prevent duplicate
  skill creation while retaining original ownership and publication checks.

- 2026-09-11: Accept ZIP enclosing directories and ignore desktop archive metadata.
  Reason: recompressing an Agent folder must not turn a valid manifest into a
  missing-root-file error or introduce metadata files into a skill.

- 2026-09-14: Added file navigation and side-by-side publication diffs with an
  enlarged view. Reason: make ZIP and Git changes reviewable without treating
  withheld text as an empty file.

- 2026-09-14: Open successful publication previews directly in a global dialog.
  Reason: show the full comparison after one click without an embedded viewer
  or a second expand action.

- 2026-09-14: Expose the current preview's resource declarations and separate
  previously imported bindings into a collapsed section. Reason: display the
  selected package's requirements without carrying over an older package's list.

- 2026-09-14: Added branch/tag/commit selection, durable publication history and
  reviewed snapshot rollback; Git Agents no longer accept new ZIP publications.
  Reason: preserve repository provenance and make previous configurations
  recoverable without depending on moving Git refs or copying credentials.
