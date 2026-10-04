# 后台进展预发交付报告

2026-10-04 13:30 收口。发布完成；首轮接单反馈、后台实际执行、工具上报幂等与最终文件送达通过。**中途阶段消息展示未通过，不能整体签收后台进展用户效果。** 本轮结束，不新增部署、镜像或长等待。

## 版本与发布

用户明确授权“不需要我确认，直接发布到预发”。Aone app342160 / pipeline66 / CR36362274 / Run3110378343，13:16:09部署成功。冻结功能源e5ef5adbea6f628efba623cf8c73d38f37d71381（原6e4af951755c3767c1accdc46e4601f9a6933ed5）；本地组合f84f629789564108e9a29281d68bd0bc0f12cf35；平台最终构建f9a6d687a346c5175f65a88a43a1e35f7da5c6ca。

共享release仅快进一次，恢复精确冲突合并任务3459344460，没有重复提交发布。已保留原steer与平台并入的eval实现78fffd467179e5ed577317d8e834ad731643abaf。两台实例均employee-loop:20、eval-report:1且normal；实际启动SLS分别13:14:27.448、13:15:38.815。人工预发验收门尚未关闭。

本地集成16项顶层检查、reader19/20组合门2项通过；源码独立审查无新增P1/P2。这些属于本地验证，不替代真实效果。

## 真实短例 PRG-LIVE-01

Director原单聊真实发消息，不伪造task token或数据库候选。workspace5f8b5b73-f912-4879-9a29-b763d103fedf，agent33af235e-e03b-4be2-be3b-bbae8b97fce5，原Runtime461aabb2-2da0-472c-9565-132042c36e25未切换。

| 检查 | 结论与证据 |
| --- | --- |
| 首轮反馈 | 13:20:19人类消息，13:20:25接单ACK实际送达，约6秒；这证明本例快速反馈，不证明所有多轮请求首轮即回复 |
| 新工作归属 | 新Task a9af04a2-32bf-42ab-be23-38e9fca8cd11，唯一Run31f720c9-9609-4fc1-9940-42e871d161ec，queue c9c41c6e-08ac-4b0a-88fb-820db11d3292；来源绑定原Director请求 |
| 实际工具 | Langfuse沙箱TOOL可读：Python第一批输出91；真实time.sleep(60)输出60.0秒；第二批及全量输出总和650 |
| MCP挂载/幂等 | 沙箱原生mcp_employee_progress_report_progress于13:20:48.723、13:20:50.212实际调用同ID/正文，返回同report_ref8048cc8e-50f5-4264-8265-c2a911b93c9a，replayed=false/true；仅一次progress wake |
| 进展状态 | wake f7fb76f2f94e4a31a8a0d0c7c1df3f7b含running状态、阶段候选、原请求、recent_delivered_progress=[]；工具未改Task目标账本，终态仍同一Run |
| 阶段IM | **未通过展示目标**。Loop可用reply/stay_quiet，实际选择stay_quiet，quiet_committed；没有阶段消息，不把accepted/woken说成已送达 |
| 最终交付 | 13:22:14真实收到progress-check.txt，fileId ndMj49yWj22GGnNLfbAxxMdRW3pmz5aA；13:22:22最终文本送达；Task13:22:20 succeeded、0 open waits、Agent idle |

## 分面证据与开放项

证据根：`/Users/yuanzhan/d1/employee-e2e-evidence/EMPLOYEE-PROGRESS-INTEGRATION-20261004`。当前manifest.json及prg-task-current/runs/entries/final-im/sandbox/wake.json固定本批结果；startups-sls.json与pipeline-watch.log固定发布。

- 产品：首轮反馈、真实Python、同ID幂等、最终文件成立；阶段展示缺口保留。进展模型没有输出理由，不能声称已经知道它为什么quiet。
- P2阶段展示：代码server/internal/handler/employee_progress.go的employeeProgressExtension.extendInput（PROGRESS DISPLAY约244行）交给Loop在reply/quiet之间判断。候选是新阶段且此前未展示，但本例仍quiet。后续先评估“不要直接重复发钉钉”是否被误解为禁止任何中间消息，并明确原请求对一次阶段展示的约定；用原例复验一次真实阶段IM。不要直接强制所有候选发消息。
- 观测：原Runtime的LF generation输出仍gzip乱码；原生TOOL、Host generation及IM/API可读，所以能签上述限定结论。13:20–13:23精确ID SLS查询为空，backend尾日志为空，这两面不作为没有事件的证明；不追加Runtime解码修复。
- P2迁移维护：最终release包含eval/progress两组10020–10023同数字前缀、不同完整文件名。当前包按完整stem迁移成功，但工具若按数字排序/去重有维护风险。后续在兼容已应用记录的前提下修规范，不重命名已应用迁移或手动迁移预发。
- 原BASE-TASK明确“新蓝杉”被复述旧答案仍FAIL；本次新Task成功不洗掉原失败。记忆交互FAIL和旧冻结snapshot治理等历史限制继续保留。
- 终态旧上报、明确禁止中间消息等真实反例本轮未跑；本地代码审查/测试不冒称真实E2E。SPEC/EVALS功能已随平台release部署，本批未运行完整评测或提交总体绿色报告。
- 在途steer真实成功复用独立证据571e31f7任务及43d68901继任队列；不扩展为所有Runtime/冷启动通过。

## 配置恢复与清理

本批未暂停routine、未切Runtime、未改Tag或账号全局配置。回读Agent idle、原Runtime461；唯一新测试Task已succeeded且无等待，保留Task/文件作为审计。三份本批独立数据库已drop且回读0。

回读原hourly的场域routine列表为空，进一步GET autopilot88783bd8-49bd-41f3-9990-94feb83282e1确认status=archived，updated_at=12:02:30（本次发布前），trigger仍enabled。此变更不属于本批，不擅自恢复别人的归档；本批没有需要恢复的暂停配置。历史10:30“hourly enabled”只作当时事实。

当前唯一摘要：[执行表](../../employee-delivery-execution.md)。本报告与Plan记录最终事实，历史报告不升级为本版通过。
