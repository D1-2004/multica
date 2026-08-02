# 工作区 DTA Token 权限与 Trace 实施计划

> 工作流：grill-and-plan
> 状态：已完成
> 创建日期：2026-07-31
> 计划 ID：20260731-workspace-access-grant-dta-trace
> 最后更新时间：2026-08-02 18:36 CST
> 当前分支：`codex/workspace-access-grants`
> 目标执行分支：`codex/workspace-access-grants`
> 基线 Commit：`origin/develop@596ed393fd1db31f4a83765a7ca686df200fc215`
> 已有实现 Commit：`df47612fa`、`0c8c4ab97`、`3ef44ea57`
> 原始工作区：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`
> Worktree 路径：`/Users/fanqi/test/code/ding-fde-agent/.worktrees/dt-fde-multica-workspace-access-grants`
> Worktree 来源：本任务于 2026-08-02 创建
> 交付状态：实现、推送和预发部署完成；生产未发布
> 收尾状态：保留中
> 当前里程碑：单层 Token 改造已完成

## 一句话结论

Multica 改为由工作区 `owner` 直接创建 DTA Token：每个 Token 就是一名外部使用方的稳定身份、权限和资源范围；不同使用方使用不同 Token，重新生成只替换该 Token 的密钥并立即废弃旧密钥，同时保留其内部 owner 和已有 Agent。

## 背景与现状证据

- `df47612fa` 已实现 Grant→多 Token、三个 capability、`own_agents/workspace`、Agent/Skill ownership、Trace、默认拒绝 operation gate、owner UI、审计和 feature flag。
- 用户进一步明确：真实使用方彼此不同，应该各自拥有不同权限；短暂中断可接受，不需要一个授权下多把密钥做无损轮换。
- 因此 Grant 与 Token 分离没有业务价值，反而会让权限主体和实际使用方错位。
- 当前分支尚未 push、发布或部署，migration 257 只在本任务隔离数据库使用，可以直接改写而无需生产兼容迁移。

## 目标

1. 一个 DTA Token 对应一个外部使用方和一个不可登录的内部 owner 主体。
2. Token 自身保存 `deployment.manage`、`deployment.retire`、`trace.read`、`own_agents/workspace`、有效期、状态和版本。
3. Workspace Owner 可以创建、查看、改权、改 scope、改有效期、重新生成和吊销 Token。
4. 重新生成原地替换密钥 Hash：旧密钥下一请求立即 401，新密钥继承原 Token ID、内部主体、权限和已有 Agent ownership。
5. 吊销是终态；已吊销 Token 不可恢复或重新生成。需要新外部身份时创建新 Token。
6. 保留已经实现的 Agent/Skill/Runtime/Trace 权限边界和真实 Trace 返回。
7. UI 平铺展示 DTA Token，不出现 Grant、多 Token 或 Multica CLI 概念。

## 非目标

- 不修改 DTA 仓库，不让外部服务商登录或使用 Multica Web/Desktop/CLI。
- 不为重新生成提供零停机双密钥窗口；旧密钥立即失效是已确认行为。
- 不让一个 Token 代表多个不同权限的外部使用方；不同人或不同权限创建不同 Token。
- 不新增业务数据脱敏、External Trace 投影、Grant 级 WebSocket 或通用 IAM。
- 不 push、开 PR、合并、部署或开启生产 feature flag。

## 已确认需求

- Token 的语义与 Multica 原生 Token 接近，但可由 Workspace Owner 配置权限、scope、有效期并吊销。
- Token 本身就是实际使用方的身份与权限，不再引入独立 Grant。
- 不同使用方分别创建 Token，权限互不关联。
- 允许重新生成造成 DTA 短暂中断。
- 用户对“改”的确认包括上一轮推荐：重新生成保留稳定内部 owner，因此仍能管理此前创建的 Agent。
- 只有 human workspace `owner` 可以管理 DTA Token；admin/member/Token 本身均不能。

## 执行假设

- `user.principal_type` 改为 `workspace_access_token`；旧 `workspace_access_grant` 值无需兼容，因为功能未发布且隔离库会重建。
- `workspace_access_token.id` 是稳定授权身份；随机 `dta_` 明文只是可替换凭证。
- Token 创建时事务内创建不可登录 subject user 和 Token，不创建 workspace member。
- Token `PATCH` 使用 `version` 乐观锁；重新生成也使用 `version`，避免并发生成两个只有最后一个有效的密钥。
- 已过期但未吊销 Token 可以由 owner 重新生成并设置新的到期时间；已吊销 Token 不能。
- feature flag 从未发布的 `workspace_access_grants` 同步改名为 `workspace_access_tokens`。

## 关键设计决定

### 1. 单层 Token 数据模型

```text
workspace_access_token
├─ id                  稳定外部身份
├─ workspace_id
├─ subject_user_id     → user.id → agent.owner_id / skill.created_by
├─ name
├─ token_hash          当前密钥 Hash
├─ token_prefix
├─ capabilities
├─ resource_scope
├─ version
├─ expires_at / last_used_at
├─ created_by / updated_by
├─ revoked_by / revoked_at
└─ created_at / updated_at
```

删除 `workspace_access_grant`。审计表只关联 `workspace_id`、`token_id` 和 actor，不再出现 `grant_id`。

### 2. 创建、改权、重新生成和吊销

- 创建：`subject user + token + audit` 同一事务；响应唯一一次返回明文。
- 改权：修改 Token 行的 capability/scope/有效期并增加 version；现有密钥下一请求使用新策略。
- 重新生成：生成新随机值，CAS 替换 Hash/prefix、清空 last-used、更新有效期并增加 version；响应唯一一次返回新明文。
- 吊销：写 `revoked_at/revoked_by`，下一请求 401；终态不可恢复。
- 创建响应丢失：只能对该 Token 执行重新生成；允许旧密钥已失效造成中断。

### 3. 权限和资源范围

| capability | 允许 operation | scope |
|---|---|---|
| 无 | `GET /api/workspace-access/self` | 只返回当前 Token 自省 |
| `deployment.manage` | Agent list/create/get/update/restore/source/sync/skill assignment；Skill list/create/search/get/update/delete/files；`GET /api/runtimes` | `own_agents` 只操作 subject 拥有资源；`workspace` 只在这些 operation 内放宽 |
| `deployment.retire` | `POST /api/agents/{id}/archive` | 只归档 scope 内 Agent |
| `trace.read` | `GET /api/agents/{id}/tasks`、`GET /api/tasks/{taskId}/messages` | task→agent 后校验 workspace/owner scope |

其他现有或未来路由默认拒绝。Token 不获得 member/admin/owner 角色。

### 4. Owner-only 管理 API

```http
POST   /api/workspaces/{workspaceId}/access-tokens
GET    /api/workspaces/{workspaceId}/access-tokens
GET    /api/workspaces/{workspaceId}/access-tokens/{tokenId}
PATCH  /api/workspaces/{workspaceId}/access-tokens/{tokenId}
POST   /api/workspaces/{workspaceId}/access-tokens/{tokenId}/regenerate
DELETE /api/workspaces/{workspaceId}/access-tokens/{tokenId}
```

- POST 和 regenerate 是仅有的明文响应。
- list/get/patch 永不返回 Hash 或明文。
- PATCH 修改 name/capability/scope/expires_at，并携带 version。
- DELETE 吊销，不物理删除 Token、subject、Agent、Skill 或 Trace。

### 5. UI

Workspace 设置页显示平铺的“DTA Token”：

- 创建时配置使用方名称、三个权限、scope 和有效期。
- 每张卡直接显示权限、scope、prefix、到期、最后使用和吊销状态。
- 支持编辑、重新生成和吊销；重新生成明确提示旧密钥立即失效。
- 新密钥只展示一次，且不进入 Query/Mutation cache。

## 被排除的方案

- **Grant→多 Token**：实际使用方不同，权限应该落在各 Token，不需要无损轮换。
- **重新生成创建新身份**：会改变 subject user，导致 `own_agents` 无法继续管理旧 Agent。
- **吊销后恢复**：违背吊销终态；临时继续使用应在吊销前改权或重新生成。
- **把权限编码进明文 Token**：改权不能立即生效；继续每请求从 PostgreSQL 读取。

## 复杂度与执行路由

- 规划复杂度：P3；数据模型、公共 API、认证主体和迁移发生变化。
- 执行复杂度：E1；修改强耦合于同一 Principal/Token policy，由主 Agent 连续实施。
- Subagent：0；不并行写入。
- Review：主 Agent 聚焦检查 migration、认证默认拒绝、重新生成 CAS 和明文缓存。
- Worktree：继续使用本任务已有 worktree，不新增。
- 分支：继续 `codex/workspace-access-grants`；不改写已有 commit，完成后按需要创建后续本地 commit。

## 文件与职责

- `server/migrations/257_workspace_access_token.*.sql`：未发布 migration 原地收敛为 Token + audit。
- `server/pkg/db/queries/workspace_access_token.sql`：单层 Token CRUD、认证、regenerate、revoke 和 audit。
- `server/internal/middleware/auth.go`、`workspace_access_principal.go`：去除 GrantID，策略直接来自 Token。
- `server/internal/handler/workspace_access.go`：平铺 Token 管理 API。
- `server/internal/handler/agent*.go`、`skill.go`、`runtime.go`、`daemon.go`：保留 subject ownership/scope/Trace 行为。
- `server/cmd/server/router.go`：改为 owner-only `/access-tokens` API。
- `packages/core/**/workspace-access*`：更新类型、schema、client、queries/mutations。
- `packages/views/settings/components/workspace-access-tab.tsx`：平铺 Token UI。
- locales/docs/feature flag 文档：删除 Grant 和多 Token 语义。

## 实施步骤

- [x] 里程碑一：重写 migration/sqlc 与 Token 生命周期测试。
- [x] 里程碑二：改造认证 Principal、owner-only API、自省、审计和 feature flag。
- [x] 里程碑三：回归 Agent/Skill/Runtime/Trace scope，证明 regenerate 保留 ownership。
- [x] 里程碑四：改造 Core 合同与 Web/Desktop 平铺 Token UI。
- [x] 里程碑五：更新文档，执行 migration、Go、TS、构建与安全验收。

## 执行记录

| 里程碑 | 状态 | 关联 Commit | 实际验证命令 | 结果与证据 |
|---|---|---|---|---|
| 已废弃 Grant+多 Token 初版 | 已提交 | `df47612fa` | 见 Git commit | 作为重构基线保留，不 amend、不 rebase |
| 单层 Token 改造 | 已完成 | `0c8c4ab97` | `make sqlc`；专项 Go + 隔离 PostgreSQL；`go vet`；`pnpm typecheck`；Core 精确测试；Docs build；257 down/up | Token 独立权限、动态改权、regenerate、吊销、ownership/scope/Trace 均通过；TS 类型通过；Core 79/79；Docs 157 页构建通过 |
| 预发开关注入 | 已完成 | `3ef44ea57` | `bash -n src/main.sh`；trait guarded replacement；pipeline/health/config 回读 | 运行时白名单包含 `FF_WORKSPACE_ACCESS_TOKENS`；预发 trait 98→99 个唯一 key，其他项不变；`/api/config` 返回 true |
| 预发 UI 反馈修复 | 已完成，待推送复验 | 本次实现提交 | Views typecheck；目标 ESLint；locale JSON；diff check | Select 使用全宽约束；权限、资源范围和有效期补齐详细说明；真实浏览器像素效果待下一次预发部署复验 |

## 验证策略

| 验收项 | 验证 |
|---|---|
| Token 创建 | subject+Token+audit 原子写；明文单次返回 |
| 独立权限 | 两个 Token 配置不同 capability/scope，允许/拒绝互不影响 |
| 动态改权 | 同一密钥在 PATCH 前后下一请求权限立即变化 |
| 重新生成 | 旧密钥立即 401，新密钥成功；Token ID、subject、Agent owner 不变 |
| 并发生成 | version CAS，过期 version 返回 409 |
| 吊销 | 下一请求 401，不能 regenerate/恢复 |
| Human 隔离 | subject 不能 JWT/PAT 登录，不是 member |
| Trace | own/workspace、跨 Agent/Workspace、分页和真实 payload 对照 |
| 前端密钥 | 明文不进入 Query/Mutation data，只进一次性对话框状态 |
| 回归 | handler 全包、Go vet、TS workspace typecheck、Core 测试、Docs build |

## 数据迁移与回滚

- 257 尚未发布，直接改写；本任务隔离数据库删除重建后全量 migrate up 验证。
- 生产不存在旧 Grant 数据，不建设 Grant→Token 数据迁移。
- 发布仍由默认关闭的 `workspace_access_tokens` feature flag 保护。
- 回滚优先关闭 flag；不删除 subject、Token、Agent 或 Trace。down migration 只允许空表环境。

## 发布边界

- 初始实现阶段只允许当前 worktree 内修改、测试和本地 commit；用户随后明确授权提交、push 和部署预发。
- 已创建 Aone CR `35392614` 并部署 pipeline 66；未授权生产发布、正式流水线、代码合并或 DTA 仓库修改。

## 计划变更记录

| 日期 | 变更 | 原因 | 是否重新确认 |
|---|---|---|---|
| 2026-07-31 | 初始 Grant 计划 | 用户要求 Multica 支持 DTA 权限与 Trace | 是 |
| 2026-08-02 | 完成 Grant→多 Token 初版 `df47612fa` | 当时采用稳定授权与多凭证模型 | 是 |
| 2026-08-02 | 改为一个 Token 一份身份和权限；原地 regenerate；删除 Grant 层 | 用户明确不同实际使用方应有不同权限，并接受重新生成导致中断；随后指示“改” | 是 |
| 2026-08-02 | 推送并部署预发；补齐 release flag 运行时白名单；预发 trait 开启功能 | 用户明确要求提交、推送、部署预发，并纠正环境变量应通过 trait 后重新部署 | 是 |
| 2026-08-02 | 修复资源范围 Select 溢出，并增强权限、范围和有效期说明 | 用户在预发截图中确认布局异常、权限说明过弱且有效期缺少字段名 | 是 |

## 最终验证结果

| 验证项 | 命令/证据 | 结果 |
|---|---|---|
| sqlc 与 diff | `make sqlc`；`git diff --check` | 通过；生成代码一致，无空白错误 |
| 真实数据库专项 | 显式 `source .env.worktree` 后运行 middleware/handler `^TestWorkspaceAccess` | 通过；未被 TestMain 静默跳过 |
| Token 生命周期 | `TestWorkspaceAccessTokenLifecycleAndRegeneration` | 旧密钥 regenerate 后 401；新密钥成功；ID、subject、Agent owner 不变；stale version 409；吊销后 401 且不可 regenerate |
| 独立与动态权限 | `TestWorkspaceAccessTokensHaveIndependentDynamicPolicies` | 两个 Token 策略互不影响；同一密钥 PATCH 后下一请求立即使用新权限 |
| scope 与 Trace | `TestWorkspaceAccessAgentScopeMatrix`、`TestWorkspaceAccessTraceScopeAndPagination` | own/workspace、跨空间拒绝、分页与真实 payload 均通过 |
| 认证边界 | middleware WorkspaceAccess 专项、auth/featureflags 专项 | 默认关闭、operation 默认拒绝、capability、到期/吊销、JWT/PAT 人类身份隔离通过 |
| Go 静态检查 | `go vet ./internal/auth ./internal/featureflags ./internal/middleware ./internal/handler` | 通过 |
| TypeScript | `pnpm typecheck` | 6 个实际包通过 |
| Core 合同 | `pnpm -C packages/core exec vitest run api/schemas.test.ts workspace-access/queries.test.ts` | 2 files、79 tests 通过 |
| Docs | `pnpm --filter @multica/docs build` | 生产构建通过，157 个静态页面生成 |
| Migration | 隔离库全量 migrate up；257 direct down/up；schema 查询 | 通过；Grant 表不存在，Token/audit/principal_type 存在，Token 测试数据为 0 |
| 安全残留 | 搜索旧 Grant symbol/API/flag、疑似 `dta_` 明文 | 范围内无残留，无提交明文 Token |
| 预发交付 | CR `35392614`；Run `3101721096`；Deploy Order `158336422` | snapshot revision `3ef44ea57`；代码合并、构建、扫描、2/2 主机部署、集成测试成功；停在人工预发验证门禁 |
| 预发可用性 | `GET /healthz`；`GET /api/config` | HTTP 200；`feature_flags.workspace_access_tokens=true` |
| UI 反馈修复 | `pnpm --filter @multica/views typecheck`；目标 ESLint；locale JSON parse；`git diff --check` | 全部通过；预发浏览器复验需随下一次部署执行 |

未通过但不归因于本改造的仓库基线验证：

- `go test -count=1 ./internal/handler` 有 10 个失败，集中在未修改的 Agent dispatch、continuation、calendar、FDE、GitHub、共享测试数据与 issue identity 用例；Workspace Access 专项随后独立复跑通过。
- 误触发的 Core 全套 905 tests 中 897 通过、8 个失败，均为既有 `localStorage` 测试环境未提供实现；本改动的两个精确测试文件 79/79 通过。
- 未把全量 `go test ./...` 宣称为通过；仓库还存在计划前已知的 `server/pkg/agent` Codex 时序基线失败。

### 最终工作区

- 原始工作区用户修改：`docs/plans/2026-07-29-fde-start-dta-managed-source.md`，不得触碰。
- 当前 worktree：本任务创建并保留。
- 当前实现提交：`df47612fa`、`0c8c4ab97`、`3ef44ea57`；均已推送，运行中预发 snapshot 为 `3ef44ea57`。
- 当前 worktree 干净并保留；未创建代码 MR，未合并，未发布生产，未修改 DTA。

## 遗留风险

- DTA 仍需后续接入新 Token 管理合同并隐藏 Multica 概念。
- 重新生成会立即中断仍使用旧密钥的 DTA，这是用户接受的产品语义。
- Trace fidelity、REST 轮询和永久 Token 风险维持原边界。
- 本次只部署到预发；pipeline 停在人工预发验证门禁。
- 一次预发数据库验证命令的解析错误把 `DATABASE_URL` 完整连接串回显到了工具输出；需轮换预发数据库凭据，生产凭据未读取。
