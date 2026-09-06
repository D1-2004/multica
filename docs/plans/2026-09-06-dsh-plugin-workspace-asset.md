# DSH Plugin as a Workspace asset

Status: design, not implemented. Phase B (per-task import in the runtime image)
is landed and verified; this document covers Phase A, the Workspace-level asset
that feeds it.

## What is already true

The runtime image can import a DSH plugin for one task and boot a profile that
carries it. `scripts/multica-dsh` in `dingtalk-ai-lab/multica-fc-hermes-runtime`
reads a JSON plugin set from `DSH_PLUGIN_SET`, fetches each package, unpacks it
into a per-task profile, writes a profile manifest whose `dsh.profile.bundles`
ends with the plugin, and launches that profile instead of stock `headless`.

Verified on pre-release 2026-09-06 with `dsh-mcp-lens@0.1.0-rc.9`
(task `40e7bdcd`, issue WS-212) and a negative control with the plugin set
cleared (task `acb1629a`, issue WS-213). Nothing about the plugin is baked into
the image; a different plugin needs no rebuild.

Accepted sources today: `npm:<name>@<version>`, `github:<owner>/<repo>#<ref>`,
`https://…tgz`, and `file:<abs path>`. An optional `integrity` field pins the
tarball to `sha256-<64 hex>`.

## The gap

`DSH_PLUGIN_SET` is agent `custom_env`. That means a plugin is configured by
hand-writing JSON into an environment variable, per agent. There is no catalog,
no import UI, no provenance record, and no way for a second agent to reuse what
the first one installed. Skills already solve exactly this shape of problem, and
the plugin asset should look like a Skill, not like a new subsystem.

## Reuse, not reinvention

Two prior questions were researched before writing any code.

**Package resolution and fetching.** Do not hand-roll it. The npm CLI's own
building blocks are published as libraries and are the de-facto standard:

| Library | Role |
| --- | --- |
| `npm-package-arg` | Parse a user-typed spec into a typed descriptor. Already understands `npm:`, `github:`, `file:`, and tarball URLs, which is the exact set we accept. |
| `pacote` | Resolve that descriptor to a concrete tarball and extract it, with registry auth, redirects, and caching handled. |
| `ssri` | Compute and verify Subresource Integrity digests, the same `sha512-…`/`sha256-…` strings npm itself records. |

They are Node libraries and the server is Go, so the fetch runs as a short-lived
Node worker the Go service invokes, not as an in-process dependency. Estimated
custom code with this split is 1,600–2,700 lines; a from-scratch resolver is
several times that and would have to re-derive npm's spec grammar.

**The asset shell.** Skills already have the import surface this needs:
`POST /api/skills/import` accepts either a JSON body naming a URL or a
multipart archive upload, and takes an `on_conflict` strategy of
`fail | overwrite | rename | skip`. Mirror that route shape, its permission
checks, and its conflict semantics rather than designing new ones. The user's
requirement that import support GitHub and file is satisfied by the same two
request shapes.

## Shape of the work

**Storage.** A workspace-scoped `dsh_plugin` table holding identity, the source
spec as typed by the importer, the resolved concrete version, the integrity
digest, and the declared bundle rows read out of the package's
`cordis.patch.yml`. Per the fork's deployment rules the tarball itself is an
immutable object and belongs in OSS, with identity and lifecycle in PostgreSQL.
No foreign keys, per repository rule.

**Assembly.** An agent references plugins by id. At dispatch the daemon
composes the same JSON the adapter already consumes and injects it, so the
runtime contract does not change and Phase B needs no second image.

**Config overrides.** A patch replaces a loader row's config wholesale, so a
plugin with required fields fails loudly at compose time. The adapter already
resolves which row a config belongs to, including the case where the row id
differs from the package name (`dsh-mcp-lens` declares row `mcp-lens`). Surface
that row id in the catalog so the UI can label the config form correctly.

## Constraints worth stating up front

- **GitHub import only accepts a prebuilt package.** Install scripts are never
  run, so a repository that ships TypeScript and builds in `prepare` is
  rejected with that reason. Most published DSH plugins ship prebuilt `lib/`,
  so the npm path is the common one and GitHub is for pinned forks.
- **The sandbox needs public egress** to fetch from the registry. Confirmed
  working on the FC template (issue WS-211). ASB is out of scope.
- **Dependencies must already exist in the image's DSH install.** The adapter
  checks this before boot, anchored at the DSH installation rather than at the
  plugin directory, because a fresh `$DSH_HOME` has no
  `profiles/node_modules` fallback until DSH's own heal step runs at boot.

## Not covered here

Langfuse verification belongs to a separate branch and is deliberately not
addressed. The image release to master is a separate rollout decision.
