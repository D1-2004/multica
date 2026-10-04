# 同场域例行任务送达后不重复回报

## Why 与结果合同

原“场域测试群”反例：2026-10-05 00:00:45员工已向本群发送目标消息，00:00:54又发送“已向一粟…送达状态delivered”。旧修复只移除Host开始/耗时横幅，仍逐字发送执行器final，不能去重已经用工具送出的结果。

本次仅处理Employee routine（schedule/once/manual/webhook）的同场域已送达重复终态输出。实际同task、workspace/agent、DWS身份与conversation的server-verified消息回执是抑制依据，不能靠final里的delivered、自述、中文关键词。不同场域或失败/未确认必须保留必要反馈；多发送有pending/unknown/failure时不因某条成功掩盖未完成部分。其他前台漏派/工具索引外露由别处接手，本次不改。

参考已有dingtalkresponse.SandboxDeliveryInTx的持久回执与worker.BeforeSend/SuppressSendError机制：如GawkBot scheduler将业务交付和运行轨迹分开，复用本仓PG outbox，不新建发送器/进程内标记或提示词过滤器。文档先于代码；无新schema/Daemon/Runtime协议。

## 架构与边界

所有例行终态仍创建唯一routine:<run>:end outbox，发送前按冻结automation origin和真实queue重新查回执。确证本场域已送达且无其他未终态发送，已有SuppressSendError将该outbox持久取消，不产生第二条消息。pending只延迟尚未提交的outbox；unknown/failure给明确说明。已提交/unknown-provider outbox继续查询，禁止被新回执改成重新发送。

claim输出指导同步：需要@/工具发送当前场域时允许执行；结果通过工具交付后无需冗余总结，最终执行事实保留Run，Host以回执决定是否发第二条。普通只返回final的例行任务继续交付。

## 验证与交付

基线当前aone feat/tag-multitenant，源codex/routine-same-scene-delivery，初始无WIP。本地独立PG routine_same_scene_1005：精确同场域/身份/queue回执、其他群/其他身份/其他task、pending→delivered晚回执、unknown/failed与failure执行状态、重放唯一outbox与无第二provider发送。低价值文字镜像测试不新增，重点风险是抑制错误和回执竞态。

真实验证等待发布协调通知；原“场域测试群”仅安全无@验收消息，不联系截图真人，不修改其原例行任务。送到本群仅一次，发往其他测试场域仍向原场域回报；无发送只返回结果照常。IM完整窗口+API Run/queue及LF真实发送工具/回执分面，不用模型delivered自述当证明。

目标CR提交feat/tag-multitenant交「发布协调」，合入部署与实际验证遵守原通知流程；用户此前部署授权保留。独立本地检查与共享部署分开，长等待先提供精确SHA/证据/接手状态，不新建监控。

## 当前状态

实现及独立复审完成，准备提交CR；尚未发布，原群真实效果与真实FC/持久设备兼容未验证。


## 范围补正：真实回执接入

独立审查发现旧routine claim的PlatformManagedLifecycle=false，因此dws shim根本不报告receipt；即使报告，原RecordDingTalkSendReceipt仅接受managed dispatch，会403。直接写表测试只证明guard，不证明生产链。实现必须同时按LoadAutomationOrigin的PG冻结receipt→Task/Run/queue校验启用既有managed消息策略，并允许task-token来源的routine回执接入；HTTP/body、模型prompt或source字符串不能构造权限。保持普通dispatch/A2A边界。复用既有dws_message_policy_v1能力门，没有新Daemon字段或镜像代码；缺能力拒绝执行，实际现有FC与本地客户端兼容仍需对应证据，不据PG替身签真实Runtime通过。

新增真正HTTP task-token入口测试：可信schedule/webhook来源、伪造headers/body、缺origin row、跨tenant/rebound identity、receipt observed delivered仍需provider查询、最终guard抑制且无第二发送。现有纯guard16边界结果保留为局部证据，不冒称完整闭环。


## 混版边界

reader marker升24。既有routine admission门及新的已受理routine claim门共同等待all-live24；后者使用Direct原有defer机制保留同一Task/Run，不因callback403永久丢掉发生次。不会启动新的Runtime构建或切换；本波改变的是已有dws_message_policy_v1布尔策略含义与server admission，必须说明当前FC/持久设备的实际验证边界。


routineNative回执必须保留Host选定的DWS gateway在冻结input内，否则预发默认网关会验证错线上群。仅可信server helper提供该字段，receipt body仍strict拒绝路由/身份字段；原普通dispatch callback未引入新gateway值。


## 本地交付结果

- 受影响handler16顶层、response service45顶层、既有shim15顶层全部0fail/skip；涵盖真实HTTP Auth task-token→pending→client delivered仍不可信→provider query→抑制outbox、晚回执、多状态优先、不同作用域、未知提交query-only与混版defer。
- server build、受影响vet、151定义/稳定ID与diff检查通过。新端点测试使用数字身份与native订阅gateway，旧claim夹具明确设置模拟reader/发送器；真实当前“场域测试群”读取到原两条消息，并API回读原周期routine已完成，未修改用户routine。
- 独立审查发现“routine未启用report且endpoint会403”的阻断，已补claim和API可信来源双接缝；复审无新阻断。仅插表guard测试保留作局部证据，未拿它替代HTTP链路。
- 无Daemon/SDK/image源改动，复用既有cap与bool；SDK代码合同检查不冒称真实持久设备/FC完成。共享Runtime/用户群未写；本地独立PG交付后drop，真实验证等待发布协调通知并选择安全无@消息。
- 新case office-cron-native-same-scene-delivery / cron-office / spec-delivery/spec-trust / G15；不签完整P0。无凭据/原用户消息写Git，私有证据ROUTINE-SAME-SCENE-20261005保留所有失败与最终日志。
