# Agent 仓库 Manifest

## 状态与版本

权威 JSON Schema：[`server/internal/agentsource/agent.schema.json`](../server/internal/agentsource/agent.schema.json)。采用 JSON Schema Draft 2020-12，文件自包含，不依赖远端 `$ref`。仓库入口仍为根目录 `agent.json`，其 `$schema` 固定为 `agent.schema.json`。

| 版本 | Schema | Git 创建、导出、发布 |
| --- | --- | --- |
| `multica.agent/v1` | 已有契约，字段与约束保持兼容 | 本地 / Git 创建；发布指令、skills 和配套文件，保留实例名称/描述 |
| `multica.agent/v2` | 表达当前 Multica 的可配置特性 | 本地 ZIP / Git 统一创建、配置写入、平台导出和 Git 发布 |

服务端通过内置 JSON Schema 执行实际结构校验，然后按 manifest 解析包内数据。Schema 校验通过仅代表文档结构合法，不代表资源可用、调用者有授权或配置写入已经完成。平台导出生成 v2；本地与 Git 获取文件后都产生完整 bundle，通过同一预览确认接口写入配置。

新建源码智能体以 `agent.json` 为唯一入口，Git 与 ZIP 均不依赖 DTA 格式或 CLI 构建产物。v1 保留最小配置契约，新包应使用 v2。内部旧托管模板的 DTA 读取器不参与公开导入流程。

## 导出当前 Agent 与下载 Schema

“导出”表示将平台当前保存的 Agent 内容整理成压缩包下载。入口为 Agent 配置 → 管理 → 导出 → **下载智能体包**，接口仍为 `GET /api/agents/{id}/export`，文件名为 `agent-{id}.zip`。普通创建和 Git 创建的 Agent 使用同一入口，沿用 Agent 管理权限。

服务端在同一次数据库只读快照中读取当前名称、描述、用户指令、24 项配置、全部已分配 skills 及其启停和配套文件、运行时 skill 禁用选择、DSH 插件关联、OKR、调用权限、平台保存的运行时/电脑/企业身份/钉钉账号/机器人绑定，以及 A2A 卡片和客户端策略。Git 来源的已发布记录只用于保留文件目录布局，内容以平台当前保存值为准，不读取远端 Git，不重新下载历史版本。

ZIP 包含 `agent.json`（v2）、`agent.schema.json`、指令文件、skill 文件以及 `EXPORT-NOTES.json`。后者提供资源引用的类别和显示名、待绑定值的 JSON 路径和说明；它是导出说明，不参与 Agent 配置写入。运行时/身份/插件实体和凭据仍由目标环境提供。GitHub 执行身份存放在外部身份服务，不属于本地数据库快照，导出说明明确要求另行检查和绑定；manifest 不将未知状态写成 `null`。

不打包任务、聊天、记忆记录、访问令牌、平台生成的系统指令或 Git 发布记录。用户编写的指令、skill 正文与配套文件保持原文。环境变量、请求头、命令参数值和未分类的 provider 字符串使用 `secret_ref`；已知公开的传输类型、可执行文件名和无认证信息/查询参数的服务 URL 保留。普通参数值也可能需要重新绑定，避免把无法分类的值当作可公开内容。包含内联参数的 MCP 字符串命令可以整体用 `secret_ref` 表达。

Agent 创建页面共用顶部栏提供 **下载 Schema**，选择创建方式、手动创建、AI 创建和从 Git 创建页面均可见。`GET /api/agent-schema` 返回服务端实际内置的完整 Schema，下载文件名为 `agent.schema.json`；无需先创建 Agent 或选择 Git 连接。前端验证响应类型和 Schema 外层结构，下载原始内容。

导出完成前使用统一包解析器校验，不返回静默丢字段或文件的包。当前 v2 包支持下载、校验、预览和确认创建。创建页提供“从本地导入”；Git 和本地共用配置预览、运行时选择及确认表单。

## 统一包解析流程

Agent 包是包含 manifest 和其引用文件的 ZIP 源码目录，不需要 CLI build。入口仍为 Agent 目录下的 `agent.json`。ZIP 可以直接包含这些文件，也可以包含压缩工具或 GitHub 下载添加的外层目录，例如 `my-agent/agent.json`。服务端先去除共同的外层目录，再按 manifest 解析相对路径；包目录外不能混入其他业务文件。根目录已有 `agent.json` 时优先使用它，不会将 `examples/agent.json` 等示例误判为另一个包。`__MACOSX`、`.DS_Store`、`._*` 压缩元数据不参与配置或 Skill 文件导入，但仍受原有条目路径、类型和大小检查约束。找不到 manifest 或无法确定唯一包目录时，错误会说明根目录、候选 manifest 路径及整理方式。Git 仓库仍要求根目录 `agent.json`。

包内 `agent.schema.json` 可以随导出提供给编辑器，但服务端不会使用上传者提供的 Schema 放宽规则，也不会请求它引用的外部 URL。

```text
上传 ZIP / 读取 Git 目录
    → 检查容器、目录与大小限制
    → 只读取 agent.json
    → 按 version 使用内置 JSON Schema 校验
    → 按 instructions、skills[].path 读取文件
    → 校验文件关系、内容与大小
    → 生成完整 ParsedAgentPackage
    → Bundle（完整定义、引用文件、规范化内容摘要）
    → 持久预览、目标环境选择、确认创建事务
```

实现入口：`ValidateManifestJSON`、`ParseAgentPackage`、`ParseAgentPackageFS`。Git `agent.json` 编译与 ZIP 使用相同的 `parseAgentPackageRepository`，文件内容延迟读取；manifest 不合法时不会读取其引用的指令和 skill 内容。Git 的公开入口 `ReadAgentRepository` 同样要求 `agent.json`。

`ParsedAgentPackage.Manifest` 保存全部已通过 Schema 校验的 JSON 字段，包括 v2 的配置、绑定、权限和 A2A 定义；`Instructions`、`Skills` 保存解析后的内容，`Warnings` 保存跳过文件等提示。不会将 v2 强制转换为只含 v1 字段的最终 Agent。`Hash` 覆盖完整 manifest 与已解析内容，仅表示解析结果摘要，不是创建授权或持久预览 ID。

服务端上传预览接口：

```http
POST /api/workspaces/{workspaceId}/agent-packages/preview
Content-Type: multipart/form-data

file: agent.zip
```

也支持 `Content-Type: application/zip`，请求体直接为 ZIP。权限与 Git 导入预览一致，要求工作区 owner/admin；multipart 只能包含一个名为 `file` 的文件。限制压缩包 40 MiB、声明解压总量 32 MiB、最多 8192 个条目；拒绝重复路径、路径穿越和符号链接，不解压到服务器磁盘。

成功返回 `preview_id/expires_at/manifest_version/package_hash/name/description/instructions/skills/manifest_fields/configuration_fields/definition/requirements/warnings`。预览归当前用户和工作区所有，30 分钟有效；快照包含完整 bundle。`definition` 提供可审阅的配置，环境变量、网关、MCP 等敏感值脱敏。预览不创建 Agent、不执行包内脚本。

两种来源统一确认：`POST /api/workspaces/{workspaceId}/agent-packages`，请求包含 `preview_id`、`runtime_id`，可覆盖 `name/description`。配置以包为准；Agent、配置、OKR、A2A 策略、专属 workspace skills 和文件在一笔事务中创建。确认会重新检查用户权限，Git 还会重查仓库权限，但不会重新解析移动后的分支；重复确认返回原 Agent。

`requirements.secrets` 列出待填写的值别名，通过确认请求的 `secrets` 映射提供。`requirements.deferred_bindings` 列出需要创建后另行配置的身份、机器人、电脑、插件、跨环境授权或其他运行时的 skill 选择；用户必须逐项或整体明确确认延后。延后的成员授权创建为私有；未确认或缺少必需值返回 422，不能半成功。绑定运行时的禁用 skill 会映射到所选运行时。环境绑定不从旧平台 UUID 自动迁移。当前适配器将账号/电脑的 null 解绑请求也列为待配置，确认延后表示此次不执行该绑定变更；创建时保持未绑定，发布时保留原绑定。

`requirements.binding_declarations` 是本次已通过 Schema 校验的包中的资源声明，元素为 `{ "path": "/bindings/github_identity", "declaration": { "ref": "maintainer" } }`。它包含实际出现的 `bindings` 子字段、`access`、`dsh_plugins` 和 `disabled_runtime_skills`，保留显式 `null`／空列表，不补入包内未声明的资源。ZIP、Git 和 Builder 复用同一解析函数；已确认资源的复用只影响待配置清单，不删除声明展示，也不返回目标环境凭据。

创建和发布页面在本次包解析成功后才展示“配置包资源绑定”。换包、换分支或重新解析时清空旧预览。发布页另有默认折叠的“已导入包的资源绑定”，用于处理已生效包的绑定回执，不能将它当作本次待发布包的声明。

配置包导入的 OKR 按 Agent 独立创建标签，同一工作区可以重复导入相同目标文案。`agent_okr.authored_text` 保存原文，标签使用带 Agent 名称的唯一显示名；碰到普通或历史同名标签时另建标签，不接管原标签。再次发布或编辑相同目标时复用当前 Agent 独占的标签 ID，保留统计归属。导出读取原始文案，不带标签区分后缀。A2A 只导入卡片和客户端策略，不生成访问凭据；撤销的客户端不能通过包重新激活。实例没有 A2A 端点时，导出的 `{"enabled":false,"clients":[]}` 再导入或历史回退不会创建默认卡片；显式声明卡片字段或客户端时仍正常写入，已有端点也仍按声明更新。

结构错误返回 422，例如：

```json
{
  "error": "manifest schema validation failed\n... expected string, but got number",
  "code": "invalid_agent_manifest",
  "schema_url": "/api/agent-schema",
  "issues": [{ "path": "/configuration/persona", "keyword": "type", "message": "expected string, but got number", "schema_path": "/properties/configuration/properties/persona/type" }],
  "validation": { "valid": false, "instanceLocation": "/configuration/persona", "keywordLocation": "/properties/configuration/properties/persona/type", "error": "expected string, but got number" }
}
```

Git 的 `agent.json` 校验也返回相同结构。返回校验器的完整错误文本、全部叶子问题和标准 DetailedOutput 错误树，不再截断为前 20 条。校验器消息可能包含提交值，仅返回当前调用者，不写入客户端错误遥测；上传超限返回 413、类型不支持返回 415、缺失文件或包内数据不合法返回 422。JSON Schema 负责结构规则；文件系统关系、字节限制和运行环境规则继续由代码负责。

## ZIP 更新已有 Agent

`POST /api/agents/{id}/source/preview` 同时接受 JSON Git 版本请求和 ZIP 上传：`application/zip` 或 multipart `file`。ZIP 预览适用于现有本地和手动创建的普通 Agent，返回 `configuration_changes`、配置包摘要及 actor/Agent 绑定的 `preview_id`。Git 来源只允许按仓库版本发布，ZIP 预览或尚未应用的 ZIP 确认返回 409；已经应用的确认仍可幂等重放。服务端自动管理的 Agent 不开放此操作。

确认统一使用 `POST /api/agents/{id}/source/sync`。确认时锁定预览、Agent 和来源，重查管理权限、过期时间与当前配置摘要；配置在预览后变化会拒绝发布，要求重新预览。新来源仅在确认事务内创建，预览不写入 Agent 配置；重复确认返回原回执。创建接口拒绝发布预览，发布接口拒绝创建预览。

原本没有来源的 Agent 首次 ZIP 发布后记录为本地来源。来源管理的专属 skills 按包更新；包中引用的现有 workspace skills 原位更新，未被包引用的手动分配 skills 保留。

## Git 版本发布与历史回退

Git 创建保留工作区 GitHub 连接、仓库 owner/name、规范仓库链接、选定 ref 及实际提交 SHA。创建和发布共用分支／Tag／Commit 选择器；`GET /api/workspaces/{id}/github/branches` 与 `GET /api/agents/{id}/source/branches` 同时返回 `branches` 和 `tags`。分支与 Tag 分别使用 `refs/heads/<name>`、`refs/tags/<name>`，避免同名歧义；Commit 输入接受 SHA，服务端统一解析并固定为完整提交 SHA。GitHub commits API 使用 `heads/<name>`／`tags/<name>` 查询，见 [GitHub Get a commit](https://docs.github.com/en/rest/commits/commits#get-a-commit)。

发布页持续显示仓库链接和最近发布的 ref/SHA。选择版本后调用 `POST /api/agents/{id}/source/preview`，请求为 `{ "ref": "refs/tags/v1.0" }`，先在全局 diff 弹窗中核对，再用 `preview_id` 确认。预览后分支或 Tag 移动不改变本次确认的内容。

`agent_source_preview` 中已经应用的记录同时构成发布历史，不按预览的 30 分钟有效期清理。创建、发布、回退与记录写入处于同一事务；失败不追加历史，重复确认不产生重复记录。记录保留仓库内容快照，并在 `snapshot.published_definition` 保存发布后通过统一 Export/Import 契约校验的可移植配置快照；不复制目标环境凭据。这样回退可以恢复旧配置值，而不是保留新版本后来添加的值。专属 skill 的快照保留目录和内容，允许重建已被后续版本删除的技能；共享技能继续按 scope/skill_id 与当前权限校验，未被包管理的手动关联按原导入规则保留。

管理者通过 `GET /api/agents/{id}/source/publications?before=<publication-id>` 分页查询历史，每页最多 50 条。返回 `publications` 和 `next_cursor`，条目包含 `id`、`source_type`、`repository_url`、`ref`、`commit_sha`、`published_at`、`published_by`、`author_name`、`changed`、`rollback_of`、`has_configuration_snapshot`、`initial_publication`。列表不返回完整配置快照；游标和历史目标必须属于当前工作区及 Agent。仓库与发布配置的序列化快照共用 64 MiB 上限，超限会明确报错并回滚本次写入。

点击“回退到此版本”使用同一预览端点，提交 `{ "publication_id": "<history-id>" }`，不得同时提供 `ref`。服务端读取该节点保存的内容，重新核验当前 GitHub 连接的仓库访问权限，不再解析旧 ref 或下载旧提交，因此旧分支删除、Tag 移动不影响已有快照。响应的 `rollback_of` 必须匹配请求的历史 ID，前端拒绝忽略该字段的旧服务端响应。回退预览的基础 `definition` 同样保存完整执行配置，滚动部署期间旧副本处理确认也不会丢失需要清空的字段。查看差异后仍通过原 `/source/sync` 确认，校验当前配置、权限及资源绑定，成功后新增带 `rollback_of` 的历史节点，不重写旧历史或 Git 仓库。缺失或已变更的密钥、外部账号认证仍需在目标环境处理。

本次能力上线前的历史记录仅有当时保存的包声明，没有可还原的完整平台配置。此类条目明确提示限制，回退只恢复已记录的声明；包中未声明的配置保留当前值。不能为旧历史补造未知的配置值。

### 导出后更新原 Agent 的 Skill 身份

v2 导出的每个 `skills[]` 项保留 `scope + skill_id`，`path` 仅用于定位包内文件：

```json
{
  "scope": {"type": "workspace", "id": "00000000-0000-4000-8000-000000000001"},
  "skill_id": "00000000-0000-4000-8000-000000000002",
  "path": "skills/review",
  "name": "review",
  "enabled": true
}
```

`scope` 和 `skill_id` 必须同时提供或同时省略；同一个包不能声明重复身份。新编写的模板可以省略身份。导出包发布回原 workspace 的原 Agent 时，服务端按身份查找该 Agent 已绑定的 Skill；正文、描述、名称和附属文件相同则复用，不重写内容或新建副本。有变更则显示现有内容与包内容的 diff，确认后更新原 Skill，保留 Skill ID、共享／专属属性及其他 Agent 的绑定。目录改名不会改变已有来源 Skill 的 ID。

预览与确认使用相同的身份解析，并锁定被引用的 Skill、文件所属行和绑定状态。预览后正文、附属文件、配置或绑定变化时拒绝过期确认。修改共享 Skill 复用原有 Skill 管理权限：作者或 workspace owner/admin；管理 Agent 本身不等于有权修改他人创建的共享 Skill。同 workspace 的显式 Skill 身份不存在、未绑定当前 Agent 或属于其他 Agent 专属时返回错误，不悄悄创建替代品。

已下载的旧平台导出包没有这两个字段时，仅从固定的 `workspace-skills/<skill-id>` 目录恢复当前 Agent 已绑定的本 workspace Skill；其他路径不按名称推断身份。新建 Agent 和跨 workspace 创建仍生成目标 Agent 的专属副本，不凭来源 UUID 改写其他 workspace。此处的身份是内容资源定位，不代表账号或权限授权。

## Builder 完整配置包

Builder 回复末尾输出 `<agent_package>{"manifest":{...},"files":{"AGENTS.md":"..."}}</agent_package>`。manifest 使用同一 v2 Schema；files 包含所有声明引用的 UTF-8 文件。`agent.json` 由 manifest 生成，`agent.schema.json` 使用服务端权威版本，files 中不能覆盖这两个保留文件。旧客户端仍可读取同时输出的 `<agent_draft>` 摘要。

创建页面提交 `POST /api/workspaces/{id}/agent-packages/prepare`，请求体就是上述 manifest/files 对象。服务端先校验 manifest，再生成并按同一 ZIP 解析器校验包，保存预览快照。`GET /api/workspaces/{id}/agent-packages/{previewId}/download` 下载该调用者的已校验快照；创建仍使用 `POST /api/workspaces/{id}/agent-packages`，不会退回普通表单创建接口。

编辑包或收到新回复后必须重新预览。完整包文本跟随会话草稿保存，离开后可恢复。Builder 的服务端提示词包含当前权威 Schema；新页面发送 `multica.agent-package/v1` 协议标记，旧会话仅在其创建者继续使用新协议发送消息时更新隐藏 Builder 提示词，不改写普通 Agent。

导入及发布页面持续显示完整错误详情和当前 `/api/agent-schema` 下载链接。JSON 语法错误给出字节位置和解析原因；包路径、缺失文件、大小与运行时兼容错误给出对应原因。内部持久化异常保留服务端错误边界，不返回数据库语句或凭据。

## 目录与顶层结构

```text
my-agent/
├── agent.json
├── agent.schema.json
├── AGENTS.md
└── skills/
    └── code-review/
        ├── SKILL.md
        ├── references/conventions.md
        └── scripts/check.py
```

可直接上传的完整测试目录：[`examples/package-inspector-agent`](examples/package-inspector-agent/README.md)。扩展字段说明样例：[`examples/agent-manifest-v2.json`](examples/agent-manifest-v2.json)。使用时复制为仓库根目录的 `agent.json`，复制权威 Schema，并提供指令和 skill 文件。样例中的地址与资源别名是占位数据，需要在目标环境配置。

| 顶层字段 | 内容 |
| --- | --- |
| `$schema`、`version` | Schema 文件名和契约版本 |
| `name`、`description` | Agent 名称与描述 |
| `instructions` | 用户指令文件的仓库相对路径 |
| `skills` | 仓库内 skill 目录、名称、描述、启停状态；平台导出附带 `scope` 与 `skill_id` |
| `configuration` | Agent 配置，字段名沿用现有 API |
| `disabled_runtime_skills` | 禁用的运行时自带 skills，不复制运行时自带内容 |
| `dsh_plugins` | 工作区 DSH 插件引用及启停状态 |
| `okrs` | 目标和关键结果定义 |
| `bindings` | 运行时、电脑、身份、账号和机器人所需的目标环境资源 |
| `access` | 调用权限策略 |
| `a2a` | A2A 与托管 MCP 共用的端点配置、客户端策略 |

`$schema`、`version`、`name`、`instructions`、`skills` 必填。`skills: []` 合法。其他部分可省略，不使用 Schema `default` 自动补全配置。未知的业务字段拒绝；仅 provider 原生配置和 A2A 标准扩展明确保留扩展字段。

## 与当前配置界面的映射

下表定义 v2 与平台配置的映射。配置写入与导出使用当前平台数据；外部资源引用通过目标环境设置处理。

| 界面分区 | v2 字段 | 现有模型与边界 |
| --- | --- | --- |
| 数字员工：资料 | `name`、`description`、`configuration.avatar_url` | `Agent.name/description/avatar_url`；头像沿用 emoji/URL 表示，资源上传另外处理 |
| 数字员工：人设、语气 | `configuration.persona/reply_tone` | Coordinator 的人设与语气，独立于执行任务使用的 `instructions` |
| 数字员工：入站判断 | `configuration.inbound_coordinator` | 先判断还是直接执行 |
| 数字员工：消息响应 | `configuration.dingtalk_response_enabled/dingtalk_show_ai_tag` | 统一响应与 AI 标记 |
| 数字员工：会话与跟进 | `configuration.chat_session_resume/dispatch_always_new_issue/task_finished_loop_enabled` | 会话续接、每条消息新建任务、任务完成后跟进 |
| 场景记忆行为 | `configuration.scene_memory_write_enabled/scene_memory_recall_enabled/scene_memory_ui_enabled/scene_memory_bootstrap_enabled` | 仅行为设置，不包括记忆记录 |
| 指令 | `instructions` | `Agent.instructions`；平台内置 `system_instructions` 不可覆盖 |
| 机器人接入：指令分段 | `configuration.dispatch_prompt_overrides` | `policy`、`reply_formatting`、`enterprise_identity`、`scene_graph`；禁止覆盖 `context` 与 `dingtalk_conversation` |
| OKR | `okrs[].objective/key_results` | `SetAgentOKRsRequest`；数组顺序决定顺序，标签和用量由平台管理 |
| Skills | `skills[]` | 正文与配套文本文件；创建时生成专属 workspace skill，导出后更新原 Agent 时按身份复用原 Skill |
| Skills：运行时自带 | `disabled_runtime_skills[]` | `root/key/plugin/provider` 与目标运行时引用；只保存禁用选择 |
| DSH 插件 | `dsh_plugins[].ref/enabled` | 工作区插件关联，插件实体和产物仍在工作区管理 |
| MCP 工具 | `configuration.mcp_config` | 保留当前 `mcpServers` 和 `mcp` 两种容器及 provider 扩展 |
| Composio 工具 | `configuration.composio_toolkit_allowlist` | 仅工具选择，使用目标 owner 已授权连接，沿用 owner-only 权限 |
| 运行时 | `bindings.runtime` | 解析到 `runtime_id`；`provider/runtime_mode` 是兼容要求，不修改 Runtime 实体 |
| 模型与执行参数 | `configuration.model/thinking_level/service_tier/max_concurrent_tasks/custom_args` | 模型、思考强度、服务等级、并发和自定义参数 |
| 环境变量 | `configuration.custom_env` | 普通字符串或 `secret_ref`；使用独立 env 权限与审计流程 |
| OpenClaw 运行配置 | `configuration.runtime_config.mode/gateway` | `local/gateway`；网关 host、port、tls 与 token 引用 |
| 我的电脑 / 本机 MCP | `bindings.runner` | 已授权电脑挂载；本机 MCP 清单由电脑提供，不复制发现结果 |
| 企业数字员工 | `bindings.enterprise_identity` | 目标环境中已授权企业身份，不复制 BUC 授权或身份 ID |
| GitHub 执行身份 | `bindings.github_identity` | Agent 执行身份；与读取 Agent 仓库的 GitHub App 连接分别处理 |
| 钉钉账号 | `bindings.dingtalk_account` | 目标账号绑定，不复制 Token，也不自动接管已有绑定 |
| 机器人接入 | `bindings.bots[].platform/ref` | 当前 DingTalk、Lark、Slack、WeCom 集成引用 |
| MCP 接入 / A2A | `a2a` | 两个页面共用 A2A 端点与客户端凭据体系；不另造 MCP 开关或令牌模型 |
| 访问权限 | `access.permission_mode/invocation_targets` | `private/public_to` 与目标授权，沿用 owner-only 权限；`visibility` 是派生值 |
| LLM Trace | `configuration.runtime_config.llm_trace` | `enabled/sink_url`，仅支持的云端运行时生效 |
| 导出 / 发布 | 无配置字段 | 操作入口；Git 来源、分支、SHA、预览、发布回执属于实例部署记录 |

源代码依据：`packages/core/types/agent.ts`、`packages/core/types/agent-a2a.ts`、`packages/views/agents/components/agent-config-navigation.ts`、各 tab 与对应 handler。Schema 测试对 `UpdateAgentRequest` 做字段覆盖检查，新增可写字段时需显式分类。

## 引用与凭据

资源引用是逻辑别名，例如 `bindings.runtime.ref: "review-runtime"`。创建时由用户选择目标工作区中的实际资源；适配器保存别名与实例资源的映射。禁止把别名当成资源名称直接模糊匹配后自动授权。

- `bindings.runtime` 还可声明 `provider` 和 `runtime_mode` 兼容要求。新建 Agent 最终必须选择可用运行时。
- `disabled_runtime_skills[].runtime_ref` 标识该禁用记录所需的运行时。它可以表示已有配置中不同运行时的记录，需要分别解析；当前实际运行时切换后仍应按 runtime/provider 过滤。
- `dsh_plugins[].ref` 对应已存在、可访问的工作区插件，要求目标运行时支持 DSH 插件。
- `access` 中工作区授权使用 `{ "target_type": "workspace" }`，指当前目标工作区；成员或 team 使用 `{ "target_type": "member", "ref": "review-member" }` 等逻辑引用。当前 team 是保留且不生效的类型，不得自动扩展为成员授权。
- 账号、机器人、电脑的绑定或解绑需要使用各自现有权限及接管校验。manifest 的存在不构成新授权。
- owner 默认是目标实例的创建者，后续发布不转移 owner；源实例的 owner、workspace、Agent ID 都不进入 manifest。

环境变量、MCP 请求头和参数中的凭据使用完整值引用：

```json
{
  "custom_env": {
    "LANGUAGE": "zh-CN",
    "SERVICE_TOKEN": { "secret_ref": "service-token" }
  },
  "mcp_config": {
    "mcpServers": {
      "service": {
        "url": "https://tools.example.test/mcp",
        "headers": {
          "Authorization": { "secret_ref": "service-authorization-header" }
        }
      }
    }
  }
}
```

`secret_ref` 替换整个值，不进行 `${...}` 字符串插值；例如 Authorization 所引用的密钥值应包含完整的授权头。引用必须在目标环境解析成功后才允许发布。实际密钥不写入 manifest、预览、Git diff 或发布回执。`***`、`****` 这类界面掩码不能当作可移植的环境变量值；无法读取或无法分类的值应保留为待绑定项，不能导出为空值后清掉配置。

Schema 对已知网关 Token 等位置强制使用引用；provider 扩展允许普通字符串，JSON Schema 本身不能识别任意字符串是不是密钥。导出器通过明确的公开字段分类和引用替换处理 MCP/runtime JSON；预览不回显配置值。新增 provider 字段时必须维护该分类，不能因为 Schema 合法就原样导出整份 JSON。

## 发布与清空语义

以下规则用于 v2 的导出、预览、创建和发布；跨环境绑定按上述明确延后机制处理：

1. `name`、`instructions`、`skills` 是必填的仓库管理内容，发布时按仓库更新。名称不再沿用 v1 的“仅创建时使用”规则；`description` 出现时也按仓库更新。
2. 可选配置字段省略表示仓库不管理该字段：新建时用目标平台默认值，发布时保留现有值。为了完整恢复，导出器需要显式输出能够读取的配置值，不能把 `false`、空字符串或空数组省略。
3. `false` 明确关闭。`persona/reply_tone/model/thinking_level/service_tier/avatar_url` 的空字符串明确清空；模型相关空字符串恢复运行时默认选择。
4. `custom_args`、`custom_env`、`runtime_config`、`mcp_config`、`dispatch_prompt_overrides`、工具列表、OKR 等一旦出现，按各自完整字段替换；空数组或空对象表示清空该字段。分组对象 `configuration`、`bindings`、`a2a` 只组织其内部已声明字段，不能清空整个实例。
5. `mcp_config: null` 表示恢复 provider 默认 MCP 配置；`composio_toolkit_allowlist: null` 表示清除托管选择。可为空的账号、电脑绑定使用 `null` 明确解绑。其余位置不接受 `null`。A2A 客户端两个限额允许 `null`，表示清除限额。
6. skills 只增删改当前来源管理的专属 skills，保留手动添加的其他工作区 skills；导出则包含当前全部已分配 skills 及启停状态，重新创建时转为新 Agent 的专属 skills。
7. A2A 客户端使用 manifest 内稳定的 `key` 建立来源映射，避免每次发布重复创建。`clients: []` 只清理来源管理的客户端策略，保留手动客户端。省略仍表示不管理；撤销、凭据轮换遵循现有生命周期，不能自动恢复已撤销凭据。
8. 权限和绑定变更必须出现在预览里，确认时重查原有 owner、workspace 和资源权限；不能通过普通 Agent 更新接口绕过 env、Composio、账号、A2A 等独立权限与审计要求。A2A 导入只允许人类调用方，策略记录使用当前操作人的身份。预览应区分 Git 文件差异、实际配置覆盖、待绑定资源，不显示密钥值。

## 结构校验与运行校验

Schema 已约束：字段类型、未知字段、必填项、相对路径、skill 启停状态、最大 20 skills、人设 400 字符、语气 200 字符、并发 1–50、最多 10 个 OKR 且每个最多 10 个 KR/120 字符、可改指令分段及 32000 字符上限、A2A scopes/元数据/限额、端口和私有权限空授权集合。

以下不能仅靠标准 JSON Schema 完成，必须在 v2 适配器中验证：

- 文件真实存在、UTF-8、Git 对象、字节数、skill 路径唯一与不重叠、指令不在 skill 目录内、每个目录唯一 SKILL.md；沿用单文件 1 MiB、单 skill 8 MiB、200 个配套文件、总内容 32 MiB 限制。
- manifest/skill 描述的 4000 UTF-8 字节限制，运行时 skill key/name 的 512 字节限制。JSON Schema `maxLength` 计字符，不能代替字节限制。
- 对象数组中的业务键唯一，例如 skill path、插件 ref、A2A client key、card skill id，以及 OKR 文本规范化后的唯一性。
- provider、模型、思考强度、服务等级、MCP transport、DSH 插件、LLM Trace、运行时 skills 等能力兼容；Schema 不冻结模型列表。
- A2A media modes 必须受当前端点能力支持；每个 skill 的 `securityRequirements` 不属于当前可配置项，端点安全策略由平台生成。
- 目标资源存在性、绑定范围、调用者权限、功能开关，以及预览确认之间的并发版本变化。

验证命令：

```bash
python3 -m pip install -r scripts/agent-schema-requirements.txt
python3 scripts/test-agent-schema.py
cd server
GOTOOLCHAIN=auto go test ./internal/agentsource
```

## 成对的导入／导出业务入口

业务入口统一位于 `server/internal/handler/agent_package_service.go` 的 `agentPackageService.Import` 和 `Export`。本地 ZIP、Git、Builder 各自负责取得并校验文件，生成同一种 bundle；创建和已有 Agent 发布随后进入同一个 `Import`。HTTP 层负责鉴权、预览确认、事务和发布记录，配置写入由内部模块调用原有业务方法及事务查询完成。导出由同一个服务读取当前数据库快照，再交给底层 ZIP 编码器生成下载文件。

`agentPackageFields` 是两个方向共用的唯一注册表，包含名称、描述、指令、Coordinator Contract、配置、资源声明、运行时 skill 禁用项、插件、OKR、调用权限、A2A 和 skills。各模块必须实现 `PackageCodec[Context, Value]`：`Import(*Context, Value) error` 与 `Export(*Context) (Value, error)`；缺少任何一侧，或者两侧 Value 类型不同，注册时无法通过 Go 编译。添加业务模块时不能另建单向分派列表。

编译器保证方法成对和类型一致；业务字段是否完整、清空语义和往返后的值是否正确，由 Schema 对照注册表的覆盖测试及数据库导入／导出测试验证。动态 provider 配置仍由 JSON Schema 校验，接口类型检查不能证明全部业务语义。

## 发布预览

点击“预览变更”并取得结果后，直接打开全局大弹窗：左侧按目录选择变更文件，右侧对比当前版本与待发布版本，支持展开未修改行。Git 文件变更与配置变更在同一弹窗中切换，发布页不再嵌入 diff 或提供二次放大操作。关闭弹窗后在发布页确认发布。新增、修改、删除分别标记；未提供文本内容的 Git 文件只展示对象哈希和文件模式，不将缺失文本视为空文件。

## 资源绑定的完成与复用

`agent_source.package_binding_state` 保存待配置声明、逻辑引用到已核验资源的映射、确认回执及密钥引用路径，不保存 Token、Cookie、账号密码或密钥值。历史导入尚无该状态时，从最新已应用的包快照恢复未完成声明，恢复过程不自动创建授权回执。

1. 创建时继续选择目标运行时，填写新密钥并确认延后资源配置。存在待配置项时，创建完成进入“配置 → 管理 → 发布”。其他配置页也展示待完成提示。
2. 点击“前往配置”，使用现有 GitHub OAuth、企业身份、钉钉账号、机器人、电脑、插件或调用权限配置流程。认证、账号接管与权限检查仍属于这些业务接口；包声明本身不会授予权限。
3. 包声明 `dsh_plugins: []` 且当前插件也为空时，没有待确认的资源引用，直接显示已完成；当前非空、不可用或仍有未核验引用时不自动确认。返回发布页，展开“已导入包的资源绑定”并刷新状态。`GET /api/agents/{id}/package-bindings` 返回已导入声明、当前同一 Agent 的资源、资源指纹和状态版本。GitHub 查询外部身份服务，钉钉账号核验实际路由状态，企业身份检查保存状态及有效期；核验失败显示不可用，不当作已绑定。
4. 显式选择每个逻辑引用对应的已配置资源，调用 `POST /api/agents/{id}/package-bindings/confirm`，提交 `path`、`revision`、`current_fingerprint`、`mappings`。服务端重新鉴权及核验当前资源，检查类别、启停值、权限目标、引用一一对应和版本。该接口只记录对应关系，不复制凭据或搬移账号。
5. 后续发布时，相同声明与仍有效的已确认资源可直接复用；连接变化、撤销、过期或新别名需要重新配置并确认。未声明的绑定保留。`null` 解绑请求通过原配置入口完成后再确认，不直接调用全局账号撤销。
6. 同一 Agent 的密钥只在每个引用位置均与已保存别名一致、当前配置中仍有对应值时复用；改名、新路径、跨 Agent 导入仍需提供值。一个别名在多个路径对应不同值时不复用。
7. 导出保留未完成声明和原密钥别名，确认回执及目标资源凭据不进入 ZIP。已确认后发生的普通配置变更按当前业务值导出，仍匹配的资源保留原别名。GitHub 身份不在数据库快照内，导出保留其已声明需求，不能据此认定外部身份仍有效。

资源仍未完成配置时，Agent 可以保存和发布定义；发布成功不表示所有外部依赖已认证。完整的待配置状态可以在发布页持续查询。

## 历史记录

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-09-08 | 在同一自包含 Schema 中保留 v1 并新增 v2；补齐当前 Agent 配置、资源引用、权限和 A2A/MCP 策略的定义与字段映射 | v1 仅覆盖指令和 skills，不能表达配置完整的 Agent；先冻结扩展契约，避免旧导入器静默丢字段 |
| 2026-09-08 | 接入 Go JSON Schema 校验器、统一 ZIP/Git 解析、延迟读取文件和上传预览；路径正则改为 Go/JS/Python 共用语法，保留原有路径约束 | 让实际代码直接执行 Schema 契约，按 manifest 解析 Agent 包；避免手写结构规则漂移与校验前读取引用内容 |
| 2026-09-08 | 平台导出改为当前数据库快照的 v2 Agent ZIP，增加配置、策略和待绑定引用说明；MCP 字符串命令支持整体密钥引用；创建页增加权威 Schema 下载入口 | 明确导出对象是平台当前 Agent，补齐旧源码导出遗漏的配置，并让创建者直接获取校验契约 |

- 2026-09-08：补齐本地 ZIP 创建入口，Git 与本地统一为 bundle 预览和事务创建；接入 v2 配置发布、目标环境引用选择、完整测试包和 Builder 协议说明。原因：以平台上传包为唯一公开契约，取消对 DTA 构建产物的依赖，并避免新字段在创建或发布时丢失。

- 2026-09-10：增加已有 Agent 的 ZIP 发布、Builder manifest/files 预览及下载；OKR 原始文案与独占标签分开保存；返回完整 JSON Schema 校验结果和 Schema 链接。原因：补齐包的创建更新闭环，允许模板重复导入，并让导入错误可以直接定位和修正。

- 2026-09-11：统一业务 Import／Export 与成对模块注册表；持久化资源声明、绑定核验回执和密钥别名，接入待配置界面及同一 Agent 的安全复用。原因：消除创建、更新与导出的独立分支，防止新增导入能力遗漏导出，并补齐认证资源的导入后配置闭环。

- 2026-09-11：补齐 `configuration.event_trigger_enabled` 的校验与双向持久化，复用事件触发服务的事务方法；开启时启用入站协调，显式关闭入站协调优先。原因：主干新增的开关必须随当前 Agent 配置一起导出和恢复，发布回滚不能遗留事件触发副作用。

- 2026-09-11：v2 skills 增加成对的 `scope`／`skill_id`，预览和发布共同解析现有 Skill 身份，保留旧平台导出路径的有限身份恢复。原因：修复导出后更新同一 Agent 时，手动绑定的 workspace Skill 被重复创建为专属副本的问题；同时保留原文件 ID、权限边界和并发修改检查。

- 2026-09-11：ZIP 解析增加共同外层目录识别和 macOS 压缩元数据过滤，并补齐缺失或歧义入口的错误详情。原因：直接压缩 Agent 文件夹会生成 `目录/agent.json`，旧解析器仅查询 ZIP 最外层，导致有效配置包在预览时错误提示缺少 `agent.json`。

- 2026-09-14：ZIP 和 Git 发布预览共用文件导航与左右分栏 diff，支持放大和展开未修改行，保留缺失文本的元数据提示。原因：并排原文难以定位具体变更，发布前需要逐文件核对实际差异。

- 2026-09-14：预览成功后直接打开全局大弹窗，移除页内 diff 和二次放大入口。原因：用户要求一次点击“预览变更”即进入完整对比视图。

- 2026-09-14：预览的 `requirements.binding_declarations` 返回本次包实际声明的资源需求，保留显式清空值；已导入绑定记录移入发布页底部的折叠区域。原因：选包前不应将旧包绑定展示为本次导入需求，ZIP、Git 与 Builder 必须使用同一份解析结果。

- 2026-09-14：Git 来源改为仅按分支／Tag／Commit 发布，增加持久化发布历史、完整可移植配置快照及历史回退预览。原因：保持 Agent 与仓库版本对应，回退不受远端引用移动影响，也不应遗留新版本添加的配置；旧记录缺失的值明确提示而不伪造。

- 2026-09-14：修正删除文件 `deleted` 状态的左右 diff、空插件声明的就绪判定和无 A2A 端点的导出回退形状；补齐 GitHub 连接上下文恢复与用户授权校验（见 GitHub 集成文档）。原因：修复真实 Git 创建、版本发布、历史回退验收中的差异。
