# Repository Guidelines

This file provides guidance to AI agents when working with code in this repository.

> **Single source of truth:** This file is a concise pointer document.
> All authoritative architecture, coding rules, and conventions
> live in **CLAUDE.md** at the project root. Read that file first.
> Use `Makefile`, `package.json`, and `pnpm-workspace.yaml` as the
> source of truth for the full command list.

## Quick Reference

### Architecture

Go backend + monorepo frontend (pnpm workspaces + Turborepo) with shared packages.

- `server/` - Go backend (Chi router, sqlc, gorilla/websocket)
- `apps/web/` - Next.js frontend (App Router)
- `apps/desktop/` - Electron desktop app
- `apps/mobile/` - Expo / React Native iOS app (read `apps/mobile/CLAUDE.md` first)
- `apps/docs/` - Fumadocs documentation site
- `packages/core/` - Headless business logic (Zustand stores, React Query hooks, API client)
- `packages/ui/` - Atomic UI components (shadcn/Base UI, zero business logic)
- `packages/views/` - Shared business pages/components
- `packages/tsconfig/` - Shared TypeScript config
- `packages/eslint-config/` - Shared ESLint config

### State Management (critical)

- **React Query** owns all server state (issues, members, agents, inbox, workspace list)
- **Zustand** owns client/view state (view filters, drafts, modals, desktop tab state); current workspace identity is route-driven and only mirrored for platform plumbing
- All Zustand stores live in `packages/core/` - never in `packages/views/` or app directories
- WS events update React Query for server data; store writes are only for clearing client-owned pointers with a single responder/self-event guard

### Package Boundaries (hard rules)

- `packages/core/` - zero react-dom, zero localStorage, zero process.env
- `packages/ui/` - zero `@multica/core` imports
- `packages/views/` - zero `next/*`, zero `react-router-dom`, use `NavigationAdapter` for routing
- `apps/web/platform/` - only place for Next.js APIs

### Database Migrations (hard rules)

- Never add database foreign keys or cascading actions. Enforce relationships and perform dependent cleanup explicitly in the application layer, using transactions when the operation must be atomic.
- Every index created by a migration, including unique indexes and indexes on new tables, must use `CREATE [UNIQUE] INDEX CONCURRENTLY`. Keep each concurrent index build in its own single-statement migration file.

### Commands

```bash
make dev              # Auto-setup + start everything
pnpm typecheck        # TypeScript check
pnpm test             # TS unit tests (Vitest)
make test             # Go tests
make check            # Full verification pipeline
make deploy           # Submit the current branch to the Aone 预发 pipeline
```

### Aone Fork

This repo is the Aone-deployed fork of `multica-ai/multica`. Deploying, reading
server logs, changing runtime config, and diagnosing a failed deploy are covered
by the `aone-deploy` skill in `.agents/skills/`. The migration and upstream-sync
rules that break production if missed are in CLAUDE.md ("Aone Fork").
Coordinator Scene Memory e2e plays (next-turn SLS recall) are in
`docs/plans/2026-09-02-coordinator-scene-memory-e2e.md`.

### Agent work scene (`scene_id`) — hard rules

A scene is one agent in one tenant org in one group, 1:1 chat or enterprise,
identified only by `scene_id` from `agent_scene` (`docs/agent-scene.md`).

- Never use an openConversationId, staffId, UID or chat title as a scene key.
  A 1:1 chat is keyed by its conversation, never by its person.
- Register or find scenes only through `scene.Resolve` / `scene.Lookup`
  (`server/internal/scene`); unknown kinds are rejected, never guessed.
- New events and records carry `scene.Ref` (`agent_scene`) resolved at
  admission; resource events use the enterprise scene.
- `assoc_scene` and `scene_memory` are retired: no reads, writes or fallbacks.

See CLAUDE.md for the authoritative rules and common commands.

## Unified event admission

Provider facts pass through `internal/eventrouter` before scene business handling
(`docs/event-scene-router.md`). Host constructs owner/principal/tenant metadata;
payload actors grant nothing. `scene_event_receipt` freezes one source/id receipt and
SceneRef. Canary targets are exact workspace/agent/org triples; retries retain
their route. Unknown locators are held, never sent to a guessed scene.
