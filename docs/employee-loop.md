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

Direct commit 已成功时，即使外层工具 journal 写失败或调用被取消，返回的 Run receipt 仍保留，不能错误地说未受理。确定性上下文超限保存一次无需模型的明确反馈，不截掉尾部约束，也不永久重试。

## 场域能力与前台观测

前台复用 contextcap 的全局与组织/场域配置、可信单一请求者的个人配置和合并规则。多人或未知请求者窗口不能借用一个人的个人配置。能力目录展示实际生效配置和简短描述，配置启用不代表上游连接已经可用，也不授予新权限。全局启用能力与场域 offered 开关是并集；关闭本层 offered 不撤销全局授予。

`reply` 是不派任务的明确终态；`describe_capabilities` 由 Host 给能力说明附上当前场域配置链接，`scene_config_get` 只读取目录已验证的当前场域。链接复用 Coordinator/执行器的签发规则，群聊和单聊均绑定 scene_id，单聊不依赖 staffId。链接仅进入 Host 私有回执、回复 checkpoint 和发送正文，不作为工具结果交给模型；重放不重复签发。mint 失败保留原答；数据库超时在 savepoint 内使用可恢复的 statement_timeout，不关闭外层日志事务的连接。实际场域管理继续通过 Direct 的场域 MCP 完成。

普通能力介绍和仅索取配置入口优先直接使用已提供的能力目录，在首轮调用 `describe_capabilities`；不为了“更准确”额外读取配置。只有用户明确询问开关、提示词原文或已有例行任务等配置细节，且当前上下文没有答案时，才使用 `scene_config_get`。普通介绍通常一到三句，用同事之间的自然表达说明能协助完成什么，不照抄工具名、Direct 或配置字段。明确配置查询则保留用户要求的准确技能名、开关状态与提示词原文，按所需细节完整回答；“详细配置加链接”同样适用，不受普通介绍的一到三句限制。DWS、技能、连接器和 MCP 属于后台执行能力，不能称为前台可直接调用。

**前台与后台的边界。**前台只做三件事：回复、读当前窗口/记忆/任务与本场域配置（`scene_config_get`）、给配置链接（`describe_capabilities`）。其余一律经 `dispatch_task` 交后台：DWS 查询（通讯录、主管、组织、日程、文档、窗口外的消息）、技能、连接器、MCP、脚本、文件，以及场域自管理——例行任务/定时任务的新建、修改、暂停、恢复、删除、立即运行，场域提示词，开关已公开的技能与连接器，增删远程 MCP。后台 Direct 任务挂着本场域的 `config-qwen-tag-scene` MCP（`scene_routine_create` 等）完成这些变更；例行任务每次运行都在后台执行，平台在场域里发开始和结束消息。前台没有某个工具不等于后台做不到：遇到需要执行的请求就派发，不回答“做不到/没有记录/没有工具”，只有任务结果才能证明做不到；也不把人支到管理员或平台设置。只有缺“做什么/什么时候”时才先简短追问。账号连接和连接器授权在配置页完成，前台给链接。`scene_config_get` 结果不再带 `read_only`（执行器里它表示例行任务运行只读，前台曾把它读成“本场域不可改”），改为 `how_to_change` 指向 `dispatch_task`。后台技能把请求人自己的明确、完整请求视为确认，直接执行；缺要素、多项无关变更、新增或改址远程 MCP 时才先复述确认（对齐 GawkBot：人的请求本身就是授权，高风险变更由工具侧把关）。

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

`steer_task` 是 Task Service steer 的前台入口（合同见 [task-steer.md](task-steer.md#task-service-steer)）。它只纠正同一场域内、同一请求者自己的 Direct 任务：Host 选目标，只有请求者恰好一个候选（运行中，或 30 分钟内 ready/成功）时才直接作用于它；有多个候选时把候选列表交回模型，由模型带 `task_id` 再调用或追问请求者，不新建 Task。被人工停止或失败的任务不作为隐式目标，纠正也不会重启被人工停止的任务。

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

新 wake 只读同 workspace、agent、tenant、scene 和受理 principal 的近期用户原话，以及有 provider 消息 ID 和匹配会话的已送达 Host 回复。截止时间固定为原 job 受理时间，上限 24 小时、20 条、16 KiB；当前窗口排除，截断显式标记。不读取未确认发送的模型结果，不增加总结 LLM，不写长期记忆。callback 回复必须同时匹配原 URL 和确切同步 RequestID；其他 Run 回复依赖独立的 notice 来源记录，不能仅凭复用 URL 纳入。

已有私有 memory 被 supersede 或 forget 后，新历史投影按同 scope/requester 的 `employee-message:<receipt_id>` 与 `evidence_id` 精确撤销对应源消息，并保守隐藏该原 job 的关联整条回复（含多 receipt 派生 notice、确切同步 callback 与 Run notice）。同窗其他用户消息和没有写入 memory 的普通临时纠正仍按时间保留；审计原文不删除，且输出 `withdrawn_memory_evidence_omitted`，不冒充完整对话。不扫描 insight 或按值全场域擦除；后续没有结构化来源引用的独立复述无法据此关联，不宣称全局擦除。

## 纯停止当前事项

`stop_task` 只处理同一 requester、场域和租户中，当前外层消息明确要求停止的 Direct Task。先 `read_task` 再用本 wake 的 `task_ref/read_ref`，提交时重验权限、来源、精确 Run/queue 与 Task version；普通致谢、进度询问不触发停止。本批不支持 reaction、引用消息或结构 continuation 的停止，不推断其他场域的目标。

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

处理方式开关按实际模型、发送/记忆依赖、在线副本及 Runtime 能力校验就绪状态；预发已启用，缺失依赖时拒绝新受理，不静默回退其他 Loop。具备 Runtime capability 不等于所有业务验收已完成。当前逐轮历史呈现、事项候选、`continue_task` 与 `steer_task`、共享模型计划、原通知协议及类型化 scene job（见下文“内部 Task wake”）使用 `[employee-loop:12]` 副本标记；滚动混版期间暂缓新 Employee 受理和结果通知，避免旧 worker 忽略冻结的历史呈现版本、解释新工具或错解模型选择。所有在线副本兼容后恢复；发送前再次检查来源与当前范围，已提交的未知投递结果只查询对账。worker 启停跟随现有进程生命周期，PostgreSQL 是消费和恢复真相。

首批已验证真实 PostgreSQL 的原子回执/消费、重投、lease 抢占、三请求累计预算、部分成功回执恢复、Quiet、自发消息过滤、身份缺失、超限收束及工作区删除竞争；fake 模型测试证明调用次数和队列事实。真实模型时延、真实发送回执、FC canary 和持久设备滚动兼容必须单独记录，不能用这些测试替代。

## 内部 Task wake

`employee_scene_job.kind` 区分人类消息窗口 `message`（1–32 条消息）与内部 `task_wake`（0 条消息、单 item）。`AdmitTaskWake` 为既有 Employee Task 单独写入 wake 回执（source `employee.task_wake/<producer>`，category `wake`）、消费与 job，三者同一事务；不伪造 DispatchMessage，也不并入人类合窗。来源身份是 producer source + 稳定 event id：首个提交冻结 occurred_at 与 fingerprint，同身份同内容重放返回原 job，内容不同为 conflict。新受理时重验当前 tenant fence、Task 目标版本、输入边界与停止状态；wake 载荷只是引用。Task 来源由 `TaskOriginRegistry` 按 Task request 账本的 source namespace 分派给对应 reader，从 PG 返回受理主体（含类型）、交付锚点与历史策略：`scene_principal`（按原受理主体读原场域对话）、`scene_endpoint_principal`（自动化 Task 读该场域当前 dispatch endpoint 主体，由 reader 校验）、`not_applicable`（企业场域或无会话 webhook，渲染为不适用而非不可用，且不提供 reply）。reader 同时校验来源主体的当前权限，撤销或记录不一致以原因 hold。未注册 namespace 的 Task 不能被唤醒；当前内置 `employee_scene`（场域消息 `dispatch_task`），自动化来源由各自包注册。首批 kind 为 `collection.ready`、`execution.follow_up`、`routine.decision`、`webhook.decision`。

Claim 只领取本二进制支持的 job kind、wake kind 与 schema 版本，其余保持 pending 等待支持它的副本，不解码、不重试；同一场域内未完成的人类消息窗口先于 wake 领取，wake 不越过更新的人类输入。worker 在解码 Dispatch envelope 前按 kind 分流，未知 kind 以明确原因 hold。旧消息 envelope 的范围不一致仍按原逻辑重试，但第三次领取后 hold，不再每秒空转。

wake 运行复用同一 lease/generation、模型 journal 与最多三次模型请求，只提供 `reply`/`stay_quiet`。Task 快照以 Background follow-up (data) 呈现，不是人类指令，不插入人类窗口；原生工具调用与结果一一配对。运行前和完成事务内都从 PG 重建 Task、原主体当前调用权限、原场域与请求者；停止、目标版本变化、权限撤销或绑定不一致均以原因 hold，且不调用模型或不入队发送；来源读取或输入构建的其他错误按有界重试，第三次领取后 hold。完成时最多一条 scene notice（ID 为 wake job ID）送到 Task 原会话与请求者，不使用原消息的 Router callback；同一事务写入 `employee_host_notice` 事实，使该 Host 发出的消息在送达确认后作为同场域、同主体的 assistant 历史出现在后续轮次（原 Task 来源消息的记忆证据被撤回时保守隐藏）。邀请与 watchdog 通知后续写入同一张表。模型失败只记录、不代发道歉。producer 须先确认 `TaskWakeProducerReady`（所有在线副本具备 marker 12）；未就绪时 `AdmitTaskWake` 返回 `ErrTaskWakeNotReady`。回滚到 marker 12 之前的二进制前须排空 pending wake：旧 worker 不按 kind 过滤，会对 wake 每秒重试。

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

本批不接 HumanStated、verified Distill、跨场域共享、promotion 或周期合成。真实 IM 证据与发布状态单独记录于验收计划。

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

## 跨场域收集账本（taskinput，读取端）

`internal/taskinput` 保存四类领域事实：collection、invitation、input 和 ready intent。没有外键；按工作区删除。

- **答复绑定**：沿回复链最多 8 跳，到达邀请消息才算强绑定。群里没有引用的消息不算答复；单聊里只有唯一一个待答邀请时才接受无引用答复；同一人有多个待答邀请时返回歧义，不猜。本 Agent 自己、任何 bot、卡片和系统消息一律不计入。
- **未登记私聊场域的参与者**：对还没有私聊场域的人，邀请以 `pending_scene` 写入，送达回执拿到会话后按 dm 回填场域；不按人造场域。
- **收齐判定**：最后一个必答槽位填满时，在同一事务里写入唯一的 ready intent。
- **外发检查**：发送前按最不受信的读者对最终字节做检查。

本版没有接入工具或 producer，线上行为不变。

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

**新路径的触发条件**：Agent 是 employee 模式，并且全部在线副本都具备 `[employee-loop:12]`。

**新路径的行为**
- 每次定时触发或立即运行，都在一个事务内写入以下内容：真实 AutopilotRun、冻结来源 `employee_routine_occurrence`、独立 EmployeeTask（v1 single_run，`requester_ref=routine:<id>`，没有人类发起人）、Run、queue，以及开始通知。
- `(trigger, planned_at)` 只受理一次。
- 认领时只使用冻结的工作包，不读当前的 Autopilot 说明。
- 开始/结束通知由例行任务自己发送，并且是唯一发送方。这类执行不进入 Execution Event，也不进入私有学习。

**跳过与失败**
- 上一次还在运行时，本次记为 `skipped_overlap`。
- 暂停、场域或租户不可用、授权撤销、runtime 离线：记为 skipped。
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
