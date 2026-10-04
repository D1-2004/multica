# EmployeeLoop 当前实现合同

本合同描述已交付的基础能力；实施路线和未完成项见
[开发路线](plans/2026-10-02/employee-loop-task-service-delivery.md)。

## 场域入口与归属

复用 AgentScene 与 EventRouter 的统一场域 ID。EventRouter 只管理标准事件、场域解析和回执；新增 storage-only receipt hook 在首次回执的同一事务中保存业务 owner 和 Employee job，不调用模型或外部服务。历史回执没有消费记录时保留旧路径，不能在切换模式后自动升级为 Employee 工作。

`coordination_mode` 选择新工作的默认 Loop，`inbound_coordinator` 是独立启用开关。已经受理的事件按持久 owner 重放；已有任务结构锚点优先查询原 owner。当前 Employee continuation/control 尚未接通的事件保存 held 原因，不转给旧 Loop 再派一次。普通的 route=legacy/unified 和业务 Loop owner 是两层不同选择。

首批消费 digital_employee 的 channel/message.created、DWS 回报和合法场域。缺失场域、未支持来源、自己发出的消息及 reaction 有明确 held 原因；不伪造 cid，也不回退到另一租户。逐句身份以自身 sender 字段为准，只有单消息才允许从已验证的外层 sender 补齐；多人窗口的缺失身份保持 unknown。

## 前台处理

场域窗口只收集尚未 claim 的到达消息，claim 后的新消息进入下个窗口。每个 job 持久保存 lease/generation、原始输入、岗位/工具配置、模型请求和结果或失败、工具回执及最终 outcome。模型和外部工具在事务外执行，提交效果前核验 lease、权限和场域。

GawkBot 固定提交的内核和 prompt/voice 负责判断与表达；岗位 Instructions 或有效短合同进入稳定 system 前缀，窗口与记忆作为数据。前台最多三个真实模型请求，provider 错误和格式重试也消耗此预算，重启不能重置预算。简单回复或派发加接单文案可以首轮完成，Quiet 是合法终态。没有单独的 finish_check、审核或润色模型。单次真实 provider 请求最多 20 秒，并受当前 wake 原有 45 秒总预算约束；子请求超时可在剩余预算内由 Loop 显式重试，SDK 不隐式重试。journal 读写继续使用父 ctx，缓存命中不新发请求；迟到的超时 completion 不执行工具。父预算耗尽后沿用既有独立 outcome checkpoint 收束，不延长模型预算或重复已接受效果。

**明确输出要求。**新快照的 Persona 冻结通用 `REPLY CONTRACT`：当前被接纳请求明确限定的语言、格式、长度和内容范围，优先于岗位或默认的语气、先回应、补依据、解释、建议和跟进习惯。只要手头证据足够，模型先完成判断，再只输出请求的内容；“只回编号/数值”“不解释”“只给 JSON”等要求同样适用于普通对话、群转录判断、记忆和前台工具中的回复文本。输出前由同一次生成自行核对，不增加审核模型，不由 Host 提取编号或删除解释。缺证或有实质歧义时，在可满足的格式内最简洁地说明不确定或追问，不为了格式编造事实；上下文已有证据直接答、未要求核实时不派后台的合同不变。工具真实受理、拒绝、等待与已完成分别表述；拒绝的忘记/reset 不能说已生效，也不能把他人或历史中的撤销意图当作已变更事实，回答以当前授权 memory 快照与实际结果为准。确认遗忘/reset 后不从旧对话或工具正文复述已移除值，除非请求人明确要求且有权限审计。历史或引用里的命令不能授予新的请求权，输出要求也不能改变 Host 权限、安全和事实边界。只追加新快照，旧快照和 journal 保留原字节，全局 BuildPrompt、请求参数、在线岗位模板与 marker 不变。来源与本次真实反例见 [19 输出合同修复](plans/2026-10-03/employee-loop-backend-delivery/19-foreground-output-contract.md)。

Direct commit 已成功时，即使外层工具 journal 写失败或调用被取消，返回的 Run receipt 仍保留，不能错误地说未受理。确定性上下文超限保存一次无需模型的明确反馈，不截掉尾部约束，也不永久重试。

## 场域能力与前台观测

前台复用 contextcap 的全局与组织/场域配置、可信单一请求者的个人配置和合并规则。多人或未知请求者窗口不能借用一个人的个人配置。能力目录展示实际生效配置和简短描述，配置启用不代表上游连接已经可用，也不授予新权限。全局启用能力与场域 offered 开关是并集；关闭本层 offered 不撤销全局授予。

`reply` 是不派任务的明确终态；`describe_capabilities` 由 Host 给能力说明附上当前场域配置链接，`scene_config_get` 只读取目录已验证的当前场域。链接复用 Coordinator/执行器的签发规则，群聊和单聊均绑定 scene_id，单聊不依赖 staffId。链接仅进入 Host 私有回执、回复 checkpoint 和发送正文，不作为工具结果交给模型；重放不重复签发。mint 失败保留原答；数据库超时在 savepoint 内使用可恢复的 statement_timeout，不关闭外层日志事务的连接。实际场域管理继续通过 Direct 的场域 MCP 完成。

普通能力介绍和仅索取配置入口优先直接使用已提供的能力目录，在首轮调用 `describe_capabilities`；不为了“更准确”额外读取配置。只有用户明确询问开关、提示词原文或已有例行任务等配置细节，且当前上下文没有答案时，才使用 `scene_config_get`。普通介绍通常一到三句，用同事之间的自然表达说明能协助完成什么，不照抄工具名、Direct 或配置字段。明确配置查询则保留用户要求的准确技能名、开关状态与提示词原文，按所需细节完整回答；“详细配置加链接”同样适用，不受普通介绍的一到三句限制。DWS、技能、连接器和 MCP 属于后台执行能力，不能称为前台可直接调用。

**前台与后台的边界。**前台只做三件事：回复、读当前窗口/记忆/任务与本场域配置（`scene_config_get`）、给配置链接（`describe_capabilities`）。当前请求是在询问或解释已有结果，且当前窗口、近期对话、记忆、任务简报与报告，或请求人自己给出的事实、日志、数字和观察已经包含回答所需的证据时，直接回答，不派发：判断和解释手头证据是前台自己的事，不是查询；证据不足以下结论时，如实说明它能证明什么、不能确认什么，并提出可以去后台核实，只有请求人要求核实时才派发（DS-01：「发送侧 200，对方收到了吗」曾被派成 20 次沙箱调用的后台核查）。明确要求基于完成结果制作新的独立交付物（如“基于刚才统计写一段三到五句的复盘”“合并两次统计做对比表”）属于新工作：经 `dispatch_task` 创建新 Task，并以 `builds_on` 引用本轮确切相关的上游候选，由 Host 将报告带入工作包。篇幅短、已有足够材料、“不要重新统计”只约束产出的范围和执行方式，不把新交付物变成对已有结果的解释；不在前台直接完成新产出，也不为此继续旧统计 Task。仅问数字含义、要求解释旧报告或明确没有新产出时仍直接回复；修改或重做原交付物则先读原任务再 `continue_task`。还需要上下文之外的数据或动作时，一律经 `dispatch_task` 交后台：DWS 查询（通讯录、主管、组织、日程、文档、当前窗口和近期对话之外的消息）、技能、连接器、MCP、脚本、文件，以及场域自管理——例行任务/定时任务的新建、修改、暂停、恢复、删除、立即运行，场域提示词，开关已公开的技能与连接器，增删远程 MCP。后台 Direct 任务挂着本场域的 `config-qwen-tag-scene` MCP（`scene_routine_create` 等）完成这些变更；例行任务每次运行都在后台执行，平台在场域里发开始和结束消息。前台没有某个工具不等于后台做不到：遇到需要执行的请求就派发，不回答“做不到/没有记录/没有工具”，只有任务结果才能证明做不到；也不把人支到管理员或平台设置。只有缺“做什么/什么时候”时才先简短追问。账号连接和连接器授权在配置页完成，前台给链接。`scene_config_get` 结果不再带 `read_only`（执行器里它表示例行任务运行只读，前台曾把它读成“本场域不可改”），改为 `how_to_change` 指向 `dispatch_task`。后台技能把请求人自己的明确、完整请求视为确认，直接执行；缺要素、多项无关变更、新增或改址远程 MCP 时才先复述确认（对齐 GawkBot：人的请求本身就是授权，高风险变更由工具侧把关）。

**引用回复与配置链接。**钉钉群里用引用回复下达新任务是常态：`dispatch_task` 接受带引用的源消息，外层正文就是请求，requester 仍是外层发言人，被引用消息随源证据进入工作包，只作材料、不授权（与 `steer_task`、记忆纠正对引用的处理一致）；reaction 与结构 continuation 仍不受理。历史中的配置链接改写为 `[earlier configuration link omitted]`，不再保留可照抄的 Markdown 链接形状；若模型仍在普通回复里抄出占位链接，Host 去掉死链，并按 `describe_capabilities` 同一路径签发一个新链接，签发结果记入工具日志（`host:copied-config-link`），重放不重复签发。此合同不新增模型调用、事后润色或更改三轮硬上限；真实模型首轮选择、回复长度和时延仍须通过 canary 验证。

Langfuse 前台 trace 名为 `employee_loop`，持久 job ID 用作 trace ID；后台仍为 `agent_task`，通过 job、EmployeeTask、Run 和 queue ID 关联。Generation 仅包围实际 provider I/O，记录有效模型、真实消息与完整工具 schema、响应、usage 和耗时；保存结果/失败的 journal 重放不新增 generation，重新认领后实际重发使用不同 span。工具 span 只记录实际执行，缓存重放不覆盖旧 timing；批次预检拒绝单独记录事件，不增加模型预算。

追踪输出递归脱敏凭据和配置 bearer 链接。Generation 沿用既有 64 KiB 内容上限，明确 bytes/truncated；截断数据不能作为完整提示词证据。根 trace 的 accepted/enqueued 只表示业务提交或通知入队，不表示钉钉送达。导出关闭或失败不能改变业务裁决、任务效果或模型调用次数。

## Task 与执行

EmployeeTask、Run 与 agent_task_queue ID 独立。Task 保留定义和追加记录；Direct 使用已有队列、执行器、消息、usage 和轨迹，不创建 Issue 或永久 Autopilot。Work Object Compiler 使用真实 source、Host scope/principal、完整约束和实际 ContextUsed 组装执行输入，不新增模型请求或通用配置快照。

Direct 的模型可见执行面只装配岗位原文、工作区与组织/场域/个人上下文、实际技能以及简短执行和结果约束。不会自动加入通用 Multica Runtime 命令目录、Issue/Chat/Autopilot 工作流、Mika 系统层或 OKR 标签指令。显式绑定的工作区与场域技能保留原名和内容，包括用户自定义的 `multica-` 名称；claim 内联技能和 bundle 补拉共用执行面策略，DWS 身份规则与 `config-qwen-tag-scene` 仍保留。

Direct 不自动挂载通用 `multica` MCP；场域配置 MCP、内部连接器、Agent 与 Runner 自定义 MCP 继续按原权限装配。Host token、claim finalize、取消、租约、轨迹与用量通路不变。文件通过已装配的钉钉文件工具交付，只有验证过的回执才可称为已送达，不能把沙箱本地路径当成用户可打开的文件。

Direct claim 在原工作包末尾追加明确的 Output 合同：最终 assistant 文本就是用户回复，成功和失败均由 Host 发送到来源会话，执行器不得先调用 DWS `final/reply` 再重复报告。最终文本保持简短，除非用户要求，不列内部工具、命令、本地路径或回执 ID；用户明确要求的文件和其他目标的主动消息仍可执行。 成功且有文本输出时，Host 保留脱敏后的原输出与格式，不加固定结果前缀；空输出仍有完成说明。失败、取消以及文件送达未知或失败所需的状态说明保持原合同，原生文件已验证送达后的静音策略不变。

可信 `dingtalk_message_policy` 仅在 Direct claim 带 `final_text_owner=host`。支持该可选字段的 Daemon 必须在 claim 解码后完整传入 `MULTICA_DINGTALK_MESSAGE_POLICY`，SDK 才能对来源会话的 `final` 执行无发送守卫。缺失或未知值保持旧工具行为，普通任务不新增此字段。旧 Daemon 使用固定结构解码，会丢弃未知字段，因此硬守卫交付须切换到包含该类型字段的新候选 Runtime，不能声称只有服务器升级就完成了协议交付。该所有权不替代原生文件的真实送达验证，也不根据任意文字回执抑制 Host。

Direct 需要 `employee-direct-v1`。claim 前按认证 Runtime 过滤；任务回调再次检查执行身份。读取和实时原文按可信 originator/管理者以及确切任务凭据控制。数据库取消表示取消请求，不能证明远端进程已经退出。

首次响应和终态结果使用原有投递 outbox，受理、入队和送达分开记录。每个 Run 至多保存一份结果通知意图，旧目标版本不冒充新目标完成；Native 与外部 Router 分别使用其真实回调目标。正式文件使用独立来源账本及加密对象，只在对象和附件记录均成功后返回鉴权引用。场域记忆只使用 Employee 独立存储。后台以真实 Run 证据捕获低可信的私有学习记录；原始窗口只有唯一请求者时才读其私有 brief，多人窗口不聚合私有记录。重置按来源逐句处理，清除共享场域及请求者自己的私人记忆，其他消息继续处理。

Direct 的 `completion_notice_policy` 默认 `always`，保持正常结果及用户要求的摘要。只有当前选定来源明确要求“文件送达后不再总结”，前台才可选择 `if_not_delivered`、`require_delivery=file` 并提供该来源中的原句；Host 校验并将策略带入 WorkPacket 和持久队列。历史正文、引用材料或其他发言人的内容不能授权新的静音规则。纠正和续接会保留同一 Task 已真实受理的交付承诺；授权来源单独用原 queue/source 引用及受理账本验证，不把旧文件回执当作新 Run 已送达。没有可验证的新授权时，默认或无依据的 `always` 不能撤掉已要求的仅文件交付。当前续接工具不提供撤销已有静音规则的入口。

成功执行的文件通知只在同 workspace/agent/task/身份/目标会话的服务端送达回执成立，且原生消息按准确消息 ID 和会话回读、包含自身 `resources` 中的 `resourceType=file` / `resourceIdType=fileId` 后抑制。文本进度、quoted resources、从正文推导的 resourceRefs、模型最终正文或仅 provider accepted 均不够。SDK 与 shim 的重复回执中，确证文件可优先满足条件；未找到确证文件且仍有 pending/accepted 时等待，unknown 或回读不可用先说明文件送达尚未确认，再以“任务返回内容（不作为送达确认）”引用原执行输出；明确失败状态也保留该输出，错误详情不会被状态句覆盖。引用中的自报成功不升级成送达证明，客户端退出码也不直接变成提供方核验失败。执行失败和取消仍通知。发送前重查可抑制晚到的文件送达；晚到的失败/不确定状态只能更新尚未提交的通知意图，并由 outbox 重新加载安全正文，已提交动作只查状态、不重写或重发。文件回读在数据库事务外完成，再次校验来源、权限和回执后才保存决定；不新增模型调用。


通知行新建事务提交后记录 `employee_run_notice_recorded`，包含 `state`（`enqueued` / `suppressed`）、`reason`、执行结果状态及 workspace/agent/scene/job/task/run/queue/action ID。这里的 `task_id` 是 EmployeeTask，`queue_task_id` 是实际执行任务。晚到文件导致已入队通知真正转为抑制时记录 `employee_run_notice_state_changed`；重放和回滚不重复记录成功事件。事件不含正文、请求原句、产物链接或凭据，`enqueued` 仅证明通知意图提交，不等于钉钉送达。


## 纠正在途任务（steer）

`steer_task` 是 Task Service steer 的前台入口（合同见 [task-steer.md](task-steer.md#task-service-steer)）。它只纠正同一场域内、同一请求者自己的 Direct 任务。自然对话目标使用当前 wake 的 `task_ref`（如 `t1`），Host 按冻结的 source/requester 候选映射解析真实 Task ID，并重新校验来源和任务归属；两种目标字段不能同时提供。旧 `task_id` UUID 与省略目标的唯一候选路径保留；旧冻结调用中的 `task_id=t1` 也只能通过同一 source 绑定解析，不能把短引用当 UUID 或跨消息复用。有多个候选时需明确目标或追问，不新建 Task。被人工停止或失败的任务不作为隐式目标，纠正不会重启被人工停止的任务。

完整纠正在任何取消或合并写入前读取，最多一百条、总六十四 KiB；读取失败或加上本次后超限就回滚，不静默丢弃旧约束。执行中的 Run 被取消并挂上退出门闩，进程确认退出后续跑才能被认领；续跑带着全部纠正和原工作包，在同一 runtime 上接回原 provider session 与 workdir。尚未认领的 Run 直接吸收纠正。纠正只带来自己的身份令牌，续跑的结果仍作为原请求的回复送达；被替换的旧 Run 不发取消通知。前台调用预算不变，一次模型调用即可完成纠正与接单回复。

带结构锚点（continuation、task_finished 任务引用或 `control.targetExternalTaskID`）进入 Employee 的事件仍保持 `employee_continuation_not_ready` held；本工具处理没有这类锚点、由模型判断为纠正的普通消息（包括普通引用回复）。

## Execution Event 确定性消费

可验证原场域来源的 Employee Direct Run 终态，由现有周期恢复入口补录 `employee.execution` / `execution.terminal` 事件。幂等键是 Run ID；payload 只含原 task/run/queue、goal revision、result_ref、scene、job 和原 receipt 引用，occurred_at 使用 Run 的数据库完成时间。当前 Task revision 只在消费时判定，不进入事实指纹。结果正文保留在原记录中，不重复导出。

Host 核对原 receipt → consumption → job、冻结 Direct 输入、`run_started` 账本来源和已提交 `dispatch_task` tool journal 的来源及三个结果 ID。provider route 与 Loop owner 独立：具有原目录 scene、空 reason 及完整来源证明的 `legacy/legacy` 和 `unified/ready` receipt 均可承载 Employee 消费，后续事实保留原 route；unmapped、主体或来源错配不能据此通过。当前处理模式或成员资格变化不替换原 principal/owner，不授权新工作。租户围栏复用 `fencedScene/agentTenantOrg`，认可身份组织和已为该 Agent 创建的 tenant；原 DWS 身份缺失仍 held，不借用 robot 的无身份回退。正常事实和旧目标事实写 `completed`，已确认的场域缺失或 tenant 不再匹配写 `held`；临时数据库错误和取消返回可重试错误，回滚不保存 held。两种消费均 `job_id=NULL`，不进入消息窗口或模型。

缺少可信来源的历史记录仅在同 queue/run 终态上 CAS 追加 `employee_execution_event_skip`，保留 `employee_direct_input` 和其他 context。`version=1` 表示兼容存储格式，`proof_version=4` 表示当前来源校验版本（含成功续接和 steer 输入边界证明），另有 run_id 和固定 reason；此标记不证明事件消费或消息送达。新扫描会重评缺少 proof_version 的旧误判，只将同 Run 的当前或更高 proof_version 作为最终 skip，不降级较新证明。旧副本仍识别 version=1，故不会覆盖新证明而形成滚动降级循环；该事实增量本身不提升 IM marker。合法旧 Run 通过重评后，由原子提交的新事实 receipt 阻止重复消费，历史旧 skip 不再控制结果。原受理或实际纠正 job 尚未完成时等待恢复，暂时性数据库错误不记永久 skip。

事实及消费同事务提交。提交后 SLS 记录 `employee_execution_event_recorded` 的状态、原因和关联 ID；已受理事实在原 `employee_loop` job trace 中记录零时长 Event，并复用 Langfuse index 关联 Run、queue、新旧 receipt。重投不重复记录成功，回滚不导出成功；观测导出仍是尽力而为，PostgreSQL 记录是事实依据。没有 generation、token usage、新模型 job 或重复通知，结果通知及文件静音继续由既有 notice 路径决定。

此增量沿用已支持的事件类别和消费状态，不新增 schema、前台工具或 Daemon 协议。旧 Worker 只领取实际 job，因此该事实增量本身不提升 marker。Cron/Webhook 与条件后续工作的模型 wake 尚未由这个事实记录增量启用。

## 近期对话临时上下文

新快照冻结 `Config.HistoryPresentation=conversation_turns_v1`，复用 SessionEntry 与既有消息转换，把 Host 已验证的历史原话、本人已送达回复按原顺序分别呈现为 user/assistant 轮次。assistant 只含文本，不携带历史 tool calls；短元数据头保留截止、截断和撤销省略信息，每轮附最少时间/发言人/消息标识。当前窗口位于最后，历史请求不成为新的执行授权。完整结构化 `RecentConversation` 仍留在快照中供审计，不增加总结模型、Task 或长期记忆写入。

该呈现版本由 marker 10 保护。版本为空的已受理快照继续生成原单条 user JSON 历史块及原消息顺序，缓存 request 不重写；未知版本、未知历史角色、损坏 JSON 或越界数据在模型调用前拒绝，显式 history unavailable 仍作为不可用数据呈现。原快照和新渲染均保持 20 条/16 KiB 边界，模型请求总预算仍为三次。未来若改变已冻结版本的字节呈现，应另设版本，不能热改旧 journal 的渲染规则。

新 wake 的 Persona 冻结时序解释约束：同一对象的最新明确陈述或重设覆盖旧取值与旧更正，不能把旧更正再应用到更新的重设之上；单项修改保留其他当前事实。指代按最近相关交换中的对象顺序解释，旧 assistant 回复不覆盖更新的用户陈述，历史请求不当作新的执行命令。这只是新快照的提示约束，不改历史记录、全局 BuildPrompt、低延迟请求参数、长期记忆或已冻结快照；真实模型能否正确处理仍须单独验收。

**Persona 档案、语言与 Host 事实块（M3）。**新快照（chat wake 与 task wake）在 Persona 末尾冻结：`SELF PROFILE`（账号显示名、负责人显示名、本场域租户组织名，以及 M6 `employeeDirectoryFacts` 按本 wake 执行身份读出的本人通讯录直属主管/部门/职位；「通讯录未登记」如实说，「未读取」可派发查询；负责人与直属主管是两件事）、`EXECUTION CLAIMS`（本 wake 的工具结果或 Host 回执没有证明时，不说代码跑过、程序算过、产出了文件或用了工具；直接回答按自己的推算表述，MEM-01 反例）、`TIME`（Host 时间一律 Asia/Shanghai，带 +08:00 偏移，不对人说 UTC）、`MEMORY REPLIES` 补充（置顶偏好默认生效；说法不一要指出分歧；只有已验证条目算事实；群里不提供任何人的私人记忆）、chat wake 的 `AMBIGUITY`（有多种合理读法时先问一句或分别回答，不先下结论再自相矛盾，DS-03）、仅当快照真的带了群旁听转录（coverage 含 `group_transcript=loaded`）时的 `GROUP TRANSCRIPT`，最后是逐 wake 的 `LANGUAGE`：按请求人当前消息的语言回复，无明确信号时用简体中文，请求人明确要求的语言优先；Host 对窗口外层正文做确定性观察（含汉字为中文，≥3 个拉丁词且无汉字为英文），英文系统提示和 Host 数据不决定回复语言（DS-07）。`Input.Memory` 末尾追加 ≤1 KiB 的 `[S]` 场域状态块：本 wake 的 Host 时间；本场域 24 小时内最多 3 条失败或被 hold 的前台轮次（task wake 的 hold 是正常停止，不计），原因只取 `model_timeout / model_budget / tool_rejected / window_too_large / provider_error / held`；会话场域最多 3 个例行任务的名称、cron 与时区、启停、下次运行和上次运行（成功/失败/跳过/运行中/从未运行）。块头注明「被问到或直接相关时才用，不主动提起」。群场域在 `[S]` 之后追加 M6 的 `GROUP MEMBERS` 数据块（当前发言人优先，同名歧义照 M6 标注；显示名由用户控制，所以放数据区，不进系统提示）。trace 记 M6 `Metadata()` 及 `persona_language`、`persona_transcript_rules`、`host_facts_bytes`。失败查询走部分索引 `employee_scene_job_failure_idx`（9892）。这些内容只在构建新快照时读取一次，读取失败写明 unavailable、不让 wake 重试；旧快照与 journal 逐字节重放，全局 BuildPrompt 不变，不新增模型调用，不升 marker。近期对话快照的 since/before/observed_at、前台 `scene_config_get` 的例行任务时间也改为 +08:00。task wake 的 Task 快照对模型只给 `task_ref: t1`、authority/evidence 的类别和去掉 UUID 的 actor/source 引用，原始 Task/Run/plan/queue ID 只留在 Host 私有字段。

新 wake 只读同 workspace、agent、tenant、scene 和受理 principal 的近期用户原话，以及有 provider 消息 ID 和匹配会话的已送达 Host 回复。截止时间固定为原 job 受理时间，上限 24 小时、20 条、16 KiB；当前窗口排除，截断显式标记。不读取未确认发送的模型结果，不增加总结 LLM，不写长期记忆。callback 回复必须同时匹配原 URL 和确切同步 RequestID；其他 Run 回复依赖独立的 notice 来源记录，不能仅凭复用 URL 纳入。

已有私有 memory 被 supersede 或 forget 后，新历史投影按同 scope/requester 的 `employee-message:<receipt_id>` 与 `evidence_id` 精确撤销对应源消息，并保守隐藏该原 job 的关联整条回复（含多 receipt 派生 notice、确切同步 callback 与 Run notice）。同窗其他用户消息和没有写入 memory 的普通临时纠正仍按时间保留；审计原文不删除，且输出 `withdrawn_memory_evidence_omitted`，不冒充完整对话。不扫描 insight 或按值全场域擦除；后续没有结构化来源引用的独立复述无法据此关联，不宣称全局擦除。

M5 的后续问答回复同样按精确来源撤销：Host 以同场域 tombstone 记录 ID 关联冻结的 `memory_manifest`、实际 memory tool 结果，再关联实际 delivered reply 的 action/provider message ID；沿新投影范围内旧快照的历史 ID 与 `transcript_refs` 有界传播，隐藏借旧 assistant 文本继续复述的整条回复。群转录对这些撤销回复的引用正文也省略，不能借 provider readback 重新注入。其他成员同值但不同来源的消息不删，原件与已冻结 job 不改，当前授权审计由独立权限路径读取。没有结构化引用的独立复述仍不按值推断；超限或失败显式 unavailable，部分集合不能作为完整撤销结果。来源与原反例见 [20 回复来源过滤](plans/2026-10-03/employee-loop-backend-delivery/20-withdrawn-reply-provenance.md)。

当前投影窗口只限制候选回复，不能截断其来源证明。候选 B 引用窗外 A 时，按冻结的精确 action/message ID 向外读取祖先；每个祖先必须在同 workspace/agent/tenant/scene、同目标会话，有实际 delivered 事实及可核对的源 job。不得扩大日期全扫或按内容猜源；祖先集合、层数有界，缺失、外场域、未送达、关联冲突、未知来源或超限时整个相关历史返回 unavailable。原件及已冻结 job 不改。该跨窗闭合要求替代 Plan20 曾声明的跨窗局限，验证见 [21 跨窗来源闭合](plans/2026-10-03/employee-loop-backend-delivery/21-reply-ancestor-closure.md)。

祖先的非空 input 不能作为来源已知的证明。只认可显式 manifest、显式空 Memory、没有未关联 Memory 明文的合法结构化历史，或当版明确零记录的 memory_stats；旧非空 Memory 或未知字段没有这些证明时 unavailable，不扫描正文猜测记录 ID，也不改变旧 job 恢复的快照字节。

M16 修订失败粒度：未知快照、缺失/冲突/未送达的精确 assistant 祖先只隔离该 assistant 与精确依赖的后继，不使独立真人历史和正常 DWS 转录一起消失。这些 assistant 仍不可回放，记录不含正文的原因/数量；SQL/取消/全图集合或深度超限仍整段 unavailable。参见 [22 局部隔离](plans/2026-10-03/employee-loop-backend-delivery/22-assistant-quarantine.md)。

## 纯停止当前事项

`stop_task` 只处理同一 requester、场域和租户中，当前外层消息明确要求停止的 Direct Task。先 `read_task` 再用本 wake 的 `task_ref/read_ref`，提交时重验权限、来源、精确 Run/queue 与 Task version；普通致谢、进度询问不触发停止。reaction 与结构 continuation 不能停止任何事项，不推断其他场域的目标。

**引用定位事项（`[employee-loop:14]`）。**引用回复只通过 Host 事实定位事项：被引用的 provider 消息 ID 是员工自己发出并有记录的消息（场域 job 的受理回复及其已提交的任务效果、Run 结果通知、watchdog 通知、task wake 通知、执行器发送），或是请求人自己的原请求消息（其已提交效果创建/变更了该 Task）；全部按同 workspace/agent/租户/场域/会话过滤。快照构建时以 Agent 身份回读两条消息：外层发言人必须是冻结的请求人，且确切引用该消息、同一会话；被引用消息须由员工本人（发送锚点）或请求人本人（请求锚点）发出。只保留该请求人自己的 Employee Direct Task，作为 `q1` 式候选写入新快照（binding origin=quote），多个候选由模型澄清。引用回复只能 `continue_task`/`stop_task` 自己的 `q` 候选，`q` 候选也只服务于其引用源；仍须本 wake 的 `read_task` 与外层原句。引用正文、用户输入的 UUID 都不能定位事项；旧快照没有 `q` 候选，保持原拒绝。`steer_task` 的目标选择未改动。

停止沿既有 PG Task/Run/queue 控制路径执行，不创建 Task、后继 Run 或 Issue。typed stop 意图复用 `input` 账本（payload.operation=stop），关闭 Task、取消确切目标和工具回执在同一事务提交；同源重放只引用原目标，变更载荷或失效版本拒绝。已完成/失败 Run 的历史不改写，人工停止后普通 steer/continue 不得复活它。

已认领执行使用既有取消退出屏障；只有可信 daemon 的 process-group ACK、FC 真实 quiescence 或确知从未认领，才能作为退出证据。`completed` 只说明执行报告终态，不证明整个进程组退出；超时、数据库 cancelled 和 claim barrier 也不证明退出。`read_task` 分开呈现工作流状态、queue/execution_state 和 process_exit_confirmed。未退出的已取消前驱也计入 stopping；历史 completed/failed 服务不纳入本次停止范围。

首轮接单仅说明已请求停止；已完成/失败时如实说明旧执行状态。提交后沿原 runtime observer/daemon 通知停止进程，已有五秒 worker 从持久停止意图重试未确认的目标及其取消前驱；Redis 仅作提示。结果通知入队与 BeforeSend 都重验停止账本，旧未提交结果不再发送；已经 provider-accepted/unknown 的动作只查询原提交，不重发，也不声称可撤回。新工具与状态投影由 marker 11 门禁保护，不加模型请求、执行器或新调度器。

## 当前事项与成功续接

新 wake 为每条可信 source 单独提供最多五个同 workspace/agent/tenant/scene/requester 的 Employee Direct Task 候选。`t1` 等引用只在该源的冻结快照中定位事项，不是权限凭据；候选状态只是快照，询问进度须 `read_task` 实时读取 Task/version、Run 状态和执行报告。多个可能事项应澄清，不能把最近一个自动当作当前事项。普通致谢可首轮简答或 Quiet，Host 不用关键词替模型派发或停止工作。

`continue_task` 只接受当前源明确要求的成功事项续接，引用本 wake 的 `read_task.read_ref`，并在提交时重验 requester、scope、当前权限和版本。同一个 Task 保留目标与 goal_revision，PG 同事务执行 Resume、新 queue、新 Run 及 tool journal；业务失败也通过 savepoint 回滚所有 Task 写入，进程在提交后中断则重放同一回执，不再派发。外部 connector 准备在事务外，提交后复用既有 Runtime 唤醒；Redis 通知和缓存不是唯一事实来源。运行中、失败、取消、目标纠正和真正停止均不由本续接工具开放，不能把取消字段或租约到期当成外部进程已退出。

续接读取同一 Task 版本下的完整 steer 纠正，独立于最近二十条普通账本；最多一百条、总六十四 KiB，超过时明确拒绝续接，不静默丢弃约束。TaskRead 与 continue 工具首次随本次 marker 9 发布，不存在已发布的缺少该纠正快照的 TaskRead 协议。续接工作包保留本次原话与约束、原目标、追加账本和前次 Run 报告；前次报告明确标为执行方返回内容，不是文件送达或新要求已完成的证明。continue 的结果按本次受理 job/source 返回，ExecutionEvent 同时核对该 Run 的 resumed 输入边界、run_started 与已提交 continue_task 回执。steer 后继或未领取 Run 的合并纠正按 Run.input_seq 对应的 steer 账本、当前来源 job/journal、精确 Task/Run/queue ID 与现有 WithCorrections 渲染校验；它不把变化后的 queue context 本身当作新授权。steer 保持原冻结交付锚点，事实事件单独关联实际纠正的 receipt/job；当前纠正 job 尚未提交时等待，不永久静音或跳过。steer 取消的前驱保持原来的静音策略。新渲染版本证明全部有界纠正；无版本标记的已接受历史 Run 仍按当时二十条渲染规则验证，不改历史输入。旧 dispatch 证据链保持原约束。来源 proof_version 为 4，skip 的兼容存储 version 仍为 1，旧 proof 可重评且不覆盖较新 proof。以上新快照和工具由 marker 9 门禁保护；旧快照不补候选或改工具 schema。Task 与 Issue 独立，续接不创建 Issue、不引入另一执行器或额外模型轮。

## Coordinator / EmployeeLoop 共用模型配置

新 Employee wake 从 Coordinator 的全局配置读取有效主模型与降级链，未配置时沿用相同 Diamond 默认值。输入快照只冻结配置 revision、候选 provider/model 引用和计划版本，不保存 URL 或密钥。每次实际请求使用单次 adapter，重新核验当前 provider/model 是否启用并读取当前密钥和地址；密钥轮换不修改冻结选择，禁用或删除候选不能借旧快照重新授权。配置变更只改变之后的新 wake，不把恢复中的候选替换成新链。

模型 journal 在 I/O 前保存当前候选及预算预留，失败后原子保存下一候选；可降级失败沿冻结链前进，成功后的工具轮继续使用同一候选。provider 的 SDK 重试关闭，不调用 Coordinator 内部可多次请求的 Route.Chat。整个 wake 最多三次预留，因此实际 HTTP 不超过三次；当前候选已不可用的准备失败也占一个预留，但不创建 generation。每个实际请求只有一个 generation，记录真实 provider/model、候选序号、冻结配置 revision 和当前 provider 配置 revision。

新计划同时冻结 `request_profile=employee-fast-v1`，在 journal 保存前设置 4096 输出 token 上限并关闭 thinking；DeepSeek Flash 使用其 `thinking.type=disabled`，不传不支持的 `reasoning_effort=none`。这一层不强制工具调用，保留普通文本首轮直答和 Quiet。实际请求体与 Langfuse input 一致，profile 名称另记 metadata；provider adapter 不在记录后改写模型或请求参数。旧计划没有 profile 时保留原请求字节，缓存重放不新增 HTTP。该语义首次随 marker 8 发布，不修改现有全局模型配置。

预发三轮历史验收中，首轮和纠正轮正确送达，但中间指代轮三个配置候选依次超时；准确历史已进入该失败请求。此参数缺口已用真实 HTTP、journal、Langfuse 一致性回归修复，仍须新预发 E2E 复验，不能断言它是所有超时的唯一根因。

旧快照不增补模型计划或近期历史，原请求字节和已提交效果保持重放，尚需 I/O 时仍使用原 signup transport。恢复已保存 outcome 或 response/effect journal 不依赖当前新模型链就绪；当前权限、租户、服务、在线副本及 Runtime 门禁仍检查，新的实际请求仍验证凭据。模型计划与近期对话快照最初共用 marker 6；当前统一门禁见下文。

## 上线与验证边界

处理方式开关按实际模型、发送/记忆依赖、在线副本及 Runtime 能力校验就绪状态；预发已启用，缺失依赖时拒绝新受理，不静默回退其他 Loop。具备 Runtime capability 不等于所有业务验收已完成。当前逐轮历史呈现、事项候选、`continue_task` 与 `steer_task`、共享模型计划、原通知协议、类型化 scene job（见下文“内部 Task wake”）、跨场域收集（见下文“跨场域收集”）、工作计划（见下文“计划与执行后续”）使用 `[employee-loop:13]` 副本标记，引用定位的 `q` 候选使用 `[employee-loop:14]`，`employee_decide` 例行任务的 `routine.decision` task wake 使用 `[employee-loop:15]`；滚动混版期间暂缓新 Employee 受理和结果通知，避免旧 worker 忽略冻结的历史呈现版本、解释新工具或错解模型选择。所有在线副本兼容后恢复；发送前再次检查来源与当前范围，已提交的未知投递结果只查询对账。worker 启停跟随现有进程生命周期，PostgreSQL 是消费和恢复真相。

首批已验证真实 PostgreSQL 的原子回执/消费、重投、lease 抢占、三请求累计预算、部分成功回执恢复、Quiet、自发消息过滤、身份缺失、超限收束及工作区删除竞争；fake 模型测试证明调用次数和队列事实。真实模型时延、真实发送回执、FC canary 和持久设备滚动兼容必须单独记录，不能用这些测试替代。

## 内部 Task wake

`employee_scene_job.kind` 区分人类消息窗口 `message`（1–32 条消息）与内部 `task_wake`（0 条消息、单 item）。`AdmitTaskWake` 为既有 Employee Task 单独写入 wake 回执（source `employee.task_wake/<producer>`，category `wake`）、消费与 job，三者同一事务；不伪造 DispatchMessage，也不并入人类合窗。来源身份是 producer source + 稳定 event id：首个提交冻结 occurred_at 与 fingerprint，同身份同内容重放返回原 job，内容不同为 conflict。新受理时重验当前 tenant fence、Task 目标版本、输入边界与停止状态；wake 载荷只是引用。Task 来源由 `TaskOriginRegistry` 按 Task request 账本的 source namespace 分派给对应 reader，从 PG 返回受理主体（含类型）、交付锚点与历史策略：`scene_principal`（按原受理主体读原场域对话）、`scene_endpoint_principal`（自动化 Task 读该场域当前 dispatch endpoint 主体，由 reader 校验）、`not_applicable`（企业场域或无会话 webhook，渲染为不适用而非不可用，且不提供 reply）。reader 同时校验来源主体的当前权限，撤销或记录不一致以原因 hold。未注册 namespace 的 Task 不能被唤醒；当前内置 `employee_scene`（场域消息 `dispatch_task`），自动化来源由各自包注册。首批 kind 为 `collection.ready`、`execution.follow_up`、`routine.decision`、`webhook.decision`。

Claim 只领取本二进制支持的 job kind、wake kind 与 schema 版本，其余保持 pending 等待支持它的副本，不解码、不重试；同一场域内未完成的人类消息窗口先于 wake 领取，wake 不越过更新的人类输入。worker 在解码 Dispatch envelope 前按 kind 分流，未知 kind 以明确原因 hold。旧消息 envelope 的范围不一致仍按原逻辑重试，但第三次领取后 hold，不再每秒空转。

wake 运行复用同一 lease/generation、模型 journal 与最多三次模型请求，只提供 `reply`/`stay_quiet`。Task 快照以 Background follow-up (data) 呈现，不是人类指令，不插入人类窗口；原生工具调用与结果一一配对。运行前和完成事务内都从 PG 重建 Task、原主体当前调用权限、原场域与请求者；停止、目标版本变化、权限撤销或绑定不一致均以原因 hold，且不调用模型或不入队发送；来源读取或输入构建的其他错误按有界重试，第三次领取后 hold。完成时最多一条 scene notice（ID 为 wake job ID）送到 Task 原会话与请求者，不使用原消息的 Router callback；同一事务写入 `employee_host_notice` 事实，使该 Host 发出的消息在送达确认后作为同场域、同主体的 assistant 历史出现在后续轮次（原 Task 来源消息的记忆证据被撤回时保守隐藏）。邀请与 watchdog 通知后续写入同一张表。模型失败只记录、不代发道歉。producer 须先确认 `TaskWakeProducerReady`（所有在线副本具备 marker 12）；未就绪时 `AdmitTaskWake` 返回 `ErrTaskWakeNotReady`。回滚到 marker 12 之前的二进制前须排空 pending wake：旧 worker 不按 kind 过滤，会对 wake 每秒重试。

## 计划与执行后续（marker 13）

`dispatch_task` 可带 `follow_up_steps`（1–7 个后续步骤，可标 `review_first`）：dispatch 的 prompt 即第 1 步，Task 建为 lifecycle v2 `explicit_goal`，同时冻结计划 revision 1（步骤、复审标记、后续动作预算 8、第 1 步 Run），存于 `employee_task_plan`。只有来源消息明确要求“前一步完成后再单独执行下一步”时才用；一次执行能做完的工作不拆步。带计划时保持默认完成通知。

每个计划 Run 的终态事实（`employee.execution` 回执，且属于当前目标版本）由 Host 对账器消费一次，`(plan revision, Run)` 在 `employee_task_follow_up` 中唯一：
- 成功且下一步无需复审：确定性派发下一步，不调用前台模型；新 Run 绑定原请求、原 job 与原主体，自身来源为 `employee_plan/<plan>/<Run>/run`。
- 成功且下一步标了 `review_first`：入场一个 `execution.follow_up` wake（≤3 次模型请求，工具 `continue_plan`/`reply`/`stay_quiet`）；`continue_plan` 只能按计划原文启动下一步，wake 不继续则计划暂停、目标开 human_input 等待。
- 最后一步成功：按计划授权确定性 `CompleteGoal`；新输入越过边界、等待未满足或 writer 未确认时不完成，只暂停。
- 步骤失败：在该 Run 的结果通知之后暂停，开 human_input 等待，并向原会话发确定性说明（`employee_host_notice` 记为 `task_plan`）。步骤被取消（停止或 steer）只暂停，不另发说明。
- Task 已停止：计划 stopped；目标版本变化或目标已完成：计划 superseded。
- 本 plan revision 的后续动作达到预算，或 Task 连续自动推进达到 12 轮：暂停、等人并发确定性说明。

普通终态事实仍是零模型事实。计划步骤 Run 的 Execution Event 与结果通知经 `employee_plan` 的 run_started、follow-up 的 next_run_id、原请求 source 与冻结输入校验；proof_version 升为 5，旧副本写下的 version 4 skip 会被重新评估。

**自动推进上限（governor）**：每个非人类 wake 按其回执计一轮（`NoteAutonomousRound`，同一 wake 只计一次），确定性计划派发也计一轮；人类输入清零。超过 12 轮的 wake 不调用模型：v2 目标开 human_input 等待，并向原会话发一句确定性说明。

**依赖释放**：v1 Task 的 Run 终态事务内调用 `ReleaseUpstreamWaitTx` 释放 `blocked_by` 依赖（savepoint 隔离失败）；v2 由计划完成释放；全部副本具备 marker 13 后，对账器补齐旧副本或失败时未释放的依赖。被释放的目标只回到 ready，不会被唤醒（目前没有计划授权依赖释放后的 wake）。

**工具参数与沉默保护**：Host 对 `source_ref`/`task_ref`/`read_ref` 只去掉首尾空白与引号，去掉后仍须完全相同，journal 记录规范形式。Host 在任何效果之前拒绝的调用以 `employeeloop.ErrToolRefused` 返回给模型（附有效 ref），模型可在同一三次预算内改正。工具出错后模型选择 Quiet 或预算用尽时：若模型已写出回复文本则发送该文本；若窗口是单聊或 @ 了该员工则发一句诚实说明；未被点名的群聊仍可安静。

**wake 回复发送前复核**：wake 回复入队后，若 Task 被停止、目标被纠正、wake job 丢失或场域不再服务，发送前抑制。

**滚动**：计划派发、decision wake 与释放对账只在全部在线副本具备 marker 13 时运行。回滚到 marker 13 之前须先停止新计划并排空进行中的计划步骤：旧副本没有计划步骤的来源证明，会 hold 这些 Run 的结果通知。

源码入口：`internal/employeeentry`、`internal/eventrouter`、
`handler/employee_scene_entry*`、`internal/employeetask`、
`service/direct_task.go`、`service/employeeloop`、`service/employeememory`。


## 请求者私有记忆

`memory_capture`、`memory_lookup`、`memory_forget` 使用现有 Employee 工具循环，不创建后台 Task、不添加提炼模型，仍最多三次模型调用。capture 后普通回复通常两次；已有 brief 直接回答一次；lookup → forget → reply 最多三次。新工具最初由 marker 5 门禁保护（当前统一门禁见上文）；已有 job 保留冻结的 Config.Tools 和 model journal，不在恢复时追加工具 schema。

三项操作均要求整个原始收集窗口只有一个已知 requester，且所选 source_ref 唯一。Host 在 journal 事务内重验当前 scene/tenant、平台调用主体权限及 receipt → consumption → job 绑定。平台 endpoint principal 不是发言人；作用域固定当前 scene 的 requester-private，模型不能指定 actor、scope、trust 或 confidence。

capture 的 quote 必须原样出现在选定消息的外层 Text；引用背景和 Reaction 不能提供陈述。带引用的外层纠正可以处理。协议未提供可信 human/bot 类型，native 的 ForwardMessages 当前未映射到 DispatchMessage；手工复制内容也无法证明原创。因此记录只表示该账户归属的观察：Observed、confidence 4、Trusted/HumanStated/VerifiedExecution 均为 false，不提供真人、原作或验证成功保证。

SourceID 固定为 `employee-message:<receipt_id>`，EvidenceID 为该消息 OpenMsgID。一条源消息仅消费一次 learning identity；换 key/type/quote 不会创建第二条。OccurredAt 使用 receipt 首次 Host created_at，不使用重试时间。更早或同时间的来源不能替换同 type/key 的较新记忆，时间栅栏包含 forgotten/superseded 墓碑；晚到来源得到 superseded 回执。旧记录没有 evidence_occurred_at 时，以 Host 创建该记录的 created_at 作保守栅栏。后台 Run 的原 RecordTx 策略保持不变。首次在 reset 后才送达、又没有可验证源时间的历史消息无法由本协议判定为旧事件。

新输入快照只在可信场域目录明确为 DM、且窗口只有一个已知 requester 时自动注入最多四条 private brief。group 的单一发言人不代表听众只有该人，因此不自动注入其最近 private 记录；用户明确询问时仍通过原有 memory_lookup 在同一 scene/requester 范围按需读取。scene-shared brief 保持原合同，未知 kind 不猜作 DM。此规则只作用于新快照，已冻结的旧 group 上下文及 model journal 保持字节兼容；该记忆增量不提升 marker，最多三次调用不变。

lookup 在同一事务内复用 Search 的授权、排序及衰减，最多八条；brief 带 record ID/type 供定向纠正与忘记。forget 仅更新本 namespace 的精确记录 ID，保留回执墓碑，不删除替代记录或其他人的记忆；重复忘记不推进 revision。

工具缓存重放不重做效果：同事务只读复核来源和状态，失效记录不再通过缓存 lookup 返回。底层记录未变化时保留原 lookup 快照，不因读时置信度衰减制造模型请求冲突。真正状态变化导致后续已冻结模型请求不一致时，现有失败 outcome 路径终结该 wake，不修改历史 journal、不追加模型调用。记忆工具 trace 在 journal 事务结束后关闭；`journal_committed` 表示事务提交，业务拒绝仍可为 ERROR 并有已提交的失败回执，rollback 不作为写入成功证据。

新输入快照还冻结记忆回复的表达约束：遵守用户限定的输出格式，只回答目标事实；缺失时简答不知道，不列举无关记录或承诺访问其他场域私有记忆。普通确认不展示 record ID、内部状态及来源字段，忘记后不复述被忘内容；用户明确要求审计细节时例外。该约束仅追加到新快照的 Persona 与工具描述，不修改全局 BuildPrompt、历史快照、权限或调用预算，该表达增量不单独提升 marker。

verified distill 已接入：Host 验证通过的 Run 由 worker 维护 tick 经 `ReconcileEmployeeVerifications` → `ReconcileEmployeeVerifiedDistill` 写入记忆（不调模型）。本批仍不接 HumanStated、跨场域共享、promotion 或周期合成。真实 IM 证据与发布状态单独记录于验收计划。

## Task 生命周期 v2（读取端，2026-10-03）

`employee_task` 新增三列：`lifecycle_version`（1|2）、`completion_mode`（single_run|explicit_goal）和 `autonomous_rounds`。
- 旧行和旧二进制写入的行一律为 v1/single_run，不回填。
- Task 状态新增 `waiting`。数据库约束 v1 不能处于 waiting、v2 不能处于 failed，且 v2 只用于 employee 场域的 Direct Task。Run 的状态集合不变。

所有 Task 状态写入都经过同一个转移入口，按版本和原因校验。拒绝时返回带原因的 `LifecycleError`，`errors.Is` 仍匹配原有错误类型。

v1 的 `RecordResult` 行为不变。v2 中 Run 结束永远不会完成目标：
- 有未满足的必需等待时进入 waiting，否则进入 ready；
- Run 失败后目标回到 ready，等待决策，不会自动重试。

**等待事实**
- 等待事实存于 `employee_task_wait`，kind 取值为 collection / human_input / schedule / task / external。Task 的状态只是这些事实的投影。
- `task` 用于 blocked_by 依赖：只有上游 Task 真正进入终态才能释放。这一条的接线尚未完成。

**`CompleteGoal` 的条件**
- 必须同时满足：当前版本、当前 goal_revision、当前输入水位都对得上；没有未满足的必需等待；没有在跑或未确认退出的执行者。
- 还必须有真实执行或证据引用。零工作量返回 not_ready。
- 已被人工停止的目标返回 stopped。
- 同源重放返回同一条 entry；第二个来源返回 already_completed。

**停止与重开**
- 人工停止对 v2 是终态，同时关闭所有未满足的等待。
- 目标最近一次 Run 失败，或被取消而非被 steer 中断时，普通纠正不能重试它。
- 已完成的目标只能通过修正升 goal_revision 才能重开。

**发布约束**
- 本版只上线读取端，生产代码不会创建 v2 Task。
- 第一个 v2 producer 必须在全部副本都具备对应的 `[employee-loop:N]` 后才能开启。原因：旧二进制会把 v2 Run 成功当成目标完成。

## 跨场域收集（taskinput + Employee 接线，marker 13）

`internal/taskinput` 保存四类领域事实：collection、invitation、input 和 ready intent。没有外键；按工作区删除。答复绑定、收齐判定和外发检查的规则见包注释与测试。

**发起（原场域）**
- `create_collection` 只在当前来源明确要求向具体的人收集时使用。参与者只能从“在本租户与该员工说过话”的提供方实名发言人中解析：重名、未出现过或群不明确时返回原因，由 Loop 向发起人澄清，不猜人。
- 每份邀请的最终文本（Host 模板 + 模型写的问题）先过外发检查，命中原场域近期对话原文、私人记忆、密钥或配置链接就拒绝，不创建任何记录。
- 同一个工具日志事务内依次写入：v2 explicit_goal Task（来源 `employee_scene`，复用场域消息的 TaskOrigin reader）、collection wait、collection、每个邀请一条 outbox 发送（action id 等于邀请的 delivery action id）和 B 场域历史事实（`employee_host_notice`，source_kind=invitation，principal 为该 Agent 的 dispatch endpoint 主体）。任一步失败整体回滚，只留下失败的工具回执。
- 对没有私聊场域的人按 open id 发 1:1，邀请为 `pending_scene`；送达状态给出会话后按 dm 解析回填场域并补写历史事实。
- 发起人原话明确要求提醒时，提醒计划经 `CollectionReminders` 钩子在同一事务写入；钩子未接入时拒绝该请求，不静默丢弃。

**作答（B 场域）**
- 聊天快照为每条来源消息冻结 Host 绑定：回复链 8 跳内指向邀请才是强绑定；群里无引用一律不绑；单聊只有唯一待答邀请才绑；多于一个为 ambiguous。本员工自己的消息、卡片/系统占位文本永不绑定。模型只看到发言人自己的问题。
- `accept_collection_input` 只记录被绑定的那条消息原文；ambiguous 时只有单聊发言人自己的原话点明是哪一题（`reference_quote`）才可记录，否则先澄清。工具结果不含人数或进度。
- 迟到答复还可见本人同场域最近关闭的已送达问题，最多5条/24小时，只含问题、关闭状态和时间；不含其他参与者答案或origin私有上下文。关闭事实不产生accept binding，closed-only仍不提供收答工具，不授权转发/汇总/恢复承诺。见 `docs/employee-collection-late-context.md`。
- 发送前 `BeforeCollectionInviteSend` 再核邀请仍有效、场域目录/租户/身份未变，并对最终字节再做外发检查；不通过的动作被抑制，由对账器记为 held 或 failed。

**收齐与汇总**
- 对账器（scene worker 的 5 秒循环）先把 outbox 送达事实写回邀请，再把每个 pending ready intent 在同一事务里转成 `collection.ready` wake（来源 `employee.task_wake/employee.collection`，event id 为 `<collection>/<revision>`，occurred_at 沿用 intent 冻结值），并标记 admitted。所有副本都具备 marker 13 之前，intent 一直保持 pending。
- 汇总 wake 的快照带上冻结 revision 下的获准答案，只提供 reply。完成事务内依次完成 collection、解决 wait、完成目标，再写入唯一一条原场域消息；revision 已变或 collection 已关闭则 hold，不发送。模型三次都没给出回复时，Host 按答案确定性渲染一条汇总，不再调用模型。
- 停止该 Task 时，在同一事务内取消其所有进行中的 collection。

## Webhook 可信入口（2026-10-03）

**端点绑定**
- 绑定内容包括：workspace、智能体、场域、租户、创建者、派发方式、签名策略和密钥版本。
- 只从 PG 读取，冻结在 `webhook_delivery.source_binding`。payload 里的 actor/org/scene/mode 只当数据，不参与路由。

**验签与去重**
- 验签针对原始字节，并且先于去重执行。
- 有事件 id 时：同 id、同有效 payload 返回原回执（duplicate）；同 id、内容不同返回 409 conflict，并另记一条 rejected delivery。
- 没有事件 id 时，按请求逐条受理。
- 事件 id 超过 255 字节或含控制字符时返回 400。

**输入冻结**
- receivedAt 统一取 `delivery.received_at`。`source_digest` 冻结有效 payload。
- 崩溃窗口内重试时：摘要漂移则判为 failed，路由变化则判为 ignored，两种情况都不执行。
- 已受理的 run 沿用冻结时的输入。

**仍待完成**：Webhook 例行任务目前仍走 Autopilot run_only，Employee producer（F2）要等读取端在全部副本上就绪后才开启。

## 场域例行任务改走 Employee Direct（marker 12）

**新路径的触发条件**：Agent 是 employee 模式，并且全部在线副本都具备当前 `EmployeeLoopReplicaMarker`（首次随 `[employee-loop:12]` 发布，现为 17）。

**新路径的行为**
- 每次定时触发或立即运行，都在一个事务内写入以下内容：真实 AutopilotRun、冻结来源 `employee_routine_occurrence`、独立 EmployeeTask（v1 single_run，`requester_ref=routine:<id>`，没有人类发起人）、Run、queue，以及开始通知。
- `(trigger, planned_at)` 只受理一次。
- 认领时只使用冻结的工作包，不读当前的 Autopilot 说明。
- 工作包冻结场域的 openConversationId（受理时从场域目录读取，绝不是 scene_id），执行方用它读取本场域的消息。
- 开始/结束通知由例行任务自己发送，并且是唯一发送方。这类执行不进入 Execution Event，也不进入私有学习。

**跳过与失败**
- 上一次还在运行时，本次记为 `skipped_overlap`。
- 暂停、场域或租户不可用、授权撤销、runtime 离线：记为 skipped。
- 计划时刻处于暂停状态的时点记为 skipped（`routine was paused at its planned time`，依据 rule version 记录的暂停/恢复时刻）。调度器允许时点迟到 5 分钟发出，在这个窗口内恢复也不会补跑被越过的时点。
- 配置错误：记为 failed。
- 以上情况都保持原有节拍。

**门禁关闭时**：保持原 Autopilot run_only 路径。

**注意**：runtime 没有 `employee-direct-v1` 能力时，occurrence 记为 skipped，不会回退到旧路径。

## 内部唤醒的场域历史与 Host 主动消息

内部唤醒（task_wake）的快照组成：
- origin 场域的近期对话，按原受理 principal 读取，截止时间为唤醒受理时刻；
- Task 快照，作为 Background follow-up 数据放入，不是人的指令；
- 工具只有 reply 和 stay_quiet。

Host 主动发出的消息会进入之后的近期历史。范围包括：唤醒回复、邀请、停滞提醒。

- 这些消息写入 `employee_host_notice` 事实表。
- 只有同一场域、同一 principal、已送达且有 provider message id 的消息才会进入历史，角色为 assistant。

Task 来源的读取按 source namespace 注册，`history_policy` 有三种显式取值：`scene_principal`、`scene_endpoint_principal`、`not_applicable`。

## 停滞提示（watchdog）

**什么算进展**
- 算进展：真实执行输出、工具结果、artifact，以及 Host 受理的人类 Task 输入。
- 不算进展：心跳、lease、扫描、通知、账本记账，以及问进展或致谢这类闲聊。

**episode 与提示**
- 每个 Task 同时最多一个 open episode。状态变化或出现新进展时，静默关闭当前 episode。
- 每个 episode 最多发一条确定性中文提示：不调用模型，不报百分比或 ETA，不说「卡住」。同一边界最多 3 条。
- FC 执行在凭据有效期之后仍没有输出，判为执行环境不可达，提示里不会说「还在执行」。

**发送前重查**：发送前重新确认 episode 仍是 open、没有更新的进展、交付合同允许发送、目标未变。

**不发提示的情况**
- 用户要求「只发文件」时，记为 held，不发送。
- 每个 agent 有启用水位；水位之前开始的工作不判定。

**扫描条件**：扫描间隔 30 秒，前提是全部副本具备 marker。

**阈值配置**
- 配置位置是 Diamond 的 `runtime.employee_watchdog`，采用严格解析。
- 默认值：running 900s，queued 600s，waiting 3600s。
- 可以按 agent 单独覆盖。
- 必须等两个副本都已运行新二进制后，才能写入这个键。

## Host 验证与可信提炼

**验证规格的来源**，只认三种：
- 请求者本人原话中明确写出的完成标准，由确定性规则推导；
- 自动化配置；
- Host 预置算例。

模型提出的检查必须经请求者确认后才生效。

**怎样才算通过**
- 证据必须绑定到确切的 Task/Run/queue/goal revision，由确定性 checker 核验。assistant 的自述、沙箱 exit 0 都不算。
- 送达不等于正确。
- 检查期间规格或证据发生变化，本次结果作废。

**通过之后**：验证通过的 Run 经持久意图提炼为请求者私有的可信学习（confidence 7）。自动化来源永远不进入人的私有命名空间。

**运行方式**：验证与提炼由 worker 周期对账执行，两者都是幂等的 PG 消费者。

## 当前消息的附件（文本）

**读取范围**
- 只读当前消息自己的资源，或外层消息精确引用的那条消息的资源。
- 资源来自 `im/list_messages_by_ids` 的结构化 `resources`。
- 正文里的 URL 和 fileId 不授予任何读取权。

**限额与格式**
- 每条消息最多 4 个文件、2 张图。
- 单个文件不超过 10 MiB。
- 每次唤醒提取的文本合计不超过 16 KiB。
- 只支持 UTF-8 的 txt/md/csv/json。

**冻结与重放**：读取在事务外进行，在 lease 内重新校验后冻结到 `employee_message_resource`。新快照以「Host 读取的资源（数据）」放入，不增加模型调用。

**能力边界**：图片一律标注 `vision_unavailable`；当前模型链没有经过验证的视觉路径。


**原生取消收集（`[employee-loop:16]`）。**请求人要取消自己的等待收集、停止询问/催问或不再汇总时，模型先 `read_task` 读本 wake 的 source-bound候选，再 `cancel_collection` 用当前 `read_ref` 与外层逐字请求。Host复验当前 principal/requester/scene/tenant、Task version CAS 与 active collection，复用 stop 事务关闭目标、waits、collections和pending ready intents；不新建后台取消Task。成功ACK来自事务内回读的cancelled receipt，迟答不形成新输入/汇总。已送达提问不宣称撤回，真实运行进程仍保留退出证据屏障。旧工具表冻结不热改；15/16精确marker混版会暂缓新受理与旧job恢复，记not_ready并可恢复，不宣称不中断；当时全部在线副本16后恢复旧snapshot/journal；当前累计隐私reader门控需全部17。独立memory marker仍按自己的累积规则。


**同目标事项来源关联。**新冻结TaskBrief的候选除了概括goal，还包含Task时间、当前active-run与latestRun状态/时间，以及至多两条已通过本场域RecentConversation可见性过滤的人类来源关联（初始请求/最近输入）。用原请求中的命名、source与对话关系区分相似goal，信息不足仍澄清，不能默认最新或以相似goal当事项身份。不附执行report代替read_task；真实进度/续接保留source-bound读取、权限与CAS。旧冻结brief保持字节，缺字段按已有合同读或澄清；本次纯additive数据不升loop16。


**明确追加工作与方法继承。**当前source明确对既有Task追加一个工作步骤、扩展/调整/重做交付时，先source-bound `read_task`，再 `continue_task`；原请求的实际执行方法与约束（例如Python实际执行）继续约束新步骤，除非请求人明确改方法。已有数据、步骤短或可口算不免除真实执行；只执行新步骤，不无故重跑旧sleep。普通口算问题或解释已交付报告的数字含义仍direct，不按「继续/合计」词判派发。询问现在进度须read_task，冻结state/历史成功不是当前读取；复述已实际交付的报告内容可直接答，但不能当新执行证明。新独立产出仍dispatch_task+builds_on。一般选择规则进入现有trusted ForegroundBoundary，TaskBrief只给来源事实；新快照冻结该版本、旧快照不改，marker16保持。原失败及Why见26-task-execution-inheritance.md。


**本人跨源私人记录撤销（`[employee-loop:17]`）。**可信DM唯一本人可见person_view的外源private记录退休后，新历史按同workspace/agent/tenant、exact owner、recordID及真实origin scene撤销依赖回复。只读墓碑ID/来源元数据，不读外源正文或别人的private/public内容，不要求退休源目录仍存在；外源group evidence消息ID不能擦DM独立人话。lookup/me的current-scene搜索不改变。16/17混版由canonical门控暂停新受理/恢复/发送，全部17后恢复，旧冻结input/journal不热改。完整真实跨源值复活反例仍需单独验证。

## 持续会话安静（epoch18）

参考GawkBot固定71e82a的DisabledMembers/notifier_targets：明确禁用不能被@绕过。stay_quiet只结束当轮；持续安静要求通过原生set_scene_participation记录scene级quiet，只有原发起者当前外层明确恢复或重新要求回应可改active。模型选语义，Host守精确source/quote/权限，不用中文关键词自动写状态。其他账号的会话wake摄入后零模型Quiet；后台Task/routine继续，通知按原授权发送。暂停/恢复有确认回复；quiet下不能通过旧snapshot调用普通工具或发送普通前台回复。状态与journal同事务，重放不增revision。

native foreground action发送前验证其completed message job、scope和notice绑定，quiet压制尚未提交provider的其他job回复；暂停ACK例外仅该控制job。已提交远端请求不保证撤回。同步Router callback在入账时受控，尚缺独立提交前控制；旧多receipt哈希notice没有新job字段也有边界，不签所有前台投递全覆盖。新表及工具需所有live副本epoch18；目前仅本地实现，未部署。不得在quiet控制仍有效时直接回退到不支持该状态的epoch17；恢复active并排空新工具job后才可安排兼容回退，另批审核。
