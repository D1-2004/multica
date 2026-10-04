# 新事项判断、按需查找与首轮流式反馈实施

用户授权：同步远端后开发，完成交「发布和验收」统一集成、发布和真实验收。本批不自行部署/Apply、不修改Runtime或处理其他历史失败。基线remote统一发布分支eb62d0f06e，包含reader20/steer/progress/EVALS；其他checkout WIP不带入。

## 本轮修订（先于代码）

此前方案阶段一等待完整read调用后投递反馈。用户后续明确同意流式，本批采用**独立原生first_feedback公开帧**：同一次首轮模型输出一条完整、明确来源的反馈工具帧，以及必要的find/read计划。Frame合法并经Host资格检查即可持久化/入队；其他业务调用必须仍等完整finish_reason、整批和参数校验。不是任意text_delta转IM，不转thinking或半个参数。反馈不能独占新一轮模型，首轮直接回答/受理/安静/严格格式不调用反馈工具。

流提前接受的公开意图与整轮可能截断分开：合法反馈可已提交，但截断/非法的业务batch必须零业务效果，最终给真实失败，未完成的frame绝不flush。反馈也不能当工作执行receipt。模型/路由资格、三调用累计预算、旧snapshot/hash恢复保留。只用于新构建的单一人话来源；不用新工具/新行为改写reader20旧snapshot。候选marker暂定21，集成方需核对其他在途候选后统一，不能覆盖低marker。

事项：不按中文关键词Host硬派发。新输入把“新建/查找/续接”的有序合同放可信首部；明确新建不得复制旧报告。普通旧任务候选改为原生find_tasks按需查询：当前场域+当前触发人相关任务优先，未命中才在group查同场域可见共享元数据。Shared仅goal/status/时间等安全摘要，不暴露prompt/entries/result/私人材料，不得continue/stop/builds_on。DM不扩其他requester；确切引用仍按来源定位和权限。旧source-bound候选/replay保留兼容。

## 所有权与工作流

主代理集成、流式modelregistry/journal/Loop；Task worker独立查找及绑定；Feedback worker独立Host记录、outbox与发送门。各自worktree，完整输入/责任/验收后按里程碑交付。不改共享预发，不捎带其他人的未提交内容。新迁移选择10040及其后无冲突槽，无FK；每个并发索引独立单语句。

## 验收及环境

- Task：明确新事项无默认旧报告候选；真实意图须后续父会话原台词复验，local/scripted model不签真实模型通过。查找顺序、同query跨owner group仅安全元数据、DM隔离、可读不等于可改、quote/current read、源回放/版本门用独立DB验证。
- Stream：实际HTTP SSE受控夹具首反馈Frame在流尚未结束时出现；无额外模型调用；业务工具在finish之前0次；多call断包/缺ID/非法batch/length截断不执行业务；只收公开原生反馈帧、不收reasoning/普通工具参数。
- Delivery：feedback+outbox同事务、稳定key不含lease/文本，重启/unknown不重复。running专用门重查source/权限/quiet/final；pending迟到压制，final独立且不被unknown卡住。原callback不被feedback关闭，通知错误不阻断业务，权威/lease错误遵守原fence。
- 时序：首轮Stop/dispatch/恢复工具不双ACK；严格数字/JSON零feedback；多来源不猜第一个。仅允许一个feedback，源/内容改写冲突。完整工具调用恢复配对和原请求hash不变。
- 独立本地测试库/假provider/HTTP fixture；不碰默认业务库。结束drop回读。实际模型、IM、SLS/LF由接手方按manifest在获准环境验证：原NEW-TASK同场域原话、自然首句→必要工具→最终、直答/JSON、quiet/取消及独立投递核算。

## 有界交付

先交接口/设计，分块实现与必要本地检查；集成后只扩大受影响检查，失败先定位并标历史基线。未证明真实行为不做绿灯。主代理只推独立Aone分支（GitHub写仍按gh api），不触发共享部署。代码、来源、验证日志、canonical EVALS变更/检查和精确剩余验收给父会话，用户已明确授权完成后的交接消息。


## 当前执行表与交接（2026-10-04，唯一入口）

本批代码完成，本地验证通过，交接统一发布与真实验收。实现代码版本 `46cc61872c792162f20881098da21e53c211df51`；独立分支 `feat/employee-intent-stream-feedback`，基线 `eb62d0f06e7c92819c9076fb72541737d95ec980`。文档收口提交不改变下面的运行行为。实现、发布、真实验收分别记录；历史 NEW-TASK FAIL 保留，未因本地绿色检查转为通过。

| 项 | 当前结论 | 证据/代码 | 阻断与下一步 |
| --- | --- | --- | --- |
| 同人同场域明确新建 | 已实现；局部合同通过 | `employee_task_discovery.go::employeeTaskDecisionRules`；`employee_task_discovery_test.go::TestEmployeeTaskDiscoveryIndependentDispatch` | 真实模型未验。原场域原请求人原失败台词复验，不清History；核新Task/Run和Python实际执行 |
| 按需查找及来源绑定 | 已实现；独立PG通过 | `employeetask/discovery.go::Find`；`employee_task_discovery.go::discoveryBinding/discoveryReplay`；Shared控制拒绝测试 | query是有界goal字面子串。多个合理候选澄清；新真实查找路径由接手方验 |
| 同首请求流式公开反馈 | 已实现；实际HTTP SSE夹具及PG集成通过 | `employeeloop/stream.go::feedbackObserver`；`employeeJournalModel.ChatStreamed`；`employee_stream_integration_test.go` | 真实模型/IM未验。只证明完整帧可早入队、业务晚于整批、cache恢复不增加HTTP；不签实际首句早到 |
| 幂等、安静、最终答复 | 已实现；PG/发送替身合同通过 | `employee_first_feedback.go`；`dingtalkresponse/first_feedback.go`；`employee_first_feedback_test.go` | final取消pending；unknown/accepted查询不重发。已外部提交的消息可能迟到，不能承诺撤回 |
| 恢复首physical门 | 已修；审查及两子例通过 | `firstFeedbackEligible`要求`model_attempts==1`；`TestEmployeeFirstFeedbackRecoveryDoesNotCreateOnSecondPhysicalRequest` | 已入账回放保持幂等。无剩余审查阻断 |
| 混版、旧快照、迁移 | 已实现；旧journal/工具表/PG迁移回归通过 | reader21、生产`TaskDiscoveryReady`门；10040/10041、10041 INVALID清理hook | 统一集成方核对其他候选marker；全live就绪再启用新输入。无Daemon/Runtime改动 |
| SPEC/EVALS | 定义及结构检查通过 | 5 SPEC、20 P0、16场景、129用例；新6条稳定风险定义，G06/G20准确引用 | 新6例为`defined`，并未全部接runner或实际执行；定义检查不是产品通过 |
| 共享发布及真实验收 | 未执行，交给「发布和验收」 | 本批交接报告 | 仅发布负责人集成、部署及获准IM验证。本批无Apply/部署/真实消息/Runtime配置写 |
| 本地环境恢复 | 已完成 | 三个独立数据库drop回读0 | 默认业务库、账号、routine、预发配置未改；无后台测试残留 |

详细交付、原场景复验方法和证据索引见[交接报告](employee-intent-stream-handoff.md)。另两个worker切片报告为实现过程记录；当前结论统一看本表。
