# Employee 人工选择与文字答复

本页描述当前源码行为；部署与真实IM验收分别以当波交付记录为准。生产者须等待所有副本具备 `[employee-human:4]`；不扩大旧快照工具表。复用现有 Employee Loop、最多三次真实模型调用的单wake预算、持久scene jobs、工具journal和response outbox，不注入阻塞式沙箱工具、不保持Pi session。

## 询问与索引

前台新增 `a2ui_ask(source_ref,summary,choice)`：Host从当前原消息冻结workspace/agent/tenant/scene、平台principal、请求者、source receipt/job/ref；原子保存 `a2ui_interaction`、`employee_human_question` 和card outbox。纯询问不创建Task，工具返回后结束本輪。

新普通Direct任务仅在human producer就绪且没有显式多步计划时采用V2 explicit_goal，并在Run prompt附 `tag-round-result/v1` 结果契约。Pi最终输出完整JSON：summary以及可选choice（intent clarify/suggest、single/multiple/person、question、options、allow_custom/min/max）。只解析已结束Run的完整最终正文，不从stdout、日志、Markdown或嵌套JSON提取控制。choice不能指定事项、场域、人员权限或session；person表示候选选择标识，不是已验证原生人员身份。真正发送前仍由执行器查证身份。

Run的来源与退出事实经原Execution Event验证后，结果通知物化阶段处理：合法clarify保存问题与卡片并给V2 Task开该question ID的必需human_input wait；合法完成/建议按冻结目标显式CompleteGoal，建议不自动派发。畸形或未声明契约的结果不伪造目标完成；契约外旧Task保留既有普通结果。显式多步计划保留原协议，不叠加该自动轮末契约。旧V1成功事项可兼容续Run，但不启用V2轮末澄清协议。

当前投影复用basic ChoicePicker：single/person=mutuallyExclusive，multiple=multipleSelection，checkbox外观，描述为独立Text，允许补充时TextField。同名冻结候选不使用不受候选限制的原生UserPicker。无动态HTML、脚本或原生身份授权来自模型。

## 两个输入面、一个问题提交

原生 `user_card_action_triggered` 的 `sourceTurnId=ask:<uuid>` 精确查问题；原生订阅的sender/agent/org、原提问者operator、版本和展示选项均检查。事件payload的actor/scene/Task不授予权限。非法选项或无权点击不消耗合法机会。

普通文字仍是message job。新快照显式提供当前请求者本scene的pending_human_questions，既有Loop可调用 `accept_human_response` 选择对应question_ref，必须引用当前外层原话；完整原文与附加限制保存。无引用的明确回复可推进，多问歧义先追问，新话题/感谢不自动算作旧题答案；模型可按当前原话明确将不再适用的旧卡失效。取消任务继续走现有source-bound stop_task，不被答题工具吞掉。没有被摄入的群消息不会从历史中制造新授权。

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

## 统一集成边界与验收补充

原消息Job/receipt提供授权来源，typed human_response Job和真实continue_question_work journal提供本次执行证明，两者不能混用。新Run最终结果也必须走这条证明，不能只验新Run创建。steer后继仍以原steer证明优先。

文件真实送达且用户要求不再总结时，抑制额外消息不抑制已核实的V2结果推进：有效结果可完成Goal；suggest不再发额外卡片；clarify保留问题/mandatory wait供后续普通文字补充，不额外发卡。非法或非结构结果仍不宣布Goal完成。

持久quiet与人工问答结合：旧前台卡不恢复参与，不启动新执行或产生新前台回复；前台未提交的新卡在quiet时压制。已授权后台Run的结果/轮末卡保留原通知规则。效果事务与新发送前再次检查；已受理的远端请求沿原回执对账，不冒称撤回。

## 首次询问裁决

human reader就绪的新message snapshot无论是否已有pending question，都冻结人工交互策略。当前人要求先确认选项/接收人或尚缺执行前的必要信息时，由前台a2ui_ask发题后结束本轮；不得把提问包装成dispatch_task或自动follow_up_steps。后者是已授权可自动执行的步骤，不表示人工等待。普通问回不接受为答案，合成素材和全部执行/外发约束不得扩写为查真实资料或文件发送。Pi轮末澄清仍只在确有先做的授权准备工作时使用。旧冻结snapshot/journal不热改。

## 卡片呈现与关闭

Employee专用compact投影按用户确认的模板选择，卡内不拼原消息引用。当前CLI/SDK未暴露quoted A2UI发送参数；原生历史引用回复＋独立卡是两条消息，不能冒称同条引用卡。

| 模型声明/状态 | 模板与交互 |
| --- | --- |
| single/person、allow_custom=false | 06文字按钮单选，点选即答 |
| multiple、allow_custom=false | 08多选列表，一次确认 |
| allow_custom=true | 17选项＋输入框，一次确认合并所选和补充原文 |
| 已回答 | 09/10保留原题干，只显示所选项、右侧勾，无操作控件 |
| 已失效 | 原题干＋“已失效”，无按钮、选择器或输入框 |

模型用选项可选emphasis(primary/secondary/none)声明强调，映射已发布Button primary/default/borderless；不根据label关键词猜确认/取消。模型按风险选择强调，但样式不授予审批或执行权限。冻结候选短说明合成文字，重名部门/角色不能丢。Button child只用已真机验证Text，不能嵌套Row/Column；Row在静态完成态仍可用。基础目录未提供width/radius参数，当前native按钮仍为胶囊；较宽小圆角模板属于尚待客户端能力，不添加猜造字段。普通聊天仍是正式输入。

合法答复与唯一typed job同事务保存原卡关闭意图；独立PG lease更新消费者尽快将原bizId替换为FINISH、仅保留冻结的所选标签/摘要和右侧勾、无可操作控件。原native和普通文字两种路径均覆盖。旧已受理问卡有限回填；skip仍是deferred，不占最终答案。早答/晚send回执等待真实bizId；unknown不发送新卡，更新可重试，明确未发送被suppressed则无需锁不存在的卡。更新前复验当前tenant、principal、scene、endpoint、员工UID和原来源，不因Task已完成而放弃关闭已受理卡。

单聊发卡目标使用Host冻结来源的员工视角senderOpenDingTalkId；群使用场域目录CID。两者不可混用，模型不提供目标参数。

## 暂缓、失效与任务控制

“先不管/先放着”使用 `disable_human_question(reason=deferred)`。Host 保存 `intent=defer`、`question.state=deferred`，原卡显示“本次选择已暂缓，卡片已关闭；后续以聊天为准”且没有交互控件。Task/Run/mandatory wait 保留，不创建答复 wake，不批准或完成工作。Watchdog 仅停止该题的催问，包括已排队提醒的发送前复验；同场域其他问题/等待正常。

暂缓题保留在 `pending_human_questions` 和精确引用索引。原人后续明确文字答复走 `accept_human_response`，恢复该题原来的 wait/Task；旧卡点击不能恢复。暂缓永久关闭本次交互卡，后续进展以聊天为准，不再将这张卡改成另一答复态；未知远端关闭结果的重试只投影同一关闭内容，避免不同版本远端更新乱序。未指定时间时不自行定时重问。必需澄清不能终态 dismiss 留下不可恢复等待：Host 拒绝，要求暂缓、真实答复/amend，或以 source-bound `stop_task` 停止原 Task。结案按 Goal 合同执行，不能由关卡代替。取消询问不等于取消任务。

新增语义使用 `employee-human:4` reader 门禁；3/4 混版暂停新人工生产者，既有 native 提交协议和冻结 snapshot/journal 不热改。

回滚须保留理解 defer 的 reader；持久暂缓问题仍存在时不能直接退回 human3，否则旧版提醒与投影不理解该处置。停止 producer 与排空发送队列不代表这些暂缓问题已经消失。是否恢复或停止原工作须另按明确授权处理，不能通过回滚清等待。

## 模型与工程共同处理整体失效

`disable_human_question(source_ref,question_ref,evidence_quote,reason)`只操作当前请求者、本场域的精确旧问题。reason为deferred/chat_continued/request_changed/cancelled/not_needed；证据必须来自当前可信消息外层原话。工具非终结，关卡后模型可继续回复或处理新事项。明确回答走accept_human_response并正常续接；文字已推进到不同要求、新话题或取消询问时可明确disable旧卡。感谢/闲聊不自动当答案，也不能批量关掉别人/其他场域的问题。

禁用复用scope/question锁和question answered终态，response intent=dismiss只是关闭审计，不创建typed human_response wake/Task，不释放必需等待或停止后台Task。原Task停止仍用stop_task。native点击与dismiss竞争只一个终态；重复disable不覆盖先前答案，旧卡迟点拒绝。a2ui存储status沿用answered，result.outcome=disabled区分显示，持久原卡projection outbox按原bizId更新FINISH；模型不能指定bizId/场域或人员权限。发送尚未完成时先关闭状态，晚回执后补锁卡；未知投递不重发新卡。

新引用绑定生产者须等待全部live副本支持human:4；混版3/4双方暂停新人工生产者，防止旧reader忽略引用约束。旧native submit协议不变；工具snapshot仍冻结，不热改旧journal。

## 原消息引用到问题的索引

每个当前source独立携带冻结的quoted_human_questions。Host同时查询同workspace/agent/tenant/scene/requester的response_action.provider_message_id与a2ui_interaction.message_id，不按题目或时间猜。命中唯一问题后，以员工身份重新读取outer和quoted消息，验证发送者、引用ID、当前CID和员工自身发送事实，才得到exact question_ref。多匹配为ambiguous；卡片占位引用缺消息身份为unresolved；普通引用为not_question，沿用原有文字语义。模型对精确引用的“先不管”先调用失效工具，提交成功后再回复。

Host写前检查冻结绑定，引用A不能操作B；原快照没有绑定时不现场添加新授权。关闭事务重新检查映射与来源权限。没有精确消息ID不创建假绑定，也不重发未知卡。

生产发送服务构造后才安装OnA2UIAccepted，否则成功发卡的真实BizId不会落盘。已记录BizId/MID不能被不同回执覆盖，空BizId拒绝。SDK发卡在原client/原凭据内总计最多3秒、最多6次只读补查真实MID；等待状态有界重查，永久拒绝立即停止。失败不撤销成功创建、不重发。缺CID不丢真实MID，也不制造完整delivered事实；Host仍用冻结CID、定向消息回读的发送人/引用/接收人检查授权。CLI跨进程无法保证异步任务原token，仍保留缺MID的限制。安全日志仅记录transport、request ID、查询分类及身份字段有无，不输出原回执或凭据。真实验收必须证明实际SDK路径和MID。历史已丢失BizId的卡不能由本次修改恢复，原卡更新不能被新卡样式测试替代。
