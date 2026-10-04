# 场域配置与 Employee Webhook 交付

## 目标、Why 与边界

Webhook 自动事件不是人在群里主动询问状态。固定开始播报与耗时结尾会淹没事件的业务结果。Employee所有例行任务（定时、一次性、Webhook与手动运行）改为结果优先：不发送开始通知；结束只发执行器的最终业务结果，失败/取消仍有明确说明；空结果明确未取得可交付结果，不冒称成功。不增加模型请求、关键词识别或第二套 outbox。

参考 GawkBot 固定提交 71e82a1809565281cbd0bf8185d3c125b715d934 的 internal/team/scheduler.go::processBotJob：自动化工作进入员工事件，调度轨迹/OutputSummary保留在schedulerRun；借鉴业务回复与运行记录分离，不复制其向频道发布自动化输入的具体传输。原消息只读取证（2026-10-03至本次查询，369条），私有证据完整性complete=true、hasMore=false；已见定时任务同模板，用户补充要求定时任务一并修复并部署，当前范围包括冻结scene_routine与scene_routine_webhook Employee origin；旧Coordinator与混版回退路径保持。

MCP删除入口从配置弹窗底部移到列表行，继续使用场域授权与确认框；只移除本场域配置项。Webhook复制弹窗须在窄屏容纳完整链接，完整复制且默认掩码。

## 里程碑与验证

- 基线：当轮aone feat/tag-multitenant，当前独立分支codex/scene-webhook-improvements，无初始WIP。
- 实现前更新docs/context-capabilities.md及稳定case；实现后进行现有Webhook admission→claim→CompleteTask→routine outbox合同测试，确认重放唯一Run、零start、一条end、结果原文及异常保留。模型和DWS替身与PG集成分开报告。
- 前端定向行为检查MCP确认/取消/其他配置保留；布局在发布后以真实320/375px配置页验证，不用class断言冒充视觉验收。
- 环境：本地独立PG数据库scene_webhook_ux_1004；私有读聊天使用冬翔profile，DWS线上网关保持原状。共享预发、Runtime、Agent定义与场域不写。
- 发布流程：提交源分支CR到feat/tag-multitenant，精确SHA/稳定case/验证边界交给「发布协调」；等待明确通知才进入部署验证。用户已补充授权修复后部署，由发布协调统一合目标与部署，收到其通知后进入验证。
- 真实验收：Webhook带可识别业务数据和同event id重投→IM只见一条业务结果，API唯一delivery/occurrence/task/run，失败一次可解释；SLS/LF证明冻结来源与执行器实际工具。MCP删除后重新打开及API回读；其他场域配置不变；长URL完整复制且无横向溢出。

## 状态

实现及独立审查完成，准备提交CR；未发布、未进行真实E2E。

- 后端定向PG检查：29项顶层PASS、0FAIL/0SKIP，含Webhook同源重放、零start、单条end结果、reader回退/恢复及现有schedule通知。执行器与DWS为替身，不冒称真实IM。服务构建通过。
- Webhook原集成夹具缺少WebhookSourceReady、对queued_frozen仍使用queued断言与提前退出；修复夹具声明和等待判据，使测试确实经过当前冻结队列/reader门，不放宽生产门禁。
- 前端两个文件99/99通过；变更文件ESLint及diff检查通过。views tsc被未改的agent-detail-inspector.labels.test.tsx:54 RuntimeDevice强制断言缺字段阻断，未宣称全包类型检查通过。
- 宽EmployeeRoutine选择曾在无关decision dispatch用例失败，保持失败日志；最终限定受影响Webhook与schedule通知路径，不据此宣称全量Go通过。
- 新稳定case office-hook-result-only-delivery / spec-communication、spec-delivery / webhook-office / G16；149定义结构与稳定ID检查通过，不代表真实执行。配置页两项的独立视觉/API验收步骤见上方计划。
- 证据保存在私有SCENE-WEBHOOK-UX-20261004目录；没有共享配置/Runtime修改。本地独立数据库检查结束后清理。
- CR、部署及真实验收分别由本次交接和后续发布通知签收，未收到通知之前不进行共享验证。


## 范围更新：定时任务与部署

用户要求定时任务的同类问题一并修复后部署。将零start、业务结果直接交付策略扩至Employee所有routine Direct origin，不改变原Coordinator路径。新增office-cron-result-only-delivery/G15，复验schedule claim→CompleteTask→唯一end及原once合同；部署由发布协调执行，真实验证仍等待其版本通知。此前29/99为Webhook切片本地结果，扩展后的检查另记录，不能复用为定时行为已证。


## 扩展实现检查与交接

- Employee schedule整链独立PG检查1项通过；Webhook、条件判断、结果异常边界独立批30项通过；once3项通过，最终各批0fail/skip。合批时schedule claim发生一次queued却未取得的夹具竞争，原失败日志保留；单独进程原链通过，不修改生产claim门。
- decision guidance、run_routine描述与tool result同步取消start/end承诺；quiet/reply/wait语义保留，源码审查确认所有冻结routine origin均适用。服务构建通过，150定义检查通过；前端仍使用已通过99项证据，无新增前端变化。
- CR：Code Review 30323103，目标feat/tag-multitenant；提交后由发布协调统一合入部署，按新源码和live reader通知启动真实验收。尚未将本地PG/替身结果签成真实IM通过。


## 部署后验收与创建回执补正

CR30323103已合并；source f4f582d812cbbbf91e6ef5b5666df7bdc4a16cb1 / release80941fe742b68e6d38d79efc05db6ad3a6ffd246 / Run3110401398，2026-10-04 23:15:01预发部署成功。release血缘独立核含20a564b54f；23:24实时fence normal、两个live employee-loop23/human2/webhook-source1。fence.started_at仍是旧记录，不能用作本次进程启动时间，当前发布回执由发布协调提供。

真实验收进行中，独占dxxh DM测试routine；冬翔DM页面测试仅独立disabled临时MCP及不触发的Webhook，保留原配置。发现routineCreatedMessage的周期创建回执仍承诺“每次开始和结束发消息”，与Employee新行为矛盾。本补正改为结果交付与运行记录说明；不改派发/投递/队列或Coordinator行为，不以文字修改洗掉先前现场。补正将独立CR交同发布协调，当前测试仍冻结前版并逐版签收。
