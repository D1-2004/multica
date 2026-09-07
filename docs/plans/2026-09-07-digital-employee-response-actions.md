# 数字员工统一响应 Action 与 AI 标识控制

状态：Router 已按用户要求精简并提交：一张接待表、绑定表一个策略字段、一个待处理查询索引；旧模式不新增接待记录，回执和目标复用任务元数据。177 项回归与最终 9 项真实数据库复验通过。Multica 预发和东翔测试号真实沙箱证据保留；Router 尚未迁移或部署，新响应策略仍关闭。

## 目标与执行归属

- Router 在合窗前即时已读、贴思考中；Multica 管理回复、静默和结束清理。
- 新链路只覆盖显式采用 v1 策略的 Coordinator 数字员工 message.created；其他事件与通道继续 legacy。
- Multica 通过隔离身份的服务端 DWS 发送，真实回执决定 delivered；Router 按响应回执撤掉自己添加的表情。
- 每员工 dingtalk_show_ai_tag 默认 false；任务领取下发可信快照，DWS wrapper 在调用前改写参数。
- 业务状态、发送受理、送达和未知分别记录。持久化动作、稳定幂等键、租约和事务后唤醒负责恢复。

## 跨项目 v1 合同（实现事实源）

订阅及 dispatch 可选 responsePolicy：`{version:1,mode:"multica_coordinator"|"legacy",revision:正整数,showAiTag:boolean}`。
仅 source.type=digital_employee、outbound.mode=dws、event.domain=channel、event.type=message.created 且 mode=multica_coordinator 走新动作。缺失保持旧行为；相同修订不同内容拒绝，旧修订不得覆盖。窗口不跨策略合并。

Router：`PATCH /api/subscriptions/{sourceId}/response-policy`，输入 `{agentId,responsePolicy}`，使用现有服务认证；GET subscription 回显策略。
Router dispatch 的 completionCallback 新增可选 `responseUrl=/api/v1/dispatch-tasks/{id}/response-receipt`，与原 callback 同源、同一 task，禁止自行推导不可信目标。
响应回执 POST 输入 `{requestId,agentId,actionId,state,occurredAt,openTaskId?,openConversationId?,openMessageId?,errorCode?}`；state 为 delivered/silent/failed/cancelled/unknown。2xx 只确认持久化；Router 负责异步清理、终态不能被晚到 set 覆盖。unknown 不算送达，但表示本次自动处理已结束，清除思考中并保留可对账状态。

Runtime 能力名 `dws_message_policy_v1`。任务下发可选 `dingtalk_message_policy:{show_ai_tag:boolean,platform_managed_lifecycle:boolean}`；缺失不改 CLI 行为。平台受管变量在 custom_env 之后覆盖，热任务隔离。

## Router 精简结果

- `response_policy` 合入 `agent_binding.response_policy`，在绑定行锁下校验修订，换绑其他员工时清空。
- `response_receipt` 合入 `dispatch_task.metadata.responseReceipts`，在任务行锁下幂等合并，保留其他身份/执行元数据。
- `response_reception_task` 删除，合窗创建任务时一次写入 `metadata.responseInboundEventIds`；按目标主键及 agent/environment 清理，不依赖已过期窗口成员，也不需要事后挂接写入。
- 只保留 managed 入站的 `response_reception` 和一个 pending 扫描器；投递失败与清理意图同事务提交，无第二个失败恢复扫描器。
- HTTP/Dispatch/Runtime 合同不变，因此本轮不改 Multica 执行代码或 Runtime 镜像；旧真实沙箱验收仍对应既有发送实现，不能替代尚未完成的 Router 全链路验收。

## 工作分工与进度

- [x] Router：按精简方案复用绑定/任务，只新增一张接待表；即时接待、幂等回执清理、legacy 零接待写入与原合窗键验证通过。
- [x] Multica Agent：字段/API/UI、修订号与任务快照。
- [x] Multica policy sync：持久化对账、Router 客户端、能力门禁。
- [x] Multica response service：DWS send/status、持久化动作 worker 与回执。
- [x] Multica dispatch：短循环/Issue ACK/静默/失败/合窗/降级/任务结束接入。
- [x] Daemon/Runtime：DWS 参数 hook、回执与能力声明、候选构建；FC 冷启动和热复用 canary 通过。
- [x] 跨项目契约文档、定向测试、构建和 FC 基础兼容验证。
- [x] 验收范围调整：按用户要求不再运行本机 Daemon 实机矩阵；不将 FC 结果表述为本机矩阵通过。
- [x] 真实沙箱外发：东翔测试号成功发送并独立回读，覆盖普通消息、引用回复、冲突参数及热任务策略刷新；详细证据见 [沙箱验收记录](2026-09-07-response-actions-sandbox-verification.md)。
- [ ] 预发能力/消息回读与延迟对照，满足门禁后才启用。

## 验收

首次已读/思考中 P95 不劣于同环境基线；202 路径只有接单持久化，不执行 DWS/模型。覆盖单群聊、reply/issue/silence、失败取消、合窗忙窗、重复 callback、并发领取和重启、未知发送不重复、AI 标识和正文保真。本轮按用户调整，以东翔测试号绑定的预发测试智能体在候选 FC Runtime 验证真实沙箱：先检查可信策略、身份与权限，再发送独立探针并回读，切换标识开关验证下一任务策略刷新，完成后恢复测试员工原配置。实际未执行或受环境阻塞的门禁原样记录，不能以构建成功代替真实投递。

## 结果与遗留

代码及局部验收已完成；预发兼容代码已部署，新响应策略尚未启用。

- 新增 Router 策略、独立接待、响应回执与故障补偿；Router 独立日志见其 docs/plans/2026-09-07-response-actions-router.md。
- Multica 服务端与CLI构建通过；六个受影响包完整 race 测试通过；新Handler 27个顶层测试（含12项集成测试）通过。
- 前端 Core 272项、Views 7项、Core TypeScript通过；Views全量类型检查受现有三个依赖缺失影响。
- 标准sqlc遇到基线迁移271与手工generated兼容文件问题，使用 scripts/generate-response-sqlc.py 仅生成本功能新查询，保留既有生成代码。
- 拓宽旧Handler测试发现既有提示词/绑定fixture失败，已在原始HEAD a6f2d8c3 上逐项复现6项失败（/tmp/response-baseline-tests.log），未改变旧链路。
- Aone权限及预发候选Runtime publisher检查通过。Runtime candidate 第二次构建成功，并完成私有 Runtime 创建与真实冷/热任务。
- DWS 曾临时切 pre 准备测试会话，配角的 contact.user:get-self 被组织策略拒绝；未创建群或发送消息，已恢复原 prod 环境。


## 发布进度（2026-09-07）

- Multica feature：819fc10e7026a68c96c7ad73dbf29d004f007584，已推送内网Code；CR 36002276，预发 Run 3107073778。
- 预发 release 在独立 worktree 保留已有 collect 修复，合并为 9e4f354a5194a57dd4b6a11dcc05c1296a265ad9，构建和8项DB定向测试通过，已推送并确认 CODE_MERGE_RESOLVE_CONFLICT。
- Router 精简提交：f1354513fcf56a50c5c3e6ad1106dcc5fef173d1，已推送；原 CR 36002519 尚未部署。177 项回归通过，最终清理 SQL 再跑 9 项真实 PostgreSQL 测试通过，最终构建成功。仅需执行新 024/025：一个绑定策略字段、一张接待表和一个查询索引。初版 222845d 的 026–029 未迁移，已删除。
- Runtime 初次候选 Run 69575286 在清理 sandbox 时失败，已修复并由 Run 69597298 构建成功，详见下文。
- 本机 DWS 环境恢复为原prod；后续真实外发在预发平台绑定员工的 FC 沙箱中完成。新响应策略默认关闭。

## 当前交付状态

- Multica Run 3107073778 的代码合并、构建、扫描、预发部署与集成测试均 SUCCESS，停留正常人工预发验证门禁；实际 API 已读回 dingtalk_show_ai_tag=false / revision=1。
- Runtime 修复同一 sandbox 的传输失败删除重试后，以 5a3643030765c82dfa2e4908a23cd9c408494d79 构建成功；Run 69597298，Template pz27zf41o5r2vz53plfa，display alias multica-m7-vdd95d8b615567a87-r1-5a3643；Multica 二进制固定819fc10e7026a68c96c7ad73dbf29d004f007584。
- 私有候选 Runtime c59f8a53-6590-4c1b-b959-866b8f728af7 位于测试空间 d4f9ceed-d114-4312-bb30-dd791aee039b；模板、provider、channel、visibility、能力均已读回。
- 三条真实 FC 任务完成，均实际调用终端且exit_code=0：冷启动932f8de8-337b-4286-b49f-f4dcfc0d7a8e，热状态写入8cf2dcf7-f9a0-4169-b7b1-b9c0e8b75b34，热状态读取/清理e9c89742-314d-4ff1-a525-d7d2ed0f2a9d。后两轮随机哨兵一致、Task ID每轮更新；canary员工d9cdd8d5-9a9b-423b-8778-8fbd53717b75已归档，临时文件已删除。
- 正确预发 Diamond unit为pre，已确认监听仅两台预发主机；只增加dd95d8b615567a87的provider映射及消息策略能力allowlist，两个监听均确认推送。response_policy_enabled仍false，未修改生产配置。
- 用户明确授权后，使用测试员工e2293e9e-1e79-4926-b0e6-da4cb693add0切候选Runtime完成真实沙箱验收，五条探针均送达且各出现一次；程序出站参数与false/true/false策略一致。原Runtime、热会话和AI标识设置已恢复并读回；Coordinator及task-finished未修改，完整响应策略仍关闭，过程与证据见沙箱验收记录。

## 阻塞与下一步

1. Router 精简后的 SQL 024/025 尚未执行，原 CR36002519 尚未提交部署；迁移仍需要数据库连接配置位置。已经删除未执行的 026–029，不应再执行旧稿。
2. 本机配角身份此前遭PAT_ORG_POLICY_DENIED；用户指定东翔测试号后，改用其平台已绑定身份在真实沙箱正常完成get-self、建群、发送和回读，此历史拒绝不再阻塞沙箱验收。AI角标仍须客户端展示证据：原始回读的messageAiSendFlag两种策略均为DWS，不能用来源字段宣称视觉效果通过。
3. 本机候选CLI曾返回SIGKILL/137；用户已明确取消本机验证，此项不再作为本次验收阻塞，保留历史事实。
4. 未声称服务端业务回复、思考中撤除、客户端AI角标及P50/P95全链路已验收；保持新响应开关关闭。

收到缺失数据库配置后：先执行Router新增迁移并部署CR，再在已确认的测试员工/新会话上验证完整响应链路与延迟；满足门禁后按修订号启用仅候选Runtime支持的新策略。
