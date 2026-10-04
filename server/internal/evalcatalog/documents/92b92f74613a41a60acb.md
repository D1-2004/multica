# DSH Plugin as a Workspace asset

Status: implemented. This describes what the code does and why, not a proposal.

## What DeepSeek Harness actually provides

Read the harness before reading the rest of this. Verified against a DSH
0.1.2-rc.1 install:

- **There is no plugin registry.** `dsh plugin --profile <p> add <pkg>` is a
  `spawnSync("pnpm", args)` executed with the profile directory as its working
  directory, and it is the only package-manager subprocess in any of the 223
  shipped packages. No registry URL is hardcoded anywhere. Whatever registry
  pnpm is configured with *is* the plugin source.
- **There is no catalog, index, or search endpoint.** `dsh plugin search` also
  forwards to pnpm, which hits the npm registry's standard `/-/v1/search`.
- **The official Plugins settings surface is read-only.** Its host API exposes
  one method, `pluginInventory/list`, returning four fields per row — entry id,
  module specifier, enabled, fiber phase — with no version, description, or
  install source. Its own README calls enable/disable "deliberate follow-up
  work".
- **There is no compatibility declaration and nothing enforces one.** A plugin
  built against an older harness installs cleanly and fails at boot.

So "reuse the official mechanism" cannot mean calling an official market API.
It means adopting the harness's data model and install contract, and building
the browse/install layer on top — which is the extension the harness explicitly
anticipates, since its settings surface exposes an open slot for exactly that.

## The model, and why each part is shaped that way

**A plugin is a pinned package reference.** `dsh_plugin` stores the source spec
plus the resolved version and an integrity digest. Nothing Multica-specific is
invented, because the install contract has nothing else in it.

**Configuration is addressed by loader row, not by package.** A row id is chosen
by the plugin author and routinely differs from the package name —
`dsh-mcp-lens` declares row `mcp-lens`, and the harness's own plugin-inventory
package declares `plugin-inventory`. A patch also *replaces* a row's config
rather than merging into it, so an empty config object would erase the defaults
the plugin's own bundle layer supplies. The composed plugin set therefore omits
config entirely when there is none.

**Enabling a plugin for an agent means putting its package in the profile's
`bundles` array** — the same lever `reconcilePlugins` pulls after a pnpm
install. The user's own `cordis.patch.yml` is never written on install, matching
the harness.

## Discovery

Two sources, both read rather than built, neither presented as official:

| Source | What it is | Why |
| --- | --- | --- |
| `awesome-dsh-plugin` | The community index, 3158 entries, CC0-1.0 | The de-facto ecosystem index. `deepseek-ai/awesome-dsh-plugin` does not exist; this one has ~14.6k stars, and competing markets consume it rather than maintaining their own. |
| npm registry search | `/-/v1/search` on the configured registry | The same endpoint `dsh plugin search` reaches through pnpm. |

The index is fetched from its origin with a conditional GET, falling back to the
`dsh-plugin-catalog` npm package. Both paths exist for a reason: the origin is
freshest, but it is served from GitHub Pages, which is unreliable from inside
China — which is why the npm mirror exists at all.

About half the index is installable without a human resolving anything: 1542 of
3158 entries carry both an npm package and an exact version. The browse surface
hides the rest by default, because listing an entry that cannot be imported is a
dead end.

Rows are user-submitted, so a release tarball URL is only trusted when it
belongs to the repository the entry itself names. Without that bind, an entry
can name a well-known repo while pointing the download at someone else's.

## Import is a validation gate

The import path applies the same checks the sandbox applies at boot, so a
package that cannot load fails at import, with a reason, instead of failing
inside a task where nobody is watching:

- the archive is bounded on compressed size, expanded size, member size and
  member count, and refuses links, escaping paths and multiple roots;
- the bytes must match the digest the registry published for that version, not
  merely hash consistently with themselves;
- no install script is declared, because none is ever run;
- `dsh.bundle.patch` is declared and the file it points at exists;
- what `exports` (then `main`) resolves to is actually present in the package;
- the bundle patch inserts at least one loader row.

A web-only plugin imports with a warning rather than an error: Multica runs
headless, so its UI contributes nothing, but its host half may still be useful.

**Building an arbitrary source snapshot is deliberately not supported.** Most
published plugins ship prebuilt output, and a GitHub snapshot that needs its
`prepare` script is refused with that explanation. Running an untrusted build on
the server would buy the remaining minority at the cost of arbitrary code
execution during import.

## How it reaches a task

At task claim the agent's enabled plugins are composed into `DSH_PLUGIN_SET` —
the same variable an operator previously set by hand in `custom_env`. The
daemon, the sandbox and the image are unchanged, which is the point: the managed
path emits exactly what the manual one did.

Three rules the code encodes, each of which is silent when wrong:

- Composition is skipped for a non-DSH provider, so no query runs where the
  variable is ignored.
- Composition is skipped for an external A2A turn, which has already had
  agent-owned environment stripped so an owner's credentials cannot reach a
  caller from outside the workspace. Plugin configuration is agent-owned too.
- A failure to read the bindings fails the claim rather than running the task
  without its plugins, which would be a different task.

## Constraints worth stating

- **The sandbox needs public egress** to fetch a plugin at task start.
  Confirmed on the FC template. ASB is out of scope.
- **Dependencies must already exist in the image's DSH install.** The sandbox
  installs nothing at task time. Import surfaces a package's runtime
  dependencies as a warning so this is visible before a task fails.
- **`healProfilesModuleFallback` runs only at real boot**, inside
  `composeProfile` — not in `dsh plugin`, and not in `--dump-config`. A freshly
  provisioned `$DSH_HOME` therefore has no shared module fallback until DSH
  boots once, so validating dependency resolution before that reports every
  dependency missing, including ones the image plainly ships.
- **Version drift has no static check.** Nothing in the harness enforces
  compatibility, so `validated_dsh_version` records what a plugin was last seen
  working against; the only runtime signal is the inventory's failed fiber
  phase.

## Not covered here

Langfuse belongs to a separate branch. The image release to master is a separate
rollout decision.
