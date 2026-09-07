# 数字员工响应动作与投递合同

本合同覆盖 Router、Multica 服务端、Daemon 和受管 Runtime。入站业务裁决仍是 Coordinator 的 reply/issue/silence；发送动作与业务执行状态分别持久化。

## 现状与执行归属

Router 接收 HSF/HTTP 事件后校验订阅、绑定、去重和群聊提及资格，按发送人合窗，再发送 Dispatch V2。Multica 在数据库提交接单后立即返回 202，4 秒收集窗口及模型工作在后台执行。

| 动作 | Legacy | 显式启用的 Coordinator v1 |
| --- | --- | --- |
| 已读 | Router 接入后异步执行 | Router 接入后异步执行，不等待合窗 |
| 接待表情 | 已排队、投递中、处理中等状态推进 | Router 独立持久队列贴思考中 |
| 快循环回复、Issue 接待语 | Multica callback → Router ServerPush | Multica callback outbox → response_action → 服务端 DWS |
| 沙箱主动外发 | DWS，加提示词约束 | DWS wrapper 强制 AI 策略，先登记发送意图再执行 |
| 最终状态表情 | Router 贴完成/失败等章，沙箱也可能清理 | Multica 发响应回执，Router 撤自己添加的表情，不贴完成章 |
| 控制命令 `/cancel` | Router 的即时控制 ACK | 保留原控制 ACK；原任务回执负责清理原接待 |

纯 `emotionReply` 是用户对已有消息贴/撤表情的事件，按显式 emojiReactionCids 订阅进入原窗口和 dispatch，不因本次修改进入 Coordinator。平台自己的接待表情与此不同；Multica 继续过滤自己的消息和状态表情，真实环境还需核验上游订阅的自身事件过滤。

## 策略与能力

Agent 的 `dingtalk_show_ai_tag` 默认 false；`dingtalk_response_policy_revision` 从 1 开始，仅 AI 标识或 Coordinator 设置真实变化时递增。现有 Coordinator 默认关闭的语义不变。

Router 注册、订阅读取、策略 PATCH 和 dispatch 使用同一可选快照：

```json
{"version":1,"mode":"multica_coordinator","revision":2,"showAiTag":false}
```

`mode` 也可为 `legacy`。缺失字段走旧实现。只有 digital_employee、dws、channel/message.created、非 cancel 的组合采用新响应归属。窗口不跨策略或修订合并，任务中途的配置变化不改变已冻结的归属。

`PATCH /api/subscriptions/{sourceId}/response-policy` 使用现有 Multica 服务凭据，输入 `{agentId,responsePolicy}`，不会重建上游订阅。能力探测为 `GET /api/subscriptions/response-policy-capabilities`。同步 worker 持久化 desired policy，PATCH 后 GET 回读；旧修订不能覆盖新值，部署开关也使用单调 revision 隔离滚动副本。

Daemon 在任务领取时声明 `dws_message_policy_v1`，Windows 不声明。FC 必须同时具备精确模板指纹的能力证明；不能因为 provider 名称相同就给旧镜像加能力。任务的 `dingtalk_message_policy` 含 `show_ai_tag` 和 `platform_managed_lifecycle`，缺失时 wrapper 保持旧行为。

## 响应动作与回执

接单事务把原 completion/update callback 映射到冻结的 response route。现有 callback worker 在发送执行报告前，用稳定 request ID 入队响应动作；该步骤失败时执行报告保持可重试。新模式的 Router 只记录执行报告，不再次发送 `resultMessage`。

发送使用稳定 Action ID 与 DWS 幂等键。写操作前先持久化不确定状态；若进程在外发后退出，新副本不能重新生成一个发送请求。只有拿到 openTaskId 才开始查询；`success:true` 或 openTaskId 均不是消息已送达。

`completionCallback.responseUrl` 必须是与其他 callback 同一 Router task 的可信相对路径：

```text
/api/v1/dispatch-tasks/{id}/response-receipt
```

回执主体如下，时间为 Unix 毫秒：

```json
{
  "requestId":"stable-request-id",
  "agentId":"agent-uuid",
  "actionId":"stable-action-id",
  "state":"delivered",
  "occurredAt":1788796800000,
  "openTaskId":"provider-task-id",
  "openConversationId":"cid...",
  "openMessageId":"message-id"
}
```

state 为 delivered/silent/failed/cancelled/unknown。Router ACK 的 data 回显 dispatchTaskId/actionId/state，仅表示已持久化；撤表情由独立 worker 完成。unknown 后可用同一动作补充 delivered/failed 证据，旧 unknown 重放不能降级已有确定状态。

正常发送受理后查询真实状态，15 分钟仍未确认则记录 unknown 并结束接待，24 小时内继续对账。无 provider task ID 的不确定发送不自动重发。合窗的额外 callback 在主响应真实回执成功后才关闭；忙时 park 不冒充业务静默结束。

Router 的本地终态阻止晚到 worker 再次添加表情。HSF 没有远端版本栅栏，因此远程添加超时场景使用持久化、有限退避的重复撤除补偿；它提供最终一致性，不能宣称远端强顺序。补偿耗尽保留明确的不确定观测。

## 沙箱 Hook

当前用户发送命令在执行真实 DWS 前由 Go 参数解析器统一设置 `--ai-tag`。支持原子 send/reply 和当前 Runtime 的快捷命令，保留正文、附件、目标与幂等键；帮助、读取、Bot/Webhook 不改写。Runtime 的固定路径入口也委派到同一解析器，保留既有 DEAP 身份保护。

受管发送调用 `POST /api/tasks/{taskID}/dingtalk-send-receipts`。仅接受该任务的 mat_ token，工作区、Agent、Issue 均由服务器加载，不接受调用者指定归属。先登记 pending，失败则不发；发送后报告 accepted 或 unknown，后置上报失败不会诱导再次发送。只有服务端独立查询确认后才把账本记为 delivered，并补齐真实会话关联。

task-finished 遇到 pending/accepted/unknown 时延期，不将其当作“已汇报”。查询确认 delivered 后去重；确认 failed 后允许按原 task-finished 开关补充回复。原会话之外的主动外发不抑制原会话汇报；暂未解析目标的意图保守等待。

## 部署、验证与回滚

默认不启用新归属。Diamond 主配置的 `integrations.dingtalk_response_policy_enabled` 默认 false，`dingtalk_response_policy_revision` 缺省按 1 处理；每次启用或关闭都提升部署 revision。无 Diamond 的启动支持同名 `MULTICA_DINGTALK_RESPONSE_POLICY_ENABLED/REVISION` 环境变量。

FC 能力使用 `runtime.fc_e2b.dws_message_policy_fingerprints` 精确 allowlist；无 Diamond 时使用 `MULTICA_FC_E2B_DWS_MESSAGE_POLICY_FINGERPRINTS` JSON 数组。候选镜像当前指纹为 `dd95d8b615567a87`；仍须在 Runtime provider catalog 中注册该指纹对应的 providers。

先部署兼容 Router API/SQL、Multica 服务及候选 Runtime，再用隔离员工验证能力、实际 DWS 状态格式、消息回读和延迟。回滚提升 revision 并切 legacy，仅影响新入站；已有动作按冻结归属完成。待发送动作、回执和沙箱查询计入部署 drain。

延迟分别度量接收→已读、接收→思考中、接单 202、裁决→发起发送、发送→清理。首次接待 P95 不劣于同环境基线；202 不运行 DWS/模型；队列提交后立即通知，轮询只作重启/丢通知补偿。

本分支新增 sqlc 查询可用 `python3 scripts/generate-response-sqlc.py --check` 校验。该脚本隔离现有手工生成文件与历史迁移排序问题，不运行迁移，也不改写其他功能的生成代码。具体运行结果和未完成门禁记录在 [实施计划](plans/2026-09-07-digital-employee-response-actions.md)。
