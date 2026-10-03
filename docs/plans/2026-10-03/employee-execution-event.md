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

## 实现与验证状态

实现已完成待预发。原来源同时核对持久 `run_started` 与原 job 已提交的 dispatch tool journal，queue context 不能单独证明归属。合法来源仍在提交时会等待，不永久跳过；已确认不合法的历史来源以 version/run_id/fixed reason 追加到 queue context，CAS 保留原字段与 `employee_direct_input`，不冒充消费成功。消费与 Run 终态在日志和 Langfuse 分别命名 `state`、`run_state`，并带 `result_ref`，无结果正文。

独立复审通过；作者最终 handler race 6.522s、entry/router 全包 race 通过；root 独立 handler race 5.168s、entry/router 1.838s/1.320s。最后补齐终态观测字段的成功/失败/取消参数化回归 race 2.911s 通过，build 与 diff-check 通过。旧 role/skill 测试允许新快照追加 Host 表达指导，仍核对原 role 完整前缀及能力目录。

真实 E2E 使用成功和取消两条隔离任务。现有 Direct 请求没有单任务 deadline/timeout/max-turns，执行超时是 daemon 全局配置；不为制造失败修改共享 Runtime。失败终态由 PG 回归覆盖，真实 failed E2E 尚未覆盖；错误命令被 agent 正常解释后返回 succeeded，不能当作 Run failed 的证据。取消使用已授权、精确 queue ID 的用户 API，并核对真实 daemon 停止。

## 第一轮真实 E2E 与来源修复

`1c867ee80d3dcc5df2ae78cce1647244195ff3fa` 经预发 `3110330026` 成功发布。真实 job `b463a322-98ac-438f-a6b5-0de26de61cd1` 派发 queue `7453a067-bad4-4c3c-93d6-02e8b346d946` / Run `33846107-0ee5-4971-b860-e9a4e42f1d9d`；08:36:11 的 bash transcript 证明 Python 运行，08:36:14 完成，08:36:17 Host 只发一次结果。前台只有一次 generation，但无终态 Event，本轮事件验收不通过。

跨副本 SLS 证明该 Run 及 18 条历史 Run 被误判为 `source_receipt_mismatch`。读取真实原 receipt `3e63b6ea-4500-46ea-a051-932cde9dd934` 确认其 route/state 为合法 `legacy/legacy`，canonical scene 与已完成 Employee consumption 都存在。旧校验把 provider 路由错误等同于 Loop 所有权，只接受 unified/ready。单副本日志未命中不是事件未处理的证明；复查发布分支也确认实际包含本提交。

修复通过真实 `HandleDWSNativeEvent` 解码、账户与 endpoint principal、默认 legacy admission、实际 dispatch journal 的 RED→GREEN 复现。接受 coherent legacy/legacy 与 unified/ready，场域、主体、原消费、run_started 和 journal 关联仍逐项校验，unmapped/错配仍拒绝。

旧 skip 采用 `version=1` 存储格式加 `proof_version=2` 校验版本：旧副本继续识别兼容 marker，新副本重评旧无 proof 的误判；拿 scene 锁后再次尊重当前或更高 proof。身份行 FOR SHARE 覆盖 tenant fence 至提交，复用现有服务租户判断以支持合法 secondary tenant，避免解绑时进入机器人 fallback。缺失身份仍 held，数据库/取消错误回滚重试。

作者最终 handler race 8.297s、root 独立 7.346s；entry/router race、build、独立复审通过。上线后先验证原 Run 自动补事实、无任务重执行和重复通知，再做新取消与成功输出实测；修复上线后的事件验收仍待完成。

## 修复后真实验收通过（预发 3110331461）

目标 `b9ea5040f4559026a0fafa273880b7ca6398217c` 构建、部署、集成测试成功。以下三个独立验证通过；每个原始 Employee job 均只有一次前台 generation，没有因终态消费新增模型调用。

| 场景 | Run | 执行 receipt | 结果 |
|---|---|---|---|
| 旧误标自动恢复 | `33846107-0ee5-4971-b860-e9a4e42f1d9d` | `61083031-b0ac-4aef-a427-3e6c7140db2f` | succeeded，原 job `b463a322-98ac-438f-a6b5-0de26de61cd1` 补出一条 Event |
| 运行中取消 | `4acae247-7952-4696-b6c9-f072605cf1f4` | `7175299e-8ac1-473a-a160-8273c47086b6` | cancelled，job `83634646-8a31-4405-9053-4487f190a537` 一条 Event |
| 新成功与原样输出 | `08a265f1-f504-4546-81aa-aa4a05551d30` | `a9b3fdf9-d2ae-4c7b-a857-497e025f704e` | succeeded，job `ee79a691-2540-4f21-a935-b638a084fd1c` 一条 Event |

回执 API 读到真实 `employee.execution`、原 legacy 路由和正确 scene；Langfuse 的消费 state=completed 与实际 run_state 分开，receipt/source_receipt/queue ID 逐项对应。旧 Run 没有重执行；其执行时间仍为 08:36:03—08:36:14。

取消 queue `94fe80ae-59eb-46ee-85b9-2814cb3acb7a` 的 transcript 确认 Python sleep(90) 已启动。用户取消接口在 09:45:19 返回 cancelled，Daemon cancel-ack 在 09:45:21 为 200，09:45:50 sandbox `sbx-fdb850a6-bfbd-45d1-bfee-f628560d2aa6` 被 idle_trimmed，未出现 finished 输出。群里仅一次取消说明 `msgMsxzjL7hUOXntqoVcKad9Q==`，没有追加完成总结。

新成功 queue `b4c0bdf8-fa6d-49c4-ba28-00c711b68b46` 的真实 bash 输出与 queue.result.output、钉钉消息 `msg/C8DN1EWc3tKWodMH2PMog==` 逐字一致，无固定前缀。真实 failed 终态仍只由 PG 回归覆盖。原始 SLS 查询取满 100 行，不据单页声称全时间窗日志总数；唯一回执和 Event 以精确 ID 查询核对。

后续质量项：一次派发 ACK 提前使用“Python 已在沙箱启动”的表述，实际当时仅入队；状态事实与最终结果均正确，但接受 ACK 的措辞仍可进一步收紧。该项不改变本次终态事件的验收结论。
