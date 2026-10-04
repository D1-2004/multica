# CLAUDE.md

Shared guidance for contributors and coding agents. Keep this file authoritative: rules here should be hard to infer from code or easy to get wrong.

## Instruction architecture and delivery

`AGENTS.md` is the common entrypoint; this file owns engineering invariants.
Before implementation, use `docs/development-delivery.md` to define acceptance
criteria, relevant E2E scenarios, test environment and delivery milestones.
Record them in the current task/Plan, with a stopping or handoff boundary.
Product behavior requires evidence through the relevant end-to-end path;
source review, mocks and build success do not certify runtime behavior.
At the boundary, report verified results and remaining blockers; do not silently
expand scope or turn an untested requirement into a pass.

Module contracts and operational skills are loaded only for affected paths.
Shared instructions must work without a contributor's private memory, branch,
checkout or CLI profile. Resolve environment and release targets for each task.

## Conventions

The source of truth for code naming, i18n glossary, and Chinese product voice is:

- `apps/docs/content/docs/developers/conventions.mdx`
- `apps/docs/content/docs/developers/conventions.zh.mdx`

Read it before editing translations in `packages/views/locales/`, naming routes/packages/files/DB columns/types, or writing Chinese UI/docs copy. Do not rely on `packages/views/locales/glossary.md`; it is only a redirect stub.

## Project Shape

Multica is an AI-native task management platform for small teams, with agents as first-class assignees that can own issues, comment, and change status.

- `server/`: Go backend, Chi router, sqlc, gorilla/websocket.
- `apps/web/`: Next.js App Router.
- `apps/desktop/`: Electron desktop app.
- `apps/mobile/`: Expo / React Native iOS app. Read `apps/mobile/CLAUDE.md` before touching it.
- `apps/docs/`: Fumadocs documentation site.
- `packages/core/`: headless business logic, API client, React Query hooks, Zustand stores.
- `packages/ui/`: atomic UI components only.
- `packages/views/`: shared business pages/components for web and desktop.
- `packages/tsconfig/`: shared TypeScript config.
- `packages/eslint-config/`: shared ESLint config.

Shared packages export raw `.ts` / `.tsx` and are compiled by consuming apps. Dependency direction is `views -> core + ui`; `core` and `ui` must stay independent.

## State Rules

Keep server state and client state separate.

- TanStack Query owns server state: issues, users, workspaces, inbox, agents, members, and anything fetched from the API.
- Zustand owns client/view state: filters, drafts, modals, tab layout, and navigation history. Current workspace identity is route-driven; platform stores/singletons may mirror slug/id only for headers, persistence namespaces, and reconnects.
- Shared Zustand stores live in `packages/core/`, never in `packages/views/` or app directories.
- React Context is for platform plumbing only, such as `WorkspaceIdProvider` and `NavigationProvider`.
- Only auth/workspace stores may call `api.*` directly. Other server interaction belongs in queries/mutations.
- Workspace-scoped query keys must include `wsId`.
- Optimistic updates only when ALL hold: outcome locally predictable, user stays on the same screen (no navigation), failure is rare, rollback is trivial. Canonical: status/assignee/toggle field patches — patch determinate caches, roll back on failure, invalidate uncertain projections on settle.
- Flows that navigate or confirm (create, delete, leave) must await the server before navigating or cleaning up; never optimistically remove an entity from cache.
- Chat/message send uses the pending-message pattern: render immediately with a visible pending state and retry on failure, not silent optimism.
- WebSocket events invalidate or patch Query cache for server data. They must never mirror server payload data into Zustand; clearing client-owned pointers (active session, selection, current workspace) is allowed only with a single responder and a self-initiated guard when this client can cause the event.
- Persist durable preferences/drafts/layout. Do not persist server data or ephemeral UI state.
- Zustand selectors must return stable references. Do not return freshly allocated objects/arrays from selectors without shallow comparison.
- Hooks that need workspace context should accept `wsId`; do not call `useWorkspaceId()` internally unless the hook is guaranteed to run under the provider.

## Package Boundaries

These are hard constraints:

- `packages/core/`: no `react-dom`, `localStorage` (use `StorageAdapter`), `process.env`, or UI libraries.
- `packages/ui/`: no `@multica/core` imports and no business logic.
- `packages/views/`: no `next/*`, no `react-router-dom`, no stores. Use `NavigationAdapter`, `useNavigation()`, and `<AppLink>`.
- `apps/web/platform/`: only place for Next.js navigation/platform APIs.
- `apps/desktop/src/renderer/src/platform/`: only place for `react-router-dom` navigation wiring.
- Every workspace under `apps/` and `packages/` must declare directly imported external packages in its own `package.json`.
- Shared dependencies use `catalog:` from `pnpm-workspace.yaml`; `apps/mobile/` pins Expo/React Native related versions directly.

## Sharing Rules

Web and desktop share business logic, hooks, stores, components, and views through `packages/core/`, `packages/ui/`, and `packages/views/`.

If the same logic exists in both web and desktop, extract it unless it depends on platform APIs:

1. Next.js, Electron, or router APIs stay in the app/platform layer.
2. Headless logic belongs in `packages/core/`.
3. Shared UI or business views belong in `packages/views/`.
4. Shared primitives belong in `packages/ui/`.

Mobile is independent. It may import types and pure functions from `@multica/core`, with `import type` for types, but owns its UI, state, hooks, providers, i18n, React version, build pipeline, and release cadence.

## Commands

Use the repo scripts as the source of truth. Common commands:

```bash
make dev              # auto-setup and start the app
make start            # start backend + frontend
make stop             # stop app processes for this checkout
make server           # run Go server only
make daemon           # run local daemon
make test             # Go tests
make sqlc             # regenerate sqlc code after SQL changes
pnpm install
pnpm dev:web
pnpm dev:desktop
pnpm build
pnpm typecheck
pnpm lint
pnpm test             # TS/Vitest tests through Turborepo
pnpm exec playwright test
pnpm ui:add badge     # shadcn/Base UI component into packages/ui
```

Worktrees share one PostgreSQL container and get isolated DB names/ports via `.env.worktree`. `make dev` auto-detects this. For manual setup use `make worktree-env`, `make setup-worktree`, and `make start-worktree`. `pnpm dev:desktop` additionally self-isolates per worktree (its own renderer port + app name) automatically, independent of `.env.worktree`.

CI runs Node 22, Go 1.26.1, and a `pgvector/pgvector:pg17` PostgreSQL service.

## Database and Migration Rules

These are hard requirements for every new or modified database design and production migration:

- Do not add database foreign keys (`FOREIGN KEY` / `REFERENCES`), cascading deletes, or cascading updates. Resolve relationships, validation, and dependent cleanup explicitly in application code. Use an application transaction when cleanup and the parent operation must commit or roll back atomically.
- Every index created by a migration must use `CREATE INDEX CONCURRENTLY` or `CREATE UNIQUE INDEX CONCURRENTLY`, including indexes on newly created tables. PostgreSQL rejects concurrent index creation inside a transaction or a multi-command string, so keep each concurrent index build in its own single-statement migration file. The repository migration runner executes migration files outside an explicit transaction to support this.

## Coding Rules

- TypeScript strict mode is enabled; keep types explicit.
- Go follows standard conventions: `gofmt`, `go vet`, checked errors.
- Code comments must be English.
- Prefer existing patterns/components over new parallel abstractions.
- Avoid broad refactors unless required by the task.
- For internal, non-boundary code, do not add compatibility layers, fallback paths, dual writes, legacy adapters, or temporary shims unless explicitly requested.
- API boundaries are different: installed desktop clients can talk to newer backends, so response parsing must follow the API compatibility rules below.
- If a flow or API is being replaced and the product is not live, prefer removing the old path instead of preserving both.
- New global pre-workspace routes must be a single word (`/login`, `/inbox`) or `/{noun}/{verb}` (`/workspaces/new`). Do not add hyphenated root routes like `/new-workspace`.
- Reserved slugs live in `server/internal/handler/reserved_slugs.json`. Edit it, run `pnpm generate:reserved-slugs`, and commit the generated `packages/core/paths/reserved-slugs.ts`.
- When changing CLI commands/flags, API fields, or product behavior documented by built-in skills under `server/internal/service/builtin_skills/*`, update the relevant `SKILL.md` and `references/*-source-map.md` in the same PR.

## API Compatibility

Frontend code must survive backend response drift, especially in installed desktop builds.

- Parse API JSON with `parseWithFallback` in `packages/core/api/schema.ts` and a zod schema. Do not cast network JSON to `T`.
- Endpoint responses consumed by UI logic must pass through a schema before returning.
- Downstream UI should optional-chain and default fields defensively.
- Prefer explicit boolean checks (`=== true`) over truthy/falsy checks on server fields.
- Do not pin critical affordances to one backend boolean; combine signals when possible.
- Server-driven enum switches need a `default` branch.
- When adding or changing an endpoint, add/update the schema and include a malformed-response test.

## Backend UUID Rules

In `server/internal/handler/`, always know where a UUID came from before using it in write queries.

- Resource path params that may be UUIDs or human-readable IDs must be resolved through loaders such as `loadIssueForUser`, `loadSkillForUser`, `loadAgentForUser`, or `requireDaemonRuntimeAccess`; subsequent writes use the resolved `entity.ID`.
- Pure UUID inputs from request boundaries use `parseUUIDOrBadRequest(w, s, fieldName)` and return immediately on `ok=false`.
- Trusted UUID round-trips from sqlc results or test fixtures use `parseUUID(s)`, which panics on invalid input.
- Outside handlers, `util.ParseUUID(s) (pgtype.UUID, error)` is the safe variant; always check the error.

## Web/Desktop Features

When adding a shared page or feature for web and desktop:

1. Put the page/component in `packages/views/<domain>/`.
2. Add platform wiring in both `apps/web/app/` and the desktop router, unless the desktop flow is a transition overlay.
3. Use `useNavigation().push()` or `<AppLink>` in shared code.
4. Use shared guards/providers such as `DashboardGuard` from `packages/views/layout/`.
5. Keep platform-only UI in the app or inject it through props/slots.
6. Hooks that need workspace context should accept `wsId`.

CSS for web/desktop is shared from `packages/ui/styles/`. Use semantic tokens such as `bg-background` and `text-muted-foreground`; avoid hardcoded Tailwind colors and duplicated base styles.

## Desktop Rules

Desktop routing has three categories:

- Session routes: workspace-scoped tab destinations such as `/:slug/issues`.
- Transition flows: pre-workspace one-shot actions such as create workspace or accept invite. These are `WindowOverlay` state, not routes.
- Error/stale states: stale workspace tabs should auto-heal by dropping stale tab groups, not render desktop error pages.

More desktop constraints:

- New pre-workspace desktop flows register a `WindowOverlay` type in `stores/window-overlay-store.ts`; do not add them to `routes.tsx`.
- `setCurrentWorkspace(slug, uuid)` from `@multica/core/platform` mirrors the active route for headers, storage namespaces, and reconnects; workspace route layouts own setting it.
- Code that leaves workspace context must call `setCurrentWorkspace(null, null)` explicitly.
- Workspace delete must await the server before navigation/cleanup. Workspace leave currently clears/navigates before mutation only to avoid the `member:removed` realtime race; treat that as known debt, not a reusable pattern.
- Cross-workspace navigation must go through the navigation adapter so it can call `switchWorkspace(slug, targetPath)`.
- Full-window desktop views outside the dashboard shell must mount `<DragStrip />` from `@multica/views/platform` as the first flex child. Interactive controls in the top 48px need `WebkitAppRegion: "no-drag"`.

## Mobile Rules

Read `apps/mobile/CLAUDE.md` before touching `apps/mobile/`. It contains the mandatory pre-flight process, import limits, parity rules, tech stack, UI rules, data helpers, realtime strategy, and mobile release flow.

Root-level reminders:

- Mobile shares only `@multica/core` types and pure functions.
- Mobile must match web/desktop product semantics: counts, permissions, enums/transitions, and data identity.
- Mobile may differ in UI/interaction when the phone context requires it.

## UI Rules

- Prefer shadcn/Base UI components over custom implementations. Add them with `pnpm ui:add <component>` from the repo root.
- The Pro `@reui` registry is configured in `packages/ui/components.json`; add items with `pnpm ui:add @reui/<name>` and answer `n` to every overwrite prompt so local component customizations survive. It reads `REUI_LICENSE_KEY` from the environment — agents get it from their Multica agent environment, humans export it in their own shell. Never write the key into a repo file.
- ReUI ships source, not a dependency: route the vendored output to our layout (new primitives to `packages/ui/components/ui/`, compositions to `packages/views/<domain>/`) and rewrite it to our conventions before committing.
- Use design tokens and semantic classes; avoid hardcoded colors. Font sizes come from the role-named `--text-*` scale in `packages/ui/styles/tokens.css` (`text-caption`, `text-body`, `text-title`, …), which is the authoritative list — not Tailwind's default `text-sm` / `text-base` ramp.
- An active/selected state must stay identifiable while hovered. Express it on a dimension hover does not touch (weight, text color), or define the `data-active:hover:` compound explicitly — otherwise hovering a selected row visually downgrades it to plain hover.
- Do not introduce extra local state unless the design requires it.
- Handle overflow, long text, scrolling, alignment, and spacing deliberately. Prefer more spacing over adding a divider.
- If a component is identical between web and desktop, it belongs in a shared package.

## Testing

Tests follow the code:

| What is tested | Location |
| --- | --- |
| Shared business logic, stores, queries, hooks | `packages/core/*.test.ts` |
| Shared UI components, pages, forms, modals | `packages/views/*.test.tsx` |
| Platform wiring such as cookies, redirects, search params | `apps/web/*.test.tsx` or `apps/desktop/` |
| End-to-end flows | `e2e/*.spec.ts` |
| Backend | `server/` Go tests |

Rules:

- Never test shared component behavior in an app test file.
- `packages/views/` tests must not mock `next/*` or `react-router-dom`.
- Mock `@multica/core` stores with the Zustand callable-store shape (`selectorFn` plus `getState`).
- Mock `@multica/core/api` for API calls.
- E2E tests should use `TestApiClient` for setup/teardown.
- Prefer writing the failing test in the correct package before implementation when the change is behavioral.
- Default tests must never resolve or execute user-installed agent CLIs. Pass a test-created fake executable path or a test-created missing path to agent subprocess code.
- Real-agent smoke tests belong behind the `agentintegration` build tag and must check `MULTICA_RUN_REAL_AGENT_SMOKE=1` before executable lookup or account access.
- Run an explicitly authorized real-agent smoke test with `(cd server && MULTICA_RUN_REAL_AGENT_SMOKE=1 go test -tags=agentintegration ./pkg/agent -run '<test-name>' -count=1 -v)`. This command may access an authenticated account and consume quota.
- When adding a default agent command, add it to `scripts/agent-cli-command-names.txt`; the normal Linux/macOS test entry points fail on ambient agent CLI execution.

## Verification

For code changes, run the narrowest useful checks while iterating, then run broader verification when risk justifies it or when asked.

For Employee / Tag behavior, add the domain checks in
`docs/employee-delivery-workflow.md`; other modules use their own contracts.

Useful checks:

```bash
pnpm typecheck
pnpm test
make test
pnpm exec playwright test
make check
```

Do not claim verification passed unless you ran it. If you skip checks because the change is docs-only or the user asked not to run them, say so.

## Commits and Releases

- Commits should be atomic and use conventional prefixes: `feat(scope)`, `fix(scope)`, `refactor(scope)`, `docs`, `test(scope)`, `chore(scope)`.
- A production deployment requires a CLI release tag on `main`: create `v0.x.x`, push it, and let `release.yml` publish binaries and the Homebrew tap.
- Bump patch by default unless the user specifies a version.

## Domain Reminders

- Provider event admission (`docs/event-scene-router.md`): `internal/eventrouter`
  persists one `scene_event_receipt` envelope/routing receipt per owner/source/id before
  scene business handling. Host supplies authenticated owner/principal/tenant;
  payload actors or SceneRefs grant no authority. Resolve once via `scene.Resolve`,
  retain route/ref on retries and fence the current tenant at entry. Unknown
  locators are unmapped, never guessed. `runtime.event_scene_router` selects exact
  workspace/agent/tenant triples and defaults off; no dual handling. EmployeeLoop,
  Task execution and connector binding models are outside this layer.

- Workspace shared disk and the employee private disk are separate stores. The shared-disk grant does not change `/mnt/multica` or the DSH profile. See `docs/workspace-storage-boundaries.md`.

- Before any Coordinator-related change in `inboundcoord`, handlers/dispatch/callbacks, assoc, scenememory, or trace, read `docs/inbound-coordinator-loop.md` (current behavior contract) and `server/internal/service/inboundcoord/policy/registry.json` (versioned obligations, modules, and superseded incident safeguards). Update source mapping, relevant tool/Host contracts, contrast cases, and evidence status together; run `python3 scripts/check-coordinator-policy.py`. Historical Plans are evidence, not a competing current contract.

- Agent work scene (`scene_id`, `docs/agent-scene.md`) is the ONE scene identity: one agent + tenant org + kind (`group` / `dm` / `enterprise`) + stable scene instance → one server-minted `scene_id` in `agent_scene`. These are hard rules:
  - Association graph scene nodes, `assoc_event.scene_id`, scene configuration (`scope_type='scene'` scope keys, `extra_scene_key`), Scene Memory (`agent_scene_memory`), Coordinator jobs and task context (`agent_scene` = `scene.Ref`), outbound reply targets and every new event carry the `scene_id`. Never key anything per scene by openConversationId, staffId, UID, chat title, message id or session id. A 1:1 chat is keyed by its conversation, never by its person; the person scope (staffId) is personal configuration, not a scene.
  - Register or find a scene only through `scene.Resolve` / `scene.Lookup` in `server/internal/scene`, with the tenant org from trusted data only: the org the dispatch recorded for the agent (else its identity org), and only while the agent serves that org (identity org or a created tenant). Unknown kinds are rejected; a kind is never guessed (no defaults, no "not a group, so dm"). Provider calls read the external id back from the directory; a `scene_id` is never sent as a conversation id.
  - Future event sources (calendar, approvals, documents, other providers) build a typed `scene.Locator` from trusted data, resolve once at admission, persist `scene.Ref`, and apply the use-time fence (`scene.CheckTenant` / directory lookup in the current org) before reading private state or sending. Resource events without a conversation use the enterprise scene. A new kind needs a constant, a widened `agent_scene_kind_check` migration and tests (`docs/agent-scene.md` §9).
  - `assoc_scene` and `scene_memory` are retired: no business reads, writes, dual writes or fallbacks (workspace deletion still cleans them up). API responses expose `scene_id`; `scene_key` and `memory_id` carry the same id, `conversation_id` is for display only.
- All queries filter by `workspace_id`; membership gates access; `X-Workspace-ID` selects the workspace.
- Issue assignees are polymorphic: `assignee_type` plus `assignee_id` can reference a member or an agent.
- Coordinator Scene Memory e2e plays and next-turn SLS checks: `docs/plans/2026-09-02-coordinator-scene-memory-e2e.md`. How to send/query: skill `scene-memory-e2e`.
- Langfuse traces for the Coordinator loop, the memory loop, and agent tasks are produced server-side by `server/internal/langfuse` and gated by `LANGFUSE_*` keys (whitelisted in `src/main.sh`); the daemon and the FC image need no rebuild for them. Keep the SLS lookup keys as trace metadata when adding spans: `docs/langfuse-observability.md`.
- Coordinator scene window (busy → next window, 2 @s, delegator per utterance): `docs/plans/2026-09-05-coordinator-scene-window.md`.

## Aone Fork

This repo is the Aone-deployed fork of `multica-ai/multica`: upstream code plus a
deployment layer (`APP-META/`, `src/main.sh`, `scripts/aone-deploy.sh`). Operating
the deployment — deploying, reading server logs, changing runtime config,
diagnosing a failed deploy — is covered by the `aone-deploy` skill in
`.agents/skills/`. The rules below are the ones that break production if missed.

Pre-release and production must be treated as multi-replica deployments. The
pre-release environment provides shared PostgreSQL, Tair, and OSS:

- Transactional domain state and idempotency belong in PostgreSQL. Do not use
  process memory or a node-local file as the source of truth for templates,
  bootstrap intents, provisioning state, leases, or revision coordination.
- Wire Tair through `REDIS_URL` for cross-node realtime fanout, wakeup hints,
  rate limits, and other short-lived coordination. Keep
  `REALTIME_RELAY_MODE=sharded` (the default) or `dual`; `legacy` does not fan
  daemon workspace/profile notifications across nodes. A local in-process hub
  is only a delivery optimization, never shared state.
- Use OSS for large immutable objects or artifacts when storing them directly
  in PostgreSQL is no longer appropriate. Store object identity and lifecycle
  metadata transactionally in PostgreSQL; do not use a node-local filesystem.
- Every replica must receive the same `JWT_SECRET`, integration encryption keys
  (including `MULTICA_DINGTALK_SECRET_KEY`), public app origin, and callback
  configuration. Tokens or encrypted installation credentials created on one
  replica must be readable on every other replica.
- Startup reconciliation must be idempotent and monotonic under rolling
  deployments. Old replicas must not downgrade shared state, and any new bundle
  schema must remain readable by the old and new binaries during the rollout.
  Roll back seeded content by publishing a higher release version containing
  the rollback content, never by decrementing the release version.
- Exercise concurrent state transitions with separate database transactions or
  connections, and review the old/new-binary rolling window before release.

The Aone all-in-one deployment runs the packaged `migrate up` binary from
`src/main.sh` after stopping old application processes and before starting the
new release. The migration runner holds a PostgreSQL advisory lock, so concurrent
pod startup is serialized safely. Generic container entrypoints still require an
explicit migration phase.

Upstream schema syncs use the database-authoritative deployment fence in
`server/internal/deploymentfence`:

- Operate it only through `GET/PUT /api/internal/deployment-fence`, authenticated
  with `MULTICA_LOG_TAIL_TOKEN`; never update `deployment_fence` directly.
- The required state flow is `normal -> draining -> frozen -> normal`.
  `draining` makes the user-facing API unavailable, rejects new task/channel/
  webhook admissions, and still lets already-dispatched daemon tasks finish.
  `frozen` rejects all business writes at PostgreSQL trigger level.
- Every live replica must acknowledge the draining revision and all active task,
  inbox, lease, and completion-outbox counts must reach zero before `frozen` is
  accepted. The transition also refuses to freeze if any public business table
  is missing its global fence trigger. Health and the fence/log operator
  endpoints remain available.
- The migration runner is the sole frozen-state writer. It sets a session-scoped
  bypass and reinstalls the fence triggers after `migrate up`, covering tables
  introduced later by an upstream migration below the fork namespace.

Migration rules:

- Pre-release is managed Postgres (PolarDB). It refuses `CREATE EXTENSION` to the
  app role with SQLSTATE 42501 — even when the role owns the database and the
  extension is one Postgres marks as trusted. Wrap every `CREATE EXTENSION` in
  `DO $$ ... EXCEPTION WHEN OTHERS THEN RAISE NOTICE ... END $$;`, and guard any
  index that depends on its opclass the same way (which costs `CONCURRENTLY` —
  it cannot run inside a `DO` block). Precedent: `032_issue_search_index`
  (pg_bigm), `076_task_usage_pgcron_extension` (pg_cron), `137`-`142` (pg_trgm).
- The runner tracks applied migrations by **full filename stem**, not by number.
  A renamed file re-runs, so any migration that may be replayed on a live
  database must be idempotent (`IF NOT EXISTS`).
- Verify risky migrations against the real pre-release database inside a
  rolled-back transaction. A local Postgres runs as superuser and cannot
  reproduce PolarDB's permission model.
- Fork-owned migrations use the reserved `9000+` range. When moving a migration
  into that range, add a full-stem alias in `cmd/migrate`, preserve both old and
  new bookkeeping rows for binary rollback, and keep replayed SQL idempotent.

When syncing upstream (`git merge upstream/main`):

- Never `git stash` mid-merge. It drops `MERGE_HEAD` and silently reverts staged
  conflict resolutions to their pre-merge content — which still compiles and
  still passes tests. Use a separate `git worktree` to compare against the
  pre-merge tree.
- Upstream reuses migration numbers the fork may already have taken. Renumber the
  fork's own migrations above upstream's range (the lint test in
  `internal/migrations` enforces this) and make them idempotent, since the live
  database already applied them under the old stems.
- Never carry a fork requirement inside an upstream-owned migration. Re-assert it
  in a fork-owned migration that runs after (see 166 re-adding `dingtalk_chat` on
  top of upstream's 149), or the next sync silently drops it.
