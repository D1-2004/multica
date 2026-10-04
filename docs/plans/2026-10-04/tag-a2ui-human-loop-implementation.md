# Tag A2UI 人工答复闭环实施

2026-10-04。当前开发分支 `codex/tag-a2ui-human-loop-20261004`，基线 `aone/feat/tag-multitenant@f4c4790cbea1066857f96d4b6468c9710d77646b`。本轮为开发与独立本地验证；不自动发布共享预发或发送真实IM。

## 已确定产品合同

- Employee前台可发询问；Pi默认在Run结束后输出文案和可选问题，不阻塞等人、不恢复旧session。
- 单选、多选、人员歧义用当前A2UI投影；ChoicePicker可承载已核实候选，原生UserPicker不等于冻结候选。
- 按钮和普通文字均为正式输入，原话约束保留，复用同一个Employee Loop与模型预算。
- 每个问题保存Host绑定的owner/tenant/scene/source/requester及可选Task/Run/目标版本，事件只提供问题引用，不能指定工作归属。
- 答案保存与下一步消费意图同事务、重复事件不双派；多题歧义追问，新话题不关闭所有问题，停止/过时目标不复活。
- 任务轮末结构结果明确区分clarify与suggest；缺信息仅表示本轮结束，不宣布原目标已完成。没有选择不自动批准，不新增复杂过期状态机。
- 随机短反馈设计保留，源受理/重投冻结同一句；loading只在真实渠道支持时投影，不能凭示意图宣称已实现。

## 架构依据

GawkBot固定来源 `71e82a1809565281cbd0bf8185d3c125b715d934`：按钮与引用文字共用答案变更；人话优先并明确唤醒等待/完成事项。采用其统一输入与定向唤醒，不照搬30分钟工具poll或新消息取消同频道全部interview。比较证据见 `tag-human-text-gawkbot-comparison.md`。

当前规范：根CLAUDE.md、docs/employee-loop.md、docs/employee-delivery-workflow.md及对应模块合同。首批复用持久scene jobs、原工具journal、三请求预算、response outbox、Run事实消费；不用第二个推理Loop。

## 并行任务与范围

只读查看在线任务：Gap任务在开发B/C/D/进展；Eval任务在调整黄金场景呈现；原交付任务刚验epoch18及四项修复。这里独立开发A2UI/HITL；不改它们的测试身份、Runtime、场域、共享配置或未提交代码。集成前重新fetch并检查语义冲突。

## 当前执行表

| 切片 | 用户效果 | 本地验收 | 状态 |
| --- | --- | --- | --- |
| R：结果契约与提示约束 | Pi轮末明确输出summary/choice，普通文本兼容 | 严格schema、非法选项/引用、不把stdout/Markdown误判为控制 | implemented_local |
| Q：问题与答复账本 | 每条答复明确原事项，按钮/文字统一去重 | 权限、场域、Task版本、竞态、事务重放 | implemented_local |
| F：前台工具装配 | Employee发询问后释放本轮 | 冻结工具schema、Host来源、幂等发卡意图 | implemented_local |
| C：回调与文字回流 | 两种输入进入对应Employee Loop | 两题隔离、无引用明确答复/歧义、不会伪造授权 | implemented_local |
| S：轮末卡片与下一轮 | Run结果投影选项，人答后fresh Run | 精确Run/Task、停止/旧卡、已有副作用不重做 | implemented_local |

## 验证策略与停止条件

先独立本地数据库/Redis和fake model/provider，禁止共享库与ambient真实agent CLI。新增测试只抓权限、关联、事务和竞态等真实风险；Golden E09–E12文字案例需要生产接线后在真实DWS路径验，真点击需客户端单独验，不把模拟写成真实E2E。每个切片记录implemented/integrated/deployed/e2e_verified和未证明条件；当前只有开发授权，发布与真实验收随集成阶段安排。

## 本地实现检查点

已完成R/Q/F/C/S核心接线；通过独立PG15434的32个顶层handler用例（含人工交互/原通知与交付约束回归），全部81个命名用例无fail/skip。协议、entry和transport切片的独立测试另见证据。这里是scripted model/provider，不是真实Pi/IM/模型语义验收。审批权限扩大、引用loading与即时随机反馈的生产接线不在此代码切片中。

独立审查发现并修复：cancel答复不能吞掉原stop_task输入；notice先scene后Task防锁序反转；BeforeSend检查旧目标/停止以抑制过时卡。工作区显式清理问题、响应及关联A2UI记录。

基线检查局限：migration lint已有23处历史重复数字前缀；workspace deletion manifest的旧schema分类大量缺失。新9977–9982与问题表已登记，不把这两项基线失败写成通过。一次cmd/server的go test -run空集仍触发TestMain，默认本地库上在本轮新加nil发送hook处panic；已修nil guard，并改用compile-only/显式独立DB，不能算该轮成功。子代理一次默认DB测试也因旧schema失败并清理，之后强制独立环境；均无真实IM或沙箱执行。

当前检查点：完成最新remote同步/rebase与受影响检查后交付可审查代码；预发发布、真实DWS/Pi端到端和客户端点击属于下一验收里程碑，保留E09–E12及新10条候选场景，不自动占用其他在线任务的环境。

规范更新：远端4674169c3d带入docs/development-delivery.md统一开发合同；“提示词入口”是开发指令AGENTS/CLAUDE入口，不新增产品提示词平台。本轮保持原kernel提示词与历史快照，只在Host冻结结果契约/工具。Eval候选按在线Eval分支8e7f4a447d的roles/verifies/method贡献规范编写，canonical集成待该分支落入集成基线。

环境恢复：cmd/server默认测试库panic留下的本次合成fixture，仅按确切UUID和04:10:49 UTC创建窗口清理（workspace/member/agent/runtime/user）；没有按全局slug清理他人数据。compile-only `go test -c ./cmd/server`通过，不再执行未显式指定DB的TestMain。

最终代码检查点：`81d501962a` 已rebase到最新集成基线 `5357e67fb4`，三处重叠文件自动合并后核对两边意图。受影响handler检查再次32顶层/81命名全部通过、零skip；server compile-only通过。代码仅本地提交，未推远端/部署；原型和先前设计材料保留为未跟踪session文件，不夹带进入核心实现提交。独立本地PG测试资源在本波结束停用，下一轮按manifest重启；无预发配置变更、无真实IM发送。证据索引为docs/evals/results/tag-human-local-2026-10-04/manifest.json。

## 统一分支集成预检（当前验收检查点）

用户已授权协调唯一发布方“发布和验收”。在独立worktree基于employee/progress-release@eb62d0f06e摘原候选两提交，5处文本冲突按语义并存：保留reader20、eval-report、progress/participation，新增独立human:1；publisher黄金报告WIP未触碰。独立集成分支codex/tag-human-integration-precheck-20261004，编译通过不等于已发布。

预检发现必须修的闭环风险：typed新Run最终notice混用原receipt与答复Job并走错效果journal；文件送达抑制同时跳过V2 Goal结束；typed旧卡绕过持久quiet。先按原风险编写回归并确认前两项失败，再修。补验末轮notice/Goal及quiet，真实场景增加H07静默后的旧卡不继续并原人恢复参与；其余场景不改口径。

准入仍等待唯一发布方固定下一窗口、精确source/release/run和两live新启动/employee-human:1。当前零真实台词发送、零共享配置修改；不把黄金集9/20结果作为本A2UI验收证据。DWS当前正式chat shortcut目录可发/更新流式卡，但未发现用户A2UI点击入口；真正按钮通过本机冬翔钉钉客户端验证，文字通过私有DWS执行。

本轮修复后：全部Human定向回归通过，零fail/skip（计数见integration.json），包括真实数据库下末轮通知/Goal、file-only complete/suggest/clarify和quiet五个子场景；其余未变更范围68顶层/178命名通过证据保留。quiet queued_send首次失败因最小fixture缺当前identity，补准入后回归通过；不能把该第一次失败省略。最终server compile-only再次通过。仍未发布、未发真实台词。

## 验收设计收紧（用户要求完成真实闭环）

固定[真实验收合同](tag-a2ui-human-acceptance.md)与docs/evals/tag-human-real-cases.json。9场景/10入口：H03文字和真实多选点击分开；H05分两次建题；H06用有Task的必需wait停止；H08独立验completed-suggest→新Task/builds_on；H09验scene/requester隔离。每例必须到最终通知/Goal，不以ACK、新Run创建或原型签收。稳定发布后预计60–90分钟，优先两个成功入口，关键反例与补证按受影响范围。非法控制、真实网络重投不可自然制造的边界单列本地确定性证据，不冒称客户端E2E。

真实取证面检查发现typed终态工具未导出LF Host结果，虽然journal落库，但真实验证不能直接从终态generation读取receipt/Task/Run/queue。先更新LF合同，再补同已有frontend一致的工具观测与question/response来源索引；只在实际journal回调执行时记录，replay不制造工具调用，不增加模型请求或改变派发。该有限观测改动随候选交付，验证脱敏、实际结果IDs与重放次数。

有限取证补充验证完成：Human全部＋既有frontend trace共18顶层/37命名通过，零fail/skip；真实DB journal重放不增加tool span/Run，Host receipt与实际Task/Run/queue一致，秘钥哨兵经既有脱敏消失，server compile-only通过。仍是独立本地scripted model/provider，未签真实IM。

## H01真实首轮失败与有限修复

统一source36a18c/release34ceba/Run3110385309，现场两live loop22/human1且Tag13/Runtime461验证。H01冬翔17:37:17明确先单选后执行，但Loop98de1ca7实际dispatch_task并自动follow_up_steps，17:37:25建Task0fd4c4d3，17:37:39第二Run运行；没有A2UI卡。17:39:06现场另一真人回复A（非本session发出）晚于提前执行，不清除原FAIL。系统另加真实资料查询/文件交付，17:41:54文件送达新测试群；原现场留存。原人17:42:29停止后APIcancelled/无activeRun。

初始完整LF gen含a2ui_ask，但首问没有HUMAN策略，appendHumanQuestions在pending为空时早返回。只补Host新snapshot策略，清楚禁止用自动plan表示人类wait，保留独立Pi准备/轮末与已有自动步骤合同；不写中文关键词硬路由，不改旧冻结input/journal。修后局部模型/数据库约束仅作本地证据，H01原反例仍待独立修复发布真实复验。其余依赖前台首问的case暂停，保存首轮FAIL而非清History重跑到绿。

## 后续交付工作流（用户最新要求）

后续改动全部经a1创建Code Review，目标统一feat/tag-multitenant，并向用户给出CLI返回的CR链接。合并代码、解决目标分支集成冲突及部署由“发布协调”session负责；本session只维护范围明确的源分支、CR与验收证据。收到其部署完成通知后重新核对精确source/release/run、live marker/fence、Runtime及配置，再接续原反例与冻结场景；不自行合并或发布。

本次首问修复从最新aone/feat/tag-multitenant@1b28362ca2独立建源分支codex/tag-human-first-question-cr-20261004，仅摘880c712981的5文件修复与合同/证据，避免旧分支历史及独立公共入口混入CR。原H01失败和群历史保留，CR创建不表示修后真实模型已通过。

CR目标基线复验完成：独立PG15434已按该版迁移，本Human及联合入口21顶层/40命名通过、零fail/skip，cmd/server compile-only通过。此为本地scripted model/provider，仍等待“发布协调”合并/部署完成通知，再验原H01和其余9入口。
