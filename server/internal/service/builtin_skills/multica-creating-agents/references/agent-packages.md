# Agent packages and Git publication

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
A missing connection is configured by a human in Settings → GitHub. The browser
connection flow verifies GitHub user access and workspace management permission;
never send a bare installation ID to a setup callback or put OAuth credentials in a package.
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
An empty plugin declaration is ready when the Agent has no plugins. Reimporting
disabled A2A with no card fields or clients does not create a default endpoint.

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

`creating-agents-source-map.md` maps every contract above to its
`file:line` on the current tree, the runtime effect, and a safe read-only
verification command.

