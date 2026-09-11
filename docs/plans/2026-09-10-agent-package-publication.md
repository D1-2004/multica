# Agent 配置包发布补齐

本文记录 2026-09-10 的实现与本地验收；可移植协议以 [Agent manifest](../agent-manifest.md) 为准。本次补齐 ZIP 更新已有 Agent、Builder 完整配置包、重复导入 OKR、完整校验错误四项能力。

## 已有 Agent 的 ZIP 发布

入口为 Agent 管理 → 发布。Git 来源可选择 Git 或 ZIP；本地来源和手动创建的 Agent 可上传 ZIP。

1. 上传 ZIP 到 `POST /api/agents/{id}/source/preview`，使用 `application/zip` 或单文件 multipart。原有 JSON 请求仍用于选择 Git 分支。
2. 服务端先使用内置 JSON Schema 校验 `agent.json`，再读取引用文件，生成统一 bundle。
3. 预览对比目标 Agent 的实际配置和来源管理的 skills，返回完整变更、依赖和有有效期的 `preview_id`。此时不更新 Agent，也不为手动 Agent 创建来源记录。
4. 用户确认后调用原有 `POST /api/agents/{id}/source/sync`。事务中重新检查管理权限、来源、当前配置状态和运行时兼容性，应用同一份预览快照。
5. 预览后发生编辑会返回冲突并要求重新预览；重复提交同一个已完成预览返回原回执。

发布保留 Agent ID、所有者、已有会话及目标环境绑定语义。ZIP 更新 Git Agent 时保留 Git 安装、仓库、分支和最后发布的 Git commit；后续 Git 预览分别呈现仓库变化和对当前 ZIP 配置的覆盖。手动 Agent 第一次确认 ZIP 发布时，才在事务中获得本地包来源。相同 skill 路径更新原记录，避免更换 skill ID。

只允许活跃的用户 Agent 使用此入口；平台管理模板、隐藏系统 Agent 继续由原有生命周期管理。

## Builder 输出完整包

Builder 现在输出完整的 `<agent_package>`，其中包含 manifest 和文件内容：

```json
{
  "manifest": {
    "$schema": "agent.schema.json",
    "version": "multica.agent/v2",
    "name": "代码审查助手",
    "instructions": "AGENTS.md",
    "skills": [],
    "configuration": { "persona": "仔细核对改动与证据" }
  },
  "files": { "AGENTS.md": "# 工作方式\n逐项检查改动，说明发现和依据。" }
}
```

- 隐藏 Builder 的提示词包含服务端当前完整 Schema；前端输入协议为 `multica.agent-package/v1`，携带当前完整包及 Schema。每轮修改应保留已有字段和支持文件。
- `files` 使用相对路径和 UTF-8 文本，必须包含所有引用文件；`agent.json` 和 `agent.schema.json` 由服务端生成，不能在 `files` 中重复指定。
- `<agent_draft>` 摘要暂时保留，供旧客户端识别。新版创建使用完整包，不再通过旧表单接口提交字段子集。
- 完整包在会话草稿中自动保存。前端保留原始 JSON，结构错误也能直接编辑修复；编辑后旧预览失效。
- 用户点击校验后调用 `POST /api/workspaces/{id}/agent-packages/prepare`。服务端构造 ZIP，复用本地上传的校验、解析、预览及确认链路。
- 预览成功可通过 `GET /api/workspaces/{id}/agent-packages/{previewId}/download` 下载 ZIP，也可选择运行时后通过统一 `POST /api/workspaces/{id}/agent-packages` 创建。
- 下载依据服务端持久化的作者快照，只允许该工作区内的预览创建者访问。真实密钥在确认阶段解析，不进入下载包。
- 旧隐藏 Builder 仅在所有者通过新版协议继续对话时更新契约，不批量改写普通 Agent 或其他人的 Builder。

Builder 获得工作区 skill 目录并不等于获得完整内容。复用现有 skill 时仍需通过已有权限读取文件；无法获取时应说明缺失，不能用 skill ID 或占位文案假装构造完整包。模型实际生成质量和单轮输出长度仍受所选运行时约束，最终以服务端校验结果为准。

GitHub 身份、钉钉账号、机器人、电脑、插件、成员授权仍使用现有目标环境绑定机制，本次不新增自动账号接管或授权。密钥使用 `secret_ref`，实际值在导入确认界面提供。

## 重复导入的 OKR

同一个模板可重复导入，不再因为不同 Agent 的 O/KR 同名而失败。

- 新增 nullable `agent_okr.authored_text` 保存原始文案。旧记录为空时继续从原标签名读取。
- 导入的 Agent 使用各自独占标签，优先使用带 Agent 名称的可读标签。被普通标签或历史标签占用时分配独立名称，不能接管旧标签。
- 更新同一 Agent 时，按规范化文案和 O/KR 类型复用它已有的独占标签，保留标签 ID 及关联任务统计。
- 配置界面和导出使用原始文案，不把用于区分实例的标签后缀写回 manifest。
- 已导入 Agent 后续在普通 OKR 编辑界面操作也继续使用上述规则。移除 OKR 后保留历史标签，不删除已有任务上的标记。

数据库变更为 `9222_agent_okr_authored_text`，上线需要先应用 up 迁移。已有记录不需要回填。down 迁移在存在非空原始文案时拒绝删除列，避免降级导致原文丢失。此次仅在隔离的本地测试库应用迁移，未对预发或生产数据库执行。

## 错误反馈

JSON Schema 错误返回 HTTP 422，包含：

- `error`：完整 validator 错误树文本。
- `issues`：全部叶子错误的字段路径、规则、消息、Schema 路径，不截断为前 20 条。
- `validation`：validator 的原始 detailed output。
- `schema_url`：`/api/agent-schema`，指向当前服务端权威 Schema。

JSON 语法错误显示位置和解析原因；无效 ZIP、缺少文件、不安全路径和大小限制显示具体原因。创建、Builder、Git 发布和 ZIP 发布都使用持续可见、可滚动和复制的错误面板，并附带当前 Schema 下载链接。Schema 校验错误可能引用用户输入，因此客户端遥测只记录稳定错误码，不记录整段校验文本。

权限、GitHub 可访问性、预览过期和并发冲突仍按现有 HTTP 状态区分。服务端内部数据库失败不直接透出数据库内容。

## 源码入口

| 范围 | 文件 |
| --- | --- |
| Schema 与 ZIP 构造 | `server/internal/agentsource/schema.go`、`builder.go`、`package.go` |
| Builder 预览和下载 | `server/internal/handler/agent_builder_package.go` |
| ZIP 发布、共享确认 | `server/internal/handler/agent_package_publish.go`、`agent_source_sync.go` |
| OKR 文案和标签隔离 | `server/internal/handler/agent_package_okr.go`、`agent_package_configuration.go`、`agent_okr.go` |
| Builder 页面 | `packages/views/agents/create/builder-package-panel.tsx`、`builder-workspace.tsx` |
| 发布与错误面板 | `packages/views/agents/components/tabs/zip-publish-tab.tsx`、`packages/views/agents/create/package-error.tsx` |

内置 `multica-creating-agents` skill 及其来源说明已同步此协议。

## 本地验证与边界

- Go agentsource 测试通过，涵盖完整 Builder 包、缺少文件、路径检查及超过 20 条 Schema 错误。
- 隔离数据库上的相关 handler 回归 41 项通过，涵盖 ZIP/Git 创建及发布、完整导出、OKR、Builder 和聊天兼容。追加验证了 ZIP 发布后再次 Git 分支预览仍保持正确基线。
- Core、页面相关测试和 Web 类型检查通过；前端完整 lint 通过，保留仓库已有 warnings。
- 服务端 `./cmd/server` 构建通过。
- 迁移测试中的 `TestMigrationNumericPrefixesStayUniqueAfterLegacySet` 被基线已有的两个 `9093` 文件阻断：`9093_agent_inbound_coordinator` 与 `9093_hosted_site_workspace_user_index`。已确认二者在本次起点 HEAD 中存在；不是新增 `9222` 引起，此次未修改无关迁移。
- sqlc 按隔离测试库真实 schema 定向生成 OKR query/model，未改动其他生成文件。

以上是本地自动化及数据库链路验证，不等于真实 LLM Builder 会话或预发浏览器验收。截至 9 月 10 日本地验收时尚未提交、推送或部署，也未修改真实用户 Agent。此前 9 月 8 日的预发验收属于旧版本，不能作为本次新能力已上线的证据。

## 历史记录

- 2026-09-10：补齐已有 Agent ZIP 发布、Builder 完整包、OKR 重复导入与完整错误反馈。原因：使平台创建、导出和更新共享同一份可校验配置包，同时保留权限、并发保护及目标环境绑定边界。

- 2026-09-11：发布前将尚未部署的 OKR 迁移从 9164 调整为 9222。原因：最新预发 release 已使用 9164，且后续迁移已推进至 9221；本次 SQL 内容不变，继续沿用 Agent 配置包变更单 36017688 发布。
