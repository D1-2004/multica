# 四项修复预发部署与简单E2E报告

2026-10-04，Asia/Shanghai。本轮部署及短测试已结束；没有追加代码修复、Runtime切换或第二轮发布。

**结论：部署成功，不能签全功能通过。** 安静恢复、短收集汇总、TaskWake遗忘过滤取得真实证据；明确新任务仍失败，记忆查询/删除确认仍有不一致。已有本地测试通过不等于这些真实行为已修好。

## 部署已生效

| 项 | 精确事实 |
| --- | --- |
| 来源 | employee/backend-delivery，11d6eb5061708686dca573bffbddee3accb658a2，包含四项修复及Plan |
| Release | 74802f18290a36adce1e348870cc31b8d6a7fc5f，已验证包含上述source |
| Aone | app342160 / pipeline66 / CR36362030 / run3110373861 |
| 部署 | task3459329284 SUCCESS，10:00:29完成；构建+扫描、预发集成测试SUCCESS；人工预发验证仍待处理，未把短测当完整验收 |
| 两副本 | pod149134 09:58:52.009、pod56137 10:00:02.429本次server starting（SLS逐pod及tail）；两live均epoch18/webhook1/memory1–3，fence normal |
| 实际对象 | workspace5f8b5b73 / agent33af235e / tenant44675729；Runtime461aabb2仍原配置，未Tag Apply、未改routine |

[变更单](https://cd.aone.alibaba-inc.com/unite/micro/cr/app/342160/36362030)，[预发流水线](https://cd.aone.alibaba-inc.com/unite/micro/publish/app/342160?flowId=1005452)。证据根：`/Users/yuanzhan/d1/employee-e2e-evidence/EMPLOYEE-FOUR-FIXES-PRE-20261004/`。

## 四项真实短测试

| 测试 | 结论 | 实际观察 |
| --- | --- | --- |
| 原蓝杉“新开任务” | **FAIL** | 原Director DM、原台词、旧任务保留。10:05:21明确新开且要求实际Python sleep12/平方；10:05:24直接复述旧结果和55，没有新Task/Run/工具执行。旧e0/f1/ff4版本未变。trace699e4889。不能把旧结果正确当新工作完成 |
| TaskWake遗忘过滤 | **限定PASS；记忆交互FAIL另列** | 新私人记录5f3a5176在群capture；群forget第三个实际调用已提交forgotten（journal_committed=true），后续同owner lookup为空。10:19:27真实collection.ready TaskWake有完整28355-byte GEN输入、input_truncated=false、本人person_view=true和20个正常History turns；新饮品、旧温柠檬水和旧派生答复msgeAJJ均不再进入输入。trace71d89856，taskwake-privacy-audit.json。旧冻结snapshot未治理 |
| 两人收集与过程事实 | **短路径PASS，实际催促未跑** | 两份邀请实际送达；引用答复7/9分别于10:19:11、10:19:21实际发生，10:19:28汇总16并写正确答复时间。完整snapshot process_facts包含delivered、source_occurred_at、reminder_policy=not_authorized、reminders=[]；未假称等半小时或催过。Task6326a008 succeeded、mandatory waits=0。此例按请求不设提醒，不签实际催促后的叙事已过 |
| 持续安静→恢复 | **PASS（另一个真实账号，不是DEAP actor）** | 10:15:52 native set_scene_participation quiet/rev1；SixSix 10:19:14引用并@，直到10:21:48恢复前没有员工回复。Host SLS明确kind=quiet、0 model attempts、0 action，LF无GEN；本人10:21:51 native active/rev2，10:22:57正常答16。quiet-result.json，bc9c63dd/e4d6645f/bf439333 |

短测业务窗口10:05–10:23，10:25停止新增业务输入，之后仅整理只读证据。DWS使用私有prod配置，逐次显式profile；后台为预发。原失败没有清历史或换口径。成功/失败都保留真实IM、API、LF和Host日志。

## 暴露的问题及后续动作（本轮不滚动扩大）

- **P1 新任务仍未执行**：真实新请求被当旧报告解释，只有一轮直接答复，0 dispatch。位置employee_current_tasks.go:employeeTaskExecutionInheritancePolicy、employee_scene_entry_worker.go:buildInput、employeeloop/prompt_builder.go及最终可信system/work boundary。后续查优先级是否真正进入模型的可信规则，以及旧候选/记忆与当前新请求的取舍；保持原反例，不能删旧Task或用关键词硬派发。NEW-TASK请求GEN观测被截断，不能据残缺输入断言完整system规则因果。
- **记忆查询不一致**：record已存在且DM背景memory_manifest含m1，实际memory_lookup(scope=me)却返回空，回复称没记录。trace96aac456；检查employee_memory_tools_v2.go的private lookup范围与ForegroundBrief跨源本人视图是否一致，不能只归咎模型，也不能据空lookup说从未记过。该GEN输入观测truncated=true，但可见m1/饮品和完整工具返回。
- **删除已执行却报失败**：第一次误用文字作为record_ref被拒，随后lookup→精确forget在第三次调用成功，因预算耗尽返回“没能完成受理”；43145832。后续检查已提交效果的收束与真实完成说明，避免重复写或清空所有记忆。第二个DM清理请求只说没记录，不能当删除证明；本轮以精确forget提交和之后空lookup作清理证明。
- **发布窗口hourly失败**：10:00例行任务在Runtime准备阶段失败，任务a5c3e376、错误FCE2B-TASK-ENVIRONMENT-PREPARING-FAILED；该任务发生在发布启动窗口。另列环境/Runtime问题，未切Runtime、构建镜像或重跑routine，不能把它混入本轮三个已证短路径，也不能签后台执行环境全绿。
- 已声明旧snapshot、Router同步callback提交前及旧多receipt哈希notice边界仍保留；本轮没有扩充这些模块或验证全R5。

## 证据与清理

- manifest.json固定源码、release、run和环境；deploy-task.json权威SUCCESS，pipeline-watch.log逐阶段结果。run-status.json是部署中的早期快照，不是最终摘要。
- startups-sls.json含两个pod精确本次启动；business-sls.json含25条本波摄入/完成事件；negative job有明确model_attempts=0。SLS首次45秒超时保留，后一次成功；不是拿查询超时当零事件。
- new-task-result.json、new-task-front.json：0新Task/旧版本不变和实际直接回复。Task API比较没有把10:00 routine新Task算进蓝杉分母。
- collection-task-detail.json、collection-wake-front.json：绑定的Task/collection、终态与完整过程事实/过滤后的History。
- quiet-final-read.json最新页最早时间早于负窗口，覆盖10:19:14–10:21:48；没有把分页正常的complete=false当失败或漏证。quiet-negative-front.json无GEN，quiet-resume-front.json native active/rev2，后续IM答16。
- **执行者错误保留**：首次DM quote带群@参数，被CLI validation拒绝、0投递；*-rejected.json保留。去掉不支持参数后沿同一UUID重发成功，没有假装第一次已发或制造重复答复。
- cleanup-result.json：本轮测试记忆已精确forgotten且后续查询为空；安静恢复active/rev2；新collection Task已succeeded，0 open waits。hourly仍enabled=true，下次11:00，Runtime461不变，无Tag Apply、无DEAP租约。已完成Task及历史消息保留审计，没有删除其他session对象。

本轮不再发送新测试消息或自动修复/发布。下一步最短路径是修新任务真实取舍、统一本人记忆lookup范围及已提交forget的收束，然后仅复验对应原反例。
