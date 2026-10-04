> **当前执行入口：[BCD＋主动进展开发状态](implementation.md)**。用户最新授权已进入开发和独立远端提交；下文f4/44566及“只做Plan”属于历史复核。BCD已选，主动进展最小链路已实现并通过本地检查，发布与真实E2E由原交付会话接手；不能据此签线上可用。

# EmployeeLoop 剩余能力与验收计划（最新远端）

**当前执行方式已调整**：先从单会话、一个请求人、通常1–2小时的用户工作入手，用户已选B/C/D，先B+C再D，每小轮建议1–2项。请先看[六个短任务选项](short-task-picker.html)和[小步交付规则](short-task-rounds.md)。下文十项是全量能力池，未选项不自动展开。

用户新增的[主动进展研究P](proactive-progress-design.md)已按GawkBot源码与本仓接线核对，包含最小展示判断设计和七个反例；BCD选择不变，P尚未默认进入实施队列。

2026-10-04，冻结远端 `feat/tag-multitenant@f4c4790cbea1066857f96d4b6468c9710d77646b`（06:47:12 +08:00），比初次44566d0bf7新增36笔。本工作树规划分支已rebase到该版本；只增加规划/评测材料，不修改业务实现、不发布或运行真实评测。

**现在先完成第三候选的原失败闭环，再扩持续员工能力。**旧待办中的harness未合入、首次建库依赖、DS09仍失败已经过时；同场域记忆硬门与原生收集取消也有新证据。最新阻断集中在本人跨源遗忘、真续接/当前进度、迟答假记录承诺、证据筛选/计算，以及后台Runtime轨迹可读性。

[最新详细复核](latest-remote-review.md)给出代码→发布→验收映射；[能力图与选择页](decision-board.html)可逐项选「做／后做／不做」并导出。所有用户决定仍未定，建议不等于批准。初次分析保留在[44566旧快照](previous-review-44566.md)，不能当实时状态。

## 1. 新增改变了哪些判断

| 项目 | 当前结论 | 剩余边界 |
| --- | --- | --- |
| P1 harness | memory_reset/授权API pg_read/segments/evidence_v2/file_send等已合入 | 平台/发布/运维仍按manifest证明；Webhook/AI表格/config_snapshot驱动未实现，recall/react仍排除 |
| 首次迁移/队列/retry | 9821→9996与alias、queued_frozen/guard、唯一retry已修并进入远端/已发布组合 | 局部验证和发布记录分别成立；真实回滚/故障按原证据签收，不重复开发 |
| DS09 | 原场景当版只有BR3、IM/model一致，报告pass | 全群质量未完成；G02/C03当前fail，最新修复待当版复验 |
| 收集取消 | read_task→cancel_collection、Task/wait关闭和迟答不复活已有证据 | 迟答仍出现未记入却说记下；closed/active/mixed新修复待验 |
| 记忆 | 同scene权限/遗忘/available-history硬门已过；真实14话flush成功 | 本人跨源DM旧派生reply撤销是新反例，epoch17候选待live完整旧值反例；MF纯长期使用仍partial |

事实依据来自最新提交源码和已提交报告，本 session没有独立live复跑。最新报告有发布证据的第二版是fa8302/run3110367125；第三候选含3128/8fcb/07e9仍待发布复验。来源和hash见[source-manifest.json](source-manifest.json)。

## 2. 还要做的十项及收益

| 编号 | 当前该做什么 | Why／收益 | 建议优先级 |
| --- | --- | --- | --- |
| GAP-01 | 第三候选复测G02、C03、窄lookup空结果/历史从未发生的错误推断；保留已过DS09作为影响回归 | 用户要未交清单就只列符合条件的物料；先算后判，不说自相矛盾结论 | P0，与原实现session收口 |
| GAP-02 | 真续接同Task新Run、进度read、COL关闭迟答诚实表达；继续原收集/依赖/自动化边界 | 正确数字不替代执行；不假称记下或汇总；多人输入不串事项 | P0，GP-34/35及原BASE/COL反例 |
| GAP-03 | 全副本epoch17后证明本人跨源forget的新DM完整输入不复活旧value；再补纯长期使用/新Task经验复用 | 私人材料忘记后不沿旧回复重现；经验能正确改善下次工作 | P0隐私；共享晋级/撤回另选 |
| GAP-04 | 共用A2UI底座补任意长Run的阶段通知/问题→等待→正确答案→原工作继续 | 卡点及时问，不靠催问、不重开同文件第二writer | 原失败收口后的首选能力切片 |
| GAP-05 | 完整Capsule：quiescent/checkpoint/manifest/workspace delta/统一留删/兼容恢复 | 跨日、等人、重启或回收后继续原现场；正式产物独立有效 | 有长任务需求先做最小完整版 |
| GAP-06 | 跨Task同资源共写/版本冲突，跨principal文件/session/凭据隔离，外部效果fence/对账 | 并行不覆盖、不泄露；停止不虚称撤回已发生HTTP效果 | 共写或环境复用启用前必做 |
| GAP-07 | 第三候选发布/epoch17滚动证明；独立Runtime候选修后台generation损坏并验新完整LF；保留回滚条件 | 既有修复实际生效，后台执行可审计，混版不产生不同隐私语义 | P0，沿当前runtime/deploy工作流 |
| GAP-08 | 按正式key逐源接日程/文档/OA等；PRI-78原单S3/S4/历史库恢复向责任人独立核对 | 业务事件正确落场域/事项，不借订阅人身份提升权限 | 按具体业务源选择，未见本波新签收 |
| GAP-09 | 真实像素理解，不用文本/OCR冒充完整视觉 | 可处理截图和无文字图形关系 | 有业务需求再做，可后做 |
| GAP-10 | 到期完成跨日段；按原目标验证条件提醒一次、材料到齐不催、暂停不复活 | 能兑现截止和分工，保留先审阅/不外发约定 | 先完成原segment，再选提醒切片 |

不需要恢复旧R5全部类名或换框架。A2UI卡片收发存储存在不等于已把答案注入任意后台Run；正式文件保存不等于完整Capsule；记忆workflow记录promotion不等于真实共享。

## 3. 建议接续顺序与签收

1. **第三候选发布前后**：先完成独立review/候选Runtime完整generation证明，重新固定manifest；全17后跑本人跨源旧值原反例和16/17混版对照；复测原BASE-TASK/COL/G02/C03。初始Task同ID≥2Run、真实stdout/read、无未发生的记录承诺，不能只看55/ACK。
2. **补真实前置**：MF排除近期history/admitted assistant后独立召回；G15第二段最早10-05 05:18:38 +08:00；WD04请求人之外需三真人，目前仍缺第四人。未答的授权询问不视为批准，不用DEAP凑数。
3. **刷新可跑名单并收口证据**：原wave优先22只执行11，7语义pass、2fail、1softreview、1partial；其余保留状态。学习行、WorkPacket learning引用、逐人input、occurrence原因等缺consumer的硬条件单列。最后恢复临时paused的hourly并回读，报告完整才关闭验证门。
4. **选下一能力切片**：优先长任务真实问答；Capsule/共写/共享/扩源/视觉按实际业务与用户决定推进。

原有通过且未受影响的用例不重复跑；安全/归属/重复效果/缺证不被语义平均分抵消。不提供整个R5完成百分比，也不将Golden20加到88形成新的待验分母。

## 4. 评测与计划产物

[评测套件](../../../../scripts/employee-e2e/cases/gap-plan/SUITE.md)使用最新v2 parser：现有88条不改，新增35条定义（32条原GP加GP-33跨源私有撤销、GP-34执行继承/当前read、GP-35关闭迟答假记录）。全部not_run；有定义不代表功能已实现或可跑。

本 session实际默认88条dry-run：35 runnable、19 partial、7 waiting_release、21 waiting_ops、5 blocked_harness、1 blocked_resource，suite_errors=[]；[完整离线名单](latest-88-readiness.json)。开关默认false不等于功能未上线，必须由当波证据启用。

[contracts.json](../../../../scripts/employee-e2e/cases/gap-plan/contracts.json)中的setup/身份/时间/硬事实/consumer是签收必需，旧grader不认识的事实不会被静默算pass。跨会话card/question动作、候选Runtime和故障条件未就绪继续阻断。

[静态图](capability-map.png)／[可编辑SVG](capability-map.svg)／[选择页](decision-board.html)／[决定源文件](decisions.json)／[验证记录](validation.md)。图表达已有范围、缺证和开放能力，不作为实时线上监控。

架构继续参考本仓固定GawkBot SOURCE_MAP。未来长任务输入参考[LangGraph持久interrupt](https://docs.langchain.com/oss/python/langgraph/interrupts)，外部效果沿幂等/对账边界，不新引入框架。细节参考初次规划，用户选择和当前合同优先。
