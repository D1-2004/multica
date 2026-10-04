# B 包：场域 Cron → 独立 Task → 执行与通知

> **验收与实现方针：**先读 [Step 0](/evals?tab=runtime&doc=docs%2Fplans%2F2026-10-03%2Femployee-loop-backend-delivery%2F00-step-0-environment.md) 与 [交付标准](/evals?tab=runtime&doc=docs%2Fplans%2F2026-10-03%2Femployee-loop-backend-delivery%2F10-delivery-standard.md)。本包的内部API/表结构/阈值/步骤是参考路线，可由主代理协调优化；业务结果、权限、幂等、恢复、真实证据及已合入公共合同必须保持。

> **执行方式：**B0来源测试可立即开工；B1 reader 由P/I接线并先发布；B2 run_only producer 后开启；B3 decision 依赖typed wake。每批独立提交和验收。

**目标：**持久例行任务实际到点触发一次，暂停/恢复可证明，运行指令冻结，Task与Issue解耦。最后支持明确配置的条件判断和同一事项后续工作。

## 1. 所有权与入口

B拥有拟新增`server/internal/service/routine_execution_origin.go`、`employee_routine_task.go`及相邻测试。`autopilot.go`/`scheduler/jobs_autopilot.go`仅B在I确认的窄接缝修改；若F也需修改autopilot.go，F提交独立adapter，I串行合入公共位置。

必读：`docs/plans/2026-10-03/employee-cron-task-service.md`、P3、`service/scene_routine.go`、`autopilot.go`、`scheduler/jobs_autopilot.go`，Gawk `broker_scheduler_routines.go`、`scheduler_runtime.go`、`scheduler_runs.go`。旧reader备份位置见上下文；不整包恢复未完成WIP。

## 2. B0/B1：可信发生事实和reader

- 使用现有DB时间、时区、latest-only、执行租约和canonical planned_at。
- 身份冻结：真实trigger/routine/Agent/workspace/org/scene、creator kind/ID、配置revision、planned_at、Instructions/Title、当前dispatch mode、授权引用。认证密钥不进snapshot。
- Source `scene.routine.schedule`，event ID由trigger + canonical planned_at；第一次occurred_at固定。RequesterRef `routine:<id>`，originator human为空。
- Creator为member按当前membership/private/public_to调用权限；creator为Agent必须查真实同workspace Agent和现有invoke规则，不填member UUID，不借管理员。
- 旧自主Autopilot、Issue派发保持原逻辑。仅已验证scene routine走Employee adapter，场域解绑不能自动变成无场域执行。
- 新来源reader覆盖claim、direct prompt、APRun关联、terminal、ExecutionEvent和learning排除；routine own notice是唯一sender owner，message Run notice不得并发再发一份。

- [ ] 先写schedule来源伪造/tenant rebound/权限撤销/member vs Agent creator矩阵RED。
- [ ] 写真实`DirectTaskPrompt + autopilot_run_id`完整claim测试，确认实际模型输入用冻结prompt而不是当前AP配置。
- [ ] 写原消息Direct和ordinary Autopilot对照，错误origin不能从parser“宽容”跌落旧Issue模式。
- [ ] P/I发布reader并核全部副本capability；B未到此门槛不生成新queue context。

## 3. B2：run_only原子producer

用确定性Compiler组装已注册工作，不加分类LLM；“如果”二字不自动改成decision。场域绑定`run_only + schedule`一次发生保存一个独立Task；复发通常新Task，只有配置明确绑定某持续Task时才以其原目标授权推进，不复用上次Task猜同一件事。

一次事务内容：真实AutopilotRun + frozen source receipt + EmployeeTask/Run + queue + APRun.task_id映射 + 已有启动/通知意图。需要新的source hook时storage-only；不在事务内读外部connector或发IM。提交后Notify；commit-before-notify通过PG扫描和source重放恢复同一执行身份。

- `(trigger_id, planned_at)`唯一；两个scheduler同发生只接受一次。原source重投读取已冻结输入，不重新计算planned_at/Instruction/模式。
- 真实queue.autopilot_run_id保留。不得给即时人类Task造占位APRun，也不得新起第二timer。
- 旧RecoverPartialAutopilotRun清planned_at/另建Run的分支不得作用新冻结来源；恢复断点覆盖Run/queue已提交但APRun未结算/notice缺失。
- 暂停阻止未来occurrence，不默认杀掉已接受工作；新授权失效阻止新执行/外发，不换principal。
- 失败、取消、启动失败照真实状态结算；start/end each≤1，unknown provider action只查询，不重发。

- [ ] 写双scheduler、事务每个写入边界失败全rollback、commit前无notify、commit后崩溃恢复测试。
- [ ] 写执行中改指令/模式：old occurrence重投字节及IDs不变，next occurrence新配置生效。
- [ ] 写暂停、恢复、时区边界、due fire、offline/launch fail、APRun orphan与通知恢复。
- [ ] 真PG：`go test -race ./internal/service ./internal/scheduler -run 'TestEmployeeRoutine|TestSceneRoutine|TestAutopilot' -count=1 -p=1`。新测试前缀登记为TestEmployeeRoutine，当前尚不存在。

## 4. B3：routine decision与连续任务

新增显式执行选择`run_only|employee_decide`（保留旧AP模式兼容，不将新字符串直接写入旧不认识的字段）。默认/既有run_only仍直接Compiler派发。employee_decide的新occurrence建P的typed wake，Host提供配置规则、当前获准Task、相关事件、发送策略；Loop可reply/quiet/dispatch/wait，不伪造人类问句。

计划只允许该routine配置授权的行动；模型不得扩大外联名单或共享范围。每occurrence≤3真实请求，总请求失败计数持久化；后台执行usage另记。若决定无事可做，持久quiet和APRun决策状态，不假装完成过一个Run。

条件后续任务由P的plan/step预算控制；普通timer不自动续上所有旧Task，停用routine不再产生新wake。APRun occurrence与Task continuation分别有ID，不能用同字段代表两者。

- [ ] 写run_only零foreground generation，employee_decide≤3，失败/重启不重置，Quiet零queue。
- [ ] 写decision输出只有获准效果，冻结source conflict、取消Task和旧plan不续行。
- [ ] 接线后分别跑确定性与decision真实到点，不拿手动立即运行替代schedule验收。

## 5. 真实验收与退出门槛

CRON-01：测试场域每分钟一个唯一标记，Asia/Shanghai；至少观察一次真实scheduled occurrence及输出。CRON-02：同发生技术重投不重复；受理后改指令，old不变next新。CRON-03：暂停并越过合法下一时点，零新执行；恢复后下一时点执行。CRON-04：配置decision，在一个条件不满足和一个满足的真实发生中核quiet/dispatch与调用数。

记录trigger/planned_at/APRun/receipt/Task/Run/queue/start-end action/provider message/trace。清理仅停用本次测试routine，保留审计。不重启共享预发或修改全局Daemon deadline造失败；Q用授权隔离实例验证真实failed/通知丢失。

完成B需run_only与decision都部署验收；只实现reader或跑手动run不能写“Cron已完成”。
