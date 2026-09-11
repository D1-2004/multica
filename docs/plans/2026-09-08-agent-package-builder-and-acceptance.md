# Agent 包导入与 Builder 链路

> 2026-09-10 更新：本文中的 Builder 草稿边界及预发验收是 9 月 8 日版本记录。当前实现见 [配置包发布补齐](2026-09-10-agent-package-publication.md)，协议以 [Agent manifest](../agent-manifest.md) 为准。

## 本次实现

公开导入协议以根目录 `agent.json` 和引用文件为准，结构由服务端内置的 `server/internal/agentsource/agent.schema.json` 校验。创建页面新增“从本地导入”，ZIP 与 Git 只在获取文件的方式上不同：两者都生成完整 `agentsource.Bundle`，持久化预览，再走同一确认接口和数据库事务。

Git 通过 GitHub App 安装凭据读取所选分支对应的确定提交和文件，不借用 Agent 的执行身份，不依赖 DTA CLI 编译。ZIP 在内存中读取，不向服务器磁盘解压或执行脚本。完整协议见 [Agent manifest](../agent-manifest.md)。

源码位置：

- `server/internal/agentsource/{package,portable,snapshot}.go`：获取文件、Schema 校验、bundle。
- `server/internal/handler/{agent_package,github_agent_source,agent_package_configuration,agent_package_sync}.go`：持久预览、配置解析、事务创建及发布。
- `packages/views/agents/create/source-create-agent-page.tsx`：本地与 Git 共用的预览、运行时和确认表单。
- `docs/examples/package-inspector-agent/`：可直接打包上传的测试智能体，附带实际 Schema。

## “通过 AI 创建”的现有链路

1. 创建页调用 `POST /api/agent-builder/sessions`，选择运行时和模型。
2. `CreateAgentBuilderSession` 创建私有隐藏的 `kind=system` Agent，以及真实 `chat_session`。它的 `system_key` 为 `agent_builder:<uuid>`，不会作为普通用户 Agent 出现在列表中。
3. 对话进入现有任务/聊天执行链，使用所选运行时。Builder 每次回复末尾输出 `<agent_draft>` JSON。
4. 草稿只包含名称、描述、指令、模型、skill IDs 和调用权限。前端解析后让用户检查。
5. 确认调用 `useCreateAgentSubmit → api.createAgent → POST /api/agents`。手动创建和复制智能体也使用这条表单创建链路。

因此，Builder 当前是“生成表单草稿”的入口；它没有生成 ZIP、上传 ZIP，或调用统一包确认接口。不能把一次 Builder 对话当作完整 v2 包的产出。

## Builder 有哪些技能

创建隐藏 Builder 时没有自动绑定用户的 workspace skills。工作区 skill 目录作为可选择的 ID/描述提供给它，用于配置新 Agent，不等于加载全部工作区 skill 内容。

执行时加载平台内置的 11 个 skills：

| Skill | 作用 |
| --- | --- |
| multica-creating-agents | 创建智能体、配置与导入导出 |
| multica-skill-importing | 导入及分配 skills |
| multica-runtimes-and-repos | 运行时与仓库 |
| multica-projects-and-resources | 项目与资源 |
| multica-working-on-issues | 处理任务 |
| multica-delegating-to-issues | 委派任务 |
| multica-squads | 小队协作 |
| multica-autopilots | 自动化 |
| multica-mentioning | 引用和提及 |
| multica-assoc | 关联 |
| multica-onboarding | 初次使用 |

实际运行时还可能增加运行环境技能：例如云沙箱声明 `dws` capability 时，任务服务追加 DWS skill。运行时自身发现的本地技能也取决于选中的运行环境，不能把固定 11 项当作所有机器上的完整能力清单。

来源：`server/internal/service/builtin_skills/`、`TaskService.LoadAgentExecutionSkills`。

## 是否知道 Schema 和 bundle

本次已在 `agentBuilderInstructions` 和 `multica-creating-agents` 中加入：v2 `agent.json`、Schema 下载接口、文件布局、ZIP/Git 共用 bundle、预览与确认、专属 skills，以及目标环境资源和密钥的处理方式。旧 DTA 构建产物不再作为公开创建协议。

这使 Builder 知道概念、约束和正确入口；它的输出协议仍是 `agent_draft`。完整 Schema 没有作为长文本塞入每一轮提示词，权威版本从下载接口获取。新 Builder 会话使用更新后的提示词；已存在的隐藏 Builder 不会被本次发布批量改写。

## 验收记录

本地自动检查及约束见 [实施计划](../superpowers/plans/2026-09-08-agent-local-import.md)。预发页面已完成真实上传、创建和导出：

- 工作区：浴发空间；浏览器当前登录用户/新 Agent 所有者：汪鑫。
- 智能体：[Agent 包验收助手](https://pre-fde-workbench.dingtalk.com/yufa/agents/52eb6d30-40ee-43ac-b71b-35e1bd952905)，仅所有者及管理员可用，绑定 Pre FC Codex Stable。
- 输入 ZIP：`docs/examples/package-inspector-agent/` 的 10 个文件，manifest 位于根目录。页面返回 bundle 摘要 `e56862b1c55d423c827872515c152b27fa4015d82247f3f14383ab33494ed153`。
- 页面核对：并发 3、会话续接开启、统一响应关闭、AI 标识开启；两个 workspace skills，一开一关；1 个 O/2 个 KR；A2A 停用。
- 经管理 → 导出下载真实 ZIP，使用权威 JSON Schema 校验通过。AGENTS.md、两个 SKILL.md 和四个支持文件共 7 个内容文件逐字一致；人设、配置中的业务开关、A2A 客户端稳定 key、停用状态和限额均已保存。
- 首次回读仅 `custom_env` 按设计变成 secret_ref，工具空列表 `[]` 曾被旧功能开关逻辑改成 `null`。后者已增加失败回归并修复，测试确认新导入保留显式空列表；首次创建的 Agent 保留当时的存储快照，不批量改写。
- 概览曾把本地来源显示为 GitHub，已修复为本地导入、包摘要和导入时间；最终预发页面已确认只展示本地导入、配置包摘要与导入时间，不展示 GitHub 链接、分支或同步按钮。
- 本次未修改或归档其他既有智能体，也没有绑定账号或签发 A2A 凭据。

## 历史记录

- 2026-09-08：记录统一配置包导入和 Builder 的实际边界。原因：区分完整配置包创建与 AI 表单草稿，避免误以为 Builder 已输出完整包。

发布追踪：功能分支提交 `a94184af2eac8775e8b7fef7a15a4077639df6a4`，沿用 CR `36017688`，预发流水线 `3107344462`。该 CR 的 revision 和 snapshotRevision 一致。发布合并冲突在独立目录处理，提交 `493aca726` 保留现有 API imports，并修正另一变更中的全角标点 lint；合并后的后端构建和 lint 通过。


最终预发版本：`21dae6497fb59eca78d7e07cb91d79c5f8496f09`，流水线 `3107346465`。代码合并、构建、预发部署、预发集成测试均为 SUCCESS；流程停在人工预发验证，未确认生产发布。部署后刷新测试 Agent，已验证本地来源概览修正。概览相关 41 项前端测试、Web 类型检查、lint 以及空列表后端回归/构建通过。

- 2026-09-10：标明 Builder 草稿说明的历史版本边界并链接后续实现。原因：避免把旧版本验收记录当作当前能力。
