# EmployeeLoop 当前实现合同

本合同描述已交付的基础能力；实施路线和未完成项见
[开发路线](plans/2026-10-02/employee-loop-task-service-delivery.md)。

## 场域入口与归属

复用 AgentScene 与 EventRouter 的统一场域 ID。EventRouter 只管理标准事件、场域解析和回执；新增 storage-only receipt hook 在首次回执的同一事务中保存业务 owner 和 Employee job，不调用模型或外部服务。历史回执没有消费记录时保留旧路径，不能在切换模式后自动升级为 Employee 工作。

`coordination_mode` 选择新工作的默认 Loop，`inbound_coordinator` 是独立启用开关。已经受理的事件按持久 owner 重放；已有任务结构锚点优先查询原 owner。当前 Employee continuation/control 尚未接通的事件保存 held 原因，不转给旧 Loop 再派一次。普通的 route=legacy/unified 和业务 Loop owner 是两层不同选择。

首批消费 digital_employee 的 channel/message.created、DWS 回报和合法场域。缺失场域、未支持来源、自己发出的消息及 reaction 有明确 held 原因；不伪造 cid，也不回退到另一租户。逐句身份以自身 sender 字段为准，只有单消息才允许从已验证的外层 sender 补齐；多人窗口的缺失身份保持 unknown。

## 前台处理

场域窗口只收集尚未 claim 的到达消息，claim 后的新消息进入下个窗口。每个 job 持久保存 lease/generation、原始输入、岗位/工具配置、模型请求和结果或失败、工具回执及最终 outcome。模型和外部工具在事务外执行，提交效果前核验 lease、权限和场域。

GawkBot 固定提交的内核和 prompt/voice 负责判断与表达；岗位 Instructions 或有效短合同进入稳定 system 前缀，窗口与记忆作为数据。前台最多三个真实模型请求，provider 错误和格式重试也消耗此预算，重启不能重置预算。简单回复或派发加接单文案可以首轮完成，Quiet 是合法终态。没有单独的 finish_check、审核或润色模型。

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

Direct claim 在原工作包末尾追加明确的 Output 合同：最终 assistant 文本就是用户回复，成功和失败均由 Host 发送到来源会话，执行器不得先调用 DWS `final/reply` 再重复报告。最终文本保持简短，除非用户要求，不列内部工具、命令、本地路径或回执 ID；用户明确要求的文件和其他目标的主动消息仍可执行。

可信 `dingtalk_message_policy` 仅在 Direct claim 带 `final_text_owner=host`。支持该可选字段的 Daemon 必须在 claim 解码后完整传入 `MULTICA_DINGTALK_MESSAGE_POLICY`，SDK 才能对来源会话的 `final` 执行无发送守卫。缺失或未知值保持旧工具行为，普通任务不新增此字段。旧 Daemon 使用固定结构解码，会丢弃未知字段，因此硬守卫交付须切换到包含该类型字段的新候选 Runtime，不能声称只有服务器升级就完成了协议交付。该所有权不替代原生文件的真实送达验证，也不根据任意文字回执抑制 Host。

Direct 需要 `employee-direct-v1`。claim 前按认证 Runtime 过滤；任务回调再次检查执行身份。读取和实时原文按可信 originator/管理者以及确切任务凭据控制。数据库取消表示取消请求，不能证明远端进程已经退出。

首次响应和终态结果使用原有投递 outbox，受理、入队和送达分开记录。每个 Run 至多保存一份结果通知意图，旧目标版本不冒充新目标完成；Native 与外部 Router 分别使用其真实回调目标。正式文件使用独立来源账本及加密对象，只在对象和附件记录均成功后返回鉴权引用。场域记忆只使用 Employee 独立存储。后台以真实 Run 证据捕获低可信的私有学习记录；原始窗口只有唯一请求者时才读其私有 brief，多人窗口不聚合私有记录。重置按来源逐句处理，清除共享场域及请求者自己的私人记忆，其他消息继续处理。

Direct 的 `completion_notice_policy` 默认 `always`，保持正常结果及用户要求的摘要。只有当前选定来源明确要求“文件送达后不再总结”，前台才可选择 `if_not_delivered`、`require_delivery=file` 并提供该来源中的原句；Host 校验并将策略带入 WorkPacket 和持久队列。历史、引用材料或其他发言人的内容不能授权静音。

成功执行的文件通知只在同 workspace/agent/task/身份/目标会话的服务端送达回执成立，且原生消息按准确消息 ID 和会话回读、包含自身 `resources` 中的 `resourceType=file` / `resourceIdType=fileId` 后抑制。文本进度、quoted resources、从正文推导的 resourceRefs、模型最终正文或仅 provider accepted 均不够。SDK 与 shim 的重复回执中，确证文件可优先满足条件；未找到确证文件且仍有 pending/accepted 时等待，unknown 或回读不可用只发送明确未确认文案。执行失败和取消仍通知。发送前重查可抑制晚到的文件送达；晚到的失败/不确定状态只能更新尚未提交的通知意图，并由 outbox 重新加载安全正文，已提交动作只查状态、不重写或重发。文件回读在数据库事务外完成，再次校验来源、权限和回执后才保存决定；不新增模型调用。


## 上线与验证边界

处理方式开关按实际模型、发送/记忆依赖、在线副本及 Runtime 能力校验就绪状态；预发已启用，缺失依赖时拒绝新受理，不静默回退其他 Loop。具备 Runtime capability 不等于所有业务验收已完成。当前显式文件完成通知策略、场域工具与私有投递 journal 使用 `[employee-loop:4]` 副本标记；滚动混版期间暂缓新 Employee 受理和结果通知，避免旧 worker 解释新工具或遗漏私有附链。所有在线副本兼容后恢复；发送前再次检查来源与当前范围，已提交的未知投递结果只查询对账。worker 启停跟随现有进程生命周期，PostgreSQL 是消费和恢复真相。

首批已验证真实 PostgreSQL 的原子回执/消费、重投、lease 抢占、三请求累计预算、部分成功回执恢复、Quiet、自发消息过滤、身份缺失、超限收束及工作区删除竞争；fake 模型测试证明调用次数和队列事实。真实模型时延、真实发送回执、FC canary 和持久设备滚动兼容必须单独记录，不能用这些测试替代。

源码入口：`internal/employeeentry`、`internal/eventrouter`、
`handler/employee_scene_entry*`、`internal/employeetask`、
`service/direct_task.go`、`service/employeeloop`、`service/employeememory`。
