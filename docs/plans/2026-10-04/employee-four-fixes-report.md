# 四项修复交付报告

2026-10-04 09:28，Asia/Shanghai。本轮冻结为新任务归属、遗忘遗漏、提醒事实、持续安静。实现提交：46eeef7169（安静）、50485aed3c（遗忘）、865b94d1dd（新Task/提醒）、788088f2d4（控制批次隔离）。主交付分支employee/backend-delivery；未推送、未部署、未发真实IM，不更改Runtime、Tag、routine。上轮预发仍f4c4790/epoch17，本轮本地候选epoch18。

## 签收

代码修复及限定本地机制验证通过，真实模型语义/IM验收未运行，不能把上一轮真实失败自动改为E2E成功。后台任务执行、SLA、MF独立记忆等不在本轮范围。

| 项 | 修复结果 | 对应验证 / 限制 |
| --- | --- | --- |
| 新任务归属 | 当前明确独立新工作优先dispatch新Task，同主题不授权合并；真正redo继续旧Task并保留Python方法；依赖旧成果的独立工作仍builds_on | 新Task/Run正确绑定、原Task保留；续接和builds_on回归通过。语义规则通过合同检查，scripted model不证明真实模型选择或实际Python执行 |
| 遗忘遗漏 | TaskWake history与brief共用可信DM本人资格，非automation、同Task owner、合法org-qualified身份；把MemoryPrincipal送入既有撤回过滤 | 真TaskWake builder历史过滤通过；移除参数的对照明确失败，资格11子例和既有跨源/owner隔离通过。旧冻结input不改写，历史已保存私人内容未处置，不能签所有旧job已安全 |
| 提醒事实 | collection.ready输入冻结本collection邀请、答复源时间、提醒持久化行和outbox状态；provider accepted不当delivered；时间缺失/晚于快照为unknown；空列表不证明全没催 | 8状态子例、scope及冻结界限、完整收集输入通过。真实汇总是否遵守事实仍待模型/IM复验，不补跑长等待 |
| 持续安静 | 参考Gawk频道明确禁用：持久scene级quiet、仅发起者当前外层请求恢复；异账号wake零模型Quiet；旧工具拒绝；native排队回复发送前抑制；控制独占batch，journal重放不增revision | 暂停→异账号→恢复、queued action、旧普通notice、scope、历史quote、proactive及混版门通过。后台通知不静音。Router同步callback只有入账检查，尚无提交前门；旧多receipt哈希notice无新job字段也保留边界；已提交远端请求不承诺撤回 |

## 为什么这样改（GawkBot参考）

固定本地来源 `/Users/yuanzhan/github/gawkbot@71e82a1809565281cbd0bf8185d3c125b715d934`。

- 新Task：prompt_builder.go有序工作判断与原请求工作包；本仓显式新事项优先保留人选择，不用Host关键词派发。
- 遗忘：context_assembler.go按可信namespace选择上下文；本仓brief/history共享本人资格，不能各自漂移或全局扫描私有内容。
- 提醒：task_ledger.go只记录实际消息/action，task_distill.go只提炼已核实结果；本仓提供持久过程事实，不让模型从看不见推断没发生。
- 安静：broker_office_channels.go:DisabledMembers及notifier_targets.go的明确禁用优先于@；本仓使用PostgreSQL和原生控制工具适配多副本，不复制进程内开关。

详细设计及来源：[Plan](employee-four-fixes.md)、[遗忘](employee-privacy-fix.md)、[Task/提醒](employee-task-facts-fix.md)、[安静](employee-participation-fix.md)。代码review复核未发现本轮新增阻断；以上限制明确保留。

## 验证与证据

证据根：`/Users/yuanzhan/d1/employee-e2e-evidence/EMPLOYEE-FOUR-FIXES-20261004/`。

- integrated-handler-tests.log：14个顶层定向测试通过，含资格11子例、提醒8状态子例；0 skip。使用独立employee_quiet_fix_1004库，合并源码865b94d1dd。
- quiet-final-tests.log：788088后受影响安静两项通过；kernel-final-tests.log：全部Loop测试含双顺序exclusive控制拒绝通过。前序集成的其他行为不受该局部改动影响，直接复用。
- privacy/：4顶层及既有withdraw/owner隔离，去掉修复的反例日志保留；task-facts/：8顶层及8状态子例独立DB通过。
- send-guard-tests-isolated.log：3个已有发送守卫测试通过；全部使用本地fake provider，无真实DWS操作。
- participation-migration-replay.log：新表/CONCURRENTLY唯一索引独立SQL实跑，重复执行成功且indisvalid=true；新迁移无FK、各并发索引独立单语句。server-compile.log只作编译证据，不当测试用例。
- static-kernel-tests.log的迁移lint仍失败：旧迁移编号冲突。migration-baseline-comparison.json证明基线45605cff04与本轮的重复编号组完全一致，新10000/10001没有新增冲突；本轮未顺带修历史迁移，不宣称全仓检查通过。

未部署因此没有本轮新SLS/LF/真实IM证据。下次获准发布后只复验这四个原失败，执行前同时列用户效果、Task/Run/source、实际工具、输入来源、权限及副作用，按同一清单取IM/API/SLS/LF，不滚动扩范围。

## 清理与配置

cleanup.json已回读三个本轮独立DB都不存在；service发送测试的随机独立schema自动清理。默认业务表及预发数据未写。两个干净开发worktree保留审计，其他session不动。上轮hourly恢复状态、Runtime候选和清理结论不受本轮改变；本轮没有新增远端测试对象或配置变更。
