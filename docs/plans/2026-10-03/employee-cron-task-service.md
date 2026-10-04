# 场域 Cron 接入 Employee Task Service

状态（2026-10-03 B wave 1）：routine 来源 reader 与 run_only producer 已在 `employee/w1-b-cron` 实现并本地真 PG 验证；producer 受 EmployeeLoop 副本 marker 门禁，未部署、未真实到点验收。decision（B3）未开始。

## 目标

先接场域绑定的 `run_only + schedule`。复用现有 scheduler、真实 AutopilotRun、运行记录和 routine 通知，确定性编译已配置的工作包并派发 EmployeeTask/Run，零额外前台模型调用。普通非场域 Autopilot 保持原路径；Webhook 在该来源合同稳定后复用，当前不启用条件 FollowUp。

参考 GawkBot 固定提交 `71e82a1809565281cbd0bf8185d3c125b715d934` 的 `handleRegisterRoutine`、`processBotJob` 和 `CompleteSchedulerRun`：持久例行任务、保留人工暂停、单次 occurrence 与周期配置分离。继续使用本仓 PostgreSQL scheduler，不引入另一套 timer、watchdog 或文件状态。

## 一次受理的合同

- 使用现有 DB 时间、时区、latest-only、租约及 `(trigger_id, planned_at)` 幂等。prepared Title/Instructions/scene/planned_at/timezone 足以编译；文本里出现“如果”不自动增加前台 LLM。
- 新场域 occurrence 同一事务保存真实 AutopilotRun、冻结 owner 和输入、原始 receipt、EmployeeTask/Run、queue、`autopilot_run.task_id` 与 start 意图，提交后才 Notify。
- 保留真实 `queue.autopilot_run_id`。它对应一次实际 schedule，不是即时任务的占位 Autopilot。
- 重投先读已保存 occurrence。模式或 Instructions 修改不能改写已接受工作；新冻结来源不能进入 `RecoverPartialAutopilotRun` 清 planned_at、另建 Run 的旧分支。
- 不冻结 Agent 全配置或认证密钥。运行时认证仍按现有可信 Host 装配，工作包只保留任务所需指令和来源引用。

## 权限、来源与通知

- 新增窄的 `RoutineExecutionOrigin` / `EnqueueRoutineDirectTaskInTx`，复用 Direct 写入核心。原 member-only Direct 入口和禁止任意 AutopilotRunID 的约束保留；只放行数据库验证的 routine 来源。
- Member creator 仍需当前成员身份及现有 private owner / public_to 调用权限。Agent creator 必须是同工作区真实 Agent，并遵守现有调用规则，不能借用管理员或把 Agent UUID 填入 member 字段。
- Cron originator 为空；trigger_owner → rule_owner → 既有 accountable fallback 仅用于归因，不授予权限。
- 来源为 `scene.routine.schedule`，ID 来自 trigger 与 canonical planned time，类别为 Wake。Host 构造 scene/owner/principal；不制造 DispatchMessage、人类 source_ref 或消息 job。
- 来源证明：occurrence receipt → 冻结 AutopilotRun/routine 绑定 → Task ledger/run_started → 实际 queue。先扩展 Execution Event 的来源验证，再生产此类任务；未知来源继续拒绝。
- `RequesterRef=routine:<id>` 明确不是人类，不进入 requester-private learning。共享记忆或自动化记忆需另立合同，不能借用原消息身份。
- routine 保持唯一通知 owner，排除消息型 Employee Run notice。复用 start/end outbox 幂等键，每阶段至多一次；补 queue 已终态但 AutopilotRun/最终通知未结算的持久恢复。
- 暂停阻止后续 occurrence，不撤销已接受执行；权限或 tenant 失效不改投其他场域。

## 接缝与分步交付

1. **来源读者先上线。** 修改 Direct 来源校验、claim、终态、Execution Event routine 分支和 learning 排除。`handler/daemon.go` 已有 DirectTaskPrompt 时不能以当前 Autopilot 配置覆盖它。测试 DirectTaskPrompt 与真实 AutopilotRunID 同时存在的完整 claim；不能仅凭字段存在声称旧 Runtime 已兼容。
2. **Cron 原子派发。** 在 `service/autopilot.go:DispatchAutopilotForPlan` 的场域分支接现有确定性 compiler 和新 in-tx adapter；保留 `scheduler/jobs_autopilot.go` 调度设施及普通 Autopilot 协议。
3. **恢复与实际到点验收。** 同 Run 重投、终态与通知对账、暂停和恢复；更新当前合同和 source map。

新 producer 等所有 live replicas 理解 routine 来源后才开启。使用专用 routine 能力标记，普通 IM marker 5 不需要机械升级。若现有 Daemon 对两字段组合无法正确执行，按已授权的远端 Runtime 构建链修复，禁止本地镜像构建。

## 验证

- [ ] 双 scheduler 同 occurrence 仅一组 AutopilotRun/EmployeeTask/Run/queue，事务中断全回滚，Notify 丢失可恢复。
- [ ] mode/Instructions 改动后重投保留归属与输入；Member/Agent 权限矩阵、撤权、private Agent、tenant rebound。
- [ ] 伪造 outer/inner context 不代替 occurrence/ledger 证明；无消息 job、前台 generation 或人类私有学习。
- [ ] 成功、失败、取消、启动失败及 Bus 丢失；routine start/end 至多各一份；原消息 Direct 和普通 Autopilot 回归。
- [ ] 独立复审 → 提交 → 子代理同步目标 → 预发 → 到点 E2E。
- [ ] 在授权场域创建独立测试 routine，实际等待 cron，核对所有关联 ID、输出和 Event；受理后暂停，越过下一时点证明无第二次执行，第一轮仍可结束；恢复后验证下一合法 occurrence，最后仅停用并清理本次对象。

Webhook 下一步复用现有验签 ingress、dedupe 和 delivery worker，保留已接受的 run_id 协议；payload 的身份和场域字段永远不能授予权限或改变配置好的目标。

## 实现说明（B wave 1）

- **冻结来源。** 表 `employee_routine_occurrence`（9930–9934）：每次发生一行，`source` 为 `scene.routine.schedule` / `scene.routine.manual`，`source_event_id` 为 `trigger_id/规范 UTC planned_at` 或 `manual/<AutopilotRun>`；`occurred_at` 为首次提交的 DB 时间。保存 routine/autopilot/trigger/agent/workspace/org/scene、creator kind+id、手动运行成员、`config_revision`（最新 autopilot_rule_version）、`dispatch_mode=employee_direct`、授权引用、无密钥的冻结输入和编译 prompt 的 sha256。被拒绝的发生（暂停、场域失效、权限撤销、离线、重叠、配置错误）同样落一行，重投直接返回原结果。
- **一次事务。** routine 行 `FOR UPDATE` 串行化同一 routine；同事务写真实 AutopilotRun、EmployeeTask（v1 single_run，`requester_ref=routine:<id>`，无人类 originator）、Run、queue（带真实 `autopilot_run_id`、`employee_automation_origin` 定位符、`scene_routine` 绑定、`employee_delivery_owner=scene_routine`）、APRun.task_id 映射、receipt 和 routine 开始通知意图；提交后才唤醒执行器与 outbox。`last_run_at` 在提交后更新，避免与 routine 编辑的锁序相反。
- **reader。** `ParseDirectTaskContext` 只接受“真实 autopilot_run_id + 同一 run 的已知 kind 定位符”这一组合；`service.LoadAutomationOrigin` 从 PG 交叉核对 receipt→APRun→Task→Run→queue 和 prompt 指纹，结果是只能由本包 loader 构造的 `AutomationOrigin`。claim 使用冻结 packet 并追加 routine 输出合同，不再把当前 autopilot 指令放进响应；terminal 记录结果前验证来源；Execution Event 不把 routine 当消息事实；learning 以 `automation_origin` 跳过；消息 Run notice 只消费 owner=employee，因此 routine 的开始/结束通知是唯一发送方。
- **重叠策略。** 同一 routine 上一次已受理发生的 Run 仍为 running（排队或执行中）时，本次记为 skipped AutopilotRun + `skipped_overlap`（带被重叠的 occurrence），不入队；节拍不变。
- **配置错误与节拍。** 绑定不一致、无标题/指令、通知无投递目标记 failed AutopilotRun；暂停、场域/租户失效、权限撤销、运行时离线或不支持 Direct 记 skipped。调度器都视为该 slot 已处理，不重试、不改节拍。
- **恢复。** commit 后、通知前崩溃：调度器 stale 重入读到 receipt，只重复唤醒，ID 与字节不变；旧 `RecoverPartialAutopilotRun` 分支在 receipt 检查之后，永不作用于新来源。队列已终态但 APRun 仍 running（丢失任务事件）由 `ReconcileEmployeeRoutineRuns` 每 5 秒补结算，结束通知已存在时不重发。
- **滚动门禁。** 仅当 `EmployeeLoopReplicaMarker` 被所有在线副本声明（`EmployeeSceneWorker.ReplicaReady`）时产生新形状；否则该发生仍走原 Autopilot run_only 路径。marker 编号由主代理在合入时分配。
- **后续 Task wake 接口。** `LoadAutomationTaskOrigin(scope, taskID)` 按 Task.Source.Namespace（`AutomationTaskSourceNamespaces`）返回 scope、投递锚点、creator principal 与历史策略（群/单聊：用该场域当前 dispatch endpoint principal 读历史，绝不用 routine creator；enterprise：不适用）。`ListRoutineOccurrenceOutcomes` 给 B3 提供最近 N 次发生的确定性结果（时间、状态、是否送达、正文/结果哈希）。

## 实现说明（B wave 2：routine decision）

- **执行选择。** `context_scope_routine.employee_execution`（9820，默认 `run_only`）：`run_only` / `employee_decide`。不写入旧二进制会解析的 `autopilot.execution_mode`。配置页、Admin 和场域 MCP（`scene_routine_create/update` 的 `employee_execution`）可切换；选择 `employee_decide` 需全部在线副本具备 decision reader，否则 409 `routine_decision_unavailable`。已受理的 occurrence 冻结当时的选择。
- **受理。** `employee_decide` 的 occurrence 同一事务写真实 AutopilotRun（running，无 task_id）、EmployeeTask（v1，`requester_ref=routine:<id>`）、receipt（`state=decision`、`dispatch_mode=employee_decide`、冻结 packet 指纹）、`employee_routine_decision`（pending）和一个 `routine.decision` typed wake（source `employee.task_wake/scene.routine`，event id = receipt id，occurred_at = receipt 首次提交时间）。不发开始通知，不入队。门禁关闭时记 skipped，不会退化成无条件执行。
- **Origin reader。** `employeeRoutineTaskOriginReader` 注册在 `scene.routine.schedule` / `scene.routine.manual`（`MustRegister`，重复注册在启动时 panic）：从 PG 读冻结 receipt、核对当前 routine principal 权限；历史用该 agent 当前 dispatch endpoint 的 actor（并验证其当前成员与调用权限），从不用 routine creator；DM 投递对象取 routine 冻结的 counterpart。
- **Decision wake。** 在 P2 的 task wake 上加 kind 扩展：输入追加冻结规则（标题、说明、cron、计划本地时间）和最近 5 次确定性结果；工具只有 `run_routine`（执行冻结 packet）、`reply`（本场域一次）、`wait_for_next_occurrence`、`stay_quiet`。`run_routine` 在 tool journal 事务中重核授权、重编译并核对 packet 指纹，再写 queue/Run/APRun.task_id/开始通知；之后的结束通知、ExecutionEvent 排除、learning 跳过与 run_only 相同。三次模型请求预算和失败计数沿用 job journal。
- **结算。** quiet / waited / replied / failed 在 job 完成事务中（reply 在发送入队之后）把 AutopilotRun 记为 completed（failed 记 failed），`result.employee_decision` 写明状态与理由，并由 routine requester 关闭 Task，不伪造 Run。wake 被 hold 或被旧二进制完成而未结算的，由 `ReconcileEmployeeRoutineRuns` 记 failed，避免永久 overlap。
- **重叠。** 上一次 decision 未决或其派发的 Run 仍在执行时，本次记 `skipped_overlap`。
