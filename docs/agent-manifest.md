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

服务端在同一次数据库只读快照中读取当前名称、描述、用户指令、23 项配置、全部已分配 skills 及其启停和配套文件、运行时 skill 禁用选择、DSH 插件关联、OKR、调用权限、平台保存的运行时/电脑/企业身份/钉钉账号/机器人绑定，以及 A2A 卡片和客户端策略。Git 来源的已发布记录只用于保留文件目录布局，内容以平台当前保存值为准，不读取远端 Git，不重新下载历史版本。

ZIP 包含 `agent.json`（v2）、`agent.schema.json`、指令文件、skill 文件以及 `EXPORT-NOTES.json`。后者提供资源引用的类别和显示名、待绑定值的 JSON 路径和说明；它是导出说明，不参与 Agent 配置写入。运行时/身份/插件实体和凭据仍由目标环境提供。GitHub 执行身份存放在外部身份服务，不属于本地数据库快照，导出说明明确要求另行检查和绑定；manifest 不将未知状态写成 `null`。

不打包任务、聊天、记忆记录、访问令牌、平台生成的系统指令或 Git 发布记录。用户编写的指令、skill 正文与配套文件保持原文。环境变量、请求头、命令参数值和未分类的 provider 字符串使用 `secret_ref`；已知公开的传输类型、可执行文件名和无认证信息/查询参数的服务 URL 保留。普通参数值也可能需要重新绑定，避免把无法分类的值当作可公开内容。包含内联参数的 MCP 字符串命令可以整体用 `secret_ref` 表达。

Agent 创建页面共用顶部栏提供 **下载 Schema**，选择创建方式、手动创建、AI 创建和从 Git 创建页面均可见。`GET /api/agent-schema` 返回服务端实际内置的完整 Schema，下载文件名为 `agent.schema.json`；无需先创建 Agent 或选择 Git 连接。前端验证响应类型和 Schema 外层结构，下载原始内容。

导出完成前使用统一包解析器校验，不返回静默丢字段或文件的包。当前 v2 包支持下载、校验、预览和确认创建。创建页提供“从本地导入”；Git 和本地共用配置预览、运行时选择及确认表单。

## 统一包解析流程

Agent 包是包含 manifest 和其引用文件的 ZIP 源码目录，不需要 CLI build。入口仍为根目录 `agent.json`。包内 `agent.schema.json` 可以随导出提供给编辑器，但服务端不会使用上传者提供的 Schema 放宽规则，也不会请求它引用的外部 URL。

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

配置包导入的 OKR 按 Agent 独立创建标签，同一工作区可以重复导入相同目标文案。`agent_okr.authored_text` 保存原文，标签使用带 Agent 名称的唯一显示名；碰到普通或历史同名标签时另建标签，不接管原标签。再次发布或编辑相同目标时复用当前 Agent 独占的标签 ID，保留统计归属。导出读取原始文案，不带标签区分后缀。A2A 只导入卡片和客户端策略，不生成访问凭据；撤销的客户端不能通过包重新激活。

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

`POST /api/agents/{id}/source/preview` 同时接受 JSON 分支请求和 ZIP 上传：`application/zip` 或 multipart `file`。ZIP 预览适用于现有本地、Git 和手动创建的普通 Agent，返回 `configuration_changes`、配置包摘要及 actor/Agent 绑定的 `preview_id`。服务端自动管理的 Agent 不开放此操作。

确认统一使用 `POST /api/agents/{id}/source/sync`。确认时锁定预览、Agent 和来源，重查管理权限、过期时间与当前配置摘要；配置在预览后变化会拒绝发布，要求重新预览。新来源仅在确认事务内创建，预览不写入 Agent 配置；重复确认返回原回执。创建接口拒绝发布预览，发布接口拒绝创建预览。

现有 Git Agent 上传 ZIP 后保留 Git 连接、分支及最后发布的 Git commit；下次 Git 发布仍可比较 Git 基线及平台当前配置。原本没有来源的 Agent 首次 ZIP 发布后记录为本地来源。只替换来源管理的专属 skills，保留其他手动分配的 skills。

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
| `skills` | 仓库内 skill 目录、名称、描述、启停状态 |
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
| Skills | `skills[]` | 正文与配套文本文件；导入为该 Agent 专属的工作区 skill |
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

## 历史记录

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-09-08 | 在同一自包含 Schema 中保留 v1 并新增 v2；补齐当前 Agent 配置、资源引用、权限和 A2A/MCP 策略的定义与字段映射 | v1 仅覆盖指令和 skills，不能表达配置完整的 Agent；先冻结扩展契约，避免旧导入器静默丢字段 |
| 2026-09-08 | 接入 Go JSON Schema 校验器、统一 ZIP/Git 解析、延迟读取文件和上传预览；路径正则改为 Go/JS/Python 共用语法，保留原有路径约束 | 让实际代码直接执行 Schema 契约，按 manifest 解析 Agent 包；避免手写结构规则漂移与校验前读取引用内容 |
| 2026-09-08 | 平台导出改为当前数据库快照的 v2 Agent ZIP，增加配置、策略和待绑定引用说明；MCP 字符串命令支持整体密钥引用；创建页增加权威 Schema 下载入口 | 明确导出对象是平台当前 Agent，补齐旧源码导出遗漏的配置，并让创建者直接获取校验契约 |

- 2026-09-08：补齐本地 ZIP 创建入口，Git 与本地统一为 bundle 预览和事务创建；接入 v2 配置发布、目标环境引用选择、完整测试包和 Builder 协议说明。原因：以平台上传包为唯一公开契约，取消对 DTA 构建产物的依赖，并避免新字段在创建或发布时丢失。

- 2026-09-10：增加已有 Agent 的 ZIP 发布、Builder manifest/files 预览及下载；OKR 原始文案与独占标签分开保存；返回完整 JSON Schema 校验结果和 Schema 链接。原因：补齐包的创建更新闭环，允许模板重复导入，并让导入错误可以直接定位和修正。
