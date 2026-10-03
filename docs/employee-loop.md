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

普通能力介绍和仅索取配置入口优先直接使用已提供的能力目录，在首轮调用 `describe_capabilities`；不为了“更准确”额外读取配置。只有用户明确询问开关、提示词原文或已有例行任务等配置细节，且当前上下文没有答案时，才使用 `scene_config_get`。普通介绍通常一到三句，用同事之间的自然表达说明能协助完成什么，不照抄工具名、Direct 或配置字段。明确配置查询则保留用户要求的准确技能名、开关状态与提示词原文，按所需细节完整回答；“详细配置加链接”同样适用，不受普通介绍的一到三句限制。DWS、技能、连接器和 MCP 属于后台执行能力，不能称为前台可直接调用。已有 Cron/Webhook 开关只证明存储的配置，Employee 触发执行路径尚未验收，不宣传或承诺已经接通。此合同不新增模型调用、事后润色或更改三轮硬上限；真实模型首轮选择、回复长度和时延仍须通过 canary 验证。

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

新 wake 的 Persona 冻结时序解释约束：同一对象的最新明确陈述或重设覆盖旧取值与旧更正，不能把旧更正再应用到更新的重设之上；单项修改保留其他当前事实。指代按最近相关交换中的对象顺序解释，旧 assistant 回复不覆盖更新的用户陈述，历史请求不当作新的执行命令。这只是新快照的提示约束，不改历史记录、全局 BuildPrompt、低延迟请求参数、长期记忆或已冻结快照；真实模型能否正确处理仍须单独验收。

新 wake 只读同 workspace、agent、tenant、scene 和受理 principal 的近期用户原话，以及有 provider 消息 ID 和匹配会话的已送达 Host 回复。截止时间固定为原 job 受理时间，上限 24 小时、20 条、16 KiB；当前窗口排除，截断显式标记。不读取未确认发送的模型结果，不增加总结 LLM，不写长期记忆。callback 回复必须同时匹配原 URL 和确切同步 RequestID；其他 Run 回复依赖独立的 notice 来源记录，不能仅凭复用 URL 纳入。

已有私有 memory 被 supersede 或 forget 后，新历史投影按同 scope/requester 的 `employee-message:<receipt_id>` 与 `evidence_id` 精确撤销对应源消息，并保守隐藏该原 job 的关联整条回复（含多 receipt 派生 notice、确切同步 callback 与 Run notice）。同窗其他用户消息和没有写入 memory 的普通临时纠正仍按时间保留；审计原文不删除，且输出 `withdrawn_memory_evidence_omitted`，不冒充完整对话。不扫描 insight 或按值全场域擦除；后续没有结构化来源引用的独立复述无法据此关联，不宣称全局擦除。

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

处理方式开关按实际模型、发送/记忆依赖、在线副本及 Runtime 能力校验就绪状态；预发已启用，缺失依赖时拒绝新受理，不静默回退其他 Loop。具备 Runtime capability 不等于所有业务验收已完成。当前事项候选、`continue_task` 与 `steer_task`、共享模型计划及原历史/通知协议使用 `[employee-loop:9]` 副本标记；滚动混版期间暂缓新 Employee 受理和结果通知，避免旧 worker 解释新工具、错解冻结模型选择或近期对话输入。所有在线副本兼容后恢复；发送前再次检查来源与当前范围，已提交的未知投递结果只查询对账。worker 启停跟随现有进程生命周期，PostgreSQL 是消费和恢复真相。

首批已验证真实 PostgreSQL 的原子回执/消费、重投、lease 抢占、三请求累计预算、部分成功回执恢复、Quiet、自发消息过滤、身份缺失、超限收束及工作区删除竞争；fake 模型测试证明调用次数和队列事实。真实模型时延、真实发送回执、FC canary 和持久设备滚动兼容必须单独记录，不能用这些测试替代。

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
