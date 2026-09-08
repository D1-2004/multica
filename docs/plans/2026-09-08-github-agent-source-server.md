# GitHub Agent 创建与确认同步

支持 DTA `dingtalk-agent/project@1` 以及可导出的 `multica.agent/v1` JSON manifest，直接读取仓库中的定义文件。开发者修改仓库并正常提交代码；Multica 读取固定 commit，预览后创建或同步 Agent。没有 CLI 构建、上传 Bundle 的步骤。

## 已实现范围

- GitHub 仓库 URL、`owner/repo`、`/tree/<ref>` 链接；支持 `.git` 后缀。连接由 `installation_id` 明确选择，使用工作区 GitHub App 的 installation 凭据，不使用 Agent 绑定的 GitHub 身份。
- 分支列表；创建预览固定提交；通过 `preview_id` 确认创建，重复或并发确认返回同一个 Agent。
- 已有 Git Agent 可以选择新分支，查看 Git 文件差异和当前配置将被覆盖的差异，再确认同步。
- 来源、分支、已发布 SHA，以及发布回执保存在 PostgreSQL。预览有效期为 30 分钟，限定工作区、创建者及目标 Agent；确认时重新检查 Git 权限。
- 普通 Git Agent 的专属 skill 仍在工作区 skill 表和文件表中，允许管理者修改、删除，禁止分配到其他 Agent。`agent_source_skill` 是归属校验的权威关系，新增来源 skill 的 `config.exclusive_agent_id` 提供归属元数据；修改 config 会保留服务端来源信息。平台自动管理 Agent 的 skill 保持保护。
- 创建方式选择页提供“从 Git 创建”：选择工作区连接、仓库 URL 和分支，预览固定版本后确认创建。Web/Desktop 均有独立 `/agents/new/git` 路由。
- 配置 → 管理分为“导出”和“发布”两个子 section。所有 Agent 均显示入口；导出需管理权限，未关联 Git 的发布页引导从 Git 创建。Git 来源的概览同步按钮进入发布页。

本轮同步的字段边界：

| 内容 | 行为 |
| --- | --- |
| `agent.definition` 对应的指令 | 创建、同步时读取并覆盖 |
| `agent.skills` 声明的 skills、目录内文本文件 | 创建专属工作区 skill；同步新增、修改、删除，DTA 声明项恢复 enabled=true，JSON manifest 按 enabled 字段恢复状态 |
| 名称和描述 | 创建使用默认值，可由创建请求覆盖；同步保留实例值 |
| 手动添加的其他工作区 skill | 同步保留；不修改共享 skill 内容 |
| runtime、模型、运行参数、persona、reply tone、MCP、环境变量 | 本轮不从 DTA manifest 导入，仍由实例配置管理 |
| 身份、账号授权、调用权限、owner | 保留 |
| 聊天、任务、记忆、运行数据 | 保留 |

指令、skills 和管理分组统一位于“配置”区域。导入操作的是 Agent 定义配置；这个入口不表示整个“配置”区域都被序列化或覆盖。

本轮不包括 DTA CLI 的 `init` 改造、完整运行配置 Schema 扩展、普通资源目录及二进制资产导入。现有 DTA 编译器仍限制为指令和 skill 目录内的文本文件，并对跳过的二进制文件给出 warnings。`dta init` 的模板仓库能力需在 DTA 项目中单独实施。

## API 契约

以下路径均需要现有登录和工作区权限。工作区仓库浏览、创建预览、创建保持 owner/admin 权限；Agent 来源分支、同步预览、确认需要 Agent 管理权限。

### 浏览与创建

`GET /api/workspaces/{workspaceId}/github/branches?installation_id={id}&repository={URL-or-owner/repo}`

返回 `repository`、`repository_url`、`default_branch`、`branches`。每个分支包含 `name`、`commit.sha`、`protected`。分支链接中的 `/tree/` 后全部内容视为 ref；不推测 monorepo 子目录，manifest 固定从仓库根读取。

`POST /api/workspaces/{workspaceId}/github/agent-preview`

```json
{
  "installation_id": "<workspace-installation-uuid>",
  "repository": "https://github.com/acme/reviewer",
  "ref": "main"
}
```

响应保留既有 `installation_id/repository/ref/resolved_sha/name/description/instructions/skills/compatible_providers/warnings/blockers`，增加 `preview_id/expires_at/repository_url`。

`POST /api/workspaces/{workspaceId}/github/agents`

```json
{
  "preview_id": "<preview-uuid>",
  "runtime_id": "<runtime-uuid>",
  "name": "代码审查助手"
}
```

`preview_id` 已固定来源，不能再同时传 `installation_id/repository/ref/resolved_sha`。其余创建参数沿用 CreateAgentRequest。首次创建返回 201，重试返回 200 和同一个 Agent；记录回执与 Agent/skill 写入使用同一事务。

保留旧 API 客户端的 `installation_id/repository/ref/resolved_sha` 创建请求，其中 SHA 必须不可变。该旧请求没有跨请求幂等标识；新客户端应使用 `preview_id`。

### 来源信息与同步

`GET /api/agents/{agentId}/source` 增加 `repository_url/can_sync/configuration_scope`。`can_sync` 描述来源是否支持手动同步，客户端仍需结合 Agent 编辑权限；缺少该字段的旧服务端响应不能被当作已授权。

`GET /api/agents/{agentId}/source/branches`：只浏览该 Agent 已绑定仓库的分支。

`POST /api/agents/{agentId}/source/preview`

```json
{"ref": "release/v2"}
```

空对象表示当前配置分支。响应包含：

- `preview_id/expires_at/repository_url/ref/base_sha/resolved_sha`。
- `git_changes`：两个准确提交的树差异，包含新增、删除、修改和 mode 变化，按路径排序，不使用 merge base。声明文件返回 before/after 文本，其余文件仍提供 Git 对象 SHA 和 mode；文本缺失不是空文件。
- `configuration_changes`：当前落库指令、来源 skill 内容、名称、描述、enabled 及配套文件与候选定义的差异；能显示本地编辑和删除后将恢复的内容。
- `warnings/changed`。`changed` 还包括 ref 或 SHA 变化。

两个 diff 的条目均使用 `path/status/before/after`；Git diff 额外提供 `before_sha/after_sha/before_mode/after_mode`。`before/after` 可以为 null，空字符串表示实际空文本。候选 Git 树中未被导入器消费的文件，仅改变 Git 来源版本，不隐式变成运行资源。

`POST /api/agents/{agentId}/source/sync`

```json
{"preview_id": "<sync-preview-uuid>"}
```

确认只接受预览 ID，返回既有 `source/changed/warnings`。已确认的同一预览重试返回原来源回执，不重复应用。服务端不会在确认时重新解析分支最新 SHA。

| 状态 | 含义 |
| --- | --- |
| 400 | 地址、ref、preview ID 或请求形状不合法；预览用于错误 Agent；确认携带来源覆盖字段 |
| 403 | 当前 installation 无权读取仓库，或现有权限检查拒绝管理 |
| 404 | installation、仓库/分支、Agent 来源或本用户预览不存在 |
| 409 | 预览过期、Agent/source/skill 在预览后变化、来源断开、平台自动管理来源 |
| 422 | DTA 定义校验失败或 runtime 不兼容 |
| 428 | 同步请求没有 `preview_id`，需要先预览 |

旧的无 body `/source/sync` 不再直接更新。包括旧 DTA Git deploy 调用在内的 API 客户端，必须升级为预览再确认；创建的 SHA 请求兼容性不代表旧同步协议仍可用。

## 源码导出与 JSON manifest

`GET /api/agents/{agentId}/export`：Agent 所有者或工作区 owner/admin 可以下载当前配置的源码 ZIP；普通访问者拒绝。返回 `application/zip`、附件文件名和 `Cache-Control: no-store`。只读 repeatable-read 事务读取一个一致的 Agent/skill/文件快照。

导出包含 `agent.json`、`agent.schema.json`、指令文件、全部已分配 skills 的 SKILL.md 和配套文本文件。保留停用状态。Git 来源保持已知指令路径和 skill 目录；手动添加的 skill 使用独立 `workspace-skills/<id>` 目录避免名称碰撞。来源 skill 去除服务端隔离名称后缀；内容原样保留，manifest 的 skill name/description 是导入时的元数据来源。

```json
{
  "$schema": "agent.schema.json",
  "version": "multica.agent/v1",
  "name": "代码审查助手",
  "description": "审查变更",
  "instructions": "AGENTS.md",
  "skills": [
    {"path": "skills/review", "name": "review", "description": "审查规则", "enabled": true}
  ]
}
```

权威 JSON Schema 在 `server/internal/agentsource/agent.schema.json`，随每份导出提供；服务端使用固定版本契约验证，绝不读取仓库声明的任意远端 schema。`skills: []` 合法，不自动注入 DTA 基础 skill。预览响应的每个 skill 增加 `enabled`；旧响应省略时客户端不推断停用。

仓库根只允许一个入口：`agent.json` 或 `dingtalk-agent.json`，同时存在时拒绝，避免选错定义。既有 YAML 不作为 Git 导入的自动回退。两个格式共用固定 SHA、预览确认、权限重查、事务落库和 diff 流程。

导出不读取环境变量、运行配置、MCP 凭据、账号绑定、权限授予或聊天任务记忆；skill 正文和配套文件是用户编写的内容，按原文导出。所有导入 skill 都成为新 Agent 的专属工作区 skill。源码导出完成前会经过真实导入器校验；路径冲突、二进制、超限或会被跳过的文件返回 422，避免下载后才发现无法恢复。沿用最多 20 skills、单文件 1 MiB、单 skill 8 MiB、总计 32 MiB 等导入限制。

ZIP 只用于下载源码目录，解压后正常提交到 Git；创建和发布始终直接读取 Git，无 CLI build/bundle 或上传产物阶段。后续目录移动在同一事务内先移除旧路径，再创建新路径，支持保留 skill 名称；失败整体回滚。

滚动部署时，新版能读取旧 DTA 预览；包含新 JSON manifest 的预览带额外配置摘要，旧服务端会拒绝而不会按旧格式错误应用。若请求碰到旧副本，待发布完成后重新预览即可。本轮不新增数据库迁移。

## 事务与执行边界

发布依次锁定预览、Agent、来源、来源 skill 与关联行，重新计算当前状态摘要，拒绝覆盖预览后发生的变更。指令、skills、来源 ref/SHA 和发布回执同事务提交。普通 skill 文件更新/删除锁定父 skill，避免同步读取到一半的配套文件。

预览读取不会修改活动 Agent。发布失败不会提交任何部分配置。已发布记录保留 Git 基线，因此后续 diff 不依赖旧 commit 永远可从远端读取；升级前创建且没有发布记录的 Agent，首次同步预览仍需读取旧 SHA。

运行侧继续消费现有 Agent/skill 数据；本轮没有引入任务级不可变 revision，也没有更改已启动任务的执行快照机制。不要把数据库事务提交描述成所有正在运行的会话已切换版本。

新增迁移 `9159`–`9162`，没有新增外键；三个索引各自以独立 `CREATE INDEX CONCURRENTLY` 文件创建。工作区删除会显式清理预览/发布回执。快照保存在 PostgreSQL，无本机缓存依赖。

## 验证与环境说明

测试使用单独的本地数据库 `multica_agent_source_915f` 和 HTTP GitHub fixture；未访问真实 GitHub App installation。首版已部署预发，下面保留首版验证记录。

验证结果：

- 30 项相关 handler 测试通过，其中 11 项覆盖新 Git 流程，包括固定 SHA、幂等与并发创建、同提交恢复删除 skill、预览过期、跨用户/Agent 拒绝、竞争发布冲突、权限重查和事务回滚。补齐了既有 Git 来源测试 fixture 的必填 `workspace_id`。
- `internal/agentsource`、`internal/githubapp` 全部测试通过；服务端构建和相关包 `go vet` 通过。
- 53 项相关界面及配置导航测试、7 项接口 schema 测试通过；全仓 `npm run lint`（只有既有 warnings）和 core/views 类型检查通过。
- 初始基线的 `instructions-tab.test.tsx:106` 类型问题已在合入的主干中修复；发布前 core/views 类型检查均通过。

当前基线有独立于本改动的问题：全新库按文件顺序迁移时，`271_task_completion_canceled_status` 依赖尚未创建的 `9025_task_completion_outbox`；迁移编号 lint 还会报告已有的 `9093` 重号。测试库先执行了既有 `9025` 再继续迁移。sqlc 生成使用临时 schema 副本将同一前置文件排到 `271` 前，仅拷回新增查询与模型；没有修改或重排仓库中的既有迁移。

创建与管理补齐后的本地验证：64 项 views 测试和 168 项 core 测试通过；包含分支变化使预览失效、固定预览 ID 创建、权限控制、源码下载及响应异常。Go 覆盖无 skill 的普通 Agent 导出、来源路径与停用状态往返、越权拒绝、目录重命名发布以及既有确认并发/回滚场景。Go 构建、core 类型检查和 npm lint 通过。所有检查均未调用代码格式化工具。

## 历史记录

- 2026-09-08：增加 GitHub 链接创建、固定预览确认、换分支同步和导入导出来源页。原因：让 Git 仓库直接作为 Agent 定义来源，并在覆盖前呈现可确认的 Git 与本地配置差异；取消无预览的同步写入。

- 2026-09-08 发布适配：合入 develop 的配置导航，将导入导出接到管理分组；将尚未发布的新增迁移改用 9159–9162，避开主干已有编号。

- 2026-09-08 创建与管理补齐：新增从 Git 创建页面、源码导出接口及 JSON Schema，管理拆为导出和发布。原因：补齐创建可发现性与可往返导出，支持普通 Agent 没有 DTA 基础 skill 的场景；保留启停状态和来源路径，修复改目录后的同名 skill 发布冲突。
