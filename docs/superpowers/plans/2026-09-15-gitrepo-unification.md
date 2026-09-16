# GitRepo Unification Implementation Plan

> Execute in this worktree, with focused test-first checks at each protocol boundary.

**Goal:** Let users import and publish Agent and skill configuration from a repository URL without selecting GitHub or Alibaba Code.

**Architecture:** `gitrepo` owns address parsing, authorized connection selection, provider authentication, immutable revision reads and transport errors. Agent and skill services consume provider-neutral trees and files; package validation, scope plus skill ID matching, publication diffs and history remain business responsibilities. Local ZIPs continue through the same package parser. No execution identity or sandbox changes are involved.

**Tech Stack:** Go, PostgreSQL/sqlc, TypeScript/React Query, shared web/desktop views.

## Work sequence

- [x] Add `server/internal/gitrepo` address, repository, revision, tree and error contracts. Cover HTTPS/SSH, Code aliases, unsupported hosts and invalid paths with table tests; run the tests before implementation.
- [x] Move the existing GitHub repository client into this module and bind installation credentials behind a repository instance. Add Alibaba Code PAT API transport with fixed origins, bounded reads and pagination tests. Keep provider credentials out of result types and errors.
- [x] Replace Agent compiler dependencies on GitHub installation IDs and GitHub tree types with repository-neutral readers, including local filesystem and publication snapshots. Run existing package/compiler/diff tests.
- [x] Add workspace Git connections with encrypted Code credentials and GitHub installation metadata. Migrate existing source and publication connection fields and repository URLs once. Move GitHub authorization and installation lifecycle into the same table, plus workspace cleanup.
- [x] Replace GitHub-specific Agent source endpoints and request fields with unified Git endpoints. Resolve the repository from the URL and authorized workspace connections. Confirmation rechecks access but applies the stored SHA and snapshot.
- [x] Route standalone Git skill imports, including skills.sh repository resolution, through GitRepo. Keep skill parsing and conflict handling in the skill service.
- [x] Move repository client types/hooks into `packages/core/git-repo`. Update Agent creation/publication, workspace connection management and skill import UI to enter a URL first; show authorization or account selection when needed.
- [x] Update affected tests, built-in skill references and maintained protocol documentation. Add protocol audit entries. Verify compiler, handlers, frontend typecheck, focused tests and lint without invoking formatters.

## Acceptance boundaries

- GitHub and Code addresses infer the provider server-side; no caller-supplied provider is required.
- Credentials are workspace-scoped, encrypted at rest, omitted from response logs, exports and histories, and never forwarded to another origin.
- Branch/tag selection resolves to a commit before reading; all content and confirmation use that commit.
- Agent import/export codecs retain their paired contract; skill identity remains scope plus skill ID.
- Existing GitHub data moves to the new schema through migration, with no old/new application paths or compatibility aliases.
- Tests distinguish simulated provider HTTP responses from live private-repository acceptance. Deployment is outside this implementation request.

## Verification record

- Go GitRepo, Agent package compiler and managed Agent suites passed; focused HTTP and PostgreSQL flows cover GitHub and Code, pinned previews, publication, rollback and skill scope identity.
- Core API tests passed; shared UI tests cover both GitHub and Code creation, publication, GitHub authorization, settings and locale parity. TypeScript typecheck and lint passed (existing lint warnings remain).
- Production Go build, targeted go vet and selective sqlc regeneration check passed.
- Isolated PostgreSQL verified all new up migrations, full down/up, preservation of existing GitHub identity IDs and the refusal to downgrade Code content.
- Global migration-prefix lint still fails on the pre-existing duplicate 9093 prefix; both files exist in the starting commit. The new 9261–9266 prefixes are unique; paired directions and prefix-range checks pass.
- Live Code private-repository and preproduction network acceptance has not been performed. No commit, push or deployment was performed in this implementation phase.

- 2026-09-15 发布准备：本次迁移编号顺延至 9261–9266，避开预发集成分支已存在的迁移；这六份迁移尚未发布，无需旧编号兼容。

## 后续变更记录

- 2026-09-16：因内部代码平台的安全边界，移除本计划中的 Aone Code 接入能力。当前 GitRepo 仅支持 GitHub，以 `docs/git-repositories.md` 为当前行为契约；以上勾选项保留为历史实施记录。
