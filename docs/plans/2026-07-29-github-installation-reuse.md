# GitHub 已有 Installation 跨 Workspace 复用实施计划

> 工作流：grill-and-plan
> 状态：已完成
> 创建日期：2026-07-29
> 计划 ID：20260729-github-installation-reuse
> 最后更新时间：2026-07-29 10:47
> 当前分支：codex/github-existing-installation-reuse
> 目标执行分支：codex/github-existing-installation-reuse
> 基线 Commit：25840fb6741b77a79ad8340293f68ad82646e02c
> 原始工作区：/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica
> Worktree 路径：不使用
> Worktree 来源：不使用
> 交付状态：已完成（计划与实现将作为同一原子 commit 提交）
> 收尾状态：已完成
> 当前里程碑：全部验收门禁已通过

## 背景与现状证据

- `github_installation` 已使用 `(workspace_id, installation_id)` 唯一键，数据库和 webhook 已允许同一个 GitHub App installation 同时绑定多个 workspace。
- 当前 `GET /api/workspaces/{id}/github/connect` 只生成 GitHub `/installations/new` URL；只有 setup callback 收到 `installation_id + state` 后才写入 workspace 绑定。
- GitHub App 已安装时，用户进入 Configure/更新路径，不能稳定触发新的 workspace setup callback；卸载重装会触发首次安装回调，但也会通过 `installation.deleted` 删除已有 workspace 的全部绑定。
- 现有 `TestSecondWorkspaceBindDoesNotUnbindFirst` 在 develop 基线上通过，证明底层多 workspace 数据能力可直接复用。

## 目标

- 新 workspace 的 owner/admin 可以显式复用自己在其他可管理 Multica workspace 中已经存在的 GitHub installation。
- 复用后新旧 workspace 各自保留独立 `github_installation` 行，均可继续接收 webhook、列仓库和使用 GitHub Agent Source。
- 连接过程不要求卸载 GitHub App，也不信任客户端提供的 numeric `installation_id`。
- UI 清楚区分“使用已有连接”和“安装/连接另一个 GitHub 账户”。

## 非目标

- 本轮不引入 GitHub 用户 OAuth、长期 user access token 或新的 GitHub App client secret。
- 本轮不处理“GitHub 已安装，但该 installation 从未绑定到当前用户可管理的任何 Multica workspace”的恢复场景；该场景需要独立的 GitHub 用户身份校验流程。
- 不改变 GitHub App 权限、仓库选择、webhook 格式、Agent Source 同步语义。
- 不 push、不创建 PR、不部署预发或生产。

## 已确认需求

- 从最新 `origin/develop` 创建独立分支开始修改。
- 解决新 workspace 复用已有 GitHub App installation 时必须卸载重装的问题。
- 保留 workspace 级授权与隔离，不把一份 binding 行直接跨 workspace 共用。

## 执行假设

- Multica workspace 的 owner/admin 已经拥有该 workspace GitHub integration 的管理权；只有同时能管理来源 workspace 和目标 workspace 的同一 Multica 用户，才能把来源 installation 复用到目标。
- 复用是显式用户动作，不自动把其他 workspace 的 GitHub 权限扩散到新 workspace。
- 已有 installation webhook 未删除来源绑定时，将其视为 Multica 当前仍信任的 installation；本轮不增加新的 GitHub 在线校验。

## 关键设计决定

- 新增“可复用 installation”只读查询：按当前用户在其他 workspace 的 owner/admin membership 过滤，并按 numeric installation 去重。
- API 只向客户端暴露来源 binding 的内部 UUID；复用写接口在服务端重新校验当前用户对来源 workspace 的 owner/admin 权限，再读取 numeric installation。
- 目标 workspace 仍创建自己的 binding 行，`connected_by_id` 记录本次复用者；不修改或迁移来源行。
- 前端在未连接状态展示可复用连接列表，每个条目明确来源 workspace；用户也可继续走原来的 GitHub 安装入口。

## 被排除的方案

- 让用户手工输入或客户端直接提交 numeric `installation_id`：GitHub 明确说明 setup URL 参数可伪造，且该方案不能证明当前用户有权使用该 installation。
- Connect GET 自动复用唯一 installation：GET 产生写副作用且会在多 installation 时替用户做权限扩散决定。
- 自动绑定当前用户可见的全部 installations：权限范围过宽，可能向目标 workspace 的其他管理员暴露无关仓库。
- 本轮直接引入完整 GitHub OAuth：能覆盖更广场景，但需要新增 App client secret、回调、一次性 intent 和多 installation 选择状态，超出当前已绑定跨 workspace 故障的最小修复。

## 复杂度与执行路由

- 规划复杂度：P3
- 执行复杂度：E1
- 判断依据：涉及跨 workspace 授权、公共 API、后端查询、共享前端和兼容性，但实现强耦合且可由一个主 agent 连续完成。
- 主执行者：主 agent
- Subagent 数量与职责：0；用户未要求 subagent，且代码路径共享同一授权上下文。
- Review 安排：主 agent 自检；完成前使用 verify-before-finish 对照验收条件。
- Worktree：不需要；工作区干净，用户明确要求从 develop 新建分支，当前目录已切到该独立功能分支。
- Worktree 来源与清理责任：不适用。
- 分支策略：明确的新分支 `codex/github-existing-installation-reuse`，依据为用户指令。
- Commit 策略：完成测试和验证后由主 agent结合状态决定是否创建一个本地原子 commit。

## 文件与职责

- `server/pkg/db/queries/github.sql`：列出并校验当前用户可复用的来源 binding。
- `server/pkg/db/generated/github.sql.go`：由 sqlc 生成查询代码。
- `server/internal/handler/github.go`：响应可复用列表并执行受权复用。
- `server/cmd/server/router.go`：在 GitHub admin 路由下注册复用写接口。
- `server/internal/handler/github_test.go`：覆盖列表过滤、来源权限、重复复用和双 workspace 共存。
- `packages/core/types/github.ts`、`packages/core/api/schemas.ts`、`packages/core/api/client.ts`：新增兼容的响应类型、运行时校验和复用 API。
- `packages/views/settings/components/github-tab.tsx`：展示并执行已有连接复用。
- `packages/views/settings/components/github-tab.test.tsx`：覆盖复用 UI、成功刷新和失败反馈。
- `packages/views/locales/*/settings.json`：四语种最小产品文案。
- `apps/docs/content/docs/github-integration*.mdx`：说明同一 installation 可显式复用到多个 workspace，禁止卸载重装作为连接方式。

## 实施步骤

- [x] 里程碑一：新增失败的后端授权/复用回归测试和前端交互测试。
- [x] 里程碑二：实现可复用 installation 查询与受权写接口，运行 sqlc 并通过后端窄测。
- [x] 里程碑三：实现 Core API、Settings UI、四语种文案和文档，运行前端窄测与类型检查。
- [x] 里程碑四：执行整体验证、自检 diff、更新计划结果并完成本地交付。

## 执行记录

| 里程碑 | 状态 | 关联 Commit（可选） | 实际验证命令 | 结果与证据 |
|---|---|---|---|---|
| 计划确认与环境准备 | 已完成 |  | `git fetch origin develop`；`git switch -c codex/github-existing-installation-reuse origin/develop` | 用户已明确要求开始修改；分支基于 `25840fb6`，工作区创建前干净 |
| 后端测试先行 | 已完成 | 本分支 HEAD | 新增 `TestReuseGitHubInstallationAcrossManagedWorkspaces` 后先运行对应窄测 | 实现前因 handler/type 不存在而失败；实现后通过 |
| 后端查询与 API | 已完成 | 本分支 HEAD | `make sqlc`；`go test ./internal/handler -run '^(TestReuseGitHubInstallationAcrossManagedWorkspaces\|TestListGitHubInstallations_RoleGating\|TestGitHubRoutes_RoleGating\|TestSecondWorkspaceBindDoesNotUnbindFirst)$' -count=1` | 受权复用、双 binding 共存、幂等、member/机器身份拒绝均通过 |
| Core 与 Settings UI | 已完成 | 本分支 HEAD | Core schema 测试；GitHub Settings 组件测试；Core/Views typecheck；改动 TS/TSX lint | 兼容旧响应、失败闭合、显式复用成功/失败反馈均通过 |
| 文案与文档 | 已完成 | 本分支 HEAD | 四份 locale JSON 解析；`pnpm --filter @multica/docs typecheck` | 英/中/日/韩文档和文案通过生成与类型检查 |
| 整体回归与收尾 | 已完成 | 本分支 HEAD | `go test ./...`；`NODE_OPTIONS=--no-experimental-webstorage pnpm --filter @multica/core test`；`git diff --check` | Go 全量通过；Core 86 files / 900 tests 全通过；diff 无空白错误 |

## 验证策略

| 改动或验收项 | 风险 | 验证方式 | 是否测试先行 |
|---|---|---|---|
| 仅双 workspace 管理员可复用 | 高：跨 workspace 越权 | handler + router 集成测试，覆盖 member/outsider/source 无管理权 | 是 |
| 同一 installation 双 binding 共存 | 高：旧 workspace 被覆盖或断开 | 数据库集成测试验证两个 binding 行和幂等 upsert | 是 |
| 前端明确复用且不自动扩散 | 中 | Vitest 交互测试，验证显式点击、目标 UUID 和 query invalidation | 是 |
| API/类型兼容 | 中 | Core 类型检查与相关 package 测试 | 否 |
| 文档与多语言完整 | 低 | JSON 解析、locale 测试或 typecheck | 否 |

## 风险与回滚

- 风险：管理员把来源 installation 复用到含其他管理员的目标 workspace，会扩大该目标 workspace 管理员可查看的仓库范围。缓解：只允许同一用户同时管理来源和目标，且必须显式点击具体账户/来源 workspace。
- 风险：GitHub uninstall webhook 与本地复用并发可能造成短暂陈旧绑定；该竞态已存在于 setup callback，本轮不扩大为全局 installation 实体重构。
- 回滚：移除复用 API/UI/查询即可；新增 binding 使用现有表结构，无 migration 和数据回填。已经复用的 binding 可通过目标 workspace 的 Disconnect 独立删除。

## 发布边界

- Push：本地 commit 后告知“已提交，准备 push”；用户明确同意后直接执行
- PR / 合并 / 部署 / 发布：未授权，除非用户另行明确确认

## 计划变更记录

| 日期 | 变更 | 原因 | 是否重新确认 |
|---|---|---|---|
| 2026-07-29 | 采用已绑定来源 workspace 的显式安全复用，不引入完整 GitHub OAuth | 精确解决当前故障，同时避免信任可伪造 numeric installation_id | 否；用户已要求按已诊断方向开始修改 |

## 调试假设记录（仅 Bug/故障任务）

| 序号 | 假设 | 验证方式 | 结果 | 结论 |
|---|---|---|---|---|
| 1 | 数据库仍限制一个 installation 只能绑定一个 workspace | 检查 migration/query 并运行 `TestSecondWorkspaceBindDoesNotUnbindFirst` | `(workspace_id, installation_id)` 唯一键，测试通过 | 排除 |
| 2 | GitHub App 已安装时没有触发新 workspace 的 setup callback | 对照 connect URL、setup handler 与 GitHub setup URL 契约 | Connect 仅生成 `/installations/new`，binding 只在 callback 创建 | 确认直接根因 |
| 3 | 可以直接让客户端提交 numeric installation id | 对照 GitHub 安全说明与当前授权模型 | 参数可伪造，不能证明当前用户对 installation 的权限 | 排除 |

## 最终验证结果

| 验收项 | 验证命令或检查 | 结果 | 证据摘要 |
|---|---|---|---|
| 双边 workspace 管理权限与 human actor 门禁 | `go test ./internal/handler -run '^(TestReuseGitHubInstallationAcrossManagedWorkspaces\|TestListGitHubInstallations_RoleGating\|TestGitHubRoutes_RoleGating\|TestSecondWorkspaceBindDoesNotUnbindFirst)$' -count=1` | 通过 | 来源 member-only 返回 404；目标 member/outsider 和 task token 被拒绝；客户端只提交内部 source binding UUID |
| 新旧 workspace binding 共存与幂等 | 同一 handler 回归测试；`go test ./...` | 通过 | 首次复用后保留来源并新增目标，重复请求仍只有两行；Go 全仓回归通过 |
| Core API 契约兼容 | `pnpm --filter @multica/core exec vitest run api/schemas.test.ts --reporter=dot` | 通过 | 1 file / 74 tests；旧服务端响应默认空 reusable 列表，畸形响应失败闭合 |
| Settings 显式复用 UX | `pnpm --filter @multica/views exec vitest run settings/components/github-tab.test.tsx --reporter=dot` | 通过 | 1 file / 12 tests；覆盖来源展示、显式点击、成功刷新、失败保留候选 |
| Core 全量回归 | `NODE_OPTIONS=--no-experimental-webstorage pnpm --filter @multica/core test` | 通过 | 86 files / 900 tests；关闭 Node 全局实验 Web Storage 以让 jsdom 提供 localStorage |
| 类型、文档与 lint | Core/Views/Docs `typecheck`；改动 TS/TSX `eslint`；locale JSON 解析 | 通过 | 三处类型检查、文档 MDX 生成、lint 和 JSON 解析全部退出 0 |
| 最终 diff 范围 | `git diff --check`；`git status --short --branch`；人工复核最终 diff | 通过 | 仅 GitHub 复用实现、测试、四语文案/文档和本计划；无无关生成文件 |

### 最终工作区

- 原始工作区与分支：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`，`codex/dta-agent-format-sync`
- 最终工作区与分支：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`，`codex/github-existing-installation-reuse`
- 交付状态：已完成；本计划与实现同属最终分支 HEAD
- Worktree 收尾：不适用
- 当前未提交改动：本地 commit 创建后应为无
- 未执行的验证：未连接真实 GitHub 账户做浏览器端手工验证；未 push、未建 PR、未部署

## 遗留风险

- 完整 GitHub 用户 OAuth 恢复路径仍是后续能力：当 installation 没有任何当前用户可管理的 Multica 来源 workspace 时，仍需 GitHub user access token 才能安全证明用户有权绑定该已有 installation。
