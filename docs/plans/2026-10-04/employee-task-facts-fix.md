# Task 归属和收集过程事实修复

根 Plan：`docs/plans/2026-10-04/employee-four-fixes.md`。范围只含明确独立新任务的合同优先级、collection.ready 的可信过程事实。没有 SLA、Runtime、部署或真实 IM。

## 参考和原因

参考 GawkBot `71e82a1809565281cbd0bf8185d3c125b715d934`：`internal/team/prompt_builder.go:ruleZeroBlock` 将工作／问答判断置于最高优先级，`issueJudgmentBlock` 先判断请求是否属于已有任务；`internal/team/task_ledger.go:recordTaskLedgerEntry` 从实际消息和 action log 形成事实包，不信模型自述。采用其有序语义合同和 Host 可观测事实模式，不照搬其任何工作都建 Issue 的产品约束。

当前同主题继续规则压过了用户明确要求新事项，因此先判断用户是否要求独立任务，再区分同任务 redo、新产出 builds_on、解释问答。Host 不用中文关键词判断语义。

收集汇总仅有答案而缺少实际邀请、答复和提醒时间，模型把提醒授权误当成没有催过的证据。因此按本 collection / Task / workspace / agent / org，读取持久化 invitation、有效 answer、reminder 和 response_action，将有界事实冻结进输入。响应 outbox 的 delivered 才证明送达；provider_accepted 仅是提交接受，enqueued/unknown 缺少送达证明。updated_at 标为确认记录时刻，不冒称实际用户收到时刻。未来时刻、其他 scope 和其他邀请不能混入。字段缺失明确 unknown，不从 absence 推断没催过。

## 验收

- 合同覆盖明确独立任务、同任务 redo 且继承 Python、独立产出 builds_on、纯解释；模型语义是否正确属于后续真实模型复验，本地合同检查不冒称 E2E。
- DB 验证提醒 delivered / accepted / held / suppressed / unknown、时间边界、跨 scope 隔离；未确定时禁止断言未催。
- collection.ready JSON 冻结包括答案源时刻和邀请送达确认时刻；既有配额和发送前 suppression 不改。
- 使用独立本地 DB，检查测试数量非零；记录实际命令、结果和限制。

## 2026-10-04 本地结果

执行：`DATABASE_URL=<本地 employee_task_facts_fix_1004> go run ./cmd/migrate up` 成功；随后 `go test ./internal/handler -run 'TestEmployeeCollectionFacts|TestEmployeeIndependentTask|TestEmployeeCurrentTaskContinuesSucceededWithoutIssue|TestEmployeeDispatchBuildsOnInjectsUpstreamResult|TestEmployeeCurrentTasksFrozenGuidanceReplaysStoredBytes|TestCollectionEndToEndOriginInvitationsAnswersOneSummary' -count=1 -v`，8 个顶层测试通过，含 8 个 delivery 子案例，非空执行。现有同 Task 续接、builds_on 产出、崩溃输入重放均通过；独立新 Task 的脚本模型调用未改变旧 Task/version/Run，并保留新 Python 执行指令。真实 collection.ready 模型输入携带 process_facts 和时刻字段，汇总完成/回放行为仍通过。

证据：`/Users/yuanzhan/d1/employee-e2e-evidence/EMPLOYEE-FOUR-FIXES-20261004/task-facts/handler-tests.log`、`migrate.log`。原测试 DB 为本 worker 独立创建；结束后销毁。没有触碰业务 DB、预发、IM、模型配置或其他 worker。

限制：本地 model 为脚本替身，证明合同呈现、Task/Run plumbing、持久化投影和保存的 JSON 输入；不证明真实模型会遵循新语义、不会编造提醒说明，也没有实际 Python 执行/IM 投递。提醒确认字段是 Host 记录的确认时间；不知道准确到达时间时不推断它。旧 job 保存的输入沿用原合同，未追溯改写；新输入才带新事实。部署和真实语义复验由根 Plan 统一记录。

最终复核：相同 8 个顶层测试、8 个状态子案例通过（2.768s）；`git diff --check` 通过。`dropdb employee_task_facts_fix_1004` 成功，`pg_database` 回读同名库计数 0；无本地 DB 遗留。
