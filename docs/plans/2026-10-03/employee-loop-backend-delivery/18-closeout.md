# 第五批接续交付收口与最终验收

2026-10-04：用户明确要求继续把当前任务做完，并给最终验收报告。本 Plan 承接 15–17 和原 session 的第五批交付目标，完成实际发布/验收，不以保存方法代替交付。

## 本次范围与完成条件

1. 收 Q harness 五笔未合入提交与六个脏文件，逐能力验证，撤回/原生表情保持原 session 排除项。
2. 已集成 retry 修复进入预发；关闭 DS-09 原失败，修首次建库依赖与 webhook frozen-source 混版门槛。每项先更新合同再代码，独立 worktree，主代理唯一集成与部署。
3. 八个验收包逐条有真实 IM、执行事实、SLS/LF 和环境窗口结论。已通过 CRON 不因无关修改重复跑；MEMX-L1/M5/MF 保持原场域所有权和相应时间/混版条件。
4. 广播外验收义务映射并验证：MEMX-N1/BASE-MEMORY/两人提醒/BASE-TASK；WD-04/G3b 等第三真人依赖实际核对，缺人不能换 DEAP 冒充。
5. 刷新已集成 harness/capabilities 的准确名单，运行当前可执行的 wave2 与必要 Golden 原反例；跨日用例保存开始/到期 checkpoint，未到时间不算 pass。partial/vacuous/外部阻断不混入通过数。
6. 本次范围完成后归档最终报告：代码/部署版本、测试分母、分面证据、用户效果、清理/恢复、已修/未完成、明确验收结论；有效验收后关闭本次预发验证门。

完整 R5 的 Capsule、通用中间通知、跨 Task 资源冲突与受控共享晋级等保留在13审计，不把第五批签收宣称为整个R5已实现。发现范围或外部依赖变化即时更新本Plan和进度板。

## 并行责任

- 主代理：集成/发布/环境门禁、COL/REF/builds_on/DM验收、总体报告与恢复。
- Harness：仅 scripts/employee-e2e 的 P1收尾、能力声明、driver/grader证据完整性；保护原Q未提交改动。
- 输出质量：DS-09模型输出合同与必要反例，不用Host硬截编号；不改在线模板，除非证据满足原session的修改条件。
- 发布安全：9821/9930 freshmigration依赖、通用 frozen-source reader-first/回滚合同和实现。
- 记忆验收：g_team 为M5独占，group_p_hx/group_t 为M8/MF独占；只执行分派用例，不共享模型近期历史、不要同群污染。

## 状态与证据

本地交付 HEAD 2f4e6005b4，预发第五批 a4c3aa4dab。CRON pass；M8摄入/主动唤醒/投递pass，严格DS09 fail；retry本地已修且11定向/3race通过。其他项待执行。

进度：`_shared/resume-progress.md`；本轮 evidence 用 `~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-*`，每例开始/完成/阻断立即落盘，不能只有最终绿灯摘要。部署前后时间窗固定，旧失败和invalid尝试保留。
