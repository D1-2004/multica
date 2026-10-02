# Employee Execution Event 实现计划

状态：待记忆写入、纠正、隔离、遗忘的真实 IM 验收通过后实施。

## 目标与边界

将已提交的 EmployeeRun 终态接入统一事件 admission，能够追溯“哪个终态事实已被 Employee 消费、是否仍属于当前目标”。首增量不增加模型调用、后台任务或第二次结果通知，不将完成文本解释为继续执行的授权。

参考 GawkBot 固定提交 `71e82a1809565281cbd0bf8185d3c125b715d934`：`internal/bot/loop.go:handleDone` 仅在已有排队消息时继续循环；`internal/team/headless_event.go` 输出结构化终态；`internal/bot/queues.go:FollowUp` 是单独的显式入队动作。记录完成事实与请求继续工作分开处理，底层复用本仓 PostgreSQL admission、租约和通知设施。

## 首增量合同

- 首版只处理能核对原 job/source 和 `direct_principal_id` 的原场域 Employee Direct Run/result。Issue 路径缺少对应来源时不猜身份。Host 构造 task/run/queue ID、goal revision、终态、result_ref 和数据库完成时间；结果正文只是数据。
- 使用新的稳定内部 source 与 run 身份作为幂等键。scene、tenant、owner、principal 均来自原受理记录，不取 payload 提供的身份。重试沿用原 route。
- `eventrouter.AdmitWithHook` 在同一事务持久化 `scene_event_receipt` 和 `employee_event_consumption`。它接收 locator：适配器从原 scene 目录取得 locator，并在同事务核对 receipt scene ID 等于原 ID，不接受重新解析后的替代场域。校验通过的事实沿用 `state=completed, job_id=NULL`；无法解析的事实保持 held。不能调用会创建消息 job 的现有 `Store.Admit`，需新增窄消费入口。
- 核对 queue、run、task 的对应关系与当前 tenant fence。旧 revision 作为历史事实记录，不推动当前目标；租户解绑后不得跨场域读取或发送。原 principal 失效不允许换人或授权新工作，也不通过消息 Worker 的 invoke 校验制造无限重试。
- 从终态 Run 补偿扫描缺失 receipt。提交后崩溃、双 worker 或重复事件最终只生成一份事实和消费记录；内存通知仅用于加快扫描。
- `ReconcileEmployeeRunNotices` 继续独占结果通知，独立于事件记录，不等待事件补偿链。消费成功不等于钉钉送达。
- Langfuse/SLS 记录确定性消费事实与 task/run/queue/receipt ID；不制造 generation、token usage 或假模型时长。结果正文不必重复导出。

## 实现接缝

- `server/internal/service/employee_task_lifecycle.go`：复用 Run 终态记录，新增“已终态且缺少内部 source receipt”的补偿查询；现有 `ReconcileEmployeeRuns` 只找仍 running 的修复对象，不能用于扫描正常完成的 Run。
- `server/internal/eventrouter/router.go`：现有 `RunCallback` 类别和 `AdmitWithHook`。
- `server/internal/employeeentry/store.go`：窄的确定性消费方法，保持消息合窗和模型 journal 行为。
- `server/internal/handler/employee_scene_entry_worker.go`：接入已有周期补偿，不在消息 Worker 中解码伪造 `DispatchMessage`。
- `server/internal/handler/employee_run_notice.go`、`employee_run_notice_policy.go`：保持既有通知归属和抑制规则。

实现前核对旧 receipt replay 不再调用 hook 的约束，不能依赖新增 hook 自动修复历史旧来源。不得为这一增量另建 scheduler 或事件总线。

## 验证与交付

- [ ] 真 PostgreSQL：成功、失败、取消、启动失败、重复消费、双 worker、终态提交后崩溃恢复。
- [ ] 真 PostgreSQL：旧 revision 晚到、模式切换、tenant rebound、原 principal 不再有效；事件不能授权新工作。
- [ ] 断言零新增模型 job、零模型请求、零重复 Task；原通知和文件静音规则不变。
- [ ] 独立复审，提交后由子代理同步最新 `feat/tag-multitenant`，预发部署。
- [ ] 冬翔 → Qwen 实际成功/失败 Direct 各一例，追溯 run → receipt → consumption；核对结果只送一次且没有新 generation。

## 后续 Cron / Webhook

Cron 复用 `jobs_autopilot.go`、`sys_cron_executions` 和 `(trigger_id, planned_at)` 幂等；Webhook 复用现有验签 ingress、dedupe、delivery 租约 worker 及已受理 `run_id` 协议。先补自动化 owner/creator 授权适配，不能把 Agent 创建者填成管理员或伪装成真人 DirectRequest。事件 envelope 用 category/schema 区分 scheduled/webhook wake，必要的推理进入 `Input.FollowUps`。

只有持久化、明确授权的后续工作才触发模型续行：原任务的条件后续步骤，或已启用且要求 Employee 作判断的 routine。普通成功、失败、取消、文件已送达都不自动增加一轮 LLM。自动化派发与现有 routine 通知须先确定唯一发送归属，再进入真实到点/验签 E2E。
