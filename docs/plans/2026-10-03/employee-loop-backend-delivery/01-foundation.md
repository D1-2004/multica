# P 包：独立 Task 生命周期、内部唤醒与共享接缝

> **验收与实现方针：**先读 [Step 0](00-step-0-environment.md) 与 [交付标准](10-delivery-standard.md)。本包的内部API/表结构/阈值/步骤是参考路线，可由主代理协调优化；业务结果、权限、幂等、恢复、真实证据及已合入公共合同必须保持。

> **执行方式：**P 按 P1→P2→P3→P4 逐项实施并独立审查。其他包可写自己的领域代码，但只有 P/I 修改本文件列出的共享接缝。

**目标：**一次 Run 完成与整件 Task 完成分开；聊天和内部 Task wake 共用已持久的模型/工具引擎；自动化来源可验证；Task 可独立查询和运维。

**Architecture：**扩展现有 employeetask/employeeentry，不新造执行器、模型调度器或 Redis mailbox。新任务选择生命周期 v2，旧任务和旧快照保持已接受语义；来源 reader 先发布，producer 后开启。

## 1. 所有权与现有入口

P/I 可改：

```text
server/internal/employeetask/types.go
server/internal/employeetask/store.go
server/internal/employeetask/current.go
server/internal/employeeentry/types.go
server/internal/employeeentry/store.go
server/internal/handler/employee_scene_entry_worker.go
server/internal/handler/employee_scene_entry_host.go
server/internal/handler/employee_current_tasks.go
server/internal/handler/employee_run_notice.go
server/internal/service/direct_task.go
server/internal/service/direct_task_continuation.go
server/internal/handler/employee_execution_event.go
server/internal/handler/daemon.go
server/cmd/server/router.go
```

拟新增并由 P 持有：`employeetask/lifecycle.go`、`employeeentry/task_wake.go`、`handler/employee_task_wake.go`、`service/employee_execution_origin.go`、`handler/employee_tasks_http.go` 及相邻测试。migration / sqlc / marker 统一由 I 登记和生成。A/B/F 不越过本列表自行接线。

## 2. P1：Task 生命周期 v2

**参考合同：**可新增 `lifecycle_version` 和 `completion_mode`。等价方案也可采用，但须区分目标/执行语义、保留旧任务行为并经主代理确认公共协议。既有记录默认 `1/single_run`，继续现有 RecordResult 行为；新普通 Direct 也可以 single_run。收集/多步骤目标显式 `2/explicit_goal`，Task 状态增加 waiting，Run 状态仍只有 running/succeeded/failed/cancelled。

| 当前事项 | Task | Run | 完成方式 |
| --- | --- | --- | --- |
| 普通单次计算/文件 | running→succeeded | running→succeeded | 原 single_run 合同 |
| 向三人发问后等答 | waiting | 发问 Run 如有则 succeeded | 等待槽位未满足，不能自动 Task succeeded |
| 收齐等待汇总 | ready | 无 active Run | 唯一 ready intent 创建 origin wake |
| 汇总实际回报 | succeeded | 汇总若需执行可有新 Run | explicit complete + 满足当前依赖 + 当前版本 |
| 明确停止 | cancelled | 保留真实终态/退出状态 | 原 Stop fence，不能被输入或 read 消费解除 |
| 一次执行失败 | ready/等待决策 | failed | v2 不默认消灭整个目标；无自动无限重试 |

拟新增 Host 方法：`WaitTaskTx`、`ReadyTaskTx`、`CompleteGoalTx`。它们接收已验证 Task scope、ExpectedVersion、Source、冻结 input boundary 和原授权引用，不接受自由 actor/scene。明确错误：scope mismatch→not_found；版本/同源内容变化→conflict；未满足等待/有 active writer/缺证明→not_ready；人工停止→stopped。

等待项作为领域事实保存于 A 的 collection 或 P 的 task wait 记录，Task.state 是聚合投影。不能用 Redis TTL 或模型说“等好了”作为依赖满足证据。v2 目标完成须当前 goal_revision、输入水位、无未满足 mandatory wait、无 active/pending writer，且 complete 动作有源授权。

- [ ] P1.1 写 `TestExplicitGoalRunSuccessDoesNotFinishWaitingTask` 和 `TestSingleRunLegacyResultStillFinishesTask`：同 Run 成功，新/旧 Task 得到不同且正确的目标状态。
- [ ] P1.2 写 `TestCompleteGoalRejectsUnsatisfiedWaitStaleVersionAndStopped`，覆盖并发新输入、旧版本汇总、退出未确认。
- [ ] P1.3 在真实 PG 观察上述反例失败，再加入 lifecycle 字段、waiting CHECK 和明确方法；不回填旧 Task 为 v2。
- [ ] P1.4 更新 StartRun/Resume/steer/Stop/current candidates 的 v2 合法转移。v1 cancelled/failed 的隐式复活仍拒绝；v2 retry 必须由显式授权动作且有退出证明，不借普通纠正。
- [ ] P1.5 验证：`go test -race ./internal/employeetask -count=1`，运行无 Issue/queue 表的核心 fixture；检查 rollback、CAS、重投、长账本和 stop 不变，再单独提交。

**本批可评审成果：**状态迁移表、真实 PG 测试和领域 API；尚未宣称跨场域可用。

## 3. P2：typed Task wake

`Admit` 当前要求消息数≥1；ProcessNext 把所有 item 解码为消息。新入口 `AdmitTaskWake` 必须独立建 job，禁止伪造 DispatchMessage 来复用消息分支，也不与人类合窗。

拟定内部载荷（不是对公网/模型开放的授权 API）：

```go
type TaskWake struct {
    SchemaVersion int    `json:"schema_version"`
    Kind          string `json:"kind"`
    TaskID        string `json:"task_id"`
    GoalRevision  int64  `json:"goal_revision"`
    InputSeq      int64  `json:"input_seq"`
    AuthorityRef  string `json:"authority_ref"`
    EvidenceRef   string `json:"evidence_ref"`
}
```

kind 首批为 `collection.ready`、`execution.follow_up`、`routine.decision`、`webhook.decision`。watchdog 首版无需模型 wake。字段只是引用，Host 必须从 PG 重建 scope、owner、principal kind 和 delivery anchor；不能信 payload 中自报的目标或权限。

- job 增加明确 kind（旧默认 message）；message_count 仅 message 为1–32，task_wake 必须0、单 item，非聊天合窗。
- 在冻结 input snapshot 前重验原 Task/trigger 授权、当前 Agent/tenant、目标 scene 目录。receipt+consumption+job 原子创建。
- `job.PrincipalID` 不再被一律当 member：新的冻结 authority 描述必须区分人类/endpoint principal、Agent creator、原 Task authority。Agent UUID 永不传入 member/OriginatorUserID 字段；Agent creator 按真实 Agent invoke 合同验证。
- 内部事件 source/id、occurred_at、fingerprint 在 intent 首次提交时冻结；重试不拿 now() 改 identity。
- eventrouter 已存在的 receipt replay 不再调用新 hook。新内部 wake 必须有自己的来源 identity；不能靠重放旧 provider receipt 自动补上刚安装的消费者，历史补偿须有单独且可验证的派生来源。
- 原 job 的聊天/工具 journal、history renderer 和 token 安装逻辑保留。新的 Task context 作为数据，不插入一条伪造人类指令；原生 tool role 仍一一配对。
- A 的跨场域 ready event 归 origin scene，B 原 receipt/principal 不改；一般外部无会话资源事件仍 enterprise，只有核验原 Task authority 的派生 wake 才能寻址 origin。
- 复用当前 Claim/Seal/lease/generation/model reserve/complete/outbox。新的接口入口增加 source reader，不开第二个 Run loop。
- cached outcome 恢复不依赖模型 registry 再就绪；当前权限/runtime/fence 仍核验。旧冻结 snapshot 不热补 kind/工具。

- [ ] P2.1 先写 `TestTaskWakeDoesNotCreateSyntheticChatOrMergeHumanWindow`、`TestTaskWakeReceiptReplayCreatesOneJob`，证明0消息当前会拒绝及明确分支缺失。
- [ ] P2.2 写 `TestTaskWakeSourceScopeAuthorityRevokedAndFingerprintConflict`、`TestTaskWakeCrashAfterIntentCommitRecoversOriginalJob`。
- [ ] P2.3 先扩展存储/reader 与 migration，再在 worker Dispatch 解码前按 job kind 分流；把新 builder 提取至独立文件。
- [ ] P2.4 为 complete 增加 typed return target 分支：按原 delivery anchor 发送/抑制，不调用消息 callback 对象；持久一次 intent 后通知。
- [ ] P2.5 `go test -race ./internal/employeeentry ./internal/eventrouter ./internal/service/employeeloop -count=1`；handler 新旧输入专项单独跑，证据确认无新隐式模型。

## 4. P3：Execution follow-up 与自动化 reader

普通 `execution.terminal` 保持事实消费（job_id=NULL）。不能把它改成所有成功/失败都再问一轮模型。

**execution.follow_up 授权合同：**原 Task explicit_goal 的 work plan 预先保存下一步和允许的事件条件；Host 核对确切 terminal Run、goal revision、plan revision、条件及退出事实，只为匹配步骤写一个 follow-up intent。每个 plan revision 最多消费一次该 terminal，建议起始上限为8个后续步骤，主代理按实际资源设定并冻结有界预算；超限保存需要人工输入的状态，无新请求。新目标/纠正使旧 plan wake 失效；取消后不继续。确定性单次派发不调用前台模型，语义判断使用正常 typed wake 的≤3次预算。

**自动化来源 reader：**统一验证冻结的来源引用和所有者，但保留 schedule/webhook 各自 payload/幂等协议。service constructor 从 PG 返回不可由 HTTP/model 直接构造的 validated origin。Member creator、Agent creator、endpoint installation 分别调用已有当前权限服务；不借管理员 fallback。

routine 的 Direct prompt 与真实 autopilot_run_id 组合需要新 reader：保持 queue 是真实 routine occurrence，claim 不拿当前 autopilot Instructions 覆盖冻结 prompt，terminal/ExecutionEvent/learning/通知都识别来源。Webhook 按现有真实 delivery/run 绑定，不伪造 planned_at。自动化不是 requester-private 人类学习来源。

- [ ] P3.1 写普通终态零 wake、已授权 follow-up 一 wake、旧 revision/取消零 wake、并发重放唯一 intent 的 PG 回归。
- [ ] P3.2 写 `DirectTaskPrompt + AutopilotRunID` 的完整 claim→terminal 来源测试；只测 parser 接受字段不算通过。
- [ ] P3.3 reader 发布到全部在线副本后才开放 B/F producer；新来源 marker 单独登记。若 Daemon 不支持组合，先走远端 Runtime 兼容修复，不降低来源校验。
- [ ] P3.4 测试 ordinary Autopilot、message Direct、Issue adapter、通知 owner 三组保持原行为，再提交 reader 切片。

## 5. P4：独立 Task 运维接口与当前事项

已有 `/tasks` 多为 queue 接口，不能让 UI/外部工具把 Queue ID 或 Issue ID 当 EmployeeTask。拟增加 workspace-scoped `/api/employee-tasks`、`/{taskId}`、`/{taskId}/entries`、`/{taskId}/runs` 四个只读路由；这是拟定 API，当前尚不存在。沿现有 workspace middleware/member gate；路径 UUID 先 loader 解析；originator/管理者/精确执行凭据分别沿已有 privacy 合同。

返回：Task/Run/queue 三种 ID 明确命名，goal revision、version、input_seq、state、waiting counts、execution exit state、result_ref、delivery state 和 evidence refs。participant 视图只读本 invitation，不通过通用 Task detail 读取全部答案。账本游标基于 seq，有界分页；不返回 secret、bearer link 或任意他人 private memory。

`read_task`/TaskBrief 共用该真实聚合投影，active候选包含waiting；只有一个符合权限和时窗的候选才可隐式选择。旧 `read_ref` 绑定本 wake/current version，不能拿历史文本 ID 操作控制。

- [ ] 写跨 workspace/scene/requester、机器凭据、participant 私有视图及错误 UUID 的 handler 回归，再接四个路由。
- [ ] consumer schema 若有前端调用则同步 core zod/漂移测试；本次后端完成不以 FE 全部页面作为前置。
- [ ] 更新 `docs/employee-loop.md`、employeeentry README 和 source map；把旧“控制未注册”段落改成准确当前限制。

## 6. 交付与兼容

P1/P2/P3/P4 各自独立提交、同步和门禁，不作为一坨代码最后一次发布。读者先上线，producer 后启用；新 enum/job 只能在所有副本具备 capability 后生成。混版期间旧消费不能误 claim 新 wake，停止/通知不能绕过 marker。若旧二进制不理解新状态，必须关闭新 producer并排空/保留待新读者恢复的工作，不能只切设置就硬回滚。

退出验收：旧 IM 六类剧本全过，typed wake 不产生假聊天，v2等待不被Run成功终结，自动化来源不会借用人类授权，源重投/崩溃/两副本同 claim 均唯一。P 的代码绿后才允许 A2/B2/F2/I 接线进入各自真实验收。
