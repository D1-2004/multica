# 菲迪日常巡检与可观测性改进

## 范围与原因

- 以实际 DWS 问答为入口，将 Coordinator SLS、Langfuse generation 和 Scene Memory 串为可复查证据。
- 开始时间为北京时间 2026-09-09 00:43；“今天”跨零点存在歧义，已询问用户，先准备 9 月 8 日全天及 9 日零点后的独立窗口，不混算。
- 发布目标仅预发；用户明确本轮不需要 E2E。不发送测试消息、不执行记忆重置。
- 分支：`codex/feidi-daily-inspection`；原始消息/日志放本机权限受限的 `/tmp/multica-inspection-20260908`，不提交仓库。

## 执行与验收

1. 核实 dws-env 教练身份、环境；完整获取范围内实际会话和问答，保留分页完整性。
2. 分环境查询 SLS 与 Langfuse，抽查响应资格、关联/派发、提示词、记忆写入及下一轮召回；评估查询速度和漏查原因。
3. 只修复证据支持的问题；涉及 Coordinator 合同的变更同步 policy/source map，并跑对应静态检查和必要最小验证。
4. 在 `.agents/skills/` 落地开发面巡检 Skill，复用已有查询入口，保留只读默认、范围和证据边界。
5. 提交并推送分支，发布到预发，核对构建 SHA、部署状态与健康。报告问题、已修复/待处理项及验收缺口。

## 进展

- 已读 CLAUDE.md 及相关巡检、DWS、Skill 和部署规范。
- dws-env 当前为 pre，教练 get-self 成功；profile 初始 expired 不代表调用失败，刷新后业务读取正常。
- Langfuse 的独立只读审计已并行启动。
- 主报告按刚结束的 9 月 8 日全天进行，9 日零点后独立列示。
- 取证发现 Scene Memory 连续数百次重试：1600 字超限后共享模型超时预算不足以修复；历史不可见保持 dirty。扩展修复范围为记忆压缩提示、有限修复预算及准确观测，不跳过历史水位、不重置记忆。
- Langfuse 列表有同 ID 多版本、根 input 被派生任务覆盖；查询脚本需结束时间、去重和截断披露。审计必须读取 Coordinator 根 observation 的原始输入。
- DWS pre 列会话、读会话、消息详情富化均报服务端错误，但搜索取得59条菲迪出站消息；为取得完整上下文临时切 prod 读取，完成后恢复原pre环境。
- 已完成DWS线上网关全日读取：248行/247消息/25会话；与SLS入站差集补读2条，最终249条可见消息/27会话，39条Coordinator入站全部能回读，员工出站59条。DWS环境已恢复pre。
- 已确认12条deferred中的11条由日志URL数字cid被错当会话ID引起，涉及3条入站；修复CID形态和缺失召回hint，保留真实会话链接的CID。该确定性缺陷纳入本次部署。
- 记忆侧未知DWS错误现在归HISTORY_UNAVAILABLE，并在裁剪前保留白名单诊断字段，LF和SLS均可按scene/agent/attempt定位；不推断旧错误根因、不改变未知错误的常规重试策略。
- 已实现开发面inspect-daily-qa Skill和本地证据索引；SLS增加固定结束时间、offset与满页提示，LF统一分页/去重/水合/环境与统计。
- 已完成相关Go包窄测、查询脚本单测、真实只读API查询、policy结构检查和Skill校验。用户要求不做E2E，真实模型收敛与新版本投递仍未验收。

## 结果与遗留

- 完成：巡检证据、开发面Skill、查询工具及确定性修复已提交到codex/feidi-daily-inspection；应用提交423b434e8。
- 预发发布：CR36035346，run3107363464，构建job171082354；集成提交b419ad5bc包含应用修复，2026-09-09 01:25:25+08:00部署SUCCESS，/health与/status.taobao均200；停在人工预发验证。
- 冲突：仅两份policy JSON的新增案例/引用冲突，在隔离release worktree语义合并，保留预发现有finish/purpose修复；集成后4包验证与policy结构检查通过。巡检分支未混入其他CR。
- 完整报告：docs/reports/2026-09-08-feidi-daily-inspection.md。
- 遗留：截断后的名单补造、One-One关系语义、菲迪3群DWS根因、claimed evidence不可见、SLS8000字提示词截断；详见报告。未做E2E，未宣称正式环境恢复。
- 最终报告/Skill补充说明为文档提交，不更改已部署应用代码。
