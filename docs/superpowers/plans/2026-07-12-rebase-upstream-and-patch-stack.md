# Upstream Rebase and Patch Stack Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 fork 的 `develop` 迁移到最新 `upstream/main`，保留现有功能，并形成后续可重复迁移的 PATCH 分层与验证清单。

**Architecture:** 在隔离 worktree 的 `codex/rebase-upstream-20260712` 分支上使用 `git rebase --rebase-merges upstream/main`，保留原有分支合并语义并逐项解决冲突。迁移完成后按 DingTalk、Lark/Auth、FC E2B、可靠性与通用修复五个域梳理 PATCH，记录侵入点和未来降耦建议。

**Tech Stack:** Git rebase-merges、Go server/CLI、Next.js/React/TypeScript、PostgreSQL migrations、pnpm/turbo。

---

### Task 1: 建立安全基线

**Files:**
- Create: `docs/superpowers/plans/2026-07-12-rebase-upstream-and-patch-stack.md`

- [x] **Step 1: 刷新远端并确认分叉**

  Run: `git fetch --all --prune && git rev-list --left-right --count upstream/main...develop`

  Expected: upstream 与 fork 均有独立提交，不能 fast-forward。

- [x] **Step 2: 保留回滚引用**

  Run: `git branch backup/develop-before-upstream-20260712 develop`

  Expected: backup 分支指向 rebase 前的 `develop`。

- [x] **Step 3: 创建隔离 worktree**

  Run: `git worktree add .claude/worktrees/rebase-upstream-20260712 -b codex/rebase-upstream-20260712 develop`

  Expected: 新 worktree 干净，原工作区未跟踪文件保持不变。

### Task 2: 保留拓扑执行 rebase

**Files:**
- Modify: rebase 冲突涉及的现有源码与迁移文件

- [x] **Step 1: 启动保留 merge 的 rebase**

  Run: `git rebase --rebase-merges upstream/main`

  Expected: 无冲突时完成；有冲突时停在首个冲突提交。

- [x] **Step 2: 逐提交解决冲突**

  每次运行 `git status --short` 和 `git show REBASE_HEAD --stat --oneline`，先确认 PATCH 意图，再融合 upstream 新架构；使用 `git add <resolved-files>` 与 `git rebase --continue` 推进。禁止整文件机械选择 ours/theirs。

- [x] **Step 3: 核对迁移后提交集合**

  Run: `git range-diff upstream/main...backup/develop-before-upstream-20260712 upstream/main...HEAD`

  Expected: 每个原 PATCH 均能映射到迁移后提交，主动丢弃或被 upstream 吸收的提交有明确记录。

### Task 3: 梳理可迁移 PATCH 栈

**Files:**
- Create: `docs/fork-patches.md`

- [x] **Step 1: 按业务域归类**

  将提交归类为 DingTalk Channel、Lark/Auth、FC E2B Runtime、任务可靠性、通用 CLI/Web 修复，并记录提交范围和主要目录。

- [x] **Step 2: 标注侵入点与迁移成本**

  对每组 PATCH 记录 upstream 重叠模块、配置/数据库依赖、冲突热点，以及“独立适配层、feature flag、追加 migration、生成代码重建”等降耦策略。

- [x] **Step 3: 给出下一轮 PATCH 重整顺序**

  输出可独立 cherry-pick 的建议序列：基础配置与 schema → provider/channel adapter → UI → 文档；把纯修复与产品 feature 分离。

### Task 4: 验证迁移结果

**Files:**
- Modify: 仅修复验证暴露的 rebase 回归

- [x] **Step 1: 检查仓库和冲突标记**

  Run: `git status --short && rg -n '^(<<<<<<<|=======|>>>>>>>)' --glob '!pnpm-lock.yaml'`

  Expected: 无未解决冲突，只有计划内文件变化。

- [x] **Step 2: 验证 Go 受影响包**

  Run: `go test ./cmd/multica/... ./internal/integrations/dingtalk/... ./internal/integrations/lark/... ./internal/daemon/... ./internal/service/...`

  Workdir: `server`

  Expected: exit 0。

- [x] **Step 3: 验证前端类型**

  Run: `pnpm typecheck`

  Expected: exit 0；如存在确认过的 upstream 基线错误，单独记录，不混作 PATCH 成功。

- [x] **Step 4: 回填结果与遗留项**

  将 rebase 结果、验证证据、未解决风险和后续 PATCH 重整建议写回两份计划文档。

## 执行结果

- `develop` 上 68 个 fork 提交已通过 `git rebase --rebase-merges upstream/main` 迁移到 upstream `b47e835d7`；原分支未改动，回滚引用为 `backup/develop-before-upstream-20260712`。
- `range-diff` 已逐项核对：DingTalk、Lark/Auth、FC E2B/DWS 和可靠性 PATCH 均保留；旧 feature flag dispatch/source channel 等已被 upstream 架构替代的路径没有复活。
- fork 已发布的 `132_issue_origin_dingtalk_chat`、`133_fc_e2b_sandbox_session`、`134_dws_auth_profile` 保留原 stem，并与 upstream 同号 migration 在全新数据库中共同完成 001-163 迁移。
- 受影响 Go 包、前端全量测试、typecheck、lint、Web production build 均通过。lint 保留 20 条既有 warning；全并发 Go 测试曾触发两条极短 Codex inactivity 时序用例，单独连续运行 5 次均通过，判断为负载时序抖动。
- 后续维护边界、冲突热点、重放顺序和升级检查表已固化在 `docs/fork-patches.md`。
