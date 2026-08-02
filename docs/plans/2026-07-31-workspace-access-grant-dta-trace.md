# 工作区 Access Grant：DTA 部署与 Trace 权限实施计划

> 工作流：grill-and-plan
> 状态：已完成
> 创建日期：2026-07-31
> 计划 ID：20260731-workspace-access-grant-dta-trace
> 最后更新时间：2026-08-02 17:05 CST
> 当前分支：`codex/workspace-access-grants`
> 目标执行分支：`codex/workspace-access-grants`
> 基线 Commit：`origin/develop@596ed393fd1db31f4a83765a7ca686df200fc215`
> 原始工作区：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`
> Worktree 路径：`/Users/fanqi/test/code/ding-fde-agent/.worktrees/dt-fde-multica-workspace-access-grants`
> Worktree 来源：本任务于 2026-08-02 创建
> 交付状态：本地提交
> 收尾状态：保留中
> 当前里程碑：实现与风险匹配验收完成

## 一句话结论

Multica 首版新增工作区 Access Grant：工作区 `owner` 创建并持续管理 Grant 的 `deployment.manage`、`deployment.retire`、`trace.read` 权限、`own_agents/workspace` 资源范围、状态和 Token 生命周期；Grant 使用不可登录的内部 owner 主体复用现有智能体 `owner_id` 与 `private/public_to` 体系，外部服务商只通过 DTA 使用 Token，不登录、不感知也不使用 Multica CLI。

## 背景与现状证据

- 当前 Personal Access Token 只绑定 `user_id`，没有工作区、capability 和资源范围：`server/migrations/011_personal_access_tokens.up.sql`。
- 当前 PAT 已有 Token Hash、显示前缀、到期时间、最近使用时间和立即吊销缓存的实现模式：`server/internal/handler/personal_access_token.go`、`server/pkg/db/queries/personal_access_token.sql`。
- 当前智能体已有可复用的 ownership 与访问体系：
  - `agent.owner_id` 指向 `user.id`。
  - `permission_mode=private` 时只有智能体 owner 可触发。
  - `permission_mode=public_to` 时由 `agent_invocation_target` 控制可触发范围。
  - 工作区 `owner/admin` 可查看和管理智能体，但现有规则不允许非智能体 owner 修改 `permission_mode/invocation_targets`。
  - 证据：`server/internal/handler/agent_access.go`、`server/internal/handler/agent.go`。
- 当前 `CreateAgent` 从请求 user 写入 `owner_id`；Skill 也已有 `created_by` 用户归属，可由 Grant 的内部 owner 主体复用：`server/internal/handler/agent.go`、`server/migrations/008_structured_skills.up.sql`。
- Multica 已有执行 Trace 采集与展示，不需要重建 Trace 引擎：
  - 守护进程持久化 `thinking/text/tool_use/tool_result/error`。
  - `task_message` 保存 `seq/tool/content/input/output/created_at`。
  - `GET /api/agents/{id}/tasks`、`GET /api/tasks/{taskId}/messages`、`GET /api/agent-task-snapshot` 已存在。
  - Web/Desktop 已有 Execution Log 与 Transcript 展示。
- 当前用户侧 `GET /api/tasks/{taskId}/messages` 只检查 task 属于当前工作区，没有继续校验智能体 ownership/可见性：`server/internal/handler/daemon.go:3804`。
- 当前 `task:message` 是工作区级 WebSocket fanout，不适合作为外部 Grant 首版实时通道；首版使用带游标的 REST 轮询。
- `agent_dispatch` 已有按工作区和智能体约束 task summary/messages、并提供分页的外部观察先例：`server/internal/handler/agent_dispatch_observability.go`。
- 当前工作区在上一项功能分支 `codex/fde-start-dta-managed-source`，且存在用户自己的计划修改；本需求不能叠加实现，必须从 fresh `origin/develop` 创建独立分支/worktree。

## 目标

1. 工作区 `owner` 可以创建 DTA 访问授权，创建时设置名称、capability、资源范围、状态和 Token 有效期。
2. 工作区 `owner` 可以中途修改 Grant 的 capability、资源范围和状态，修改后下一次请求立即生效，不要求重新签发 Token。
3. 首版 capability 固定为：
   - `deployment.manage`
   - `deployment.retire`
   - `trace.read`
4. Grant 的资源范围可选：
   - `own_agents`：只操作 Grant 内部 owner 主体拥有的智能体。
   - `workspace`：在三个 capability 明确覆盖的操作内作用于整个工作区。
5. Grant 创建的智能体写入现有 `agent.owner_id`；智能体的 `private/public_to` 与 invocation target 继续走现有模型，不建设第二套智能体 ownership。
6. 一个 Grant 可签发多个 Token；Token 明文只展示一次，支持独立到期、修改到期时间、吊销和安全轮换。
7. Grant 停用后所有 Token 立即失效；恢复后尚未吊销且未过期的 Token 可继续使用。
8. Token 吊销、过期或 Grant 改权不会删除、转移或停止其智能体；只影响后续控制面请求。
9. `trace.read` 能读取目标智能体的全部真实 task 过程，包括由内部员工或最终用户触发的运行，而不是只读该 Token 发起的 task。
10. 现有 human JWT、`mul_` PAT、`mcn_` Cloud PAT、`mat_` task token、daemon token 和普通工作区成员行为保持兼容。
11. 管理界面覆盖 Web/Desktop 共享设置页，对外文案使用“DTA 访问授权”和“DTA 访问密钥”，不要求管理员理解 Grant 或 Service Principal。

## 非目标

- 不让外部服务商注册、登录或使用 Multica Web/Desktop/CLI。
- 不修改 `/Users/fanqi/test/code/dingtalk-agent`；DTA 如何保存和使用新 Token 属于后续独立改造。
- 不把管理员 PAT 交给外部服务商，不让 Grant 冒充创建它的工作区 owner。
- 不新增 Grant 到智能体、运行时、skill、issue、task 的通用资源关系表。
- 不把 Grant 做成可见的工作区 `member`，不出现在成员列表、邀请、通知或钉钉组织关系中。
- 不建设通用 IAM 权限编辑器；capability 只接受首版固定枚举。
- 不开放成员、Grant/Token 管理、账单、集成、运行时创建/删除/exec、工作区设置等未列入 capability 的能力。
- 不新增或重建 `task_trace_event`，不改变 Provider 事件采集、`tool_result` 现有长度限制或历史数据保留规则。
- 不增加手机号、姓名、订单、客户文档、工具参数、工具结果等业务数据脱敏。
- 不建设复杂的 External Trace 投影；Grant Trace 返回现有已持久化 task transcript，仅对 task summary 使用字段 allowlist，避免返回工作目录、内部配置等非执行链路字段。
- 不在本计划中删除或重写现有 task message 入库脱敏；现有持久化行为保持兼容，Trace fidelity 的进一步调整另立计划。
- 不接入现有工作区级 `task:message` WebSocket；首版由 DTA 使用 REST 轮询。
- 不 push、不提交 PR、不部署预发或正式环境；这些保留独立授权边界。

## 已确认需求

- 外部服务商完全使用 DTA，不知道后端运行的是 Multica。
- 外部服务商不使用 Multica CLI。
- 工作区管理员是内部钉钉员工；首版由工作区 `owner` 创建和管理 DTA 访问授权。
- Grant 与 Token 分离：Grant 是稳定的授权、owner 身份和策略，Token 只是可失效、可轮换的凭证。
- Grant 内部映射到稳定的不可登录 owner 主体，复用现有 `agent.owner_id` 和智能体公开/非公开体系。
- Grant 权限可在创建时配置，也可中途修改；中途修改不要求更换 Token。
- Token 可以到期、吊销；Grant 可以停用和恢复。
- 默认并推荐 `own_agents`；专属工作区或可信自动化可由 owner 显式选择高权限 `workspace`。
- `trace.read` 按智能体范围授权，能查看该智能体所有真实运行链路。
- Trace 的用户输入、智能体输出、工具参数和工具结果按现有持久化内容返回，不新增业务脱敏或通用凭据过滤层。
- 计划确认后必须从最新 `origin/develop` 拉新分支，不在当前功能分支继续开发。

## 执行假设

- Grant 的内部 owner 主体通过新增 `user.principal_type`（默认 `human`，Grant 使用 `workspace_access_grant`）区分；为满足现有外键创建不可登录 user row，但不创建 member row。
- 内部 owner user 使用系统生成、不可用于登录的唯一标识；所有 human 登录、个人 PAT、成员查询和通知路径必须拒绝或过滤 `principal_type != human`。
- Grant 请求携带自己的 Principal context；允许路径直接使用 `subject_user_id` 做 owner 比较，不伪造 human member，也不把 subject 提升为 `admin`。
- `workspace` 资源范围只在三个 capability 注册的 operation 中提供跨 owner 能力，不赋予通用工作区角色。
- 智能体 owner 决定 `permission_mode/invocation_targets` 的现有规则继续成立：
  - Grant 可修改自己拥有智能体的访问设置。
  - `workspace` scope 操作其他 owner 的智能体时，仍不允许修改这些 owner-only 访问字段。
- `deployment.manage` 允许读取可用运行时并将目标智能体绑定到同工作区运行时，但不允许创建、删除或 exec 运行时。
- `own_agents` 下，Grant 可创建和更新 `created_by=subject_user_id` 的 skill，并为自己的智能体做 assignment；可读取工作区内被允许复用的共享 skill，但不能修改其他主体创建的 skill。
- 一个 Grant 可同时存在多个 Token；安全轮换采用“签发新 Token -> DTA 切换 -> 吊销旧 Token”，不提供会在响应丢失时导致全部凭证不可恢复的单步替换。
- Token `expires_at` 可为空表示不自动过期；UI 对永久 Token 给出高风险提示。owner 可中途缩短或延长尚未吊销 Token 的到期时间。
- 首版不缓存 Grant 的有效状态、capability、scope 和 Token 状态，以 PostgreSQL 每请求读取为准；`last_used_at` 使用条件更新降低写放大。
- 当前计划只修改 Multica；端到端验收使用 HTTP 合同 fixture 模拟 DTA，不要求先改 DTA 客户端。

## 关键设计决定

### 1. Grant 是稳定授权，Token 是凭证

数据关系：

```text
workspace_access_grant 1 ── N workspace_access_token
            │
            └── subject_user_id ──> user.id ──> agent.owner_id / skill.created_by
```

`workspace_access_grant` 至少包含：

```text
id
workspace_id
subject_user_id
name
capabilities
resource_scope
status
version
created_by
updated_by
created_at
updated_at
disabled_at
```

`workspace_access_token` 至少包含：

```text
id
grant_id
name
token_hash
token_prefix
expires_at
last_used_at
created_by
created_at
revoked_by
revoked_at
```

- Token 使用不暴露 Multica 用户 PAT 的新前缀，例如 `dta_`。
- 明文只在创建响应出现一次，数据库只保存 Hash 和显示前缀。
- capability、scope 和 status 不编码进 Token，因此中途改权立即生效。
- Token 吊销不删除 Grant、subject user、智能体或 skill。
- Grant 物理删除不在首版；停用和 Token 吊销保留审计与 owner 关系。

### 2. 复用现有智能体 owner 和访问权限

Grant 创建智能体时：

```text
agent.workspace_id = grant.workspace_id
agent.owner_id     = grant.subject_user_id
```

授权判断：

```text
own_agents:
  agent.owner_id == grant.subject_user_id

workspace:
  agent.workspace_id == grant.workspace_id
  AND operation 属于 Grant capability allowlist
```

Task/Trace 不增加 `grant_id`：

```text
task.agent_id
  → agent.workspace_id
  → own_agents: agent.owner_id == subject_user_id
  → workspace: agent.workspace_id == grant.workspace_id
```

这样 Token 轮换、Grant 停用或 task 由最终用户触发，都不会破坏 Trace ownership。

### 3. capability 与资源范围是两个正交维度

首版 capability：

```text
deployment.manage
deployment.retire
trace.read
```

`deployment.manage`：

- 读取固定工作区的部署所需基础信息。
- 列出/读取 scope 内智能体和可用运行时。
- 创建智能体并写入 Grant owner。
- 更新 scope 内智能体的 Definition、模型、运行时绑定、Source 和 skill assignment。
- 创建/更新 Grant subject 创建的 skill；`workspace` scope 可按 operation allowlist 管理工作区 skill。
- 恢复已归档且仍在 scope 内的智能体。
- 不包含 archive/retire，不包含 task Trace。

`deployment.retire`：

- 归档 scope 内智能体。
- 不物理删除智能体、task、Trace、skill 或运行时。

`trace.read`：

- 列出 scope 内智能体的 task 历史。
- 读取 task 状态、耗时、失败原因和用量摘要。
- 按 `since/limit` 读取真实 `TaskMessagePayload`。
- 可读取所有触发者产生的 task，不以 Token 发起者过滤。
- 不包含 cancel、rerun、issue/comment 写入或运行时诊断。

三项可独立勾选。缺少 `trace.read` 时，即使拥有 `deployment.manage` 也不能读取 task transcript；DTA 后续应据此决定是否提供部署 smoke 或调试功能。

实际冻结并由表驱动测试覆盖的 operation 矩阵：

| capability | 允许的 operation | 资源范围规则 |
|---|---|---|
| 无 | `GET /api/workspace-access/self` | 只返回当前 Grant 自省 |
| `deployment.manage` | Agent list/create/get/update/restore/source/sync/skill assignment；Skill list/create/search/get/update/delete/files；`GET /api/runtimes` | `own_agents` 只写 Grant subject 拥有的 Agent/Skill；`workspace` 只在这些 operation 内放宽到同工作区 |
| `deployment.retire` | `POST /api/agents/{id}/archive` | 只归档 scope 内 Agent |
| `trace.read` | `GET /api/agents/{id}/tasks`、`GET /api/tasks/{taskId}/messages` | task 必须先通过 task→agent→workspace/owner scope 校验 |

Agent/Skill labels、环境变量、task cancel、issue、member、Grant 管理、运行时子路由以及未来新增的未知子路由均默认拒绝。Agent Skill 子路由使用精确路径匹配，不按前缀或路径长度宽松放行。

### 4. 工作区 owner 独占 Grant 管理权

仅 human workspace `owner` 可：

- 创建和查看 Grant。
- 修改名称、capability、resource scope 和状态。
- 签发、查看元数据、修改到期时间和吊销 Token。
- 停用或恢复 Grant。
- 查看最后使用时间和审计摘要。

`admin/member`、Grant Token、task token、daemon token 均不能管理 Grant。现有 workspace `admin` 对普通智能体的治理权限保持不变，但不会因此获得签发外部 Token 的能力。

Grant 管理 API 使用 `version` 乐观并发控制；过期版本返回 409，避免两个 owner 会话互相覆盖权限。

### 5. Grant 只能进入显式允许的 HTTP operation

新增 request Principal：

```go
type RequestPrincipal struct {
    Type          PrincipalType
    UserID        string
    GrantID       string
    TokenID       string
    WorkspaceID   string
    Capabilities  []string
    ResourceScope string
}
```

Grant Token 认证后：

1. 根据 Hash 读取 Token、Grant 和 subject user。
2. 检查 Token 未吊销、未过期，Grant 为 active。
3. 从 Grant 写入可信 workspace、capability、scope 和 subject context。
4. 拒绝客户端伪造的 user/grant/workspace actor header。
5. 只允许 router 中显式注册给 Grant 的 method/path；其他现有或未来路由默认拒绝。
6. 每个 operation 再做 capability、工作区和目标 owner/scope 校验。

Grant 不通过 `RequireWorkspaceMember`，不创建隐藏 member，也不依赖 human role 检查来获得能力。

### 6. Trace 复用现有 transcript，不新建安全投影

Grant Trace API 复用已有 `task_message` 和 `TaskMessagePayload`：

```text
task_id
issue_id
seq
type
tool
content
input
output
created_at
```

规则：

- 先根据 task 找到 agent，再执行 Grant workspace/scope/capability 判断。
- 返回数据库现有持久化内容，不新增客户数据、第三方凭据或业务字段脱敏。
- 用户输入若存放在 Chat/Issue/Comment，不自动拼入 task messages；首版 task summary 只返回已有 `trigger_summary` 等必要诊断字段。
- task summary 使用字段 allowlist，不返回 `work_dir`、内部运行时配置、系统 Prompt、Grant Token、daemon/task token 等控制面信息。
- 不改变现有 task message 入库 redaction；这是当前系统行为，不在本 Grant 计划中扩大或移除。
- 保留现有 Provider 能提供多少 `thinking` 就返回多少的语义，不承诺所有 Provider 都有完整 reasoning。
- 使用分页 REST；首版不订阅工作区 WebSocket，避免工作区 fanout 越权。

### 7. 管理 UI 不向服务商暴露 Multica 概念

Web/Desktop 共享工作区设置页新增“DTA 访问授权”：

- owner 创建授权并填写名称。
- 勾选三个 capability。
- 选择“仅自己的智能体”或“整个工作区”；后者显示高风险确认。
- 设置 Token 名称和有效期，可选择永久并确认风险。
- Token 明文只展示一次，要求复制确认后关闭。
- 中途修改 capability/scope/status。
- 签发新 Token、修改到期时间、吊销旧 Token。
- 显示 Token prefix、创建时间、到期时间、最后使用时间和状态。

外部服务商不进入该 UI；DTA 侧的 Multica 隐身属于后续 DTA 改造。

### 8. 审计与立即生效

新增轻量 `workspace_access_audit`，记录：

```text
workspace_id
grant_id
token_id
actor_user_id
action
resource_type
resource_id
result
request_id
created_at
```

- Grant 创建、改权、改 scope、停用/恢复和 Token 签发/改期/吊销在同一数据库事务内写审计。
- Grant 的业务请求由认证中间件记录 method/path、capability、Grant/Token 和结果，不记录明文 Token 或大 payload；该请求级审计是响应后的 best-effort 写入，不反向改变已经完成的业务响应。
- Trace 每页读取使用同一请求级轻量审计，拒绝发生在 handler 前时也记录 denied。
- capability/scope/status/Token 状态每请求读取 PostgreSQL；更新后无需等待本地 TTL。
- 复用现有 PAT cache invalidation 与 `last_used_at` 条件更新模式，但不复用 PAT 的用户权限语义。

## 被排除的方案

### 1. 把管理员 PAT 发给服务商

PAT 表示管理员本人，可能覆盖该用户加入的其他工作区，无法按 DTA capability 限权，也无法把外部操作与管理员操作分开审计。

### 2. 把 capability 编码进 Token

会导致中途改权必须重发 Token，并产生旧 Token 持有旧权限的窗口。Token 只保存随机密钥，实时策略以 Grant 行为准。

### 3. 为 Grant 再建智能体资源绑定表

首版“自己的智能体”可由现有 `agent.owner_id == grant.subject_user_id` 直接判定；Task/Trace 也可经 `task.agent_id` 继承，不需要重复 ownership。

### 4. 把 Grant 设成工作区 admin/member

会污染成员列表、通知、邀请和组织语义，并让普通 workspace route 获得隐式访问。Grant subject 只复用 user 外键和 owner 语义，不创建 member row。

### 5. 只在创建时固定权限

每次降权都需要换 Grant/Token，会破坏 owner 连续性。Grant 权限保存在数据库，新增中途 PATCH 与即时生效成本很低。

### 6. 单步原地轮换唯一 Token

如果新 Token 响应丢失，旧 Token 已失效会造成不可恢复中断。一个 Grant 支持多个 Token，按“先签发、切换、再吊销”完成安全轮换。

### 7. 新建复杂 External Trace 投影或业务脱敏

服务商调试自己创建和使用的智能体，需要看到真实执行链路。首版只做授权与 task summary 字段收敛，不改变 Agent transcript 内容。

### 8. 让 Grant 直接通过所有成员路由

未来新增成员 API 会自动暴露。Grant 必须由显式 method/path/operation registry 默认拒绝。

## 复杂度与执行路由

- 规划复杂度：P3
- 执行复杂度：E1
- 判断依据：涉及新认证主体、数据库迁移、公共 API、动态权限、Token 生命周期、安全默认拒绝、Web/Desktop 管理界面和多副本发布；实现强耦合于同一套 Principal/Grant policy，单一上下文更安全。
- 主执行者：主 Agent 在计划确认后于独立分支/worktree 连续实现。
- Subagent 数量与职责：0；不并行写入、不委托。
- Review 安排：E1 主 Agent 自检；实现结束后执行一次聚焦安全边界检查并使用 `verify-before-finish` 对照验收矩阵，不启动独立 reviewer。
- Worktree：需要。当前工作区处于上一项功能分支且有用户修改，本任务跨迁移、认证、后端和共享 UI，需要从 fresh `origin/develop` 隔离。
- Worktree 来源与清理责任：计划确认后由本任务创建；完成后默认保留，push、合并、清理由用户另行决定。
- 分支策略：执行前读取 Git 授权与 worktree 生命周期说明，执行 `git fetch`，从最新 `origin/develop` 创建 `codex/workspace-access-grants`；不得从当前分支分叉。
- Commit 策略：由主 Agent 结合可恢复里程碑和工作区状态自主判断；不自动 push。

## 文件与职责

### 数据库与 sqlc

- `server/migrations/<next>_workspace_access_grant.up.sql`
- `server/migrations/<next>_workspace_access_grant.down.sql`
  - 新增 `user.principal_type`。
  - 新增 `workspace_access_grant`、`workspace_access_token`、`workspace_access_audit`。
  - migration 编号以执行时 fresh `origin/develop` 为准，遵守 Aone fork 的 full filename stem 与幂等规则。
- `server/pkg/db/queries/workspace_access_grant.sql`
  - Grant CRUD、版本更新、Token 生命周期、subject 创建、认证查询、last-used 和审计。
- `server/pkg/db/queries/agent.sql`、`skill.sql`、`task_message.sql`
  - 增加 owner/workspace scope 约束查询和 Trace 分页查询。
- `server/pkg/db/generated/`
  - 通过 `make sqlc` 生成，不手改。

### 认证、Principal 与 operation gate

- `server/internal/auth/`
  - Grant Token 生成、Hash 和前缀。
- `server/internal/middleware/auth.go`
  - 识别 Grant Token，构造可信 Principal；现有 JWT/PAT/task/cloud 行为不变。
- `server/internal/middleware/principal.go`（新增）
  - Principal 类型、context accessor、human-only 与 grant-only guard。
- `server/internal/middleware/workspace_access.go`（新增）
  - Grant 状态、capability、scope 和 operation allowlist。
- `server/cmd/server/router.go`
  - 注册 owner-only 管理 API和显式 Grant operation。

### Grant 管理与现有 owner 复用

- `server/internal/handler/workspace_access.go`（新增）
  - Grant、Token、状态、改权、审计管理 API。
- `server/internal/handler/agent.go`、`agent_access.go`
  - Grant subject 的 create/owner 判断、scope override、private/public_to 调用和 owner-only 字段保护。
- `server/internal/handler/skill.go`
  - `created_by` owner 复用与 own/workspace 写边界。
- `server/internal/handler/daemon.go`
  - 修正 task messages 读取的 Agent 访问校验；Grant 使用分页读路径。
- `server/internal/handler/agent_dispatch_observability.go`
  - 复用 summary/messages 分页模式，不复用 agent-dispatch credential。

### Web/Desktop 管理界面

- `packages/core/types/`
  - Grant、capability、scope、Token 类型。
- `packages/core/api/schema.ts` 与 API client
  - zod/`parseWithFallback` 响应解析和 malformed-response 测试。
- `packages/core/workspace-access/`（新增）
  - TanStack Query keys、queries、mutations；所有 key 包含 `wsId`。
- `packages/views/settings/components/workspace-access-tab.tsx`（新增）
  - owner-only Grant/Token 管理。
- `packages/views/settings/components/settings-page.tsx`
  - 增加“DTA 访问授权”入口。
- `packages/views/locales/en/settings.json`
- `packages/views/locales/zh-Hans/settings.json`
  - 遵循术语与中文风格规范。

### 文档

- `apps/docs/content/docs/auth-tokens.mdx`
- `apps/docs/content/docs/auth-tokens.zh.mdx`
  - 区分个人 PAT 与工作区 DTA 访问密钥，说明 capability、scope、过期、停用和吊销。
- 不修改 Multica CLI 文档，不向外部服务商提供 Multica CLI 用法。

## 接口合同

### owner-only 管理 API

```http
POST   /api/workspaces/{workspaceId}/access-grants
GET    /api/workspaces/{workspaceId}/access-grants
GET    /api/workspaces/{workspaceId}/access-grants/{grantId}
PATCH  /api/workspaces/{workspaceId}/access-grants/{grantId}
POST   /api/workspaces/{workspaceId}/access-grants/{grantId}/disable
POST   /api/workspaces/{workspaceId}/access-grants/{grantId}/enable

POST   /api/workspaces/{workspaceId}/access-grants/{grantId}/tokens
GET    /api/workspaces/{workspaceId}/access-grants/{grantId}/tokens
PATCH  /api/workspaces/{workspaceId}/access-grants/{grantId}/tokens/{tokenId}
DELETE /api/workspaces/{workspaceId}/access-grants/{grantId}/tokens/{tokenId}
```

约束：

- 只有 human workspace `owner` 可调用。
- `PATCH Grant` 可修改名称、capability、resource scope；携带 `version`。
- `PATCH Token` 只修改 `expires_at`，不能恢复已吊销 Token。
- Token 创建响应是唯一返回明文的响应；list/get 永不返回 Hash 或明文。
- Grant Token 调用管理 API 固定拒绝。

### Grant 自省与业务 API

Grant Token 至少可读取自己的稳定自省：

```json
{
  "principal_type": "workspace_access_grant",
  "grant_id": "...",
  "name": "服务商 A",
  "workspace_id": "...",
  "capabilities": ["deployment.manage", "trace.read"],
  "resource_scope": "own_agents",
  "status": "active"
}
```

业务 API 复用现有 resource URL 和 payload 形状，但只有显式注册的 method/path 可由 Grant 调用。错误使用稳定 code：

```text
grant_token_invalid
grant_token_expired
grant_token_revoked
grant_disabled
grant_capability_denied
grant_workspace_mismatch
grant_resource_not_allowed
grant_operation_not_allowed
```

## 实施步骤

- [x] 里程碑一：建立独立执行环境并冻结 operation 矩阵
  - 读取计划、Git 状态、未提交 diff 和当前分支近期提交。
  - 按技能要求读取 Git/worktree 授权说明。
  - `git fetch` 后记录 fresh `origin/develop` commit。
  - 从 fresh `origin/develop` 创建 `codex/workspace-access-grants` 和独立 worktree。
  - 将本计划安全带入新 worktree，原工作区不触碰用户已有计划修改。
  - 冻结三个 capability 的 method/path/resource/read-write 矩阵和 owner/workspace scope 矩阵。

- [x] 里程碑二：建立数据库模型和 owner-only 管理 API
  - 先写 migration/handler 失败测试：非 owner、跨工作区、非法 capability/scope、明文重复读取、过期版本更新。
  - 新增 Grant/Token/subject/audit migration、查询和 sqlc 代码。
  - 创建 Grant 时事务创建不可登录 subject user，不创建 member。
  - 实现 Grant 创建、列表、改权、改 scope、停用/恢复。
  - 实现多 Token 签发、到期修改、吊销和安全轮换流程。
  - 证明改权、停用、到期和吊销在下一请求立即生效。

- [x] 里程碑三：接入 Grant Principal 和默认拒绝 operation gate
  - 增加 Grant Token auth 和可信 Principal context。
  - Grant 请求不进入通用成员授权，不允许创建 PAT 或管理 Grant。
  - 显式注册部署、retire、Trace operation；其他路由默认拒绝。
  - 证明客户端 Header、path、query 不能切换 Grant 固定工作区或 owner 身份。
  - 回归现有 human JWT/PAT、task、daemon 和 cloud auth。

- [x] 里程碑四：复用 owner 实现部署权限
  - Grant 创建智能体时写 `owner_id=subject_user_id`。
  - `own_agents` 只允许自己的智能体和 skill；`workspace` 只在 capability operation 内放宽。
  - 接通 Agent Definition、Source、模型、运行时绑定、skill 创建/更新/assignment 和 restore。
  - 保留非智能体 owner 不能修改 `permission_mode/invocation_targets` 的规则。
  - `deployment.retire` 只归档，不能删除 task/Trace/运行时。
  - 对 owned、同工作区非 owned、跨工作区三组资源做允许/拒绝对照测试。

- [x] 里程碑五：接通 `trace.read`
  - 修正普通用户 Task Transcript 的智能体可见性检查，防止同工作区私有智能体 UUID 绕过。
  - Grant task list/summary/messages 先做 workspace+agent owner/scope 校验。
  - 复用现有 `TaskMessagePayload`，增加 `since/limit` 分页，不增加业务脱敏或 External Trace 投影。
  - 证明 `trace.read` 可见目标智能体所有触发者的运行，看不到 scope 外智能体或跨工作区 task。
  - 证明缺少 `trace.read` 时部署 capability 不能读取 transcript。
  - 首版只做 REST follow；不接入工作区 WebSocket。

- [x] 里程碑六：实现 Web/Desktop owner 管理界面
  - 增加共享类型、schema、query/mutation 和设置 tab。
  - 只有 owner 显示管理入口；admin/member 无管理能力。
  - 覆盖创建时权限、scope、有效期和 Token 单次展示。
  - 覆盖中途改权、scope 高风险确认、停用/恢复、签发新 Token、改期和吊销。
  - API 响应使用 zod/`parseWithFallback`，补 malformed-response 测试。

- [x] 里程碑七：安全检查与完整验收
  - 检查 internal subject 不可登录、不出现在成员列表、不拥有个人 PAT。
  - 检查 workspace scope 没有转化成 admin/member 通用权限。
  - 检查日志、响应、审计和前端缓存不含 Token 明文/Hash。
  - 用 HTTP fixture 模拟 DTA 完成 create/update/readback/retire/Trace/吊销，不修改 DTA 仓库。
  - 运行聚焦 Go/TS 测试和风险匹配的全量验证。
  - 使用 `verify-before-finish` 对照最终验收矩阵；只有新鲜证据通过才标记完成。

## 执行记录

| 里程碑 | 状态 | 关联 Commit（可选） | 实际验证命令 | 结果与证据 |
|---|---|---|---|---|
| 独立环境与分支 | 完成 | 本次本地提交 | `git branch --show-current`、`git rev-parse HEAD origin/develop` | 分支为 `codex/workspace-access-grants`；HEAD 与 fresh `origin/develop` 均为 `596ed393...`；本任务创建的 worktree 保留 |
| Migration / SQL | 完成 | 本次本地提交 | 隔离 PostgreSQL 执行 257 down 后全量 up；最终 `psql` 表与版本回读 | `workspace_access_grant/token/audit` 均存在，schema 版本 `257_workspace_access_grant`；仓库 `make migrate-down` 依赖 Docker，本机无 Docker，改用 Homebrew PostgreSQL 16 隔离库 |
| Grant / Token / Auth | 完成 | 本次本地提交 | `go test -count=1 ./internal/auth ./internal/featureflags ./internal/middleware ./internal/handler -run 'WorkspaceAccess|ReleaseFlags'` | 通过；覆盖生命周期、动态 scope、停用/吊销下一请求、默认拒绝、拒绝审计、human JWT/PAT 隔离和 flag 默认关闭 |
| Agent / Skill / Runtime / Trace | 完成 | 本次本地提交 | `go test -count=1 ./internal/handler`、`go vet ./internal/auth ./internal/featureflags ./internal/middleware ./internal/handler` | 通过；覆盖 own/workspace scope、真实 task messages、分页和字段保真 |
| Core / Web / Desktop | 完成 | 本次本地提交 | `pnpm typecheck`；`pnpm --dir packages/core exec vitest run api/schemas.test.ts workspace-access/queries.test.ts` | 6 个 TS package typecheck 全部通过；2 个测试文件、79 个测试通过；一次性密钥不进入 Query/Mutation data |
| Docs | 完成 | 本次本地提交 | `pnpm --filter @multica/docs build` | Next.js 文档构建成功，157 个静态页面生成 |
| 最终范围检查 | 完成但有无关基线失败记录 | 本次本地提交 | `git diff --check`、密钥字面量扫描、`go test -count=1 ./...` | diff 检查和扫描通过；全量 Go 仅 `server/pkg/agent` 的 Codex 72–100ms 时序用例失败，该目录相对 `origin/develop` 无任何 diff；本改造相关包及其余已输出包通过 |

未执行或保留到后续边界：

- 未在真实浏览器中手工点验 Web/Desktop 设置页；共享视图已通过 Core/Views/Web/Desktop typecheck，交互仍建议在预发开启 feature flag 后做一次 owner/admin/member 人工验收。
- 未修改或联调 DTA 仓库；DTA 保存和消费 `dta_` 密钥是后续独立改造。
- 未 push、未开 PR、未部署，也未在共享或生产数据库执行迁移。
- 全量 Go 命令存在上述未改动 `server/pkg/agent` 的短超时时序失败；不将其误记为本 Grant 改造通过，也不越界修改。

## 验证策略

| 改动或验收项 | 风险 | 验证方式 | 是否测试先行 |
|---|---|---|---|
| Migration up/down/replay | 迁移失败或污染 human user | 隔离 PostgreSQL 执行 up/down/up，检查默认 `principal_type=human` 与现有数据 | 是 |
| Subject user | 可登录或出现在成员体系 | 登录/PAT/member/list/notification 负向测试；无 member row | 是 |
| Owner-only 管理 | admin/member 可扩大外部权限 | owner/admin/member/Grant Token 表驱动 handler 测试 | 是 |
| Grant 动态改权 | 旧权限继续生效 | 同 Token 改权前后请求对照；多连接/多 handler 实例读回 | 是 |
| Token 生命周期 | 明文泄露、吊销延迟、轮换中断 | 单次明文、到期、改期、吊销、双 Token 轮换、last-used 测试 | 是 |
| Capability allowlist | 新路由意外暴露 | method/path/operation registry 表驱动默认拒绝测试 | 是 |
| Workspace 强绑定 | Header/path/query 越权 | 同 Token 枚举两个工作区和伪造 Header，全量拒绝 | 是 |
| `own_agents` | 操作其他主体智能体 | owned/同工作区非 owned/跨工作区三组测试 | 是 |
| `workspace` | capability 逃逸成 admin | 允许目标 operation，成员/Token/账单/运行时 exec 等仍拒绝 | 是 |
| `private/public_to` | Grant owner 不能配置自己的智能体或修改他人 owner-only 字段 | own/workspace + permission mode 矩阵测试 | 是 |
| Skill ownership | 覆盖他人共享 skill | created_by、assignment、workspace scope 对照测试 | 是 |
| Trace 访问 | 同工作区私有 task 越权 | task→agent→owner/scope 校验；UUID 枚举负向测试 | 是 |
| Trace 完整性 | 额外投影误删真实工具链路 | text/thinking/tool input/output/error fixture 原样对照 | 是 |
| Trace 分页 | 重复、漏项、无界读取 | `since/limit` 顺序、游标、最大页测试 | 是 |
| Human/PAT 兼容 | 现有用户行为回归 | 现有 auth/workspace/agent/skill/task 测试全量回归 | 否 |
| 管理 UI | 权限控件漂移或响应异常崩溃 | shared view、query、schema malformed-response 测试 | 是 |
| DTA 消费合同 | Multica 后端可用但 DTA 后续无法接入 | 独立 HTTP fixture 覆盖 token/auth/deploy/trace/retire JSON 合同 | 否 |

建议执行期验证命令按范围逐步运行：

```bash
# Worktree 使用任务隔离的 Homebrew PostgreSQL 16 数据库
source .env.worktree

cd server
go test -count=1 ./internal/auth ./internal/middleware ./internal/handler ./internal/migrations
go vet ./internal/auth ./internal/middleware ./internal/handler
cd ..

make sqlc
pnpm --dir packages/core exec vitest run api/schemas.test.ts workspace-access/queries.test.ts
pnpm typecheck
pnpm --filter @multica/docs build
git diff --check

# 最终风险匹配验证
go test -count=1 ./...
```

不把一次 `exit 0` 当作完整证明；最终证据至少包含：

- Grant、subject user、Token、capability、scope 数据库回读。
- own/workspace 两种范围的允许与拒绝对照。
- 智能体 owner 与 `private/public_to` 行为回读。
- Trace 中 text/thinking/tool input/output/error 的真实 payload 对照。
- 跨工作区、跨智能体、未授权路由负向测试。
- Grant 改权、停用、Token 到期/吊销后的下一请求结果。

## 系统边界与职责

```text
内部钉钉员工（workspace owner）
  └─ Multica 设置页
       └─ DTA 访问授权
            ├─ capability
            ├─ own_agents / workspace
            ├─ active / disabled
            └─ Token 签发、改期、吊销

外部服务商
  └─ DTA
       └─ Bearer dta_...
            └─ Multica Grant Auth
                 ├─ stable Grant policy
                 ├─ internal subject_user_id
                 ├─ existing agent.owner_id
                 └─ explicit operation gate
```

- 工作区 owner 决定 Grant 可以做什么、对哪些智能体生效、何时失效。
- Grant subject 决定“自己的智能体”归属，不是可登录的人类账号。
- 智能体 `permission_mode` 决定谁能查看/触发智能体，与 Grant capability 正交。
- DTA 决定对服务商呈现的产品交互；Multica 只提供受限后端合同。
- Grant/Token 不能查看、修改或扩大自身权限。

## 关键数据流与状态归属

### 创建与管理

```text
human workspace owner
  → owner-only Grant API
  → transaction:
      service user + grant + audit
  → issue Token:
      return plaintext once
      persist token_hash only
```

### Grant 请求

```text
Bearer dta_...
  → hash lookup
  → token active + expiry
  → grant active + capability + scope
  → subject_user_id Principal
  → explicit operation gate
  → workspace equality
  → agent owner/scope
  → shared handler/service
  → audit/log
```

### Trace

```text
task_id
  → task.agent_id
  → agent.workspace_id + owner_id
  → trace.read + resource_scope
  → existing task_message page
  → existing TaskMessagePayload
```

## 接口与兼容性

- Human API 与 PAT 响应保持不变。
- `user.principal_type` 默认 `human`，现有 user 数据无需回填行为变化。
- Grant Token 使用新前缀；旧服务器副本无法识别，因此功能开关开启前不能签发。
- Grant 复用现有 resource URL 时不扩大 response；task summary 只使用字段 allowlist。
- Web/Desktop API 响应经过 zod schema；旧桌面端不会看到新设置入口，也不依赖新字段。
- 不修改 Multica CLI，不要求 Grant Token 能执行 `multica login`。
- 不修改 DTA；后续 DTA adapter 通过本计划冻结的 HTTP 合同接入。

## 数据迁移与幂等

- Migration 为 additive：新增 `user.principal_type` 与三张 Grant 表。
- `principal_type` 默认 `human`，现有行保持 human 语义。
- `workspace_access_grant.subject_user_id` 唯一并引用 `user.id`；不创建 member。
- Token Hash 唯一；吊销使用 `revoked_at`，不物理删除。
- Grant update 通过 `version` CAS；过期写返回 409。
- Token 签发非幂等；响应丢失后明文不可恢复，只能根据 prefix 吊销并重新签发。
- 迁移文件遵守 Aone full filename stem 规则；up/down 都提供，风险迁移在预发数据库事务内验证。
- 生产回滚不立即执行 down migration，保留 additive 表和审计数据。

## 失败模式与恢复

- **Token 创建响应丢失**：明文不可恢复；owner 从 list 找到 prefix，吊销后重新签发。
- **Grant 被停用/改权**：下一请求从数据库读取新状态并拒绝；不删除智能体。
- **Token 被吊销/到期**：下一请求 401；其他 Token 不受影响。
- **安全轮换失败**：旧 Token 在 owner 明确吊销前仍有效；DTA 可继续使用旧 Token 恢复。
- **Subject user 创建成功但 Grant 创建失败**：同一事务回滚，避免孤儿 user。
- **Grant 创建智能体成功但 owner 写入错误**：事务/回读失败，不把智能体声明为可用。
- **Trace scope 检查失败**：返回稳定拒绝，不查询/序列化 task messages。
- **审计写失败**：Grant/Token 管理写与事务一起失败；Grant 业务请求和 Trace 读取的响应后审计失败会写结构化告警，但不撤销已经完成的业务响应。
- **多副本版本混跑**：功能开关保持关闭，不签发 Token；全部新副本 ready 后再开启。

## 并发与一致性

- PostgreSQL 是 Grant、Token、capability、scope 和 status 的唯一权威。
- Grant update 使用 `version` 乐观并发控制。
- Token 吊销/改期使用条件更新，已吊销 Token 不可恢复。
- `last_used_at` 使用时间阈值条件更新，不参与授权结果。
- Grant 创建与 subject user、首次审计同事务。
- 智能体创建与 `owner_id=subject_user_id` 同一写操作；无需另写关系表。
- 多 Token 轮换不改变 Grant ID、subject user、capability 或智能体 owner。

## 可观测性

首版新增 `workspace_access_audit` 与结构化告警，不记录 Token 明文、Hash、用户输入或工具 payload。指标维度暂不新增，待预发观察实际请求量后再决定，避免引入未经消费的高基数指标。

审计/日志字段：

```text
request_id
workspace_id
grant_id
token_id
subject_user_id
capability
resource_scope
operation
resource_type
resource_id
result
error_code
```

owner UI 首版展示 Grant 状态、capability、scope、Token prefix、创建时间、到期时间和最后使用时间；完整审计检索页后续再做。

## 发布策略与功能开关

1. 在预发执行 additive migration。
2. 发布包含 Grant 代码但默认关闭 `workspace_access_grants` 的版本。
3. 验证 human JWT/PAT、task token、daemon token、智能体 owner 与 Trace 回归。
4. 等待全部 API 副本完成滚动，再在预发开启功能开关。
5. 只创建隔离测试 Grant/Token，用 HTTP fixture 验证三项 capability、两种 scope、改权、停用、到期和吊销。
6. 观察拒绝率、审计与数据库认证查询负载。
7. DTA 改造、正式环境开启和 Token 交付分别另行确认。

旧副本不识别 Grant Token，滚动窗口中禁止提前签发或交付。

## 回滚步骤与触发条件

触发条件：

- 发现跨工作区、跨 scope、未授权路由或 Trace 越权。
- Grant subject 可作为 human 登录、创建 PAT 或出现在成员体系。
- capability/scope 修改、Grant 停用或 Token 吊销不能下一请求生效。
- Grant 请求被错误记成创建它的 workspace owner。
- 现有 human/PAT/Agent owner 行为回归。

步骤：

1. 关闭 `workspace_access_grants` 功能开关，停止 Grant Token 认证和新 Token 签发。
2. 批量将 Grant 设置为 disabled，保留 Token、subject、owner 和审计数据。
3. 回滚应用版本，不立即删除新表或修改智能体 owner。
4. 验证 human JWT/PAT、task token、daemon token 和原工作区路由恢复。
5. 只有确认无新数据依赖并经单独批准，才在受控环境执行 down migration。

## 风险与回滚

- **最大风险：内部 subject user 被误当 human。**
  - 缓解：`principal_type` 默认 human、Grant subject 显式非 human；登录/PAT/member/notification 负向测试；不创建 member。
- **Workspace scope 被误实现成通用 admin。**
  - 缓解：scope 只进入 capability operation gate，不赋 member role；默认 `own_agents`，UI 高风险确认。
- **现有 owner-only 访问字段被 workspace scope 越权修改。**
  - 缓解：非 owner 的 `permission_mode/invocation_targets` 保持拒绝；矩阵测试锁定。
- **共享 skill 被 `own_agents` 覆盖。**
  - 缓解：更新按 `created_by=subject_user_id`，读取/assignment 与修改分开授权。
- **Trace 同工作区 UUID 枚举。**
  - 缓解：所有 messages 查询先解析 task→agent，再做 capability+scope；修正现有 human read gate。
- **Token 明文或权限状态被缓存。**
  - 缓解：明文单次返回；首版授权每请求读 DB；日志/API/UI 禁止 Hash 与明文。
- **现有 Trace 不完全等于原始 Provider 全量事件。**
  - 说明：Provider thinking 可用性、8KB tool result 限制和现有入库 redaction 保持现状；本计划只授权已有 Trace，不承诺提升采集 fidelity。

## 发布边界

- Push：本地 commit 后告知“已提交，准备 push”；用户明确同意后直接执行。
- PR / 合并 / 预发 / 正式部署 / 功能开关 / DTA 改造：未授权，除非用户另行明确确认。
- 本计划获批只授权计划内本地实现、明确的新分支/worktree、测试和本地 commit 判断。

## 计划变更记录

| 日期 | 变更 | 原因 | 是否重新确认 |
|---|---|---|---|
| 2026-07-31 | 初始计划：Multica 单仓 Grant，覆盖 DTA 部署与 Trace 权限 | 用户要求先做 Grant 改造 | 是 |
| 2026-07-31 | 明确从 fresh `origin/develop` 创建 `codex/workspace-access-grants` 独立分支和 worktree | 用户要求“记得拉新分支”，当前分支属于上一项功能 | 是 |
| 2026-08-02 | 重写为 Grant+Token 分离、动态改权/停用/吊销、复用 `agent.owner_id` 与 `private/public_to`、`own_agents/workspace` 双范围、三个 capability | 用户逐项确认 owner 复用与 owner 管控模型 | 是 |
| 2026-08-02 | 删除资源关系表、Multica CLI 兼容、复杂 External Trace 投影和额外业务脱敏 | 用户明确外部不使用 Multica CLI，Trace 返回真实 Agent 链路 | 是 |
| 2026-08-02 | 完成 migration、Principal/operation gate、owner-only API、Agent/Skill/Runtime/Trace scope、共享管理 UI、文档和 feature flag | 用户确认分支后要求开始实现 | 是 |

## 最终验证结果

| 验收项 | 验证命令或检查 | 结果 | 证据摘要 |
|---|---|---|---|
| Grant/Token/Auth/Trace 专项 | `go test -count=1 ./internal/auth ./internal/featureflags ./internal/middleware ./internal/handler -run 'WorkspaceAccess|ReleaseFlags'` | 通过 | 生命周期、动态权限、scope、Trace 分页/保真、停用/吊销和默认拒绝均有测试 |
| 后端相关包回归 | `go test -count=1 ./internal/handler`；`go vet ./internal/auth ./internal/featureflags ./internal/middleware ./internal/handler` | 通过 | handler 全包与相关静态检查通过 |
| TypeScript 工作区 | `pnpm typecheck` | 通过 | Core、Views、Web、Desktop、Docs 等 6 个实际任务成功 |
| Core 合同 | `pnpm --dir packages/core exec vitest run api/schemas.test.ts workspace-access/queries.test.ts` | 通过 | 2 files / 79 tests；枚举 fail-closed、malformed fallback、query key 含 wsId |
| Docs | `pnpm --filter @multica/docs build` | 通过 | Next.js production build 与 157 个静态页面成功 |
| Migration 最终状态 | 隔离数据库 257 down 后全量 up；`psql` 回读 | 通过 | 三张表存在，schema version 为 `257_workspace_access_grant` |
| 全量 Go | `go test -count=1 ./...` | 非阻塞失败 | 未改动的 `server/pkg/agent` Codex 72–100ms 时序测试失败；该目录相对 `origin/develop` diff 为空，Grant 相关包通过 |
| 范围与密钥检查 | `git diff --check`；`dta_` 长密钥字面量扫描；`git status --short` | 通过 | 无 whitespace 错误、无真实 DTA 密钥字面量、改动全部位于本任务 worktree |

### 最终工作区

- 原始工作区与分支：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica` / `codex/fde-start-dta-managed-source`
- 最终工作区与分支：`/Users/fanqi/test/code/ding-fde-agent/.worktrees/dt-fde-multica-workspace-access-grants` / `codex/workspace-access-grants`
- 基线：fresh `origin/develop@596ed393fd1db31f4a83765a7ca686df200fc215`
- 交付状态：实现完成并创建本地提交
- Worktree 收尾：本任务创建并保留，未 push、未开 PR、未合并、未清理
- 原始工作区的用户修改：`docs/plans/2026-07-29-fde-start-dta-managed-source.md`，未触碰
- 未执行的验证：真实浏览器手工 UI 点验、DTA 仓库端到端联调、预发/生产迁移与部署

## 遗留风险

- 本计划不解决 DTA 包、命令和错误文案中的 Multica 概念隐藏；后续 DTA 改造必须只暴露 DTA 产品语义。
- 首版不提升 Trace capture fidelity，也不补不可变 Agent/Skill revision snapshot。
- 首版不接入 Grant 级 WebSocket，只能通过 REST 轮询实时跟踪。
- 首版管理 UI 不提供完整审计检索页，只展示关键 Token 使用信息。
- 永久 Token 由 workspace owner 显式选择，仍存在长期泄漏风险；后续可增加最大 TTL、IP allowlist、配额和速率策略。
