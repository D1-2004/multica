# Product Feature Releases Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a global, searchable product-feature release feed to the shared Web/Desktop sidebar, with immutable version history, release detail, image-upgrade guidance, and previous-version navigation.

**Architecture:** PostgreSQL stores stable `product_feature` identities and immutable `product_feature_release` events. Authenticated users read a global feed through `/api/features`; operators publish a new feature or append an improvement through a bearer-protected internal endpoint. Shared React Query data access and shared `packages/views` pages serve both Next.js and Electron routes.

**Tech Stack:** PostgreSQL migrations, sqlc, Go/Chi, Zod, TanStack Query, React, Vitest, React Testing Library, i18next.

---

### Task 1: Persist immutable product feature releases

**Files:**
- Create: `server/migrations/9223_product_feature_tables.up.sql`
- Create: `server/migrations/9223_product_feature_tables.down.sql`
- Create: `server/migrations/9224_product_feature_id_index.up.sql`
- Create: `server/migrations/9224_product_feature_id_index.down.sql`
- Create: `server/migrations/9225_product_feature_slug_index.up.sql`
- Create: `server/migrations/9225_product_feature_slug_index.down.sql`
- Create: `server/migrations/9226_product_feature_release_id_index.up.sql`
- Create: `server/migrations/9226_product_feature_release_id_index.down.sql`
- Create: `server/migrations/9227_product_feature_release_published_index.up.sql`
- Create: `server/migrations/9227_product_feature_release_published_index.down.sql`
- Create: `server/migrations/9228_product_feature_release_history_index.up.sql`
- Create: `server/migrations/9228_product_feature_release_history_index.down.sql`
- Create: `server/internal/handler/product_feature.go`

- [ ] **Step 1: Write a failing handler integration test that inserts a new release and an improvement, then expects newest-first list order and an explicit previous release.**

```go
func TestProductFeatureReleaseHistoryAndOrdering(t *testing.T) {
    publishProductFeatureRelease(t, PublishProductFeatureReleaseRequest{FeatureSlug: "agent-chat", ReleaseType: "new", Title: "Agent chat", PublishedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
    latest := publishProductFeatureRelease(t, PublishProductFeatureReleaseRequest{FeatureSlug: "agent-chat", ReleaseType: "improvement", Title: "Agent chat follow-ups", PublishedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)})
    if latest.PreviousRelease == nil || latest.PreviousRelease.Title != "Agent chat" { t.Fatalf("missing previous release: %#v", latest) }
}
```

- [ ] **Step 2: Run the test and verify RED.**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/handler -run TestProductFeatureReleaseHistoryAndOrdering -count=1`

Expected: build failure because the product feature release contract is not implemented.

- [ ] **Step 3: Add two singular tables without foreign keys and add every identity, slug, ordering, and history index in its own concurrent migration.**

`product_feature` owns `id`, stable `slug`, `status`, and timestamps. `product_feature_release` owns `feature_id`, `release_type`, title/description/use-case/usage Markdown, `version_label`, `image_requirement`, `required_image_version`, `previous_release_id`, `published_at`, and `created_at`. Database checks accept only `new|improvement`, `active|archived`, and `none|latest_at_publish|min_version`; every non-`none` image requirement records the concrete image version.

- [ ] **Step 4: Add centralized explicit SQL for create/find/lock/list/detail.**

The list query accepts search terms and keeps a row only when every term is a case-insensitive substring of the concatenated searchable fields. It filters inactive features and future releases, returns `COUNT(*) OVER()`, and orders by `published_at DESC, id DESC`. Keep the SQL and its explicit scan list together in `product_feature.go`: repository-wide sqlc generation is currently blocked before this domain is analyzed because migration 271 references `task_completion_outbox` before its later schema declaration.

### Task 2: Publish and read feature releases through APIs

**Files:**
- Create: `server/internal/handler/product_feature.go`
- Create: `server/internal/handler/product_feature_test.go`
- Create: `server/cmd/server/product_feature.go`
- Create: `server/cmd/server/product_feature_test.go`
- Modify: `server/cmd/server/router.go`
- Create: `docs/product-feature-releases.md`

- [ ] **Step 1: Implement the minimal handlers required by the RED test.**

`POST /api/internal/features/releases` validates the slug and content, defaults `published_at` to now, locks the stable feature row, creates the feature for a first `new` release, requires an existing prior release for `improvement`, assigns `previous_release_id`, inserts once, and commits. It never exposes an update or delete endpoint.

- [ ] **Step 2: Add read handlers.**

`GET /api/features?q=&limit=&offset=` returns `{releases,total,limit,offset}`. `GET /api/features/{id}` returns the requested published release plus a compact `previous_release` from the same stable feature, or 404 for invalid/unpublished IDs.

- [ ] **Step 3: Protect the publish route and register authenticated reads.**

The internal write requires `Authorization: Bearer <MULTICA_LOG_TAIL_TOKEN>` and falls back to direct loopback only when the token is unset. Reads live in the authenticated, account-level router group and do not require workspace membership.

- [ ] **Step 4: Verify GREEN and API validation behavior.**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/handler ./cmd/server -run 'ProductFeature|FeatureRelease' -count=1`

Expected: history, ordering, search, validation, unpublished filtering, and bearer-auth tests pass.

- [ ] **Step 5: Document the protocol and append an audit history.**

The document records the public read endpoints, internal publish payload, immutability rules, search semantics, image requirement meanings, and a dated history entry explaining why version rows are appended rather than overwritten.

### Task 3: Add defensive TypeScript API parsing and queries

**Files:**
- Create: `packages/core/product-features/types.ts`
- Create: `packages/core/product-features/queries.ts`
- Create: `packages/core/product-features/index.ts`
- Modify: `packages/core/package.json`
- Modify: `packages/core/api/client.ts`
- Modify: `packages/core/api/schemas.ts`
- Modify: `packages/core/api/schemas.test.ts`

- [ ] **Step 1: Add failing schema tests.**

```ts
it("parses feature release wire fields into camelCase", () => {
  const parsed = ProductFeatureReleaseSchema.parse({
    id: "release-1", feature_id: "feature-1", feature_slug: "agent-chat",
    release_type: "new", title: "Agent chat", description: "Talk to agents",
    use_cases: "Support", usage_guide: "Open chat", version_label: "2026.9",
    image_requirement: "none", required_image_version: null,
    previous_release_id: null, published_at: "2026-09-01T00:00:00Z"
  });
  expect(parsed.featureSlug).toBe("agent-chat");
  expect(parsed.imageRequirement).toBe("none");
});
```

- [ ] **Step 2: Run and verify RED.**

Run: `pnpm --filter @multica/core test -- schemas.test.ts`

Expected: build failure because the feature release schema is absent.

- [ ] **Step 3: Implement strict types, compatible schemas, API methods, and query options.**

Unknown release/image enum values degrade to safe display defaults, optional fields receive explicit defaults, booleans use explicit equality, and list/detail methods use `parseWithFallback`. Query keys are global rather than workspace-scoped because content is identical across workspaces.

- [ ] **Step 4: Run and verify GREEN.**

Run: `pnpm --filter @multica/core test -- schemas.test.ts`

Expected: the new schema tests and existing schema compatibility tests pass.

### Task 4: Add shared routes, icons, sidebar entry, and localized copy

**Files:**
- Modify: `packages/core/paths/paths.ts`
- Modify: `packages/core/paths/route-icons.ts`
- Modify: `packages/core/paths/route-icons.test.ts`
- Modify: `packages/core/paths/tab-subject.test.ts`
- Modify: `packages/views/layout/route-icon-components.tsx`
- Modify: `packages/views/layout/app-sidebar.tsx`
- Modify: `packages/views/layout/app-sidebar.test.tsx`
- Create: `packages/views/locales/{en,zh-Hans,ja,ko}/product-features.json`
- Modify: `packages/views/locales/{en,zh-Hans,ja,ko}/layout.json`
- Modify: `packages/views/locales/index.ts`
- Modify: `packages/views/i18n/resources-types.ts`

- [ ] **Step 1: Extend route/sidebar tests first and verify RED.**

Expect `paths.workspace("acme").featureUpdates()` to equal `/acme/features`, `featureReleaseDetail("r1")` to encode its ID, `resolveRouteIconName` to return `Megaphone`, and the shared sidebar to render the localized feature-updates entry.

Run: `pnpm --filter @multica/core test -- route-icons.test.ts tab-subject.test.ts && pnpm --filter @multica/views test -- app-sidebar.test.tsx`

Expected: failures for the missing route and navigation item.

- [ ] **Step 2: Add the route registry, icon, sidebar item, and all four locale resources.**

The item is placed in the Configuration group immediately before Settings. The Chinese name is `功能动态`; English is `Product updates`; Japanese and Korean use natural localized equivalents.

- [ ] **Step 3: Run and verify GREEN.**

Run the same focused command and expect all route/sidebar tests to pass.

### Task 5: Build the shared list and detail pages

**Files:**
- Create: `packages/views/product-features/components/product-feature-list-page.tsx`
- Create: `packages/views/product-features/components/product-feature-list-page.test.tsx`
- Create: `packages/views/product-features/components/product-feature-detail-page.tsx`
- Create: `packages/views/product-features/components/product-feature-detail-page.test.tsx`
- Create: `packages/views/product-features/index.ts`
- Modify: `packages/views/package.json`

- [ ] **Step 1: Add failing list and detail component tests.**

The list test supplies out-of-order releases and asserts title, description, type label, and newest-first published date while verifying that typing a query changes the query options. The detail test asserts title, description, Markdown sections, concrete image-upgrade guidance, and previous-version navigation.

- [ ] **Step 2: Run and verify RED.**

Run: `pnpm --filter @multica/views test -- product-feature-list-page.test.tsx product-feature-detail-page.test.tsx`

Expected: build failures because the pages are absent.

- [ ] **Step 3: Implement the pages with existing shared primitives.**

Use `CollectionPageHeader`, `CollectionPageState`, `Input`, `Badge`, `Button`, the canonical `RichContent` renderer, `useDebouncedValue`, React Query, semantic design tokens, deliberate overflow handling, and `useNavigation()`/workspace paths only.

- [ ] **Step 4: Run and verify GREEN.**

Run the same focused command and expect both page test files to pass.

### Task 6: Wire Web and Desktop routes

**Files:**
- Create: `apps/web/app/[workspaceSlug]/(dashboard)/features/page.tsx`
- Create: `apps/web/app/[workspaceSlug]/(dashboard)/features/[id]/page.tsx`
- Create: `apps/desktop/src/renderer/src/pages/product-feature-detail-page.tsx`
- Modify: `apps/desktop/src/renderer/src/routes.tsx`

- [ ] **Step 1: Add the shared list page to `/features` and detail page to `/features/:id` in both platforms.**

The desktop wrapper reads the route ID, fetches the release through the shared query option, and updates the document title to the release title while rendering the same shared detail component as Web.

- [ ] **Step 2: Run focused type checks.**

Run: `pnpm --filter @multica/core typecheck && pnpm --filter @multica/views typecheck && pnpm --filter @multica/web typecheck && pnpm --filter @multica/desktop typecheck`

Expected: all four packages exit zero.

### Task 7: Final verification and review

**Files:**
- Review: all changed files

- [ ] **Step 1: Run migration and generated-code checks.**

Run: `cd server && GOTOOLCHAIN=auto go test ./internal/migrations ./internal/handler ./cmd/server -count=1`

- [ ] **Step 2: Run focused frontend tests and type checks.**

Run: `pnpm --filter @multica/core test -- schemas.test.ts route-icons.test.ts tab-subject.test.ts && pnpm --filter @multica/views test -- app-sidebar.test.tsx product-feature-list-page.test.tsx product-feature-detail-page.test.tsx && pnpm --filter @multica/core typecheck && pnpm --filter @multica/views typecheck && pnpm --filter @multica/web typecheck && pnpm --filter @multica/desktop typecheck`

- [ ] **Step 3: Inspect scope without formatting or committing.**

Run: `git status --short && git diff --stat && git diff --check`

Expected: only product-feature implementation, protocol documentation, plan, and generated sqlc files are changed; no whitespace errors; no commit, push, PR, or deployment.
