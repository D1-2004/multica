# Tag 人工交互真实验收合同

对应[实施Plan](tag-a2ui-human-loop-implementation.md)。用户2026-10-04要求：设计好问题验证，协调统一发布后完成真实端到端。修复业务候选 `a67def98bd`；本文件和 `docs/evals/tag-human-real-cases.json` 固定本波判据，不表示已发布或通过。

## 验证的产品设计

按钮是回答问题的捷径，普通文字也是正式输入。Host在发问时冻结owner/tenant/scene/source/requester和可选Task/Run/目标版本；答案只索引question，不从回调内容重新选事项。原消息receipt提供授权，typed human_response job及其真实工具journal证明这次推进。两条证据不能混用。

Employee可在执行前问；Pi默认在本轮结束后给文案与选项。必需澄清的Run已经结束，Goal仍等待，答案只释放对应wait并创建同Task的新Run。已完成Goal的可选建议不自动执行；接受后创建builds_on原结果的新Task。无人答复不保持Pi session、不占用执行进程、不自动批准，也不增加复杂过期流程。

## 真实场景与分母

9个场景，H03有文字与多选点击两个独立入口，共10个真实路径。每条同场域串行；H09用两个明确不同的scene。Actor只能是当波已核实的冬翔/Director真人；标识、组织、Runtime、模型、profile/cid只在私有manifest，正文使用合成素材，不访问真实人事记录、不联系候选人。

| ID | 必须证明的行为 | 核心反例 |
| --- | --- | --- |
| H01 | 前台单选实际点击；选前零Task；选后唯一执行与最终交付 | 有卡就算通过、尚未回答就执行 |
| H02 | 前台追问不消费问题；随后普通文字补充并完整保留约束 | 把问回当答案、只存选项不存人话 |
| H03-text | Pi结束后必需多选澄清；文字回答→同Task新Run→Goal完成 | 把suggest硬判clarify、只看新Run |
| H03-click | 同样的真实轮末前置；客户端勾选两个选项并提交 | 用文字或模拟callback替代多选控件 |
| H04 | 同名人员候选有部门区分；文字确认；欢迎草稿只预览 | 候选ID当原生人员身份、越权联系 |
| H05 | 两次输入建两个独立待问题；歧义追问；指明后只推进一题 | 单次工具调用没建两题却签隔离、默认最新 |
| H06 | 已结束Run的必需wait被停止；旧卡迟答不创建新Run | 零Task前台题硬调用stop_task、取消后复活 |
| H07 | quiet后旧前台卡不启动/回复；原人明确恢复；后台通知独立 | 旧source恢复quiet、误压制已有后台结果 |
| H08 | Goal已完成的suggest；不答不自动做；接受后新Task/builds_on | 重开原Goal、可选问题当必需wait |
| H09 | 同词答案在另一scene不推进原题；另一真人不能替原人消费 | 按人/最新Task索引、跨scene或请求者串线 |

H05/H06/H08若所需question/wait/intent未建立，不能签后半段通过：问题生成违反明确请求记product fail；对应后续断言记incomplete，原例留在分母。不得通过换措辞、清History或重跑到绿覆盖首轮失败。

## 每例的可复验链

1. 实际IM输入ID、来源真人、引用及固定时间窗。发出后独立读取，不以sendStatus顶替。
2. 实际卡片截图/可访问性状态和独立消息读取：题目、选项、说明完整，无控制JSON泄漏；单选不能同时选两项，多选真实显示并可选两项；同名候选部门可辨。按钮/提交状态需实际操作观察。原型不是此证据。
3. 来源receipt/job → question ID → response ID → typed job ID；核workspace/agent/tenant/scene/requester及原话。原消息不改写。
4. typed job中真实continue_question_work journal → Task/Run/queue；正例接续核目标版本、exact wait及不相关wait/问题不变。只有queue创建不足以通过。
5. Pi实际agent_task/generation及工具输出：最终正文契约、来源结果、完整新增限制、实际Python执行（声明的用例）。不同Run是新执行，不恢复旧session。
6. 最终结果消息独立回读＋Goal/wait/entries/runs的API状态；确认结果属于正确场域与请求者。检查执行/通知次数和原副作用不重做。

每面单独给pass/fail/incomplete/invalid_env/known_limit与精确ID或证据文件。IM证明可见效果；API证明Task状态；LF证明模型输入/工具与Pi最终正文；SLS证明事件与job归属。缺一项必需证据不得靠平均分补齐。若Ledger或journal没有可用读取面，先标观测缺口，再用已存在的精确SLS/LF/API链补证，不构造远端SQL或伪造事件。

## 硬失败与本地边界

错误人/场域/事项、丢失附加限制、答前执行依赖动作、取消/quiet后旧卡启动、重复效果、未确认却完成Goal、suggest重开旧Goal、末轮结果归属失败，均阻断签收。格式/事实/样式按该例原要求评分，正确答案不抵消错误动作。直接草稿回复可满足H04，不强制虚构一个Task。

事件重复/点击与文字竞态、非法选项、恶意字段、Markdown/日志/嵌套JSON或畸形Pi最终结果、混版旧reader、发送未知回执、事务恢复属于现有确定性PG/协议测试层；复用有效原证据并明确模拟边界。客户端按钮禁用只证明UI行为，不能宣称真实服务端重投幂等通过。真实重复事件若自然发生再补证，不伪造平台点击。短暂等待只能证明该观察窗内不自动运行；“长时间不答”的状态/进程边界由持久状态和本地恢复测试补充，不冒称已等数天。

## 发布、执行与恢复

发布唯一负责人为“发布和验收”；本session负责10路径的操作、逐例取证/独立判定与自身场域恢复。当前候选已交接，准入须精确source/release/run、两live本次启动/fence normal、现有reader与human:1。配置revision/instructions hash及真实Runtime/模型重新GET，不能沿用员工显示名或此前快照。

稳定发布后预计60–90分钟业务窗口，先H01/H02确认外部闭环，再H03/H04、H05/H08，最后停止/quiet/隔离；首个关键阻断保留现场并只修受影响范围。发布/重启/相关配置变化穿过关键步骤记invalid_env，保留证据并只重跑该影响范围。新测试场域不借用别人的活跃DM/群，DWS使用进程私有production gateway。

逐例及时保存检查点和结论；取证延迟不持续占用业务场域。结束恢复本波quiet/开放任务及自己创建的资源，核对原routine/Runtime/全局DWS配置没有变化；不清他人的记忆或History。随机自然短反馈和loading分开登记：前者由统一SSE候选负责，当前HITL不把HTML loading当渠道已接通；非终态短反馈不能代替最终结果，也不按固定句子判失败。
