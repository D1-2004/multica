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


## 03:50 当版复验与新增修复切片

交付 e4424c6739507860e26f5752a44934813889abd7 / release c0a719bd448203c1ed692cd8894a47751e0a07e8，预发 run3110364914 已部署。两 live backend 本次启动 03:04:45.035 / 03:05:53.237，loop16/webhook1/memory1–3且fence normal；验证门保持等待。

- DS09 原场景当版只输出 BR3，真实IM与employee_model一致，pass。原失败保留。
- COL03 已实际调用 read_task/cancel_collection，原Task cancelled，迟到两人7/4没有复活汇总；补最终窗口证据。
- MF 原group_t 14条真人触发成功flush：03:18:05.785 committed、accepted7/rejected0/1call；等待≥30分钟独立召回。不可在此等待中发布穿窗。
- M5 新阻断：退休record后的reply-ancestor校验遇到合法零模型/reset快照NULL会让整个History和DWS transcript不可用。安全修复范围为局部剔除不可证明assistant及依赖后继，保留独立真人；未知快照不能宽松放行。定向DB反例→集成→预发→原场景M5复验。
- wave G19有真实轻UX缺陷：社交点赞后主动销售能力；独立judge不采纳普通聊天字数阈值。改通用Persona边界，不做Host截断。T03套件regex与已交付表格不一致，保留原断言失败并另判用户效果/时态/续接事实。
- 两人提醒账本、WD04、BASE-TASK继续推进独占验证；wave跨日及环境依赖保留真实checkpoint，不用not_run补成pass。

新增核心修复采用上述切片合同，发布后只复测受影响范围。报告分别列第五批切片、wave覆盖与完整R5开放承诺。


## 04:05 新反例与验收边界更新

- BASE-TASK 原Director DM复验出现实质wrongTask：新ff4ab139任务只有1Run，续接读t5旧f1fb4e67并在旧任务开新Run11468b00，数字55正确但归属错误。修复当前TaskBrief新快照的原可信请求/最近人类输入来源与时间，帮助模型以明确对话关联区分同goal；没有明确关联仍澄清，不默认最新、不靠goal或题材Host硬分类、不热改旧snapshot。read_task仍当前状态/授权前置。
- COL03当版执行取消正确，迟到无consume/无origin汇总，但Director收到‘我汇总给冬翔’不实承诺。LF证明新输入只有旧历史邀请、没有该已关闭邀请事实。新增当前sender/scene自己的近期关闭邀请数据，保持封闭绑定/工具门禁，模型只说明已关闭、不承诺转发；不能暴露其他对象/答案/来源私信息。
- MF33min后回答正确，[m1]检索注入成立，无[O]；原句仍在History，因此仅写入/Host检索/召回用户效果通过，排除原History后的纯记忆使用证据partial，不降低标准签收。
- WD04需发起人之外三真人，当前winter/Director/dxxh共三人仍缺第四人。已向用户异步询问授权号池，其余推进；不得把自邀或DEAP凑人数。
