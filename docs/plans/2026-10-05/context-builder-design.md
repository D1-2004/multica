# Context Builder：链路核对与设计

## 本轮范围与验收

用户要求确认提示词清理的代码现状，检查 Skill/MCP 发现、调用和场域配置修改，再设计有序的执行上下文构建，并保留多租户、多场域能力。

- 源码基线：`feat/tag-multitenant@219f6a7a4825f021b62543f2bc243be9a3f855ee`，包含提示词清理 `e34151b236`。当前会话新分支只做审查与设计，不发布、切 Runtime、Apply 或发 IM。
- 判据：Direct 保留已绑定及有效层技能、DWS 身份规则、场域配置 MCP、连接器和 Runner；inline/bundle 一致；配置写入绑定本任务 scene_id，外场域与撤权拒绝；发现失败不能假装业务成功。
- 环境：隔离本地 PostgreSQL 数据库，已有集成夹具与真实本地 HTTP MCP 交互；Provider 配置/技能呈现检查。它们不证明 FC 上真实模型使用或钉钉送达。
- 相关 E2E：真实 Direct 从受理到沙箱发现技能/MCP，分别完成查询和同场域配置修改、回读；群/单聊、另一场域与多人窗口的权限反例。沿用发布协调窗口，真实执行本轮仅定义判据、不触发。
- 里程碑：核对当前代码 → 定向链路检查 → 记录缺口及证据 → 形成 Context Builder 设计与分阶段实施/验收。检查有失败先归因，不以设计掩盖；数据库环境不足明确记录。
- 停止边界：本轮交付设计与本地证据，结构性实现及真实发布验收单独列出，避免在设计中悄悄引入新状态机、配置快照或 Runtime 协议。

## 当前进度

配置上下文已存在 `contextcap.MergeContext` 与 handler `taskEffectiveContext`，本次设计必须复用它们，不能为执行上下文再造多场域合并规则。

检查范围修订：额外输出合同检查发现两个过期断言，分别仍要求禁止所有同场域主动发送和仅一次性任务无开始通知；最新场域例行任务合同已允许明确要求的单次发送、核验后抑制重复终态，并统一所有 occurrence 无开始通知。本轮只更新这两处测试断言，不改业务实现，保留首次失败证据。Context Builder 仍为设计候选。

## 检查结果与交付

- `implemented`：提示词清理在目标分支；有效层 Skill/MCP 解析、场域管理及发现/恢复代码仍在。新近期对话 `recent_conversation_v1` 已接派发，不再列为待新增；旧版本仍保留 unavailable。
- 本地链路：独立 PostgreSQL 数据库 `context_builder_20261005_a9c843` 完整迁移后，handler 链路33、冻结历史5、DWS/绑定技能1、最终输出合同3，共42个顶层检查最终通过，无 skip。覆盖 inline/ref、org/scene/person 合并、Runner、当前场域修改/回读、外场域拒绝、调用撤权、发现故障与同Run恢复。
- 纯检查：contextcap 合并/Prompt、execenv Direct/Skill 可见性与目录、Pi MCP 配置和测试自建 Provider 进程检查通过。未运行机器上真实用户 Agent CLI。
- 首次额外输出检查2失败/1通过，原因是现有断言与最新合同不一致；首次修正有1条措辞引用不准确，重新核对原文后3项全部通过。原日志保留，不把失败覆盖成从未发生。
- 原始日志和汇总：本工作树 gitignored `var/context-builder-review/summary.json` 与同目录 JSONL。独立数据库已删除；共享库、预发配置、Runtime、IM、周期任务均未改。
- [执行 Context Builder 设计](../../execution-context-builder.md) 已完成：明确六阶段、四类输出、现有复用点、多场域隔离、当前Run与下一Run能力变更、旧包版本边界与分阶段验收。Builder 尚未实现；只修正上述已有测试断言。
- 不能签真实效果通过：未触发FC任务/真实模型/IM。直配远程MCP/stdio动态发现未证明；`share_in_groups` 当前尚未全面应用是既有合同限制；它们不能通过本地绿测消除。
- 下一最小实施：阶段A只整理 Host/compiler/投影边界，要求模型可见输出及绑定等价；阶段B补 ledger/安全来源需新工作包版本，单独跑副作用重复/隐私撤销/旧journal反例。发布与验收仍走协调窗口。
