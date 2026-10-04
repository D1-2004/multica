# Employee 人工选择与文字答复

本能力在当前分支接入，尚未发布或真实IM验收。生产者须等待所有副本具备 `[employee-human:1]`；不扩大旧快照工具表。复用现有 Employee Loop、最多三次真实模型调用的单wake预算、持久scene jobs、工具journal和response outbox，不注入阻塞式沙箱工具、不保持Pi session。

## 询问与索引

前台新增 `a2ui_ask(source_ref,summary,choice)`：Host从当前原消息冻结workspace/agent/tenant/scene、平台principal、请求者、source receipt/job/ref；原子保存 `a2ui_interaction`、`employee_human_question` 和card outbox。纯询问不创建Task，工具返回后结束本輪。

新普通Direct任务仅在human producer就绪且没有显式多步计划时采用V2 explicit_goal，并在Run prompt附 `tag-round-result/v1` 结果契约。Pi最终输出完整JSON：summary以及可选choice（intent clarify/suggest、single/multiple/person、question、options、allow_custom/min/max）。只解析已结束Run的完整最终正文，不从stdout、日志、Markdown或嵌套JSON提取控制。choice不能指定事项、场域、人员权限或session；person表示候选选择标识，不是已验证原生人员身份。真正发送前仍由执行器查证身份。

Run的来源与退出事实经原Execution Event验证后，结果通知物化阶段处理：合法clarify保存问题与卡片并给V2 Task开该question ID的必需human_input wait；合法完成/建议按冻结目标显式CompleteGoal，建议不自动派发。畸形或未声明契约的结果不伪造目标完成；契约外旧Task保留既有普通结果。显式多步计划保留原协议，不叠加该自动轮末契约。旧V1成功事项可兼容续Run，但不启用V2轮末澄清协议。

当前投影复用basic ChoicePicker：single/person=mutuallyExclusive，multiple=multipleSelection，checkbox外观，描述为独立Text，允许补充时TextField。同名冻结候选不使用不受候选限制的原生UserPicker。无动态HTML、脚本或原生身份授权来自模型。

## 两个输入面、一个问题提交

原生 `user_card_action_triggered` 的 `sourceTurnId=ask:<uuid>` 精确查问题；原生订阅的sender/agent/org、原提问者operator、版本和展示选项均检查。事件payload的actor/scene/Task不授予权限。非法选项或无权点击不消耗合法机会。

普通文字仍是message job。新快照显式提供当前请求者本scene的pending_human_questions，既有Loop可调用 `accept_human_response` 选择对应question_ref，必须引用当前外层原话；完整原文与附加限制保存。无引用的明确回复可推进，多问歧义先追问，新话题/感谢不自动关闭旧题。取消任务继续走现有source-bound stop_task，不被答题工具吞掉。没有被摄入的群消息不会从历史中制造新授权。

答案CAS与类型化 `human_response` receipt/consumption/job在同一事务，重投返回原工作；同事件不同内容冲突，按钮/文字首个合法答案胜出。skip只让问题deferred，不满足必需等待、不授权下一轮。后续明确文字纠正已受理工作继续使用现有任务控制，不让旧卡覆盖。

human_response item仅question_ref/response_ref/version、message_count=0，不伪造IM、不要求已存在Task。同scene的人类消息和响应先于task wake。消费从PG重新核问题、答复、原消息受理/endpoint/invocation权限、tenant/scene、目标版本、停止与后继Run；原消息不改写，实际答复作为独立数据与工作包correction。

## 推进与效果证明

消费复用同一 `employeeloop.New`：前台问句补齐后按原请求创建V2 Task；V2轮末澄清仅释放该问题的human_input wait，其他mandatory wait仍阻止执行，启动同Task新的Run。明确amend先写原请求者的definition correction与新goal revision；已完成V2的suggest另建Task并builds_on旧结果。旧V1的amend安全拒绝，普通成功续接保留原合同。

执行job必须是真实human_response job，queue冻结employee_human_response_id、原source_ref及真实effect job ID。execution proof version 6按这个job的真实tool journal核Run/queue/来源，notice与继承交付约束也走typed reader，不能冒用原message job的工具journal。回到原场域的回复使用scene notice，不复用旧Router callback。

锁序为workspace→scene→question/Task；Run notice先读可信scope再锁scene和Task/Run。发送前重验问题、权限及当前Task/Run/目标，停止或过时问题抑制。卡片发送用原response_action；provider ACK只记录已受理，真实message/conversation才作已送达。ACK-only与unknown不自动重发，早到答案状态不被晚到发送回执覆盖。

## 交付边界

随机简短收到反馈与引用loading保留设计，不增加分类或润色模型；当前分支不把HTML spinner宣称为已接通的钉钉A2UI loading。真正引用+A2UI挂载仍需渠道验收。审批工具的实际权限/审批动作首版不扩大；使用本次single/multiple/person确认。

没有答复就保留问题和wait，释放执行进程；不自动批准、不持续占用沙箱、不引入复杂过期或升级规则。工作区删除显式清理问题/答复，无数据库FK或级联。

滚动先reader后producer；回滚前先停止新生产者并排空typed jobs与card outbox。旧response worker不了解card字段，不能在有pending卡片时直接降回旧二进制。

源码：`internal/humanquestion`、`internal/employeeentry/human_response.go`、`handler/employee_human_*`、`service/a2ui/stage.go`、`service/dingtalkresponse/a2ui_question.go`。GawkBot借鉴和不照搬边界见本轮Plan与固定源码对比。验收状态分本地模拟、真实IM/模型/Pi，详见 [实施Plan](plans/2026-10-04/tag-a2ui-human-loop-implementation.md)。
