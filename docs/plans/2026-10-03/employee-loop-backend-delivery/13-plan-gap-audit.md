# 交付 Plan、R5 原设计与 PRI-78 的差距核对

日期：2026-10-03（Asia/Shanghai）。用途：回答原设计承诺怎样演进、现有交付覆盖到哪里、哪些还不能算完成；本报告不修改 PRI-78、生产配置或新增功能范围。

## 1. 核对来源与证据边界

- 用户指定工作树 `/Users/mac-m3/.codex/worktrees/0156/dt-fde-multica`，HEAD `843d918328fce7587a47990f6a4a5b62ade0ad00`：
  - [R5 详细架构](../../2026-09-30/employee-loop-design.md)，主要核对 §3–11、§13–14。
  - [R5 十三任务实施拆解](../../2026-10-01/employee-loop-delivery-plan.md)。
  - 两份文件与本次目标分支上的同路径文件字节一致。它们仍保留历史“未实施”标签，不能据此推断今天的实现状态。
- 已交付规划包：本目录 [总控](README.md)、[Step 0](00-step-0-environment.md)、[交付标准](10-delivery-standard.md) 及 P/A/B/F/C/D/G/I-Q 文档。
- 最新目标代码冻结在 `origin/feat/tag-multitenant@727a7e38f89d0cb4e0ab4a0367e4114bc932f119`。本轮fetch发现它比上次文档提交 `99d733db9` 多了一波生命周期、typed wake、routine、watchdog、资源和验证实现，已按新代码更新判断；后续提交不自动包含在本报告内。
- [PRI-78「Employee 架构设计」](https://fde-workbench.dingtalk.com/private/issues/PRI-78)：通过当前账号只读API读正文、28条评论，状态 `in_review`，updated_at `2026-10-02T13:57:24+08:00`。另读取并核验平台hash的四份附件：`employee-review.md`、`employee-source-mapping.md`、`event-scene-router-research.md`、`s3-current-result.md`。没有声称读完其全部34个附件或原始共享GPT逐字会话。
- 最新实施/测试记录：[执行板](11-execution-board.md)、[GoldenCase-20](../employee-loop-golden20-baseline.md)、[当前实现合同](../../../employee-loop.md)、[先前Qwen-DWS真实验收](../employee-loop-e2e-results.md)。

本报告做静态源码与记录核对，没有重跑新一波Go/PG测试、线上模型或真实IM。本文“有代码”“有发布记录”“有真实case记录”分别陈述；独立验收和当前线上版本不能由文档记录替代。

## 2. 总体判断

核心路线一致：独立EmployeeLoop、不嵌套Coordinator；Event/场域定位与业务处理分层；Task独立于Issue；PG事实与既有执行队列；轻量HostGate与真实outbox。

变化主要在三处：PRI-78后来只负责统一事件路由；Employee实现按速度和现有设施收轻；最新交付由大设计转为环境准入、并行包、主代理集成和结果验收。

当前不能说“R5完整实现”或“GawkBot能力基本齐全已验”。问题既有尚未接通的功能，也有新Plan未明确承接的旧承诺，还有真实体验失败与文档状态滞后。

## 3. PRI-78范围如何变化，不能混为一张完成清单

| 时点 / 依据 | 当时范围 | 对今天的含义 |
| --- | --- | --- |
| 10-01 08:51审查，评论 `415cef83` | 云端Brain + 持续Task + 执行器；指出Session连续性、控制/保留、跨wake预算三项设计缺口 | 是整套R5的审查建议，不是这些能力都已实现 |
| 10-01 16:42 P7，评论 `7db66e36` | Provider→NormalizedEvent→旧Coordinator→原Task→原回报 | 曾建议先穿透旧业务链路 |
| 10-02 01:20 P8，评论 `e2f5b0b3` | Provider→统一Event→SceneResolver/Router→场域入口，处理器后接 | 后来取代P7；不能要求PRI-78单独交完整Employee/Task平台 |
| 10-02 11:22–11:23，评论 `31130586` / `d03833eb` | 全部交付归feat/tag-multitenant；PRI-78/91/95仍各自跟踪验收 | 单一交付分支一致，但不应把三张单的验证状态合并 |
| 10-02 13:56，评论 `1a7eed76` 和正文 | Native单聊/群通过；MessageRouter第三路因production/staging mismatch被拒；S3未通过、S4未发起 | 当前API没有后续完成记录。本轮Employee其他case不能替第三路/S4签收 |

PRI-78另外留下默认本机库恢复缺少可验证事前备份的问题。这是原验收/环境事故的待收尾项，不代表新隔离数据库不能开发Employee；也不能因Step 0新环境可用而声称原库已恢复。需原环境负责人另行提供安全恢复源。

建议在Step 0/Q登记PRI-78原有路由验收依赖：正确的预发MessageRouter输入、真实新receipt/replay/conflict/unmapped探针、非作者复核，以及原库恢复责任；不绕过环境保护或伪造provider事实。

## 4. R5十三任务逐项映射

状态说明：**已覆盖**表示主要实现及既有证据可定位；**部分**表示只覆盖其中一些语义；**待接线**表示库/reader已有但产品链未完整；**遗漏/弱覆盖**表示新任务包没有清楚的端到端承接。都不等同本轮独立复测通过。

| 原任务 | 最新代码/交付包 | 当前覆盖与变化 |
| --- | --- | --- |
| 1 入口/lane选择 | eventrouter、employeeentry、scene owner/fence | 已覆盖核心；route与Loop owner分开，比旧单一lane更精确。PRI-78第三路/S4记录仍未闭环 |
| 2 移植Go BotLoop | service/employeeloop及SOURCE_MAP | 已覆盖活跃内核/工具/ctx/预算。实际采用bounded Chat请求，不保留原流式chunk/无限tick，属于刻意适配 |
| 3 PG mailbox/journal/三ID | employee_task/entry/run、scene_job、task_wake | Task/执行映射与job恢复已覆盖；最新补typed wake和人类优先。不能把每wake内存队列当完整Task邮箱治理 |
| 4 DWS原生事件/订阅 | dwseventsource、native dispatch、eventrouter | 部分：主要IM。Consumer仍EventKey/Identities/line Handle，未完整开放SubscriptionSpec/typed payload通用consumer；广义资源事件未在新Plan单列 |
| 5 definition/ledger/note/dependency/resource lane | Compiler、完整纠正、Stop、lifecycle v2、taskinput | 定义/账本/限制已覆盖；WaitUpstreamTask只预留，Task links/依赖结果释放待接线。跨Task资源写冲突lane缺明确包 |
| 6 source-specific Builder | Host buildInput、native history、resources、TaskOrigin registry | 消息历史和资源已覆盖一部分；非@群材料、更多provider、dispatch工作包历史及自动化origin历史仍缺 |
| 7 HostGuard/单主模型 | source/read_ref/version/lease授权、模型计划、tool journal | 核心已覆盖，无旧finish_check。真实参数修复后静默、判断题过派仍影响用户体验 |
| 8 RunOnly/Issue投影 | direct_task、EmployeeIssueBackend；Coordinator create/continue实际调用adapter | 已覆盖主要双后端，无占位Issue/AP。跨场域复杂Task不必自动转Issue；完整跨环境Session恢复未覆盖 |
| 9 timer/webhook | routine occurrence/Direct producer；Webhook冻结入口；P2 wake reader | Cron run_only有代码/发布记录；F1入口有真实验收记录。decision/follow-up/Employee Webhook producer仍第二波 |
| 10 SceneNotice/ReplyComposer | terminal notice、真实文件策略、watchdog/host_notice | 部分：没有通用milestone/needs_input/blocked/question中间信号→持久Task状态→及时outbox闭环；watchdog不能替代它 |
| 11 control/wait/sandbox isolation | stop/steer、退出屏障、v2 wait | stop/cancel+resume已有真实证据；live append/answer、pause+checkpoint和全部backend隔离/滚动矩阵仍不足 |
| 12 Capsule/产物/留存 | Employee artifact加密、ready/hash/ACL、上传/删除恢复 | 最低产物保存部分已覆盖；完整manifest/checkpoint/workspace delta/统一留删/跨环境恢复没有executioncapsule实现，新Plan未单列 |
| 13 设置/灰度/证据 | Loop设置、sharedmodel、marker12、执行板、E2E harness | 已覆盖更多操作设施；发布/E2E状态需同步，不能仅以代码push或集成阶段SUCCESS判定产品已验 |

## 5. 已发生且合理的设计变化

1. **场域收敛**：旧R5的personal应读成如今的dm会话；个人配置不是scene。唯一scene_id来自agent_scene，不按staffId/UID造私聊场域。
2. **先分离事实再分离消费者**：eventrouter负责provider事实/场域回执，employeeentry冻结work consumer；legacy/unified不等于Coordinator/Employee。合法legacy receipt也可持完整Employee来源证明。
3. **RunOnly更轻**：即时Employee Direct不依赖真实Autopilot配置/Run；Cron才保留自己的真实APRun。旧“通用Autopilot”含义收敛为复用底层queue/claim/Runtime设施。
4. **快前台/执行器明确分工**：前台native模型/有限工具，业务DWS/MCP在后台。与GawkBot headless bot同一MCP工具面的实现不同，保留快回复目标；需要真实能力目录和准确派发边界。
5. **表达与模型预算简化**：单轮能答就答；每wake≤3真实请求，无额外分类/finish_check/润色。较新实现使用shared Coordinator模型、fast profile、native history，解决真实超时/指代缺陷。
6. **记忆隔离加强**：Employee独立于Coordinator，private/scene共享边界、来源时间、墓碑与遗忘过滤；最新新增Host verification和durable Distill，仍未等于共享promotion全闭环。
7. **交付治理明确**：新增Step 0、可并行包、团队资源、结果标准；表结构/API/阈值与波次参考化。最新执行板进一步确定了另一机器的实际环境、号段和Qwen-Real演员。

这些是设计演进，不应作为“偏离原方案”机械否定；但旧承诺减少/延期的部分应显式登记，不能默默算完成。

## 6. 最新一波确实补了什么，还没补什么

已合入的主要实现：

- 生命周期v2、waiting/CompleteGoal、source-deduplicated自主轮次字段、Task等待事实。
- message/task_wake job区分、0消息wake、人类优先、TaskOrigin registry、Host主动已送达消息进入近期历史，marker12门禁。
- taskinput集合/邀请/输入/ready intent及取消/撤权/过期修复；目前领域层，不是完整跨场域IM工具接线。
- routine冻结occurrence、当前权限来源、独立Task/Run/queue原子派发、原通知owner；仍是v1/single_run的run_only。
- Webhook endpoint冻结/验签/身份与accepted target防漂移；不等于Employee全链producer和decision。
- 确定性watchdog、当前配置阈值和发送前门禁。
- 当前/确切引用消息own resources的文本读取、冻结和有界提取；实际视觉理解未由它实现。
- Host确定性verification、PG发现/提炼意图、事务Distill及中文检索；共享晋级与撤回未接。
- Qwen-Real真实E2E harness、精确trace归属和部署窗口判定。

需要特别避免的三个“已有类型=完整功能”误判：

- `employee_task_wake.go::employeeTaskWakeTools`只注册reply/stay_quiet。collection.ready / execution.follow_up / routine.decision / webhook.decision在存储中有kind，不表示能派下一Run、更新等待或完成目标。
- `AutonomousRounds/NoteAutonomousRound`已存在，但生产worker未调用该计数方法，也未闭合Task级上限governor；每wake三次预算不等于整个持续Task有界。
- `WaitUpstreamTask`已存在，Task-to-Task link、上游结果注入/授权与真实终态释放仍未实现。

执行板第二波已把Task关系、其他场域只读事项、工作包历史、自动化origin、首次私聊pending_scene列出；这补了新Plan早期弱覆盖。但Capsule、通用SceneNotice、广义DWS资源事件、资源冲突lane仍缺清晰承接。

## 7. 最新真实体验记录：必须优先修复的缺口

[GoldenCase-20记录](../employee-loop-golden20-baseline.md)报告20条通过10条；52次Employee唤醒共59次请求、单wake最多3次。它是最新提交中的验收报告，本次没有独立复跑其原始Langfuse或IM。

| 问题 | 记录/源码依据 | 为什么影响完整交付 |
| --- | --- | --- |
| 非@群材料不可见 | 群9条仅1通过；native dispatch只接受IMAt群事件，RecentConversation仅admitted轮次 | 无法持续理解同事讨论或正确主动参与。未收到事件而没有回复，不能当quiet决策通过 |
| source_ref坏参数后静默 | DS-12及BASE-TASK，Host拒绝后模型quiet；已生成答案未发 | 权限拒绝正确，但错误修复/收束不足；普通明确请求被悄悄吞掉 |
| 有充分证据的判断题被后台派发 | DS-01一次判断被派后台，记录20次sandbox请求并回复两条 | “前台没业务工具就派发”过强，损害快回复和必要调用标准 |
| 模型先下结论再矛盾 | DS-03超时口径判断 | 属于语义质量，Host/更多硬模板不能代替真实反例优化 |
| 同目标多个候选误新建 | BASE-TASK a2被报告测试污染；既没澄清也未续接 | 测试隔离要改，真实产品歧义处理也要验。群版直接算总和不能证明同Task新Run续接 |

建议修复顺序：先可获得的群历史/观察数据及明确权限水位，再source错误的可恢复提示/非静默收束，再前台“已有证据判断”与实际查询的边界。避免为了方便随意宽松匹配他人source_ref；可以提供本wake合法引用和有界repair，不让错误串成为授权。

非@观察与主动工作分别处理：观察可以提供已授权材料，不自动形成执行授权；主动参与仍由当前配置和Host资格判断。平台未暴露某事件时，核验可用订阅/历史读取，不伪造key，也不把DWS整个产品都判定为无能力。

## 8. 新Plan建议补齐的承接与最终验收

以下是本次审查建议，尚未新增生产实现或替用户批准扩大范围：

| 建议承接 | 归属与依赖建议 | 结果验收 |
| --- | --- | --- |
| PRI-78原事件验收收尾 | Step 0资源表/Q；独立issue仍分别跟踪 | 正确预发Router真实receipt+探针+S4；原库恢复单列 |
| 群观察/广义source Builder | 增事件/source单元，P/I与D配合；先处理已有IM/history，再OA/Todo/日程/文档等正式发布key | 未@材料可获准读取且不自动授权，真实多源事件有独立payload/scene证明 |
| Task mailbox/关系/有限推进 | P3/P4；现有v2/wake基础上接governor、links、等待/下一步工具 | 原目标延续、上游结果不越权、跨重启预算不重置、cancel高优先且无无限效果 |
| 通用SceneNotice/HumanQuestion | 单独通知/输入小包或P3/C扩展；复用task run events和outbox | 长Run未结束即可发关键问题/结果；回答绑定正确question和Run；stage不覆盖final |
| Capsule及执行恢复 | 独立执行现场小包，接现有artifact/trajectory/GC和Runtime接口 | checkpoint完整才发布、GC不删未保存现场、权限撤销拒绝resume、正式产物独立、期限前可靠处理 |
| 跨Task资源冲突与安全计算复用 | 执行/资源域单元；不只依赖Task单writer | 不同Task操作同获准资源时序列化/冲突处理；私有文件/session/credential负向实测 |
| 外部效果控制与执行能力接口 | 核对PRI-78提及的PRI-84执行层当前合同，再由能力/执行负责人承接 | 取消后新平台动作被fence；已向外部提交的动作只对账，不承诺撤回或所有CLI出口零竞态 |
| 高质量交互回归 | Q与前台合同负责人 | Golden关键失败逐项修复/原场景复测；同义表达允许，事实/格式/必要效果有证据 |

仍保持用户最新方针：结果优先、强模型优化、实现细节主要参考，主代理按资源和拓扑决定迭代。不能把旧R5全部类名/状态重抄一遍作为“补齐”；也不能把“不按固定实现验收”误解为可以省掉现场恢复、输入归属或可靠通知的结果。

R5 §9已经说明：后台进程取消不等于此前HTTP动作没有发生，直接走外网的CLI不能由SIGTERM证明零竞态。本次只读核对了PRI-78中对PRI-84的历史引用，未单独读取PRI-84今天的实现/验收，故该接口承接属于待核项，不断言另一个项目当前完全未实现。

## 9. 文档与协作上的变化/冲突

- 老设计两份原稿仍“未实施”；新实现合同/执行板/测试记录持续推进。建议旧稿保持设计沿革，加清楚的当前索引，不回填39个checkbox假装每条已按原方法完成。
- Step 0/00-context的本机origin-only、Qwen-DWS、临时Go/PG路径与执行板另一机器的aone/origin、Qwen-Real、Docker路径是不同环境，不应全局互相覆盖。每个主代理按自己的就绪表登记实际资源。
- 原I/Q要求关键审查后开放；新执行板允许异步审查不阻塞预发。可区分“候选canary发布”和“正式能力启用/验收签收”，但已知越权、重复效果、失效lease仍提交等阻断项不得因异步审查而忽略。
- 执行板记录deadline相关race失败、handler/service基线失败和migration历史冲突；可以比对新旧失败集合，但预算/恢复相关失败不能自动当无关baseline或以skip证明正确。需独立复现并记录负责人与风险。
- 新板第一/第二波迁移号段已登记9900–9989与9800–9899；旧Step 0的“由I分配”需以实际执行板更新，不各代理另占同号。
- 执行板第二批还写部署中，Golden报告已有19:09 marker12记录；必须按实际pipeline/运行副本/用例窗口重建版本，不能只用其中一份文档当实时状态。

## 10. 本次结论

架构方向没有反转；基础链路比R5时已前进很多，新一波也补了waiting/wake等关键地基。完整持续员工能力仍取决于真实上下文、语义交互、Task目标推进和运行现场/通知闭环。

本次最需要更新的是**需求→设计→代码→部署→真实证据的逐项映射**。PRI-78的未完成事件验收、R5尚未兑现的Capsule/中间通知、多源上下文，以及Golden真实失败，应分别登记；不能合成一句“基本一致/基本完成”。
