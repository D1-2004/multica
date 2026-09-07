# 数字员工统一响应 Action 与 AI 标识控制

状态：实施中。依据本任务已确认的方案；不修改 Coordinator 默认值，不自动启用生产新链路。

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

## 工作分工与进度

- [ ] Router：策略存储/更新/快照、即时接待、持久化回执清理、legacy 隔离与定向验证。
- [x] Multica Agent：字段/API/UI、修订号与任务快照。
- [x] Multica policy sync：持久化对账、Router 客户端、能力门禁。
- [x] Multica response service：DWS send/status、持久化动作 worker 与回执。
- [x] Multica dispatch：短循环/Issue ACK/静默/失败/合窗/降级/任务结束接入。
- [ ] Daemon/Runtime：DWS 参数 hook、回执与能力声明、候选构建。
- [ ] 跨项目契约文档、定向测试、构建和兼容验证。
- [ ] 预发能力/消息回读与延迟对照，满足门禁后才启用。

## 验收

首次已读/思考中 P95 不劣于同环境基线；202 路径只有接单持久化，不执行 DWS/模型。覆盖单群聊、reply/issue/silence、失败取消、合窗忙窗、重复 callback、并发领取和重启、未知发送不重复、AI 标识和正文保真。旧/新 Daemon 与候选 FC Runtime 分别验证。实际未执行或受环境阻塞的门禁原样记录，不能以构建成功代替真实投递。

## 结果与遗留

代码与本地验证已完成主体，预发尚未启用。

- 新增 Router 策略、独立接待、响应回执与故障补偿；Router 独立日志见其 docs/plans/2026-09-07-response-actions-router.md。
- Multica 服务端与CLI构建通过；六个受影响包完整 race 测试通过；新Handler 27个顶层测试（含12项集成测试）通过。
- 前端 Core 272项、Views 7项、Core TypeScript通过；Views全量类型检查受现有三个依赖缺失影响。
- 标准sqlc遇到基线迁移271与手工generated兼容文件问题，使用 scripts/generate-response-sqlc.py 仅生成本功能新查询，保留既有生成代码。
- 拓宽旧Handler测试发现既有提示词/绑定fixture失败，已在原始HEAD a6f2d8c3 上逐项复现6项失败（/tmp/response-baseline-tests.log），未改变旧链路。
- Aone权限及预发候选Runtime publisher检查通过。Runtime candidate YAML已准备，待本次Multica提交pin及推送后构建。
- DWS本机当前环境prod；测试号与教练profile显示expired，主角与配角active；尚未切换环境或发送消息。

