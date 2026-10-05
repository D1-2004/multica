# Qwen-DWS 一次性创建重复播报

## 当前状态

- 范围：中等；只读调查原事故，修复 Employee Direct 一次性任务创建的重复配置通知，交付可审查 CR。不得发送钉钉消息、部署或合并。
- 基线：`aone/feat/tag-multitenant@0c626efa138ef137bb8a23e82a3d7d2af85fd347`；隔离分支 `codex/qwen-once-create-notice-20261005`。原 checkout 的 WIP 不进入提交。
- 实现 completed；本地验证 passed；原事故只读取证 completed；独立审查 passed；[CR30323684](https://code.alibaba-inc.com/dingtalk-ai-lab/dt-fde-multica/codereview/30323684) 已 opened、can_be_merged，待人工评审，未配置默认评审人。代码候选 `b33137baf6923c8c1e90c9f7377d8a080d97b704`，后续仅回填本 Plan。集成/发布不在本轮范围；修复版本真实 IM 验收 pending，交给后续明确授权的集成/验收负责人。

## 用户结果与判据

用户要求“5分钟后和我讲个笑话”时，保存一次原请求锚定的 once 资源，创建阶段只有一条简短的最终结果；不再同时播报配置更新和后台创建回执。接单意图、创建已完成、到点实际笑话是不同事实：接单不证明创建成功，创建不证明到点已投递。既有前台接单协议本轮不改。

工具写入事实/错误保持完整，失败仍由原 Host 结果路径告知；真实重投返回原资源，不新增配置消息。只改变 Employee Direct 的 once 创建通知所有权；普通任务、其他配置变更（尤其远程 MCP 的安全审计消息）、routine 到点结果和既有创建幂等不改。

最小完整路径：原 IM request → Employee receipt/job → dispatch → Direct queue/Run → `scene_routine_create(kind=once)` → 一份资源/来源 → Host 创建结果 outbox → 独立 IM 回读。反例：普通任务仍有配置通知，Direct 其他配置变更仍通知，拒绝/失败不能声称创建成功，创建重放不能产生额外配置通知或重新武装 consumed once。

## 调查与参考

- 2026-10-05 北京时间 09:20–09:40；Qwen-DWS 单聊外部 cid `cidMF9FOe1ACHthSFNyLtfy5uHGOC8F8fjtAQpiGLe8r1I=`。先读精确 openMsgID，再以 Task/trace 取 SLS/LF，不按相近措辞推断动作相同。
- `msgP5PB12oyWc8UFIZ1PvKaKw==` 与 `msgKPEYaHFWkd4adgz2EdUsow==` 各自被冬翔打叉；`msgXWvucgRygsmIuTKQZoMTUQ==` 只引用 KPE 的详细创建回执。前者为相邻负反馈，不能冒充同一引用说明。
- 配置列举与两次列表查询已逐条核对：创建 Task 在 09:30:29.855 调用 `scene_config_get`，只是同一后台创建动作的只读预读，没有另发配置列举回执；09:30:24“你有什么例行任务”和 09:31:01“你有哪些定时任务”各有独立用户请求/Job，见下表。更早窗口未命中的配置发言不擅自归入本次创建。
- CR [30323268](https://code.alibaba-inc.com/dingtalk-ai-lab/dt-fde-multica/codereview/30323268) 已 merged，只有 `BuildPrompt` 和 `buildTaskExecutionPacketWithContext` 的简短表达/证据约束及文档来源表更新；明确没有改变结果协议和所有权，不能当作本故障已修。
- 参考 [GawkBot 固定源](https://github.com/najmuzzaman-mohammad/gawkbot/blob/71e82a1809565281cbd0bf8185d3c125b715d934/internal/team/broker_scheduler_routines.go)：`handleRegisterRoutine` 持久保存配置 revision/activity、用目的+日程去重，最后返回 job/updated；不会另发创建聊天通知。本仓复用 PG 资源/来源和 Host outbox，不复制 Broker、改变存储或增加模型裁决。
- 本仓 Direct claim 和 `final_text_owner=host` 负责最终结果；`callSceneConfigTool` 成功后的通用 `postSceneConfigNotice` 是另一个发送方。原运行确实产生两条独立 request/action，根因是工具变更通知与 Run 最终通知的所有权重叠；不是第二次资源写入，也不是同一个 action 的重试二次送达。

## 原事故证据

原始证据及 SHA256 持久保存在私有目录 `employee-e2e-evidence/QWEN-ONCE-CREATE-NOTICE-20261005/evidence-summary.json`，不将含会话原文和能力链接的原始响应提交到仓库。DWS 精确消息和 09:20–09:40 窗口 complete=true（13 条）；LF 按 cid/scene 定位后读精确 trace；SLS 09:30–09:32:20 23/100 条，没有满页截断。后台环境 `pre`，DWS 独立只读使用线上网关；事故源码/release SHA 未由启动日志证明，不把当前开发基线冒充事故部署版本。

| 事实 | 可复核关联 |
| --- | --- |
| 09:30:12 前台派发、09:30:13 接单 | job `8ac5443c-5773-4631-ad65-d9fc4b3e9e9e`，EmployeeTask `695b42ee-af6c-4192-88cc-7dc64f53a978`，Run `f209980c-6eb0-4fac-94ed-25d8c1718cd6`，queue/trace `d38c1053-5e54-4259-bcc2-829f09f4010b` |
| 09:30:32.351 唯一创建调用 | LF tool `mcp_config_qwen_tag_scene_scene_routine_create`，observation `e11bbc89ec292605`；routine `4f5b3863-e9d5-45fe-8c94-674b7b6ff613`、autopilot `712f72f4-aba2-4818-a54f-67762386e307`。09:30:34.353 的 `scene_routine_list` 只读复核，没有第二次写入；一次 observation 的两份 content 不是两次调用。 |
| 09:30:32 配置通知 | request `scene-notice:813359ea-afc6-4f7e-bd8c-7302b74eac54`，action `response-796d8c403fd14c5c334b9b804e2a9b545324565c21e154d982ed95fec011badb`，09:30:33.447 delivered；DWS 回读正文与 `sceneConfigNoticeText` 一致。 |
| 09:30:39 最终回执 | request `employee-run:f209980c-6eb0-4fac-94ed-25d8c1718cd6`，action `response-1a1def79c796402ed40435b430db43683fb2c1dd15da92e433d4ea078520c817`，09:30:40.840 delivered；DWS 正文与同 queue trace 的最终 `summary` 完全对应。 |
| 09:30:24 的例行任务列表 | job `15248ce1-5f92-40fe-9269-d257a11e8672`，09:30:27.462 `scene_config_get` 只看到旧 Webhook；09:30:30 答复该提问，无 dispatch/写入。 |
| 09:31:01 的定时任务列表 | job `51488ebc-41cb-4fc8-9553-2f90fab71bcc`，09:31:03.363 `scene_config_get` 看到旧 Webhook 和新 once；09:31:05 答复该提问，无 dispatch/写入。 |

发送日志没有 provider message ID，action→openMsgID 的关联依据是同 Agent、准确文案、时间和 Run，不冒称直接数据库 join。两条 action 的 `unknown(submission_interrupted) → provider_accepted → delivered` 是提交后查询对账；attempts=2 不表示两次发送。两个 reaction 原始返回仅有 openMessageId/emoji/replyUsers，不能编造更细 key、reaction ID 或时间。

事故后台四个 `llm.call.N` 的输入都包含 CR30323268 的“一两句简短表达”规则，导出 request_truncated=false。这证明表达规则实际进入本轮，但没有消除两个通知所有者；动态场域 skill 还要求复述标题/日程/时区/暂停说明及 `tell_the_human`，本次增加明确 once 短句例外，避免与旧详细回执要求冲突。

已发现但不扩展修复：事故请求 09:30:07 加五分钟应是 09:35:07，而工具实际保存 09:35:00；最终正文仍自述保持原消息时间。这是独立的秒级时间事实偏差，本轮只修重复播报与简洁回执，留作后续问题。

## 实现与本地结果

- 合同、skill/source map 和既有 `office-cron-once-short-due`（P0 `G15`）先更新。该用例增加创建阶段的唯一最终回执及重放核对，保持稳定 ID/场景归属；没有扩大真实执行授权。
- `sceneConfigRoutineCreate` 在成功后只对 once 且 `CaptureRoutineSource` 已在 PG 验证 EmployeeRunID 的来源返回空 change notice。工具事实/资源/来源不变，失败早于此分支返回；不抑制原 Host 终态、不重发历史消息、不改普通任务、其他配置或到点运行。
- 原代码的实际 MCP 创建/重放在本地复现一个资源、两条配置通知；首轮夹具缺 Agent 头及非 Employee 模式，修正 setup 后才使用真正的重复通知失败作为产品回归证据。
- 隔离 PG 17.10，源码基线 `0c626efa13` 加本次 diff：新两项检查及十项既有检查共 12 顶层 PASS、0 fail、0 skip。覆盖原 Direct DM 受理→MCP创建/重放→单资源→无新增配置 outbox→原 Host唯一最终 outbox（重复 reconcile）、普通任务保留配置通知、Direct远程 MCP安全消息/URL脱敏、跨场域和token拒绝、routine只读、once取消/消费/重放，以及成功/失败/取消终态通知唯一性。模型、DWS、时钟输入为既有夹具；不把 outbox enqueued 当作 IM 已送达。
- `go vet ./internal/handler`、`go build ./cmd/server`、`make eval-check`、`check-eval-catalog.py --base-ref 0c626efa...`、`git diff --check` passed。定义结构/引用检查不是真实模型质量验收；没有新增协议、迁移、配置或 Runtime 镜像依赖。
- 修复版本的自然短句表达和真实 IM 次数尚未运行；须后续明确授权后部署候选并按本例最小路径复测。原失败 evidence 保留，不以换会话或模拟 PASS 签收产品效果。
- 独立源码/grounded 边界审查指出 skill Step 3 的旧详细回执指令与 once 短句冲突，已改为明确 once 例外并复审通过。审查确认 PG 来源 proof、失败/普通任务/远程 MCP、原 Host 状态栅栏未改变。它不证明真实候选 generation；旧副本仍可能发配置通知、旧 claim skill 仍可能长答，必须部署全部修复副本并用新 claim 验收。
- 本地隔离数据库已删除；只读取证没有修改 live 配置、routine 或账号，无恢复责任遗留。

## 环境、验证与交接

- 调查：预发后台与线上 DWS 只读；无业务写入、共享测试窗口或恢复动作。
- 本地：隔离 PostgreSQL、实际 MCP handler/资源写入和 outbox，模型与 DWS 用既有夹具。新增检查须证明实际重复通知或错误边界，不只断言实现字符串。
- 先复现同一 Direct once 创建的重复配置 outbox，再修复并定向检查创建/重放、普通任务和远程 MCP 保留通知、原 Host 结果通知；运行相关 vet/build。不扩展全仓测试。
- 本地检查不证明模型表达或 IM 次数已通过。后续必须部署此候选并重新授权原场域最小 IM 复测，固定源码/各副本/Runtime，核对任务、工具、outbox和独立投递结果。无新 Runtime 字段、迁移或 live 配置要求时按实际结果注明。
- 实现及提交由本次代理负责；集成/发布/真实验收后续接手。完成后回填本入口的证据、未证明项与准确接手步骤。
