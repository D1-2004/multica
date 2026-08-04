# DTA Token 普通成员身份改造实施计划

> 工作流：grill-and-plan
> 状态：已完成
> 创建日期：2026-08-03
> 计划 ID：20260803-dta-service-member
> 最后更新时间：2026-08-04 CST
> 当前分支：`codex/dta-service-member`
> 目标执行分支：`codex/dta-service-member`
> 基线 Commit：`d839e6c382aa60724f4e43ccb9ab8034dd62fd8f`
> 原始工作区：`/Users/fanqi/test/code/ding-fde-agent/.worktrees/dt-fde-multica-workspace-access-grants`
> 原始分支：`codex/workspace-access-grants`
> Worktree 路径：`/Users/fanqi/test/code/ding-fde-agent/.worktrees/dt-fde-multica-dta-service-member`
> Worktree 来源：本次任务创建
> 交付状态：已基于最新正式基线完成 rebase、migration 重编号和发布前回归，可进入推送与正式合并流程
> 收尾状态：Worktree 保留；本地已完成，尚未 push 重写后的分支
> 当前里程碑：正式发布准备

## 一句话结论

把每个 DTA Token 的稳定 subject 建成目标 Workspace 中 role=`member` 的不可登录 service user；Token 请求走 Multica 原生成员、ownership、Runtime、Trace、Autopilot 和钉钉绑定权限，不再维护 DTA 专属 capability 与逐路由 allowlist。普通成员可创建并管理自己拥有的 stable/private FC/E2B Runtime，candidate、public 和发布管理仍受限。

## 背景与现状证据

- 基线分支已经实现单层 `workspace_access_token`、稳定 `subject_user_id`、明文单次返回、过期、重新生成、吊销、审计、设置页和 feature flag。
- 基线把 subject 明确排除在 `workspace_member` 之外，依赖 `WorkspaceAccessPrincipal`、三个 capability、逐路由 allowlist 和每个 Handler 的 Token ownership 分支。
- 该模型每增加一个 DTA 交付功能都要同步扩展 allowlist、capability、UI、迁移和特殊 ownership；Runtime、`mat_`、snapshot、Autopilot 和钉钉绑定已暴露出持续扩张成本。
- 用户已决定初版不做细粒度 Token 权限：DTA subject 权限与普通非管理员用户一致，并追加确认普通成员需要创建自有 Runtime。
- 旧 worktree 仍保留孤儿钉钉绑定过滤和组织目录 deny 测试的未提交修改；新分支不复制这些代码，因为普通 member 模型会删除 Token 专属过滤和 allowlist，随后按原生 member 行为重新验收。

## 目标

1. Owner 创建 Token 时，事务内创建不可交互登录的 subject user、role=`member` 的 Workspace membership、Token 和审计记录。
2. 已存在的预发 Token subject 通过追加 migration 幂等补齐并强制归一为 member；成员管理接口不能提升 service member，且有关联 Token 时不能移除，不修改 Agent、Skill、Task 或 Trace ownership。
3. `dta_` 认证继续校验 Token 的 workspace、有效期、吊销和 feature flag，但授权进入普通 Workspace member 链路。
4. 删除运行时 capability gate、DTA 专属路由 allowlist和 Handler 特判；Agent/Skill/Runtime/Trace/Autopilot/钉钉绑定复用普通 member 行为。
5. Token 创建、编辑页面只管理名称、有效期、重新生成、吊销和删除；不展示或编辑 capability。
6. 普通成员可创建 stable-channel、private FC/E2B Runtime，owner 为调用者；可编辑名称和删除自己的 Runtime。非发布者 member 不能创建 candidate，非管理员 member 不能创建或切换为 public，也不能管理稳定发布。
7. Agent 领取任务时 `mat_.user_id = runtime.owner_id` 的现有机制保持；DTA service member 同时拥有其 Runtime 与 Agent，因此沙箱 Multica CLI 继续走现有 task-token 链路。
8. 不修改 DTA 仓库；Multica 侧保持当前 DTA 所需自省/profile兼容接口，并确保完整普通 member API 合同可用。
9. 不兼容旧 DTA Token capability/resource-scope 合同；数据库、API、sqlc、认证和 UI 直接删除两项旧策略字段。

## 非目标

- 不新增或保留 `deployment.manage`、`deployment.retire`、`trace.read`、`schedule.manage` 等可配置权限。
- 不向非发布者普通成员开放 candidate Runtime，不向非管理员普通成员开放 public managed Runtime、稳定版本发布或 fleet 管理。
- 不让 service user 登录 Multica Web/Desktop、签发 human PAT、成为 admin/owner 或管理成员。
- 不限制 Token 必须由 DTA 客户端调用；当前目标是先跑通服务商交付链路，不建设 CLI/调用方识别门禁。
- 不承诺 DTA Token 只能被某个客户端调用；服务端只能识别凭据身份，不能区分 HTTPS 请求来自 DTA、curl 或 CLI。
- 不修改 DTA、不发布生产、不自动 push、不开 PR、不合并、不部署。
- 不兼容已经部署到预发但尚无人使用的旧 DTA Token capability 请求/响应。

## 已确认需求

- 每个 Token 对应一个稳定 service user；不同外部使用方创建不同 Token。
- service user 是 Workspace 普通 member，拥有与其他普通成员相同的权限，不是管理员。
- Owner 只控制 Token 生命周期，暂不配置细粒度业务权限。
- 普通成员和 DTA service member 可以创建并管理自己的 stable/private FC/E2B Runtime。
- Agent、Skill 等资源继续使用原生 owner/creator 字段；Token 重新生成不改变 subject 或既有资源归属。
- 节律、snapshot、Trace、机器人和数字员工绑定不再建设 DTA 专属放行层，按普通 member 的现有能力验收。
- 本轮只改 Multica；旧分支保留，不在其上继续开发。

## 执行假设

- `user.principal_type=workspace_access_token` 继续标识不可交互登录的 service user；membership 只提供 Workspace 授权上下文，不开放登录能力。
- membership 使用现有普通 `member` role；不新增 `service_member` 角色，避免再造一套权限体系。
- migration 直接删除 `workspace_access_token.capabilities` 与 `resource_scope`；新版本不再返回或读取这两项旧策略。
- migration 对现有有效、过期和已吊销 Token subject 都补 member，并将既有更高角色强制归一为 member；吊销只使凭据无效，不删除 membership 或已拥有资源。
- Token hard delete 只删除 Token 行并暂时保留 subject/member 和资源；随后 owner/admin 可通过成员删除流程显式清理该身份及其资源。
- 普通 member 当前能看到的公共 Workspace 数据，service user 也能看到；这是用户选择该模型后接受的权限语义。

## 关键设计决定

### 1. 身份与授权

```text
Owner 创建 DTA Token
        ↓ 同一事务
service user + workspace_member(role=member) + token + audit
        ↓
Bearer dta_ → Token 生命周期校验 → X-User-ID=subject
        ↓
RequireWorkspaceMember → 原生 member context
        ↓
现有 Agent / Skill / Runtime / Trace / Autopilot / DingTalk handlers
```

Token 类型继续用于禁止交互登录、吊销、过期、审计和唯一 Workspace 绑定；业务授权不再读取 Token capability。

### 2. 数据迁移与发布窗口

- 新增 fork-owned migration，幂等向 `workspace_member` 插入每个 Token 的 `(workspace_id, subject_user_id, role=member)`；冲突时更新 role 为 member，修复既有错误角色。
- 创建 Token 的事务同步写 membership，避免应用已升级但 migration 之后创建的身份缺成员。
- 同一 migration 删除 `workspace_access_token.capabilities` 和 `resource_scope`；依附于两列的 CHECK 约束随列删除。
- 预发尚无真实使用方，不建设旧 capability 请求/响应兼容。
- 因数据库迁移先于应用滚动且旧 binary 会读取该列，发布前先关闭 `workspace_access_tokens` feature flag，迁移完成并滚动到新 binary 后再开启。

### 3. Runtime 与任务 Token

- `POST /api/runtimes/fc-e2b` 对 Workspace member 开放；普通成员只允许 stable channel 且服务端强制 private，owner 为调用者。
- Workspace owner/admin 继续可以创建 public Runtime；candidate 仍要求 stable publisher。
- 普通成员不能通过 PATCH 把 FC/E2B Runtime 切为 public，但可以重命名、删除自己的 Runtime。
- 领取任务继续生成绑定 Agent/Task/Workspace/Runtime owner 的 `mat_`；Runtime owner 是正式 member，因此无需修改 Workspace task-token middleware。
- 沙箱永不注入长期 `dta_`，只注入现有短期 `mat_`。

### 4. Token 管理 UI/API

- 保留 create/list/get/update/regenerate/revoke/delete、自省和审计 API；活动 Token 先吊销，已吊销记录才可硬删除，删除不级联 service member 或业务资源。
- create/update schema 删除可配置 capability；旧客户端携带字段时忽略，不能改变权限。
- 设置页删除权限卡片和权限说明，明确“拥有工作区普通成员权限，可创建自有 stable/private Runtime，不具备管理员与发布权限”。
- Token 明文仍只在创建/重新生成响应展示一次。
- 成员管理接口拒绝把 service member 提升为 admin/owner，也拒绝 service member 主动离开；凭据停用继续走 Token 吊销/删除。
- service member 仍有关联 Token 行时禁止从成员管理移除；Token 已硬删除后允许 owner/admin 使用原生成员删除流程清理身份及其资源，避免永久幽灵成员。
- 钉钉机器人 install/revoke/retry/status 路由允许普通 member 进入，Handler 对齐 Lark 按 Agent 二次授权：Agent owner 或 Workspace owner/admin 可操作，其他成员拒绝。

## 被排除的方案

- **继续 capability + allowlist 或保留废弃列兼容旧 Token**：功能面扩张时维护成本和遗漏风险持续增长，且当前没有旧 DTA Token 使用方，已被用户否决。
- **新增 service_member 角色**：仍会形成第二套权限矩阵，不能实现“与其他普通用户一样”。
- **无附加治理地让普通成员创建 candidate/public managed Runtime**：会扩大模板发布和全 Workspace 基础设施影响面；candidate 继续要求 publisher，public 继续要求 owner/admin。
- **把 DTA Token 换成 human PAT**：无法独立吊销、审计和保持不可登录身份，并可能继承管理员权限。
- **把长期 dta_ 注入沙箱**：扩大凭据泄漏半径；继续使用任务绑定的 `mat_`。

## 复杂度与执行路由

- 规划复杂度：P3。
- 执行复杂度：E1。
- 判断依据：身份授权边界、数据库迁移、滚动兼容、UI/API 合同和沙箱任务链同时变化，但修改强耦合，主 Agent 串行实现更安全。
- 主执行者：当前主 Agent。
- Subagent 数量与职责：0；用户未要求并行代理，且认证、迁移、API、UI共享同一模型。
- Review 安排：主 Agent 对 migration、认证默认行为、登录隔离、member 越权和滚动回滚做专项自检。
- Worktree：需要；用户明确要求不在旧分支开发，本次新建独立 worktree。
- Worktree 来源与清理责任：本次任务创建；完成后默认保留，未经用户授权不删除。
- 分支策略：从 `codex/workspace-access-grants@e716729b0` 新建 `codex/dta-service-member`。
- Commit 策略：验证通过后由主 Agent 自主创建本地原子 commit；push、部署继续单独确认。

## 文件与职责

- `server/migrations/269_*`：幂等补齐 service user membership，并删除 capability/resource-scope 列；down 仅为本地恢复重建列及约束。
- `server/pkg/db/queries/workspace_access_token.sql` 与 sqlc 生成物：Token 创建事务增加 membership，生命周期查询保持。
- `server/internal/middleware/auth.go`、`workspace.go`、`workspace_access_principal.go`：把 `dta_` 接入普通 member 上下文，移除 operation allowlist/capability gate，保留 Token 生命周期与审计来源。
- `server/internal/handler/workspace_access.go`：API schema 删除 capability 编辑，创建 subject/member/token 原子化。
- `server/internal/handler/agent*.go`、`skill.go`、DingTalk handlers：删除仅为 DTA principal 存在的过滤/ownership 分支，复用普通 member 行为。
- `server/cmd/server/router.go`：移除 DTA operation gate 接线但保留 owner-only Token 管理和 feature flag。
- `packages/core/**/workspace-access*`：删除 capability schema/type/mutation 字段，保持兼容解析。
- `packages/views/settings/components/workspace-access-tab.tsx` 与 locales/docs：删除权限配置，说明普通成员权限与自有 stable/private Runtime 边界。
- 测试：认证/member、Token 生命周期、原生 Agent ownership、stable/private Runtime 创建与 candidate/public deny、Autopilot/snapshot/Trace/绑定、`mat_` CLI 与登录隔离。

## 实施步骤

- [x] 里程碑一：从已推送基线建立新分支/worktree，迁移历史计划并记录替代架构。
- [x] 里程碑二：补 migration 与 Token 创建事务，确保新旧 subject 都是普通 member。
- [x] 里程碑三：移除 capability/allowlist 执行路径，让 `dta_` 走原生 member middleware，并锁定 admin/login 隔离。
- [x] 里程碑四：删除 Handler 的 DTA 专属资源过滤，验收 Agent、Skill、公共 Runtime、Trace、snapshot、Autopilot 和钉钉绑定的原生成员行为。
- [x] 里程碑五：收敛 Core/UI/docs 合同，删除权限编辑并补普通成员/公共 Runtime 说明。
- [x] 里程碑六：运行数据库、Go、TypeScript、Docs 和安全回归，更新最终证据并决定本地 commit。
- [x] 里程碑七：开放普通成员创建自有 stable/private FC/E2B Runtime，锁定 candidate/public 边界并完成追加验证。

## 执行记录

| 里程碑 | 状态 | 关联 Commit（可选） | 实际验证命令 | 结果与证据 |
|---|---|---|---|---|
| 计划确认与环境准备 | 已完成 |  | `git worktree add -b codex/dta-service-member ... e716729b0`；分支/状态核对 | 新 worktree 位于记录路径；旧 worktree 未提交代码未被复制或修改 |
| 数据与身份 | 已完成 |  | migration 269 down/up/replay；SQL schema/member readback；Token 生命周期测试 | 旧列为 0；所有 Token subject 都存在且 role=`member`；创建、重新生成、吊销、硬删除均通过 |
| 认证与原生权限 | 已完成 |  | `go test -p 1` 精准回归；`go vet` | 原生 member context、跨 workspace、owner deny、登录隔离、成员角色/移除不变量均通过 |
| 原生业务能力 | 已完成 |  | Runtime、snapshot、Trace、Autopilot、DingTalk、load-smoke、claim/mat 定点测试 | 公共 Runtime 可使用、他人私有 Runtime 拒绝；任务领取可铸造短期 Token；节律与调试链路通过 |
| Core/UI/docs | 已完成 |  | Core Vitest 79 项；DingTalk Views Vitest 27 项；Core/Views/Docs typecheck | schema 不再暴露权限字段；普通 Agent owner 可见机器人管理入口；设置页与文档合同通过 |
| 最终验证 | 已完成 |  | 连接 migration 269 测试库的 Go 定向回归；目标包 `go vet`；`git diff --check`；修改 Go 文件 gofmt | 普通成员路由、Agent ownership、Token/成员生命周期均通过；无 whitespace/格式问题 |
| 普通成员 Runtime 创建 | 已完成 |  | 创建/visibility 红绿回归；Views Vitest 48 项；Core Vitest 84 项；Views/Core/Docs typecheck；目标 Go 包测试与 `go vet` | member stable 创建 201、owner 为调用者且强制 private；非 publisher candidate 403；member public PATCH 403；admin public 保持成功 |

## 验证策略

| 改动或验收项 | 风险 | 验证方式 | 是否测试先行 |
|---|---|---|---|
| 现有 Token membership migration | 重复 membership、错误管理员角色、迁移重放 | 隔离 PostgreSQL down/up/replay/readback；验证既有 Token subject 强制归一为 member | 是 |
| 新 Token 原子创建 member | 半成品身份、并发重复 | Handler 生命周期测试与事务失败回滚 | 是 |
| 普通 member 授权 | capability 残留拒绝或意外 admin | middleware/router 集成测试；member/admin deny 对照 | 是 |
| 登录隔离 | service user 获得 Web/PAT 登录 | `/api/me`、PAT/owner API、成员管理拒绝测试 | 是 |
| 原生资源面 | Agent/Autopilot/Trace/绑定行为漂移 | 使用相同 member 与 dta_ subject 的对照测试 | 是 |
| Runtime | 普通成员被错误拒绝，或借创建/更新越权为 candidate/public | member stable/private 创建成功；candidate/public 创建与 public PATCH 拒绝；owner/admin 既有路径通过 | 是 |
| 沙箱 CLI | 长期 Token 泄漏或 `mat_` 失效 | claim/mint/inject/auth/Workspace 现有测试 + service member fixture | 是 |
| UI/API | 旧 capability 仍可编辑或响应解析失败 | Core schema tests、Views tests/typecheck、docs build | 否 |
| 旧策略列删除 | 残留 SQL/API/UI 读取导致运行失败 | migration schema readback、sqlc、全仓精准残留扫描 | 是 |

## 风险与回滚

- 最大产品风险是 service user 获得普通成员可见的全部 Workspace 数据，不再是最小权限；这是本次明确选择，设置页必须提示。
- 最大安全风险是误把 membership 等同于可交互登录；认证测试必须证明 `principal_type=workspace_access_token` 不能走 human JWT/PAT 签发路径。
- migration 会把 Token subject 的既有 owner/admin 错误角色强制归一为 member；普通 human membership 不受影响。
- 发现越权或登录隔离失败时关闭 `workspace_access_tokens` feature flag；数据库 member 行可保留，不存在有效凭据时无法登录。
- 删除列后旧 binary 不兼容；预发回滚必须先保持 feature flag 关闭并执行 migration down 恢复 capability 列，不能只回滚应用。

## 系统边界与职责

- Workspace Owner 管理 Token 生命周期；普通成员管理自己创建的 stable/private Runtime，管理员治理 public Runtime 和发布面。
- Multica 原生 member/ownership 是业务授权权威；Token 表只负责机器凭据生命周期、Workspace 绑定和审计。
- DTA 保持上层交付编排，本轮不修改；沙箱继续使用 `mat_`。

## 关键数据流与状态归属

```text
Owner → service user/member/token → PostgreSQL
DTA HTTPS → dta_ 生命周期校验 → 原生 member middleware → 业务 Handler
任务 claim → Runtime owner(service member) → mat_ → 沙箱 Multica CLI → task-bound API
```

## 接口与兼容性

- owner-only Token URL 保持不变，降低 DTA/设置页迁移成本。
- capability/resource-scope 请求字段和响应字段直接删除；旧请求不是兼容目标。
- profile/self 兼容接口保持，外部用户仍不需要进入 Multica。
- 普通 member 的 API 可见面即 DTA Token 的 API 可见面；不再承诺服务端 DTA-only allowlist。

## 数据迁移与幂等

- 正式发布 rebase 后使用唯一编号 266–269；预发曾应用旧 stem 257–260，因此 266/267/268/269 的 up migration 必须允许在旧 schema 上幂等重放并记录新 stem，不能再次创建表、列或放宽资源范围。
- `INSERT ... SELECT ... ON CONFLICT DO UPDATE SET role='member'` 补 membership 并修复 Token subject 的错误管理员角色；只影响 `workspace_access_token.subject_user_id`。
- migration 删除 capability/resource-scope 列时使用 `DROP COLUMN IF EXISTS`，可重放且完整 filename stem 稳定；down 不删除 member，避免误删随后产生资源的身份，但会为本地/预发回滚重建两列和原约束。

## 失败模式与恢复

- Token 已创建但 member 写入失败：事务整体回滚，不返回明文。
- 旧 Token migration 前请求：发布流程先执行 migration，再滚动应用；不存在临时非成员窗口。
- Token 吊销：下一请求 401；subject/member 与资源保留，重新创建新 Token 不自动接管旧资源。
- stable channel 未初始化或 Runtime 创建失败：DTA 部署返回明确错误，不回退到 candidate/public 创建。

## 并发与一致性

- Token 创建沿用单事务；membership 唯一约束负责并发去重。
- regenerate/version CAS、过期和吊销语义保持不变。
- member 写入口额外锁定 service member：只能保持 role=`member`，有关联 Token 时不能由管理员移除，也不能主动离开；Token 硬删除后管理员可显式清理。

## 可观测性

- 审计继续记录 token ID、subject、workspace、method、path、result 和 request ID；capability 字段不再作为授权证据。
- 任务审计继续以 `mat_` 的 Agent/Task/Workspace 绑定为准。

## 发布策略与功能开关

- migration 前先关闭 `workspace_access_tokens`；migration 先行，应用再滚动，验证新版本后重新开启。
- 预发需验证现有 Token 被补 member、设置页无权限配置、自有 stable/private Runtime 创建成功、candidate/public 拒绝、节律/snapshot/Trace 正常。
- 本轮完成后仅创建本地 commit；push和预发部署等待用户另行授权。

## 回滚步骤与触发条件

- 越权、可交互登录、跨 Workspace 或 Runtime 管理放宽：立即关闭 feature flag 并回滚应用。
- 回滚新应用时先关闭 feature flag，再执行 migration down 恢复 capability 列，最后滚动旧应用；membership 保留不删除。

## 发布边界

- Push：本地 commit 后告知“已提交，准备 push”；用户明确同意后执行。
- PR、合并、部署、发布：当前均未授权。

## 计划变更记录

| 日期 | 变更 | 原因 | 是否重新确认 |
|---|---|---|---|
| 2026-08-03 | 从 capability/allowlist 改为不可登录 service user + 普通 member；管理员预建公共 Runtime | 用户认为细粒度权限引入过多跨功能缺口，并明确选择普通成员模型与预建 Runtime | 是，用户已明确要求新分支迁移计划并继续实现 |
| 2026-08-03 | 不保留旧 capability 数据/API/滚动兼容，migration 直接删除列 | 用户明确当前没有旧 DTA Token 使用方，兼容没有价值 | 是，用户已明确要求继续 |
| 2026-08-03 | service member 角色与 membership 设为不可变；migration 归一错误管理员角色 | 用户确认 service member 必须始终是非管理员，不能被成员接口提升或移除 | 是，用户已明确允许增加 |
| 2026-08-03 | 暂不限制外部服务商直接使用 Token/CLI | 当前优先目标是降低耦合并先跑通完整交付链路 | 是，用户明确要求先不管 |
| 2026-08-03 | Token 删除后允许管理员清理无 Token 的 service member；机器人安装改为 member 路由 + Agent ownership | 评审发现无清理出口和普通 service member 机器人交付能力退化；用户明确要求普通用户可绑定机器人 | 是，用户已明确要求修改 |
| 2026-08-03 | 普通成员可创建自有 stable/private FC/E2B Runtime；candidate/public 和发布面保持受限 | 用户确认普通成员也需要创建 Runtime，并接受前一轮推荐边界 | 是，用户明确回复“加” |
| 2026-08-04 | rebase 到最新正式基线，并把 Workspace Access migration 从预发历史编号 257–260 调整为 266–269 | 最新正式基线已占用 257–265；直接发布存在重复 migration 前缀和 Runtime/数字员工绑定代码冲突 | 是，用户明确要求 rebase 并更新迁移编号 |

## 正式发布准备（2026-08-04）

- [x] 从 `origin/develop@d839e6c38` rebase 19 个功能提交，并保留 rebase 前本地备份分支 `codex/dta-service-member-pre-rebase-20260804`。
- [x] 解决数字员工绑定、Runtime 和 Router subscription 测试冲突；以最新正式账号键、ASB Runtime 与绑定权限模型为基线，只重放普通成员/DTA service member 增量。
- [x] 将 migration 调整为 `266_workspace_access_token`、`267_workspace_access_native_ownership`、`268_dta_load_smoke_operation_idempotency`、`269_workspace_access_service_member`。
- [x] 验证新 migration 在全新正式 schema 与已执行旧 257–260 的预发 schema 上均可安全执行。
- [x] 运行 migration lint、认证/成员、Runtime、数字员工绑定/Router、Core/Views 类型检查与目标测试。
- [x] 完成最终 diff 审核和正式发布风险记录；本地提交后等待 force-with-lease push 的单独授权。

## 调试假设记录

| 假设 | 验证方式 | 结果 |
|---|---|---|
| 幽灵成员由 hard delete 保留 membership 且 `DeleteMember` 无条件 409 共同造成 | 对照 `DeleteWorkspaceAccessToken`、`DeleteMember` 与生命周期测试 | 已证实；Token 行删除后没有任何可达清理路径 |
| 普通成员历史上可直接进入钉钉机器人安装路由 | 检查引入提交 `2b23679e6d`、基线 `e716729b0` 和当前 router | 否；历史路由一直是 owner/admin，旧 DTA Token 依靠中间件提前放行 |
| 将机器人路由直接降为 member 即可安全恢复 | 对照 DingTalk 与 Lark Handler 的 Agent ownership 校验 | 否；DingTalk 当前只校验 Agent 属于 Workspace，必须补 `canManageAgent`、session Agent ownership 和孤儿安装 admin fallback |

## 最终验证结果

- 基于 `origin/develop@d839e6c38` 完成 19 个功能提交的 rebase；数字员工绑定、Runtime 与 Router 冲突按最新正式实现合并，未保留冲突标记。
- 隔离 PostgreSQL 双路径验证通过：全新正式 schema 可直接执行 266–269；已执行旧 257–260 的预发 schema 可幂等继续执行 266–269。两条路径最终均无 `capabilities`、`resource_scope` 列，并保留唯一 load-smoke operation 索引。
- 隔离 PostgreSQL migration 269 down/up/replay 成功；`capabilities`、`resource_scope` 列数量为 0，`token_subject_non_member=0`。
- Token 生命周期、原生 member middleware、workspace 绑定、过期/吊销、human JWT/PAT 隔离通过；service member 在 Token 存在时不可提升或移除，Token 硬删除后可由管理员通过原生成员清理流程删除。
- 普通成员可使用 public Runtime，不能使用他人 private Runtime；本次追加允许创建自有 stable/private Runtime，任务 claim、workspace context 和短期任务 Token 链路保持通过。
- Trace 映射、Agent task snapshot、Autopilot 权限、DingTalk 原生绑定、DTA load-smoke 通过；普通 Agent owner 可安装、重试、轮询和解绑自己的钉钉机器人，其他普通成员被拒绝。
- DingTalk Views Vitest 27 项、Core Vitest 79 项通过；Core、Views、Docs typecheck 通过；目标 Go 包 `go vet` 通过。
- Runtime 追加验证通过：普通 member stable 创建成功并归属调用者，显式 public 输入被强制 private；非 publisher candidate 和 member public PATCH 被拒绝，owner/admin public 创建与修改保持成功。
- 发布前新鲜回归通过：认证、成员、Handler、Server、CLI 目标 Go 测试通过，Router package 全量测试通过，目标 Go 包 `go vet` 通过；Core typecheck + 89 项 Vitest、Views typecheck + 51 项 Vitest、Docs typecheck 通过。
- 后端 `internal/handler` 整包测试仍存在本分支外的 dispatch/onboarding/GitHub 等既有失败；本次修改涉及的定点回归全部通过，未借此改动无关代码。

### 最终工作区

- 原始工作区与分支：`/Users/fanqi/test/code/ding-fde-agent/.worktrees/dt-fde-multica-workspace-access-grants`，`codex/workspace-access-grants`
- 最终工作区与分支：`/Users/fanqi/test/code/ding-fde-agent/.worktrees/dt-fde-multica-dta-service-member`，`codex/dta-service-member`
- 交付状态：最新正式基线 rebase、migration 266–269 重编号与发布前回归完成；提交号以 Git 元数据为准
- Worktree 收尾：保留
- 当前未提交改动：本计划更新时尚待创建 migration 重编号与发布准备提交
- 未执行的验证：DTA 仓库端到端联调与预发部署，均不在本轮授权范围内

## 遗留风险

- DTA 仓库尚未对新 Token 做真实联调；本轮只保证 Multica 合同。
- 普通 member 可见面可能随 Multica 原生权限演进而扩大；未来若重新要求细粒度授权，应设计标准 RBAC role，而不是恢复逐路由 allowlist。
- 正式环境当前未配置 `FF_WORKSPACE_ACCESS_TOKENS`；合并与部署代码不会自动启用 DTA Token 页面和认证入口，需在 migration 与应用验证完成后单独开启。
