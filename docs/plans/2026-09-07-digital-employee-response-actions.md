# 数字员工统一响应 Action 与 AI 标识控制

状态：本轮员工级“统一响应”前端开关已部署预发并完成页面开关与 Router 策略回读验证，默认关闭，无需 Diamond 配置。Router 已按用户要求精简并提交：一张接待表、绑定表一个策略字段、一个待处理查询索引；旧模式不新增接待记录，回执和目标复用任务元数据。177 项回归与最终 9 项真实数据库复验通过。Multica 预发和东翔测试号真实沙箱证据保留；Router 024/025 已迁移，预发部署及服务接口验证通过，新响应策略仍关闭。

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

### 员工级前端开关（本轮）

- [x] 仅改 Multica，新增员工字段 `dingtalk_response_enabled`，默认 false；复用现有员工 API、管理权限和策略修订号。
- [x] Web/Desktop 数字员工设置增加“统一响应”开关；AI 标识开关继续独立，关闭响应模式不改变 Coordinator 设置。
- [x] 移除策略同步对 Diamond/环境变量总开关的依赖；只根据员工开关、Coordinator 与已部署能力决定模式。未开启且从未同步过的员工不增加 Router 策略写入。
- [x] 复用既有数据库同步版本屏障，在迁移中提高一次版本以隔离仍运行的旧配置驱动副本；该版本不再是运维配置开关，不新增表。
- [x] 验证默认关闭、单员工开启/关闭、部分更新修订、权限与失败回滚、旧配置不影响新开关；更新文档。Router/Runtime 协议与代码均不改动。

本轮提交 `7bb8512d000b95c51894c823b20a2123aba8f8f7`，只修改 Multica。验证：Core 全量 1512 项、Views 开关 9 项、Handler 7 个顶层测试及 13 子场景、Router 客户端/同步包 race、Core 类型检查、runtimeconfig 测试、服务构建、go vet、sqlc 校验通过。

预发最初 Run3107130284 被同应用另一轮发布接替；当前 Run3107130713 明确包含该提交，构建、部署和集成测试于 2026-09-07 21:03:53 +08:00 全部 SUCCESS，仅停留正常人工预发验证。合并保留其他在役改动，没有重新部署 Router 或构建 Runtime。

浏览器实测入口为员工详情 → 配置 → 数字员工 → 会话与跟进 → 统一响应。使用东翔测试号绑定员工和已有候选 Runtime：页面默认关闭；真实点击开启后，员工字段 true / revision4，Router 回读 multica_coordinator / revision4；再点击关闭，员工字段 false / revision5，Router 回读 legacy / revision5。AI 角标始终 false；测试后已恢复原 Runtime48ac8d56-8c72-4a11-8f8d-ee5a27c28635 和关闭状态。未发额外 IM 消息，未把策略同步验收当成完整入站回复/撤表情验收。临时证据 `/tmp/response-toggle-live-state.json` 不含凭据。

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
- Router 精简提交 f1354513fcf56a50c5c3e6ad1106dcc5fef173d1；合并在役表情修复后为 7be53958c98f52227245b2f1004bf73a926e0b50，61 项交叉回归与构建通过。CR36002519 / Run3107117013 已成功部署预发（2026-09-07 19:54:45 +08:00），release c509931 与已验证合并版本树一致。新 024/025 已应用并复核；初版未执行的 026–029 已删除。
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

### 2026-09-08 HTTP 入站策略丢失修复

用户报告开启统一响应后钉钉没有收到回复。Router 23:55 的两个 dispatch（90d3ac36、5438b50e）确实发送了 responsePolicy 与 responseUrl，Coordinator 生成了 reply，但执行回调没有 managed 标志或响应回执。代码定位为 AgentDispatchV2Request 漏声明并透传 responsePolicy，HTTP 解码后模式丢失。

- [x] 在 HTTP DTO 与 DispatchCommand 转换中保留策略，缺失字段仍保持 legacy。
- [x] 从真实 JSON/HTTP 入口回归到路由注册及发送动作；验证错误策略被拒绝，避免仅构造内部 DispatchCommand 的测试遗漏边界。
- [x] 修复 b8fecbac 已通过 Run3107156896 部署预发，恢复员工统一响应为true；两条旧故障接待已按failed人工清理，未补发旧正文。Router/Runtime 无需改代码。
- [x] 部署后的“你有哪些 skill？”已获得真实delivered回执和message ID，接待记录全部清理完成。完整证据见 [本次HTTP修复记录](2026-09-08-unified-response-http-fix.md)。

1. 数据库与部署阻塞已解除：通过当前 CR 的 prepub#APP#1 配置项定位连接，在目标库完成 024 预演回滚、024/025 正式应用及结构复核。绑定仍87条、新接待表0行；Router预发健康接口和受认证的v1能力接口均HTTP200。流水线仅停留正常人工预发验证，未发布生产服务。
2. 本机配角身份此前遭PAT_ORG_POLICY_DENIED；用户指定东翔测试号后，改用其平台已绑定身份在真实沙箱正常完成get-self、建群、发送和回读，此历史拒绝不再阻塞沙箱验收。AI角标仍须客户端展示证据：原始回读的messageAiSendFlag两种策略均为DWS，不能用来源字段宣称视觉效果通过。
3. 本机候选CLI曾返回SIGKILL/137；用户已明确取消本机验证，此项不再作为本次验收阻塞，保留历史事实。
4. 未声称服务端业务回复、思考中撤除、客户端AI角标及P50/P95全链路已验收；保持新响应开关关闭。

下一步：在已确认的测试员工/新会话上验证 Router 入站到清理的完整链路与延迟；满足门禁后按修订号启用新策略。部署后核对87条绑定的 response_policy 均未设置，代码部署不会自动迁移回复归属。
