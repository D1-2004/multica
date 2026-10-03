# 场域 Cron 接入 Employee Task Service

状态：Execution Event 的 legacy 修复真实 E2E 已通过，正在实施第 1 个可信来源增量；尚未开启 Cron producer。

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
