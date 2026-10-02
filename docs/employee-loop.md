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

## Task 与执行

EmployeeTask、Run 与 agent_task_queue ID 独立。Task 保留定义和追加记录；Direct 使用已有队列、执行器、消息、usage 和轨迹，不创建 Issue 或永久 Autopilot。Work Object Compiler 使用真实 source、Host scope/principal、完整约束和实际 ContextUsed 组装执行输入，不新增模型请求或通用配置快照。

Direct 的模型可见执行面只装配岗位原文、工作区与组织/场域/个人上下文、实际技能以及简短执行和结果约束。不会自动加入通用 Multica Runtime 命令目录、Issue/Chat/Autopilot 工作流、Mika 系统层或 OKR 标签指令。显式绑定的工作区与场域技能保留原名和内容，包括用户自定义的 `multica-` 名称；claim 内联技能和 bundle 补拉共用执行面策略，DWS 身份规则与 `config-qwen-tag-scene` 仍保留。

Direct 不自动挂载通用 `multica` MCP；场域配置 MCP、内部连接器、Agent 与 Runner 自定义 MCP 继续按原权限装配。Host token、claim finalize、取消、租约、轨迹与用量通路不变。文件通过已装配的钉钉文件工具交付，只有验证过的回执才可称为已送达，不能把沙箱本地路径当成用户可打开的文件。

Direct 需要 `employee-direct-v1`。claim 前按认证 Runtime 过滤；任务回调再次检查执行身份。读取和实时原文按可信 originator/管理者以及确切任务凭据控制。数据库取消表示取消请求，不能证明远端进程已经退出。

首次响应和终态结果使用原有投递 outbox，受理、入队和送达分开记录。每个 Run 至多保存一份结果通知意图，旧目标版本不冒充新目标完成；Native 与外部 Router 分别使用其真实回调目标。正式文件使用独立来源账本及加密对象，只在对象和附件记录均成功后返回鉴权引用。场域记忆只使用 Employee 独立存储。后台以真实 Run 证据捕获低可信的私有学习记录；原始窗口只有唯一请求者时才读其私有 brief，多人窗口不聚合私有记录。重置按来源逐句处理，清除共享场域及请求者自己的私人记忆，其他消息继续处理。

## 上线与验证边界

生产处理方式开关保持不可启用，直到独立结果通知链装配并验收；具备 Runtime capability 不等于整个工作闭环已完成。每个在线副本必须具备 `[employee-loop:2]` 标记才生成新结果通知；发送前再次检查来源与当前范围，已提交的未知投递结果只查询对账。worker 启停跟随现有进程生命周期，PostgreSQL 是消费和恢复真相。

首批已验证真实 PostgreSQL 的原子回执/消费、重投、lease 抢占、三请求累计预算、部分成功回执恢复、Quiet、自发消息过滤、身份缺失、超限收束及工作区删除竞争；fake 模型测试证明调用次数和队列事实。真实模型时延、真实发送回执、FC canary 和持久设备滚动兼容必须单独记录，不能用这些测试替代。

源码入口：`internal/employeeentry`、`internal/eventrouter`、
`handler/employee_scene_entry*`、`internal/employeetask`、
`service/direct_task.go`、`service/employeeloop`、`service/employeememory`。
