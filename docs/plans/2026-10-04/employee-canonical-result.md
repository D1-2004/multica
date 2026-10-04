# 修复结构化执行结果落账与续接失败

本批由用户明确授权实现并合并到Aone `feat/tag-multitenant`。基线c5c13339c6c56daa461031779e2622d13d88f2a4；本会话automation-2保持PAUSED。独立worktree，保护其他session WIP；完成后只快进目标分支并交统一发布方，本批不部署、改Runtime或写共享业务状态。

## 证据和Why

Qwen-DWS 2026-10-04 17:35引用旧报告要求“一周的，给我一个表格”。模型选对Task和当前要求，Host以ready拒绝。前一轮原final为合法tag-round-result/v1 JSON，保存Run时调用展示文本反斜杠解码，使字符串内的转义换行变为非法控制字符；Human结果解析失败，Goal未收口。失败与原始证据保留在当前私有diagnosis目录；不清History或放宽非法JSON判断。

## 冻结范围与合同（先于代码）

- 机器结果与展示文案分开：声明结构化轮末合同的可信queue context选择canonical输出，解外层payload一次，保留内层JSON转义。脱敏仍执行，不能泄漏secret，不能通过parse/remarshal吞掉重复字段或修合法性。
- 原纯文本结果与Router展示路径保留原语义。schema、来源权限、Task/Run归属、等待/取消/幂等均不放宽。
- ready续接拒绝使用准确状态说明，不承诺“稍后再试”会自然修复；不无条件把ready当成功。
- 目标分支不包含本波Human/SSE代码，仅合入本修复；另从线上组合36a18c03创建独立验证环境，应用同一代码验证完整链，不带其他候选入目标。
- 现存损坏Run恢复属于发布后的独立操作：先核原canonical、版本/后续输入、权限和已发送效果。不在本批直接改库或重复送达。

## 验收与方法

1. 合法多行summary、反斜杠与引号从CompleteTask真实入口经过PG落账，Run结果仍是可解析结构；一次脱敏保持有效JSON，重复字段/未知字段仍按严格decoder拒绝。原纯文本及回调展示不退化。
2. 独立线上组合：CompleteTask→Run ready→Human materialize→Goal succeeded→原quote read/continue，形成唯一新Run并保留本人的来源与新要求；通知/replay不重复。非法结果保持Goal未完成，不产生card或新执行。
3. 精确原final仅在私有证据目录用于本地对照，不进仓库；公共夹具用脱敏合成内容。mock/PG/编译与真实IM分开，不冒称当前对话已修复。
4. 独立PG数据库，准备和清理均由本session负责，结束drop回读；不使用默认业务库。只运行与该真实风险有关的定向检查。

## 有界交付流程

先复现→实现→目标分支本地合同及独立组合接缝验证→复核目标分支新HEAD并按语义rebase→普通非force push快进feat/tag-multitenant→回读SHA和提交正文→交统一发布验收。目标并发修改造成push拒绝时重新fetch/rebase和验证，不覆盖其他提交。

## 当前状态

实现和本地验证完成；目标已语义rebase到1b28362ca27112b7df1a07210983ab8565cdddfe，保留其他session并发代码和发布回执。目标合入将以非force push及远端SHA回读确认；未部署或恢复共享旧数据。

### 完整链复验发现的必要接缝（18:22修订，先于补代码）

修复落账后，合法结果已经使V2 Goal succeeded，但引用续接仍返回changed：ContinueDirectTaskTx对V2调用旧Resume，领域合同明确要求已完成Goal以新revision重新打开。因此本批增加必要的V2续接适配：通过明确的ReopenGoal领域操作按amendment边转换记录当前本人来源并增加Goal revision，保留原Definition约束与新prompt，原V1继续Resume。当前状态必须为succeeded，源重放可恢复同一已入账执行；ready/waiting/cancelled、其他人及过期CAS不放行。不改变Human回答路径、不放宽schema/等待屏障。这是原“结果→续接”验收不可缺的修复，其他新能力不纳入。


### 独立审查补齐（同轮范围）

- 不伪造同值Definition变更：新增ReopenGoal仅允许本人、已完成V2 Goal，CAS/精确源重放和取消屏障保留；新revision的Definition约束保持。
- 引用续接从已验证LatestRun queue继承其冻结round-result合同，并附原PromptContract；缺对应reader或未知合同拒绝。第二个Run也走真实完成落账和Goal收口，不只签新Run已创建。
- 非法JSON脱敏可能去掉原始换行而变合法，因此非法结构化结果只保存确定的非协议诊断，不被修成合法控制数据；原queue保留事实，重复/未知字段保持strict拒绝。

当前里程碑：canonical/严格拒绝与V2明确reopen、续接协议继承已实现；第二轮CompleteTask后Goal再次收口的PG验正在执行，其他既有控制回归通过。目标归并发布窗口已解除，合入前将以最新目标HEAD（包括211d及发布回执）语义rebase并复核。原修复前/中间失败日志保留。共享发布、现存三ready恢复及真实IM未执行。


## 最终本地验证与交付边界

| 项 | 当前结论 |
| --- | --- |
| canonical/脱敏/严格拒绝 | 已实现。结构化协议保留内层转义；PEM非法JSON不被脱敏修成合法；重复/未知字段仍拒绝 |
| V2续接 | 已实现。本人已完成Goal以ReopenGoal增加revision，保留Definition；取消/未完成/CAS和源重放门保留，原V1行为不变 |
| 两轮完整PG链 | PASS。CompleteTask→Run→materialize→Goal succeeded→quote q1续接唯一Run/revision2→第二轮CompleteTask/materialize→Goal succeeded；模型和DWS由替身提供 |
| 受影响回归 | handler21、service/领域25顶层通过，0fail/skip；race、server build、vet、146定义检查通过 |
| 最新目标基线验证 | 重放本修复6个顶层核心检查及服务构建/稳定ID检查通过；并发新增连接器/OAuth及发布回执保留，没有替换其他WIP |
| 独立审查 | 三个必要接缝已修；复审无剩余阻断 |
| 真实效果及旧现场 | 未证明。未新发钉钉消息、部署、切Runtime、重写旧Run或清History；既有ready恢复需从原queue canonical/当前版本和送达事实单独核实 |
| 定时 | automation-2保持PAUSED，没有自行恢复 |

证据位于当前私有目录EMPLOYEE-CANONICAL-RESULT-20261004：handler-final.log、service-domain-final.log、pipeline-and-domain-final.log、race-final.log、post-rebase.log及build/vet/eval日志。修复前和中间失败保留，最终结论以上述最终日志为准。没有把正确结构或本地新Run当真实GitHub表格交付；发布后仍须原场域原话复验。

稳定风险用例office-structured-completion-continue已加入continuation-steer/G07，146定义仅表示结构与映射有效。当前版本合入目标和发布责任交接后，本批停止扩展范围。
