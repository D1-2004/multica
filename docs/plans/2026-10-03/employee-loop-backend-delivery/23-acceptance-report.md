# Employee 本轮最终验收报告

收口时间：2026-10-04 08:39，Asia/Shanghai。用户要求08:43前结束；本轮已结束，不等待完整R5。当前状态唯一入口：[执行表](../../../employee-delivery-execution.md)。[效率纠正](../../../employee-delivery-efficiency.md)记录五个实际问题、轨迹及执行规则。

## 签收结论

**本轮交付与收口完成；整体功能验收不通过，保留两个P1代码/行为阻断及一项提醒证据硬门缺口。** 用户明确接受跨日、长等待、多真人复杂场景采用代码review：WD04、G15按此方式通过，真实E2E未完成。不能用review或补充机制成功覆盖原真实失败。

08:14冻结范围；停止新增真实测试、长等待、镜像构建及部署。截止前只读review、恢复配置、清理测试对象、同步文档；没有为新问题继续滚动开发。

## 版本与发布事实

- Multica功能源码 `f4c4790cbea1066857f96d4b6468c9710d77646b`；release `a4c7b77e9225290e91de8ed1d0cafde611011225`。
- Aone app342160 / pipeline66 / CR36355253 / run3110369623，07:00:25部署成功；pod149134于06:58:47.051、pod56137于06:59:57.988启动，均normal、employee-loop17/webhook1/memory1–3。验证门未关闭，不能当全功能通过。
- RealNiubility agent33af235e、tenant44675729、applied rev11。Runtime461aabb2仍用原Template4osx6sfmkew1ysmdck4n；未切Runtime、未Tag Apply，其他tenant rev10未动。
- Runtime修复独立交付：source55ac122f（实现2b463197）、CLI32feedf1、CI314792/run77295044成功，Template53zkmuykn69wy7nhidpv、private Pi Runtime14835d95。未晋级Employee运行环境。

## 已交付与已有真实证据

| 项 | 可签的事实 | 不能扩大成的结论 |
| --- | --- | --- |
| CRON04、COL歧义、REF01a | 真实自动任务、来源归属/澄清、引用取消与退出确认已有IM/API/SLS/LF证据 | 不代表所有自然语言任务选择均正确 |
| COL取消/迟答 | 实际取消、迟答不consume/不复活汇总；epoch17迟答只回复4家 | 原假记下/假汇总失败保留，话术质量不能全绿 |
| G5 builds_on | fresh stats→fresh retro，真实执行、正确数字和实际交付链成立 | 原Runtime461 LF generation乱码未恢复，观测完整性partial |
| 同scope及本人跨source遗忘 | human-message次轮availableHistory中旧值/派生答复剔除，真人独立消息保留 | TaskWake有P1遗漏，privacy整体FAIL |
| BASE实际Python | 补充机制真实Run/Python stdout55成立 | 最初显式新Goal仍continue旧Task，原用例FAIL |
| 两人提醒 | 两个真实独立提醒，10/14→24与终态成立 | “半小时内/没有催”的叙事失实；ledger/SLS缺面，完整提醒硬门incomplete |
| MF/L1/M8/DS09 | flush、召回命中及既有摄入/限定来源回答成立 | MF原答案仍在History，纯长期Memory独立因果未证 |
| Runtime候选 | 真实cold/warm Task、实际Python、11个generation完整JSON、0乱码、完整性门通过 | generic Pi未声明employee-direct-v1，不能证明Employee direct等价兼容 |

完整证据目录逐项在唯一执行表。复用未受影响的已有证据，不重新运行已通过检查。Harness既有80离线通过；不能计作80业务E2E。旧取消日志出现“no tests to run”，已排除；有效取消DB验证另有非空测试日志。既有7个legacy handler失败与release基线相同，不宣称整仓测试全绿。

## 本轮代码review验收

| 路径 | 具体检查与位置 | 结论 |
| --- | --- | --- |
| Source/权限/状态 | employee_task_wake.go:employeeSceneOriginParts；employee_stop_task.go:stopTask；employeetask/stop.go:Store.Stop | 可信来源、精确scope/requester、use-time权限及CAS路径通过；不抵消模型选择失败 |
| 取消/迟答 | service/direct_task_stop.go:StopDirectTaskTx；employee_task_collection_stop.go:closeTaskCollectionsTx；taskinput/collection.go:CloseCollectionTx；employee_collection.go:employeeClosedQuestions | 事务锁、撤销邀请、revision、supersedePending和迟答无consume通过；provider已接受发送的撤回不在保证内 |
| WD04 / 提醒 | service/employee_task_reminder.go:remindOne、beforeReminderSend、stopReason | 每invitation独立授权/持久化ordinal、锁后重读、answered/cancel抑制通过。WD04 code_review_accepted，真实第三答复人场景未跑 |
| G15 / routine | service/employee_routine_task.go:verifyRoutineOccurrence、verifyRoutineAuthority、acceptRoutineOccurrenceTx；el2e/driver_v2.py segment checkpoint | trigger+plannedAt去重、冻结revision/timezone、恢复、时间门通过。G15 code_review_accepted，≥25h真实召回质量未跑 |
| R1恢复 | 当前f4c/17双binary与R1-M17证据：16→17暂缓、冻结v2 hash/record不变、恢复0追加模型、TaskWake replay幂等 | 本地协议通过，混版暂缓为已知限制；fake LLM不当真实IM，旧v1证明另列 |
| Harness / Runtime | driver envgate/typedcursor/fail-closed；Runtime codec stream/secret/generation隔离 | 通过限定review；诊断字段及能力兼容限制另列 |

Review原文：[任务与质量](../../../../../employee-e2e-evidence/CODEX-CLOSEOUT-20261004-ROOT/CLOSEOUT-REVIEWS/task-and-policy.md)、[Collection/routine](../../../../../employee-e2e-evidence/CODEX-CLOSEOUT-20261004-ROOT/CLOSEOUT-REVIEWS/collection-schedule-harness.md)、[隐私/恢复](../../../../../employee-e2e-evidence/CLOSEOUT-REVIEWS/privacy-and-recovery.md)、[Runtime](../../../../../employee-e2e-evidence/CODEX-CLOSEOUT-20261004-ROOT/CLOSEOUT-REVIEWS/runtime-observability.md)。review是限定路径验收，不是整仓无缺陷证明。

## 开放问题：冻结登记，后续批次处理

| 严重度 | 问题/影响 | 代码位置 / 后续动作 |
| --- | --- | --- |
| P1 阻断 | 显式要求开新Goal仍read/continue旧Task；用户工作归属错误 | employee_current_tasks.go:employeeTaskExecutionInheritancePolicy（40）、prepareContinuation（231）；employee_scene_entry_host.go（58/68）的工具合同与Persona规则；task-and-policy review列原trace。先补明确新工作优先级与旧Task隔离，再实际复验；本轮不修不发 |
| P1 阻断 | TaskWake未带MemoryPrincipal，已forgotten私人饮品值再次进入模型History；未观察到再次外发该值 | employee_task_wake.go:buildTaskWakeInput约635；复用可信DM/nonautomation requester资格，保持owner隔离。旧InputSnapshot治理单列，禁止直接改冻结journal |
| P1 验收缺证 | 两人提醒ledger/SLS关联缺面；消息事实不能证明全部授权、额度、抑制 | employee_task_reminder.go / 当波action-outbox-reminder。后续只读精确补账；本轮incomplete，长等逻辑review已接受 |
| P2 | G08“安静”承诺后仍答机器人；没有持续参与状态 | Persona quiet/scene decision、task-and-policy原trace；后续明确持续状态合同 |
| P2 | 今日说昨晚、从未记过、未催等无证过去叙事；C03 SLA旧真实失败未再验 | Persona/task progress与memory回答规则；后续以真实事实约束评测，不用数字正确掩盖 |
| P2 | Harness未完整透传page_ledger/stop_reason，诊断不足 | el2e/driver_v2.py:snapshot；后续透传，covered仍fail-closed |
| P2/P3 | LF解码诊断字段未全部映射；原乱码不可恢复；candidate缺Employee direct能力声明 | Runtime observability review；候选兼容能力门另批，不切461 |
| 限制 | MF pure-memory、G15真实跨日、WD04第四真人、hourly下一次触发未证 | 本轮review/限制签收，后续有条件再真实验，不等待本轮 |

## 恢复与清理回读

- hourly `e02d1d7b-adb8-4a7f-bdb0-0e940e8837ba`：PATCH恢复enabled=true，08:38 GET确认；原instructions、`0 * * * *`、Asia/Shanghai未改。`next_run_at`仍返回04:00旧游标；没有等待下一次真实触发，恢复配置成功不等于调度重新触发已验。
- 临时routine bae62ef5：既有DELETE204、回读不存在。
- Canary Issue059079ca：当前bare及slash GET均404；DELETE尝试返回405/404，**不声称本次删除接口成功**。三Task删除前均completed，无运行中任务。
- 私有Canary Agent7e2c82e7：archive200，GET确认archived_at=08:38:05，active Agent list无该ID。审计记录保留。
- Candidate Runtime14835d95与Template53zkm作为未晋级交付产物保留；不删除共享/被审计引用制品。已completed sandbox不另做危险清理，未核验远端TTL后实际销毁。
- 隔离本地R1 DB/Redis已清理；新测试记忆精确forgotten，actorleases已释放，历史消息保留。
- 本轮仅改实际交付worktree文档；主checkout及其他session WIP未触碰。无新构建、部署或真实业务测试。

恢复/回读原始文件在 `CODEX-CLOSEOUT-20261004-ROOT/closeout-hourly-*-readback.json`、`cleanup-canary-*.json`。本报告、唯一执行表和效率文档已同步；共享resume只保留指针，旧记录作为沿革。
