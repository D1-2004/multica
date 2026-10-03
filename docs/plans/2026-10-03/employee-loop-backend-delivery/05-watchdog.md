# C 包：真实进展、停滞 episode 与主动跟进

> **验收与实现方针：**先读 [Step 0](00-step-0-environment.md) 与 [交付标准](10-delivery-standard.md)。本包的内部API/表结构/阈值/步骤是参考路线，可由主代理协调优化；业务结果、权限、幂等、恢复、真实证据及已合入公共合同必须保持。

> **执行方式：**C1纯活动规则与PG episode可并行；C2发送接线由I；C3授权等待提醒在A/P稳定后做。首批不改执行器或stop。

**目标：**Employee知道当前工作仍在执行、在等输入、在等时点或在确认停止；无可观测进展时只提醒一次，恢复后正确清除。

## 1. 所有权与Gawk参考

拟新增`server/internal/service/employee_task_watchdog.go`、`employee_task_activity.go`及测试。PG episode/notice schema由C设计、I分配migration。只读`employeetask`、`handler/task_run_events.go`、task_message、已有outbox；I负责现有worker周期调用与发送。

Gawk`broker_task_stall.go::lastObservableTraceTime/markSilentRunningTasksLocked`排除system message/action，但它把Task.UpdatedAt也算activity。本仓Task版本/账本会被扫描、通知和控制更新，不能直接复制“updated_at新就有进展”。

## 2. C1：活动与episode合同

活动分类固定：真实执行输出/工具结果/可验证artifact/有效用户补充为progress；daemon heartbeat、lease续租、routine扫描、trace导出、watchdog自身、空poll和系统通知不算progress。保留各源ID、Host时间及watermark，晚到旧事件不能覆盖新水位。

episode身份=Task/Run/input boundary + 首次无进展起点；state open/cleared；通知intent unique episode/kind/recipient。worker两副本同时扫描只提交一个episode/notice。freshprogress清除当前episode，不删除历史；下一次独立停滞可新episode。

状态判定优先：cancelled/stopping、waiting_inputs、scheduled_wait、execution_running、无active执行。stop pending不宣称“卡死”；waiting同事不伪装执行进度。配置阈值按现有受控scope配置，测试使用fixture clock，不修改共享线上阈值。

- [ ] `TestWatchdogIgnoresHeartbeatBookkeepingAndOwnNotice`：只有系统活动，threshold后必须仍触发episode。
- [ ] `TestWatchdogRealProgressClearsEpisodeOlderEventsDoNot`。
- [ ] `TestWatchdogConcurrentScanRestartCreatesOneNotice`、`TestWatchdogWaitingAndStoppingAreNotExecutionStalls`。
- [ ] 拿真实PG观察失败后实现原子CAS/唯一键，`go test -race ./internal/service -run '^TestEmployeeWatchdog|^TestWatchdog' -count=1`。

## 3. C2：一次确定性提醒

用已核实状态生成简短文字，例如“这项工作还在运行，最近没有新的可观测输出。”不报告百分比/ETA/确定卡死；无需模型。原Task authority与当前发送权限决定收件场域，不能猜DM收件人。

写notice intent后提交，再用现有outbox发送；BeforeSend重验状态/episode仍open。progress早于发送到达则抑制旧提醒。unknown provider提交结果只查询原action，不能重发。用户静音/仅文件交付合同决定是否允许主动提示；该开关独立且可观察，不偷偷覆盖已接受承诺。

- [ ] 写进展与send竞争、Task取消/租户解绑、provider unknown/重试、提交后崩溃恢复、两副本一次effect。
- [ ] 断言提醒无新Task/Run/foreground generation。告知事实不等于启动新工作。

## 4. C3：等待对象提醒与主动推进

只有A的原邀请授权明确允许提醒、目标有效、提醒频率/最大次数冻结时，才可向尚未回答者发提醒。默认一次；取消/撤权/过期/已答停止。集合其他答案不进入提醒正文。Loop的语义下一步需P typed wake且原Task plan授权，不在watchdog库偷偷调用模型。

- [ ] 写无提醒授权零external effect、已答/已停/过期零新提醒、同一人两邀请不串、不同episode频率和次数上界。
- [ ] 与A一起确认只有邀请目标可收到有限内容，汇总仍归原场域。

## 5. 真实验收

WD-01：授权隔离测试任务长等待无输出，缩短该测试scope阈值，到时收到一次诚实提示。WD-02：仅heartbeat不能清除，真实tool/output之后episode清除。WD-03：两消费者/受控测试进程重启无重复提醒，无新Run。WD-04：等待输入显示2/3；只有获准邀请收到一次提醒，回复后不再催。

记录last activity evidence/time、episode、notice/action、实际消息及Task/Run数量。不得重启共享预发、关闭共享Redis或靠sleep+“没有看到”替代PG和渠道证据。
