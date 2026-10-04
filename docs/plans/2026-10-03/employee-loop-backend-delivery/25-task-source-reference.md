# BASE-TASK 同目标错接的来源关联修复

2026-10-04，基线f322868405，独立employee/codex-task-reference。共享文件仅employee_current_tasks.go与新增候选assembler/helper及相应测试；不改dispatch、persona、retry或privacy实现。

## Why：真实错接

Director原DM，03:54蓝杉请求创建ff4ab139/Run023e3ae8。03:55续接却read_task t5并continue旧f1fb4e67/Run11468b00，旧任务创建2026-10-03 18:53，旧报告10:54:03。初始ff4始终1Run，合计55正确不能证明同Task。精确LF f2dc28816ba343a2a98b2252eb4f3957中t1/t4/t5仅goal/state，goal三者近乎相同；dispatch压掉goal里的蓝杉，但Host原请求与执行prompt保留了这个名字。

当前候选缺原来源/用户命名/时间/执行事实。修复让模型根据可信关联判意图，不能硬编码蓝杉、默认最新、Host猜目标或删除旧候选逃避。

## 合同

- 新冻结TaskBrief每候选增加创建/更新时间、has_active_run、latest_run真实state及created/finished时间（不附report，仍按需read_task）。不从成功状态推断真实进程退出。
- 附至多两条Host关联的人类来源（初始请求及最近input）；用Task entry的Host source与requester匹配，并且只引用同scope/principal的现有、已过滤RecentConversation人话。不另取原始历史绕过reset/forgotten过滤；缺引用可用timestamps与按需read，不猜别名、不扩大可见性。
- 原请求中的名字、明确source ID及当前对话关联比相同概括goal更能分辨Task。时间只作事实线索，不是默认选最新；信息仍无法区分时澄清。进度与续接仍须成功read_task，实际效果仍source-bound/read_ref/当前权限/版本CAS。
- schema是新快照的additive数据；旧冻结snapshot/model journal保持原字节不重建；无新工具/表/producer，不升级loop16。

参考固定GawkBot71e82a18的task_addressing/task_ledger：依据broker可观测来源关联事项、执行事实与消息身份分开，不以相似目标当身份。复用本仓既有RecentConversation隐私边界，禁止建立第二条历史通道。

## 验证

真实隔离PG复现两个相同goal、不同原请求/时间；当前候选保留原请求主题与关联ref而旧相似目标不能冒充。原source缺失、伪造requester/外scope、被历史过滤的原话不回填；old frozen输入仍相同。实际语义选择质量须集中部署后原DirectorDM再跑Task初始ID→续接Run父ID比较，不能仅看55或任意Task有2Run。

## 本地结果

- fresh隔离库 `multica_codex_task_reference_101` full migrate up成功。
- 原candidate只有goal/state的基线 `task-reference-red` 1 FAIL；补丁 `task-reference-green` 22 PASS/0 SKIP，覆盖当前读取/权限/续接CAS、感谢无效果、旧冻结guidance字节保持。
- `task-reference-source-privacy` 2 PASS/0 SKIP：真实PG同goal原请求关联及旧Taskupdated更近仍不替代命名；插入授权forgotten tombstone后，Task ledger仍保留原body，但新brief不复活已过滤原话。独立helper反例也拒绝未知/foreign speaker、伪receipt，输出只用已过滤text而不是raw body。
- `task-reference-race` 6 PASS/0 SKIP；handler build/vet退出0。日志在 `_shared/logs/codex-task-reference/`。

没有选择目标的Host启发式，没有默认最新，没有额外report或原话历史读取。sourceHints只提供真实关联线索，原绑定/权限/read_ref/CAS不变；仍需部署后原DirectorDM再次比较新初始TaskID与续接Run父TaskID。

原BASE-TASK e442失败证据在 `CODEX-CLOSEOUT-20261004-TASK-REMINDER/`，保留初始ff4与错接f1两个API/Run、完整前台LF、IM。SLS查询尚需补齐，不能把其失败输出当零事件；最终验收由主代理当版复验收口。
