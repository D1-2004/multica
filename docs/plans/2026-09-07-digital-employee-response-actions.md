# 数字员工统一响应 Action 与 AI 标识控制

状态：代码实施完成，Multica 预发与 FC 基础验收通过；Router 迁移、真实钉钉链路和本机 Daemon 实机门禁受阻。Coordinator 默认值未修改，新响应策略仍关闭。

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

- [x] Router：策略存储/更新/快照、即时接待、持久化回执清理、legacy 隔离与定向验证。
- [x] Multica Agent：字段/API/UI、修订号与任务快照。
- [x] Multica policy sync：持久化对账、Router 客户端、能力门禁。
- [x] Multica response service：DWS send/status、持久化动作 worker 与回执。
- [x] Multica dispatch：短循环/Issue ACK/静默/失败/合窗/降级/任务结束接入。
- [x] Daemon/Runtime：DWS 参数 hook、回执与能力声明、候选构建；FC 冷启动和热复用 canary 通过。
- [x] 跨项目契约文档、定向测试、构建和 FC 基础兼容验证。
- [ ] 本机旧/新 Daemon 实机矩阵：本机候选 CLI 启动被 macOS 终止，尚未通过。
- [ ] 预发能力/消息回读与延迟对照，满足门禁后才启用。

## 验收

首次已读/思考中 P95 不劣于同环境基线；202 路径只有接单持久化，不执行 DWS/模型。覆盖单群聊、reply/issue/silence、失败取消、合窗忙窗、重复 callback、并发领取和重启、未知发送不重复、AI 标识和正文保真。旧/新 Daemon 与候选 FC Runtime 分别验证。实际未执行或受环境阻塞的门禁原样记录，不能以构建成功代替真实投递。

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
- Router：222845d2ef47bed446c38670b3c54498e6cd69a0，已推送；CR 36002519。171项测试（含4项真实PG故障恢复/非阻塞投递测试）通过。新增SQL 024–029，部署前须迁移；当前缺少迁移连接配置位置，已询问用户，未部署Router。
- Runtime：3c7abe73c165030f1fa06b661134d0b4dc07e1e5，Pipeline 297979 / Run 69575286，独立分支PUSH触发，固定Multica feature提交；构建进行中。
- DWS 环境仍为原prod，没有真实发消息或切换。新响应策略默认关闭。

## 当前交付状态

- Multica Run 3107073778 的代码合并、构建、扫描、预发部署与集成测试均 SUCCESS，停留正常人工预发验证门禁；实际 API 已读回 dingtalk_show_ai_tag=false / revision=1。
- Runtime 修复同一 sandbox 的传输失败删除重试后，以 5a3643030765c82dfa2e4908a23cd9c408494d79 构建成功；Run 69597298，Template pz27zf41o5r2vz53plfa，display alias multica-m7-vdd95d8b615567a87-r1-5a3643；Multica 二进制固定819fc10e7026a68c96c7ad73dbf29d004f007584。
- 私有候选 Runtime c59f8a53-6590-4c1b-b959-866b8f728af7 位于测试空间 d4f9ceed-d114-4312-bb30-dd791aee039b；模板、provider、channel、visibility、能力均已读回。
- 三条真实 FC 任务完成，均实际调用终端且exit_code=0：冷启动932f8de8-337b-4286-b49f-f4dcfc0d7a8e，热状态写入8cf2dcf7-f9a0-4169-b7b1-b9c0e8b75b34，热状态读取/清理e9c89742-314d-4ff1-a525-d7d2ed0f2a9d。后两轮随机哨兵一致、Task ID每轮更新；canary员工d9cdd8d5-9a9b-423b-8778-8fbd53717b75已归档，临时文件已删除。
- 正确预发 Diamond unit为pre，已确认监听仅两台预发主机；只增加dd95d8b615567a87的provider映射及消息策略能力allowlist，两个监听均确认推送。response_policy_enabled仍false，未修改生产配置。
- 原测试员工e2293e9e-1e79-4926-b0e6-da4cb693add0保持原Runtime48ac8d56-8c72-4a11-8f8d-ee5a27c28635，AI标识false、Coordinator及task-finished原值保持不变。

## 阻塞与下一步

1. Router SQL024–029尚未执行，CR36002519尚未提交部署；已请求用户提供迁移用数据库连接配置的文件位置或Keychain条目，不要求在聊天中提供密码。
2. DWS新建隔离测试会话在写入前被PAT_ORG_POLICY_DENIED拒绝，scope=contact.user:get-self、tool=get_current_user_profile；需要组织管理员授权后才能继续真实外发/标识回读。未绕过策略或改用其他身份发送。
3. 本机候选CLI --version返回SIGKILL/137，AMFI记录过CT签名错误；签名验证及正常开发证书重签后仍未能启动，未完成真实本地Daemon矩阵。未修改系统安全设置。
4. 因此未声称平台回复、思考中撤除、真实AI标识及P50/P95全链路已验收；保持新响应开关关闭。

收到缺失配置并解除组织策略后：先执行Router新增迁移并部署CR，再在已确认的测试员工/新会话上验证端到端与延迟；按修订号启用仅候选Runtime支持的新策略。
