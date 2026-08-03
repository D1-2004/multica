# 工作区 DTA Token 权限与 Trace 实施计划

> 工作流：grill-and-plan
> 状态：已完成（仅 Multica 范围）
> 创建日期：2026-07-31
> 计划 ID：20260731-workspace-access-grant-dta-trace
> 最后更新时间：2026-08-03 CST
> 当前分支：`codex/workspace-access-grants`
> 目标执行分支：`codex/workspace-access-grants`
> 基线 Commit：`origin/develop@596ed393fd1db31f4a83765a7ca686df200fc215`
> 已有实现 Commit：`df47612fa`、`0c8c4ab97`、`3ef44ea57`、`a1df2accf`、`f42726e66`、`fab9a8fc5`、`c682a7b8d`、`dd44505cc`、`f8e47238b`、`7a95c20d6`
> 原始工作区：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`
> Worktree 路径：`/Users/fanqi/test/code/ding-fde-agent/.worktrees/dt-fde-multica-workspace-access-grants`
> Worktree 来源：本任务于 2026-08-02 创建
> DTA 工作区：本轮不修改；曾创建的 `codex/workspace-access-profile` 工作树已恢复为干净状态
> 交付状态：截至 `7a95c20d6` 已推送并部署预发；本次绑定能力纠正已获授权提交、推送并部署预发，执行中；生产未发布
> 收尾状态：保留中
> 当前里程碑：机器人与数字员工绑定基础能力已补齐并完成本地验证

## 一句话结论

Multica 由工作区 `owner` 直接创建 DTA Token；第二阶段让该 `dta_` 机器身份复用 Multica 的具名 profile/CLI 输出合同，但不冒充 human PAT，并补齐 GitHub Source 与专用 load-smoke，使 DTA 保留现有 plan → apply → 独立回读 → Receipt 主链。

## 背景与现状证据

- `df47612fa` 已实现 Grant→多 Token、三个 capability、`own_agents/workspace`、Agent/Skill ownership、Trace、默认拒绝 operation gate、owner UI、审计和 feature flag。
- 用户进一步明确：真实使用方彼此不同，应该各自拥有不同权限；短暂中断可接受，不需要一个授权下多把密钥做无损轮换。
- 因此 Grant 与 Token 分离没有业务价值，反而会让权限主体和实际使用方错位。
- 初始实现时分支尚未发布，migration 257 可在隔离数据库直接收敛；当前截至 `7a95c20d6` 已推送并部署预发，后续变更按追加提交和重新部署处理。

## 目标

1. 一个 DTA Token 对应一个外部使用方和一个不可登录的内部 owner 主体。
2. Token 自身保存 `deployment.manage`、`deployment.retire`、`trace.read`、有效期、状态和版本；不再配置独立资源范围。
3. Workspace Owner 可以创建、查看、改权、改有效期、重新生成和吊销 Token。
4. 重新生成原地替换密钥 Hash：旧密钥下一请求立即 401，新密钥继承原 Token ID、内部主体、权限和已有 Agent ownership。
5. 吊销是终态；已吊销 Token 不可恢复或重新生成。需要新外部身份时创建新 Token。
6. 保留已经实现的 Agent/Skill/Runtime/Trace 权限边界和真实 Trace 返回。
7. UI 平铺展示 DTA Token，不出现 Grant、多 Token 或 Multica CLI 概念。
8. `dta_` 可以被 DTA 托管为具名 Multica profile；外部用户仍只使用 DTA，不进入 Multica 产品界面。
9. profile 的 `auth status`、`workspace list/get` 对机器身份返回稳定兼容输出，不开放人类 `/api/me` 或成员身份。
10. `deployment.manage` 覆盖现有 DTA GitHub Source 的只读 installation/repository、preview/create、source readback/sync 合同，并继续执行 workspace/ownership 校验。
11. 以 DTA 专用 load-smoke API 替代对外开放通用 Issue/Comment 权限；部署验证可用 `deployment.manage`，普通 Trace 仍要求 `trace.read`。
12. 本轮只完成 Multica 的身份、权限和专用传输合同；DTA 如何接入留到后续独立任务。
13. Token 固定绑定一个 Workspace；可管理资源由其稳定 `subject_user_id` 按 Multica 原生 ownership 决定，不允许额外放宽到 Workspace 内其他 owner 的资源。
14. 机器人安装和已有数字员工绑定不新增独立 capability；所有有效 `dta_` Token 默认可用，但只能查看和操作其 subject 自己拥有的 Agent 绑定。

## 非目标

- 不把 `dta_` 转换成 `mul_` PAT，不复用管理员 Cookie/PAT，不让 Token subject 成为 workspace member/admin/owner。
- 不把 Multica Web/Desktop/通用 CLI 暴露为服务商产品入口；DTA 内部可以使用托管 profile 调用兼容 CLI。
- 不为重新生成提供零停机双密钥窗口；旧密钥立即失效是已确认行为。
- 不让一个 Token 代表多个不同权限的外部使用方；不同人或不同权限创建不同 Token。
- 不新增业务数据脱敏、External Trace 投影、Grant 级 WebSocket 或通用 IAM。
- 不修改、提交或推送 DTA；不在本轮验证 DTA Receipt 或部署编排。
- 不新增机器人、数字员工或其他消息渠道的独立 capability 开关；本轮只复用 Multica 已有钉钉机器人安装与数字员工绑定能力，不扩展新的渠道类型。
- 不开 PR、不合并、不部署生产或开启生产 feature flag；本次仅按用户授权提交、推送当前分支并重新部署预发。

## 已确认需求

- Token 的语义与 Multica 原生 Token 接近，但可由 Workspace Owner 配置权限、有效期并吊销。
- Token 本身就是实际使用方的身份与权限，不再引入独立 Grant。
- 不同使用方分别创建 Token，权限互不关联。
- 允许重新生成造成 DTA 短暂中断。
- 用户对“改”的确认包括上一轮推荐：重新生成保留稳定内部 owner，因此仍能管理此前创建的 Agent。
- 只有 human workspace `owner` 可以管理 DTA Token；admin/member/Token 本身均不能。
- 用户确认复用 Multica profile 合同而非 human 登录态；profile 中保存的仍是 `dta_`，权限继续由服务端 Token policy 执行。
- 用户最终收窄本轮范围为只改 Multica；DTA 适配、Receipt 和跨仓库联调后置。
- 用户澄清“这版先不加机器人/数字员工权限”是指暂不提供独立权限控制，而不是禁止 Token 使用；因此所有有效 Token 默认拥有绑定能力，且继续受绑定 Workspace 与原生 Agent ownership 约束。

## 执行假设

- `user.principal_type` 改为 `workspace_access_token`；旧 `workspace_access_grant` 值无需兼容，因为功能未发布且隔离库会重建。
- `workspace_access_token.id` 是稳定授权身份；随机 `dta_` 明文只是可替换凭证。
- Token 创建时事务内创建不可登录 subject user 和 Token，不创建 workspace member。
- Token `PATCH` 使用 `version` 乐观锁；重新生成也使用 `version`，避免并发生成两个只有最后一个有效的密钥。
- 已过期但未吊销 Token 可以由 owner 重新生成并设置新的到期时间；已吊销 Token 不能。
- feature flag 从未发布的 `workspace_access_grants` 同步改名为 `workspace_access_tokens`。
- Multica CLI 可通过 token 前缀识别机器 profile；自省和 workspace 兼容数据来自 `/api/workspace-access/self`，不信任本地缓存作为授权依据。
- Token 明文只写入权限为 `0600` 的 DTA 托管 profile；每个远端请求仍实时校验数据库中的 capability、ownership、版本、有效期和吊销状态。
- GitHub installation 只能读取 workspace 已有授权，不允许 DTA Token 创建、复用或修改 installation。
- load-smoke 是部署验收子资源，不授予通用 Issue/Comment 能力；创建、重试和回读都绑定目标 Agent 与 Token ownership。

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
├─ version
├─ expires_at / last_used_at
├─ created_by / updated_by
├─ revoked_by / revoked_at
└─ created_at / updated_at
```

删除 `workspace_access_grant`。审计表只关联 `workspace_id`、`token_id` 和 actor，不再出现 `grant_id`。

### 2. 创建、改权、重新生成和吊销

- 创建：`subject user + token + audit` 同一事务；响应唯一一次返回明文。
- 改权：修改 Token 行的 capability/有效期并增加 version；现有密钥下一请求使用新策略。
- 重新生成：生成新随机值，CAS 替换 Hash/prefix、清空 last-used、更新有效期并增加 version；响应唯一一次返回新明文。
- 吊销：写 `revoked_at/revoked_by`，下一请求 401；终态不可恢复。
- 创建响应丢失：只能对该 Token 执行重新生成；允许旧密钥已失效造成中断。

### 3. 权限和原生 ownership

| capability | 允许 operation | 资源边界 |
|---|---|---|
| 基础能力（不要求特定 capability） | `GET /api/workspace-access/self`；钉钉机器人 installation 查询/创建/状态/撤销/重试；已有数字员工 binding 查询/创建/surface/解绑 | 自省只返回当前 Token；绑定能力只返回和操作 subject owner 的 Agent 资源 |
| `deployment.manage` | Agent list/create/get/update/restore/source/sync/skill assignment；Skill list/create/search/get/update/delete/files；`GET /api/runtimes` | 仅 subject owner/creator 的 Agent、Skill；Runtime 复用原生 public/owner 规则 |
| `deployment.retire` | `POST /api/agents/{id}/archive` | 只归档 subject owner 的 Agent |
| `trace.read` | `GET /api/agents/{id}/tasks`、`GET /api/tasks/{taskId}/messages` | task→agent 后校验 Workspace 与 Agent owner |

其他现有或未来路由默认拒绝。Token 不获得 member/admin/owner 角色。机器人与数字员工绑定是所有有效 Token 的基础能力，不由 `deployment.manage`、`deployment.retire` 或 `trace.read` 控制。

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
- PATCH 修改 name/capability/expires_at，并携带 version。
- DELETE 吊销，不物理删除 Token、subject、Agent、Skill 或 Trace。

### 5. UI

Workspace 设置页显示平铺的“DTA Token”：

- 创建时配置使用方名称、三个权限和有效期。
- 每张卡直接显示权限、prefix、到期、最后使用和吊销状态。
- 支持编辑、重新生成和吊销；重新生成明确提示旧密钥立即失效。
- 新密钥只展示一次，且不进入 Query/Mutation cache。

### 6. DTA 托管 profile 兼容

- profile 仍保存 `server_url`、`workspace_id` 和 token；token 类型为 `dta_`。
- CLI 登录/配置只在 DTA 托管路径接受 `dta_`，通过 `/api/workspace-access/self` 验证并写入绑定 workspace。
- `auth status` 为兼容 DTA 当前 parser 继续输出 `User / Server / Token`，其中 User 是稳定的 Token principal 描述，不是 human user。
- `workspace list/get` 对 `dta_` 只返回自省绑定的唯一 workspace；无法枚举其他 workspace。
- regenerate 后 Token ID/subject 不变，profile 更新明文后继续管理原 owner 资源。

### 7. GitHub Source 与 load-smoke

- `deployment.manage` 允许读取 workspace 已授权的 GitHub installations/repositories、执行 agent preview/create、source readback/sync；handler 必须按 Token workspace 和 agent ownership 复核。
- installation 的创建、复用、授权变更保持 human-only。
- 新增 DTA load-smoke create/resolve/runs/messages/comments/retry 合同，服务端内部完成 Issue/task 编排并只返回部署验收所需状态和证据；`marker` 是可重放、可恢复的 operation ID。
- Multica 提供隐藏的内部 CLI 传输命令；DTA 何时切换该接缝不属于本轮。

## 被排除的方案

- **Grant→多 Token**：实际使用方不同，权限应该落在各 Token，不需要无损轮换。
- **重新生成创建新身份**：会改变 subject user，导致 Token 无法继续管理旧 Agent。
- **吊销后恢复**：违背吊销终态；临时继续使用应在吊销前改权或重新生成。
- **把权限编码进明文 Token**：改权不能立即生效；继续每请求从 PostgreSQL 读取。
- **把 `dta_` 兑换成管理员或 subject 的 `mul_` PAT**：会绕过 capability/ownership 并混淆机器与 human 身份。
- **Token 额外配置 `own_agents/workspace`**：重复 Multica 原生 owner/admin 体系，并会给机器身份增加绕过 owner 的跨资源管理权；已删除。
- **在 DTA 重写一套直接 HTTP Multica Provider**：会复制 CLI schema、错误处理、profile、回读与 GitHub/load-smoke 编排，改造和长期漂移成本最大。
- **给 DTA Token 开放通用 Issue/Comment API**：权限面超过部署验收需要；采用专用 load-smoke 子资源。

## 复杂度与执行路由

- 规划复杂度：P3；机器身份、CLI 合同、GitHub 授权面和部署验收 API 相互关联。
- 执行复杂度：E1；Multica allowlist、handler 与 CLI 串行实现和验证。
- Subagent：0；不并行写入。
- Review：主 Agent 聚焦检查 migration、认证默认拒绝、重新生成 CAS 和明文缓存。
- Worktree：Multica 继续已有 worktree；DTA 工作树不修改，保护用户未跟踪 `.qoder/`。
- 分支：Multica 继续 `codex/workspace-access-grants`，不改写已有历史。

## 文件与职责

- `server/migrations/257_workspace_access_token.*.sql`：未发布 migration 原地收敛为 Token + audit。
- `server/migrations/259_dta_load_smoke_operation_idempotency.*.sql`：以 Workspace＋Token＋Agent＋marker 建立 load-smoke 永久唯一约束。
- `server/pkg/db/queries/workspace_access_token.sql`：单层 Token CRUD、认证、regenerate、revoke 和 audit。
- `server/internal/middleware/auth.go`、`workspace_access_principal.go`：去除 GrantID，策略直接来自 Token。
- `server/internal/handler/workspace_access.go`：平铺 Token 管理 API。
- `server/internal/handler/agent*.go`、`skill.go`、`runtime.go`、`daemon.go`：保留 subject ownership/Trace 行为。
- `server/cmd/server/router.go`：改为 owner-only `/access-tokens` API。
- `packages/core/**/workspace-access*`：更新类型、schema、client、queries/mutations。
- `packages/views/settings/components/workspace-access-tab.tsx`：平铺 Token UI。
- locales/docs/feature flag 文档：删除 Grant 和多 Token 语义。
- `server/cmd/multica/cmd_auth.go`、`cmd_login.go`、`cmd_workspace.go`：DTA 托管 profile、自省和唯一 workspace 兼容。
- `server/internal/handler/workspace_access.go`：自省返回绑定 workspace 展示数据和稳定 principal。
- `server/internal/middleware/workspace_access_principal.go`、GitHub/source handler：补齐受控 GitHub Source operation 与 ownership 校验。
- Multica load-smoke handler/router/CLI：提供 DTA 专用创建、状态和必要重试合同，不开放通用 Issue 面。
- `server/internal/middleware/workspace_access_principal.go`、DingTalk install/account-binding handler：把已有机器人和数字员工绑定路由作为有效 Token 的基础能力开放，并按 subject owner 过滤和校验 Agent 资源。

## 实施步骤

- [x] 里程碑一：重写 migration/sqlc 与 Token 生命周期测试。
- [x] 里程碑二：改造认证 Principal、owner-only API、自省、审计和 feature flag。
- [x] 里程碑三：回归 Agent/Skill/Runtime/Trace scope，证明 regenerate 保留 ownership。
- [x] 里程碑四：改造 Core 合同与 Web/Desktop 平铺 Token UI。
- [x] 里程碑五：更新文档，执行 migration、Go、TS、构建与安全验收。
- [x] 里程碑六：实现 `dta_` 托管 profile、自省和唯一 Workspace CLI 兼容，并用失败回归测试锁定 human 隔离。
- [x] 里程碑七：补齐 GitHub Source allowlist，保持 installation 管理默认拒绝并增加专项测试。
- [x] 里程碑八：实现 DTA 专用 load-smoke API/隐藏 CLI，保持通用 Issue/Comment 默认拒绝。
- [x] 里程碑九：运行 Multica CLI、handler、middleware、router 和 Go 全量回归；DTA 联调明确后置。
- [x] 里程碑十：删除产品/API 中的 `own_agents/workspace` 资源范围，迁移已有宽权限值，统一按原生 ownership 验证。
- [x] 里程碑十一：把 load-smoke marker 收敛为幂等 operation key，补齐并发唯一约束与按 marker 恢复接口。
- [x] 里程碑十二：纠正绑定权限语义，开放机器人/数字员工绑定基础能力并补齐 Token Agent ownership 过滤。

## 调试假设记录

| 假设 | 验证方式 | 结果 |
|---|---|---|
| H1：`marker` 只写 metadata，未参与查重或唯一约束，因此响应丢失后重放会创建第二个 Issue | 检查 `CreateDTALoadSmoke`、`IssueService.Create` 与 router | 已确认：`AllowDuplicate:true` 显式跳过 active duplicate guard，且只有 `issueId` 子资源读取接口 |
| H2：只改为 `AllowDuplicate:false` 就足够 | 对照通用 Issue duplicate key | 已否定：通用 key 只按 Workspace/project/parent/title 且只约束 active Issue，无法表达 Token＋Agent＋marker 的永久 operation identity |
| H3：需要 metadata 查询、数据库唯一约束和恢复 API 三者同时存在 | 增加重放、并发、payload 冲突、跨 Token 与恢复回归测试 | 已确认：同 operation 同 payload 返回原 Issue；不同 payload 409；并发只创建一条；跨 Token 404；GET 可按 Agent＋marker 恢复 |

## 执行记录

| 里程碑 | 状态 | 关联 Commit | 实际验证命令 | 结果与证据 |
|---|---|---|---|---|
| 已废弃 Grant+多 Token 初版 | 已提交 | `df47612fa` | 见 Git commit | 作为重构基线保留，不 amend、不 rebase |
| 单层 Token 改造 | 已完成 | `0c8c4ab97` | `make sqlc`；专项 Go + 隔离 PostgreSQL；`go vet`；`pnpm typecheck`；Core 精确测试；Docs build；257 down/up | Token 独立权限、动态改权、regenerate、吊销、ownership/scope/Trace 均通过；TS 类型通过；Core 79/79；Docs 157 页构建通过 |
| 预发开关注入 | 已完成 | `3ef44ea57` | `bash -n src/main.sh`；trait guarded replacement；pipeline/health/config 回读 | 运行时白名单包含 `FF_WORKSPACE_ACCESS_TOKENS`；预发 trait 98→99 个唯一 key，其他项不变；`/api/config` 返回 true |
| 预发 UI 反馈修复 | 已完成 | `fab9a8fc5` | Views typecheck；目标 ESLint；locale JSON；diff check | Select 使用全宽约束；权限、资源范围和有效期补齐详细说明；真实浏览器像素效果待下一次预发部署复验 |
| 第二阶段 Multica 机器身份交付面 | 已完成 | `c682a7b8d` | `go test ./cmd/multica ./internal/handler ./internal/middleware ./cmd/server`；`go test ./...`；专项 profile/load-smoke 测试 | profile、GitHub allowlist、专用 smoke 均通过；全量测试仅命中既有 `pkg/agent` 72ms 时序测试失败，专项复跑可稳定复现且与本改动无关；DTA 工作树干净 |
| 原生 ownership 收敛 | 已提交 | `dd44505cc` | Workspace Access 精确 Go 测试；middleware/CLI 精确测试；`go vet`；Core 80 tests；全量 TS typecheck；Docs build；migration lint/up/readback | UI/API/self 删除资源范围；旧 `resource_scope=workspace` 请求无法放宽；Agent/Skill 按 owner/creator，Runtime 按原生 public/owner；258 将预发已有宽权限值归一为 `own_agents`；当时把“暂不单独控制绑定权限”错误理解为不开放，已由里程碑十二纠正 |
| load-smoke operation 修复 | 已完成 | 本次实现提交 | handler 专项连续 3 次；middleware/CLI/router/migrate 测试；`go vet`；migration 259 up/index readback/唯一冲突探针 | 同 payload 重放与终态重放返回原 Issue；不同 payload 409；并发只创建一条；Token 隔离；按 marker 恢复；数据库永久唯一约束均通过 |
| 机器人与数字员工绑定基础能力 | 已完成，待交付 | 本次实现提交 | middleware auth/allowlist；handler ownership/list；DingTalk session；router 专项；`go vet`；Views typecheck；Docs build | trace-only Token 可进入绑定路由；机器人和数字员工列表只返回 subject owner Agent；own Agent 可绑定，其他 owner 拒绝；human 更新/解绑回归通过；不新增 capability |

## 验证策略

| 验收项 | 验证 |
|---|---|
| Token 创建 | subject+Token+audit 原子写；明文单次返回 |
| 独立权限 | 两个 Token 配置不同 capability，允许/拒绝互不影响 |
| 动态改权 | 同一密钥在 PATCH 前后下一请求权限立即变化 |
| 重新生成 | 旧密钥立即 401，新密钥成功；Token ID、subject、Agent owner 不变 |
| 并发生成 | version CAS，过期 version 返回 409 |
| 吊销 | 下一请求 401，不能 regenerate/恢复 |
| Human 隔离 | subject 不能 JWT/PAT 登录，不是 member |
| Trace | owner/非 owner、跨 Workspace、分页和真实 payload 对照 |
| 前端密钥 | 明文不进入 Query/Mutation data，只进一次性对话框状态 |
| 回归 | handler 全包、Go vet、TS workspace typecheck、Core 测试、Docs build |
| profile 兼容 | `dta_` 登录/状态/workspace list/get 专项；证明 `/api/me` 和 human PAT 仍拒绝 |
| GitHub Source | installation/repository/preview/create/source sync/readback allowlist 与跨 Workspace、非 owner 拒绝测试 |
| load-smoke | DTA 专用 create/resolve/runs/messages/comments/retry；server-stamped metadata、Token/Agent ownership；operation 幂等、payload 冲突、并发唯一、终态重放、跨 Token 拒绝；普通 Issue/Comment 仍拒绝 |
| 机器人与数字员工绑定 | 不要求特定 capability 的有效 Token 可调用既有钉钉 installation/account-binding 合同；列表只含 subject owner Agent，创建/状态/surface/解绑/撤销/重试拒绝其他 owner 与跨 Workspace Agent；human 行为不变 |
| Multica 回归 | Go/CLI/handler/middleware/router 专项 + `go test ./...`，记录无关基线失败 |

## 数据迁移与回滚

- 257 已在预发应用，保留 legacy `resource_scope` 列以兼容滚动窗口；新代码不再读取或返回该字段。
- 新增 258，仅把已存在的 `workspace` 值归一为 `own_agents`；生产尚无 Token 数据，但同一迁移可安全处理预发已有数据。
- 生产不存在旧 Grant 数据，不建设 Grant→Token 数据迁移。
- 发布仍由默认关闭的 `workspace_access_tokens` feature flag 保护。
- 回滚优先关闭 flag；不删除 subject、Token、Agent 或 Trace。down migration 只允许空表环境。
- 第二阶段不新增持久表；migration 259 只增加 load-smoke operation 部分唯一索引。关闭 `workspace_access_tokens` flag 会同时关闭 profile、GitHub 和 load-smoke 机器身份入口。

## 系统边界与职责

- Workspace Owner 管理 Token；Multica 服务端是 capability、ownership、吊销和审计权威。
- Multica CLI 只是 DTA 内部的稳定传输适配器，不签发身份、不扩大权限。
- DTA 后续继续拥有部署事务、planId、write budget、reconcile、Receipt 和完整交付门禁；本轮不改动。
- GitHub App installation 授权仍由 human 管理；DTA Token 只消费 workspace 已批准的 installation。

## 关键数据流与状态归属

```text
Owner 创建/改权/吊销 dta_ → PostgreSQL Token policy
         ↓ 一次性配置
DTA 托管 0600 profile → Multica CLI → Bearer dta_
         ↓
Auth 每请求加载 Token policy → workspace/operation/ownership gate
         ↓
Agent/Skill/Source 或 DTA load-smoke → DTA 独立回读 → Receipt
```

profile 只保存连接信息与当前明文密钥；授权状态、Token ID、subject、Agent ownership、source SHA、smoke 状态和审计都由服务端数据库或 DTA Receipt 各自持有。

## 接口与兼容性

- 保持 DTA 现有 CLI stdout 解析合同；机器身份以兼容的 `User / Server / Token` 文本出现。
- human PAT/JWT 行为不变；`dta_` 不开放 `/api/me`、成员列表或 workspace 枚举。
- 新 load-smoke API/CLI 为加法；旧 human 部署链仍可继续使用通用 Issue smoke，DTA Token 后续可切换专用 API。

## 失败模式与恢复

- Token 吊销/过期/重新生成未更新 profile：下一请求明确失败；更新同一 profile 后按稳定 subject 恢复。
- GitHub installation 不可见：返回可识别的授权缺口，不 fallback 到 direct snapshot。
- load-smoke 超时或创建响应丢失：调用 `GET /api/dta/load-smokes?agent_id=<id>&marker=<operation>` 恢复原 Issue；重复 POST 同 payload 也返回原 Issue，不创建第二个 smoke。
- 新后端与旧 CLI：旧 CLI 明确拒绝 `dta_`，不会误当 human；升级兼容 CLI 后才启用 DTA profile。

## 并发与一致性

- Token policy 每请求读取，regenerate/version CAS 和现有 Agent ownership 不变。
- load-smoke 的 marker 由调用方生成；`(workspace, token, agent, marker)` 是永久 operation identity。相同 payload 重放返回同一 Issue（首次 201、重放 200），不同 `required_skills` 返回 409；稳定 operation title 负责事务内串行化，migration 259 唯一索引兜底终态与并发竞态。
- source create/sync 沿用现有服务端事务与 DTA frozen SHA/readback，不增加双写。

## 可观测性

- workspace access audit 记录 profile、GitHub 和 load-smoke operation 的 capability/result/request ID。
- 后续 DTA Receipt 应记录 principalHash、workspace、source SHA、smoke operation 与最终证据；本轮 Multica 不持久化 Token 明文。

## 发布策略与功能开关

- 所有第二阶段机器身份能力继续受 `workspace_access_tokens` 控制；先完成 Multica 专项与全量回归，再请求 push/预发授权。
- 新 Multica CLI 必须随服务端兼容代码一起进入预发；DTA 使用该 CLI 做真实 profile/smoke 验证后才可称 ready。

## 回滚步骤与触发条件

- 发现越权、跨 workspace、human 混淆或审计缺失时立即关闭 `workspace_access_tokens`。
- CLI 兼容故障可回滚 Multica CLI 使用版本，不影响 human PAT；服务端数据不删除。
- load-smoke 故障保持部署为 `verifying/unverified`，不得降级绕过门禁。

## 发布边界

- 初始实现阶段只允许当前 worktree 内修改、测试和本地 commit；用户随后明确授权提交、push 和部署预发。
- 已创建 Aone CR `35392614` 并部署 pipeline 66；未授权生产发布、正式流水线、代码合并或 DTA 仓库修改。
- 用户最终要求本轮只修改 Multica；新 commit、push、PR、合并、预发和生产动作仍按独立边界处理。

## 计划变更记录

| 日期 | 变更 | 原因 | 是否重新确认 |
|---|---|---|---|
| 2026-07-31 | 初始 Grant 计划 | 用户要求 Multica 支持 DTA 权限与 Trace | 是 |
| 2026-08-02 | 完成 Grant→多 Token 初版 `df47612fa` | 当时采用稳定授权与多凭证模型 | 是 |
| 2026-08-02 | 改为一个 Token 一份身份和权限；原地 regenerate；删除 Grant 层 | 用户明确不同实际使用方应有不同权限，并接受重新生成导致中断；随后指示“改” | 是 |
| 2026-08-02 | 推送并部署预发；补齐 release flag 运行时白名单；预发 trait 开启功能 | 用户明确要求提交、推送、部署预发，并纠正环境变量应通过 trait 后重新部署 | 是 |
| 2026-08-02 | 修复资源范围 Select 溢出，并增强权限、范围和有效期说明 | 用户在预发截图中确认布局异常、权限说明过弱且有效期缺少字段名 | 是 |
| 2026-08-02 | 增加 DTA 托管 profile、GitHub Source 完整合同和专用 load-smoke；允许最小 DTA 接缝 | 用户确认不改用 `mul_` PAT，要求按推荐方案更新计划并完成 | 是，已确认 |
| 2026-08-02 | 收窄为只完成 Multica；撤销 DTA 工作树内全部未提交适配 | 用户明确“dta你不用管” | 是，已确认 |
| 2026-08-02 | 删除 `own_agents/workspace` 可配置范围，统一按 Multica 原生 ownership；当时把“本版不加机器人/数字员工绑定权限”理解为完全不开放 | 用户后续澄清其原意是暂不做独立权限开关，而不是禁止使用 | 已由 2026-08-03 决策替代 |
| 2026-08-02 | 将 load-smoke marker 升级为幂等 operation key，增加永久唯一索引与按 marker 恢复 | Review 指出 `AllowDuplicate:true`、缺少 operation 回读以及计划承诺互相矛盾 | 是，按 review 修复 |
| 2026-08-03 | 机器人和已有数字员工绑定改为所有有效 Token 默认可用，不新增独立 capability，并保持 subject Agent ownership | 用户澄清“先不加权限”表示暂不控制而不是全部禁止，并明确要求更新计划和代码 | 是，已确认并执行 |

## 最终验证结果

| 验证项 | 命令/证据 | 结果 |
|---|---|---|
| sqlc 与 diff | `make sqlc`；`git diff --check` | 通过；生成代码一致，无空白错误 |
| 真实数据库专项 | 显式 `source .env.worktree` 后运行 middleware/handler `^TestWorkspaceAccess` | 通过；未被 TestMain 静默跳过 |
| Token 生命周期 | `TestWorkspaceAccessTokenLifecycleAndRegeneration` | 旧密钥 regenerate 后 401；新密钥成功；ID、subject、Agent owner 不变；stale version 409；吊销后 401 且不可 regenerate |
| 独立与动态权限 | `TestWorkspaceAccessTokensHaveIndependentDynamicPolicies` | 两个 Token 策略互不影响；同一密钥 PATCH 后下一请求立即使用新权限 |
| ownership 与 Trace | `TestWorkspaceAccessNativeOwnership`、`TestWorkspaceAccessTraceOwnershipAndPagination` | own/foreign owner、公开/私有 Runtime、跨空间拒绝、分页与真实 payload 均通过 |
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
| `dta_` profile | `TestWorkspaceAccessProfileUsesSelfWithoutHumanEndpoints` | login/status/workspace list/get 只调用 `/api/workspace-access/self`；不触达 `/api/me` 或 workspace 枚举 |
| GitHub Source allowlist | `TestWorkspaceAccessCapabilityForRequest` | installation/repository/preview/create 归入 `deployment.manage`；connect/reuse/delete installation 继续拒绝 |
| 专用 load-smoke | `TestDTALoadSmokeIsServerStampedAndTokenScoped`、`TestDTALoadSmokeCLIUsesDedicatedAPI` | 服务端固定 prompt/metadata；Token ID/Agent ownership 与 marker 注入拒绝通过；通用 Issue/Comment 未开放 |
| load-smoke operation 幂等 | handler 专项 `-count=3`；middleware/CLI/router/migrate 测试；migration 259 index readback 与重复插入探针；`go vet` | 首次 201、同 payload/终态重放 200 且同 Issue、不同 payload 409、并发唯一、跨 Token 404、Agent＋marker 恢复及数据库唯一冲突均通过 |
| 删除资源范围 | API/Core/UI 残留扫描；旧字段请求回归；migration 258 up/readback | 产品合同不再出现 `resource_scope`；旧客户端字段被忽略且不能放宽；数据库宽权限值为 0 |
| 绑定路由基础权限 | `TestWorkspaceAccessCapabilityForRequest`、`TestWorkspaceAccessAuthAllowsDingTalkBindingWithoutDeploymentCapability` | installation/account-binding 精确路由允许；未知 method/path 继续默认拒绝；只有 `trace.read` 的 Token 也能通过认证进入 Handler |
| 绑定资源边界 | account-binding owned/foreign/list 测试、installation list filter、registration session AgentID 回读 | own Agent 创建成功；foreign Agent 的 begin/surface/unbind 返回 403；机器人与数字员工列表只含 own Agent；跨副本 status 可按持久化 AgentID 复核 ownership |
| 绑定 human 回归 | 既有 surface chat/auto、unbind 事件测试 | human 请求不增加 Token 专用 ownership 查询，原有成功行为保持 |
| 绑定说明 | locale JSON parse、Views typecheck、Docs build | 中英文设置说明和认证文档明确绑定是所有有效 Token 的基础能力；Views typecheck 通过；Docs 157 页构建通过 |
| 第二阶段 Go 静态与回归 | `go vet ./cmd/multica ./cmd/server ./internal/handler ./internal/middleware`；`go test` 同四包 `-count=1` | 全部通过 |
| 第二阶段全量 Go | `go test ./...` | 除 `pkg/agent.TestCodexExecuteSemanticInactivityAllowsContinuousMessages` 外全部通过；该测试单独复跑仍在未修改包内以 72ms no-progress timeout 失败 |
| DTA 范围 | DTA worktree `git status --short` | 空；本轮未修改 DTA |

未通过但不归因于本改造的仓库基线验证：

- 未把全量 `go test ./...` 宣称为通过；唯一失败是未修改的 `server/pkg/agent` 72ms Codex 时序测试，单独复跑同样失败。
- 本轮扩大到整个 handler 包时还命中共享测试库中的无关基线/残留数据失败（dispatch 断言、重复唯一键等）；本次 Workspace Access 精确测试在迁移恢复后的隔离数据库中全部通过。
- 本轮运行 `cmd/server` 全包时，未配置 readiness 健康依赖导致 `/readyz`、`/healthz` 期望 200 实际 503；本次修改覆盖的 DingTalk callback/router 专项单独通过。
- 仓库 `migrate down` 会连续回退全部迁移而非只回退最新一条，在隔离测试库回退到 133 时命中既有唯一索引冲突；随后已重新 `migrate up` 到 258 并确认宽权限值为 0。

### 最终工作区

- 原始工作区用户修改：`docs/plans/2026-07-29-fde-start-dta-managed-source.md`，不得触碰。
- 当前 worktree：本任务创建并保留。
- 当前分支已推送至 `7a95c20d6`；本次机器人/数字员工绑定改动与计划收尾待提交、推送和预发部署。
- 当前 worktree 由本任务创建并保留；本轮不创建代码 MR、不合并、不发布生产，未修改 DTA。

## 遗留风险

- DTA 仍需后续接入新 Token 管理合同并隐藏 Multica 概念。
- 重新生成会立即中断仍使用旧密钥的 DTA，这是用户接受的产品语义。
- Trace fidelity、REST 轮询和永久 Token 风险维持原边界。
- 本次只部署到预发；pipeline 停在人工预发验证门禁。
- 本次机器人/数字员工绑定基础能力已完成本地实现与验证，待本轮提交并部署预发；生产仍是旧的“全部拒绝”行为。
- 一次预发数据库验证命令的解析错误把 `DATABASE_URL` 完整连接串回显到了工具输出；需轮换预发数据库凭据，生产凭据未读取。
