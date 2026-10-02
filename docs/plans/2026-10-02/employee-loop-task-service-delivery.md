# EmployeeLoop 与 Task Service 开发路线

修订 R2（2026-10-02）：首版收轻 Direct 派发；记忆与进化直接移植 GawkBot 并与旧 Loop 隔离；首轮回复优先，前台每次交互最多 3 次模型请求。

> 面向实施者：用户已于 2026-10-02 批准实施、分批推送 feat/tag-multitenant、预发部署及验证。实施时使用 `executing-plans` 逐项细化并完成；只有用户选择并行实施时再使用 `subagent-driven-development`。所有 checkbox 保持未完成，直到有实际证据。

**Goal:** 在保留现有执行底座的前提下，交付独立 EmployeeTask、Issue/Direct 两种派发后端和可切换的 EmployeeLoop，并跑通多人场域中的连续工作闭环。

**Architecture:** 复用 AgentScene/EventRouter；场域入口持久选择一个 Loop。新的 Task Service 管目标、追加记录和 Run，Issue/Direct 后端接现有域服务与队列；EmployeeLoop 直接移植固定 GawkBot 内核、prompt/口吻和记忆/经验机制。记忆异步更新且与旧 Loop 隔离；前台不串联分类、审核或润色模型。

**Tech Stack:** Go、PostgreSQL/sqlc、Tair 唤醒、既有 TaskService/Daemon/FC/DSH、共享 React Query/UI。代码基线 `feat/tag-multitenant@7d1a398bb`；GawkBot 固定 `71e82a1809565281cbd0bf8185d3c125b715d934`。

设计合同：[完整设计](employee-loop-task-service-design.md)。历史 R5 清单保留为来源，本轮以已落地的 scene/eventrouter 为起点，不重复创建事件入口。

## 1. 四个可验收里程碑

| 里程碑 | 人可以看到的结果 | 包含工作包 |
| --- | --- | --- |
| A：任务抽象能承接旧链 | Coordinator 仍按原方式工作，后台能查 Task/Issue/Run 对应关系 | D01–D03 |
| B：独立 Task 快速直接运行 | 薄入口复用 RunOnly，不创建 Issue/永久 Autopilot，不等待完整配置快照 | D04–D05 |
| C：首轮回复与新 Loop 闭环 | 简单输入首轮回复，复杂输入至多 3 轮；隔离记忆异步学习，工作可持续推进 | D06–D09 |
| D：可控切换与扩来源 | 配置页能切换且不争抢旧任务；timer/webhook 逐项进入同一链 | D10–D12 |

UI 可以提前开发，但 Employee 选项只有 C 的验收通过后才可生效。暂不提供无条件切流和活任务迁移。首版单个 Agent 的新目标默认使用 Employee Direct；复杂多步骤仍可 Direct，已有 Issue/明确协作才用 Issue。

开发优先级：D06 的首轮回复/口吻样机可先用 fake Task backend 跑出实际模型时延，再接 D05 的真实 Direct 链。完整配置冻结、通用 RunSpec 协议、全量快照/hash、复杂 playbook 综合及完整 Capsule 不挡住这个最短闭环；未具备的控制能力明确显示为排队/不支持。

## 2. 文件与模块边界

建议新增文件：

| 目录/文件 | 职责 |
| --- | --- |
| `server/internal/employeetask/{types,definition,service,store,ledger,run,wait,effect}.go` | 领域类型、业务聚合、PG 持久化、Run/等待/效果，不 import handler/service |
| `server/internal/employeetask/{compiler,packet,backend}.go` | TaskDefinition→Work Packet、ContextUsed、派发端口 |
| `server/internal/employeeentry/{consumer,route,mailbox,window}.go` | 场域入口消费/Loop 路由/持久窗口，不复制 provider envelope |
| `server/internal/service/employeeloop/{loop,types,queues,tools,journal,service,builder,policy}.go` | GawkBot 内核适配及 EmployeeProfile |
| `server/internal/service/employeeloop/SOURCE_MAP.md` | 每个复制符号的 SHA、来源、修正原因和对应测试 |
| `server/internal/service/employeememory/{scope,learning,workflow,distill,store}.go` | 移植 GawkBot 的隔离记忆与经验机制；不调用旧 Coordinator flush |
| `server/internal/service/employeeloop/{prompt,voice}.go` | 移植可用 prompt builder/口吻片段，适配钉钉工具和 Agent 配置 |
| `server/internal/service/{employee_task_backend,employee_issue_backend,direct_task,employee_task_lifecycle,employee_notice}.go` | 依赖注入、Issue/Direct backend、执行信号、发送桥 |
| `server/internal/handler/{employee_task,employee_run_claim,employee_scene_entry}.go` | 授权 API、Run claim 装配及现有 Dispatch 接缝 |
| `server/pkg/db/queries/{employee_task,employee_task_run,employee_task_entry,employee_loop_mailbox,employee_task_effect,employee_task_wait,employee_event_consumption}.sql` | 新持久化查询；binding/attempt 按相邻域放置 |

修改现有入口时以小范围委托调用为主。不要先拆解整个 `task.go`、`daemon.go` 或重新实现 IssueService。生产行为合同和 built-in Skill source map 与对应代码一起更新。

## 3. D01：冻结接口、状态与兼容矩阵

依赖：无。

文件：新 `employeetask/types.go`、`backend.go`；现有 `docs/agent-scene.md`、`docs/event-scene-router.md`、`docs/inbound-coordinator-loop.md` 只在相应行为真正实施时更新。

- [ ] 将三个 ID、state、BackendRef、最小 Direct 输入和 control outcome 编成可编译类型；完整 RunSpec 快照不进入首版接口。
- [ ] 以人工请求、纯观察、修正、停止、需要回答、完成回调、timer 和 webhook 建立 fixture；使用 canonical UUID，无凭据。
- [ ] 冻结 scope：Employee 场域任务必须有合法 SceneRef；legacy Web/Issue 不能伪造 DingTalk enterprise 场域，使用现有来源范围并限制 Employee 开关支持面。
- [ ] 定义 route/admission 与 owner_loop 两层字段；配置切换只改变新目标默认值。

验收：各处使用同一种 TaskRef/RunRef，不能把 IssueID 或 queue task ID 当 TaskID；新包无 handler/service 循环依赖。接口编译不算行为完成。

## 4. D02：Task 快照、ledger、Run 和 effect 存储

依赖：D01。

文件：新 `employeetask/{store,service,ledger,run,wait,effect}.go` 与相邻 `_test.go`；新 sqlc queries、迁移。

- [ ] 先写真实 PG 两连接测试：同幂等请求创建、CAS 冲突、lease 被抢、同 effect 重投、跨 workspace/agent 访问拒绝。
- [ ] 测试“暂停→补充→迟到完成”：旧 generation 留证但不能推进目标，普通补充不解除暂停。
- [ ] 首批只实现当前目标＋entry＋Run/queue 关联与待处理消费；复用现有回执/outbox，按功能增加 wait/effect/binding，避免一次建齐全部目标表。
- [ ] 故障注入：commit 前退出、commit 后未唤醒；新进程通过扫描恢复。
- [ ] `make sqlc`，核对每个并发索引独立迁移，无 FK/cascade。

验证命令：在 `server` 下运行 `go test ./internal/employeetask -run 'TestTask|TestLedger|TestRun|TestEffect|TestLease' -count=1`。`DATABASE_URL` 指向测试隔离库；用例 skip、未迁移或无法连接不得记为通过。

## 5. D03：IssueBackend 与 Coordinator 接入

依赖：D02。

文件：新 `service/employee_issue_backend.go`；修改 `handler/agent_dispatch_v2_handler.go`、`coordinator_window_plan.go`、`chat_coordinator_plan.go` 及必要的 IssueService 接缝。

- [ ] 先写对照：现有 start/continue 的 Issue、comment、queue task、callback 数量与新 Adapter 路径一致。
- [ ] 以 per-action effect key 调用原 Issue/Comment 域服务，保存唯一 Task↔Issue binding；已有 Issue 续接不重复创建任务。
- [ ] 验证 `IssueService.Create` 自动派单只执行一次；忙时 follow-up 仍走原持久等待逻辑。
- [ ] 在原多动作计划的每个提交点故障注入，确保成功项不重放，未成功项可恢复。
- [ ] 执行原 Coordinator 对照用例并更新 policy source mapping 和证据状态，保留旧 finish_check 合同。

验证：`python3 scripts/check-coordinator-policy.py`；在 `server` 下运行新增 IssueBackend 集成用例和受影响 `./internal/handler`、`./internal/service/inboundcoord` 测试。不得把纯字符串检查当模型行为认证。

交付：旧链行为不变，但新调用可直接读取独立 Task 的定义、记录及 Issue/Run 对应关系。

## 6. D04：移植 TaskDefinition / Work Compiler

依赖：D02；可先于 D03 的业务接线完成，但提交应能独立验证。

文件：新 `employeetask/{definition,compiler,packet}.go`、测试及来源说明。

- [ ] 移植 Goal/Deliverables/SuccessCriteria/AccessNeeded 的结构与 canonicalization；Goal 必填，其他可省略；AccessNeeded 不授予能力。
- [ ] 测试 packet 保留完整目标、当前修正、来源、已完成步骤、结果/材料引用、能力范围和回报地址。
- [ ] 直接移植 Work Packet 文本组装，验证目标和必要材料完整；ContextUsed 来自实际 Builder。首版不开发全量快照/hash 系统。
- [ ] 模拟 compile 成功后崩溃、失败和重试，证明不会推进 human-input cursor 或清除 stop fence。
- [ ] 测试历史读取 unavailable/empty/truncated 的区别和跨 principal 私有材料不混入。

验证：在 `server` 下运行 `go test ./internal/employeetask -run 'TestDefinition|TestCompiler|TestPacket|TestContextUsed' -count=1`。行为断言针对范围/来源/消费语义，不固定回复措辞。

## 7. D05：薄 Direct 入口复用现有 RunOnly

依赖：D02、D04。

文件：新 `service/direct_task.go`、`employee_task_backend.go`；修改 `service/autopilot.go`、`task.go` 和 `handler/daemon.go` 的最小 claim 接缝。只有现有字段/执行能力确实不能承载时才增加协议字段；不以通用 Runtime 协议重构为前置。

- [ ] 先测试 Autopilot RunOnly 原路径抽取前后 readiness、调用权限、归责、入队和通知等价。
- [ ] 独立 Task 传 TaskRef＋prompt＋可信 scope＋幂等键，无 Issue、无永久 Autopilot；Task/Run 记录与队列关联同次写入后立即唤醒。
- [ ] 即时 Task 读取自己的 prompt/context；既有 Autopilot claim 行为保留，不新增全局配置冻结或重复规划流程。
- [ ] 只补同 Task 单 writer、不同 Task/principal 不混上下文所需的 SQL/Go/Runtime scope 接缝；完整 session 复用后置。
- [ ] 优先沿用已发布执行输入；确需新字段时再做精确能力协商和滚动兼容测试，不改变旧字段含义。
- [ ] 复用已有 task_message、usage、trajectory 与产物存储；结果可回读，正式文件有稳定引用。

验证：fake Runtime 的派发/claim/complete 集成用例；受影响 Go 包测试；实际 Runtime 验收依照仓库 `fc-runtime-dev-loop` 技能，覆盖候选 FC 与持久本地设备。镜像/daemon 变更必须有不可变 provenance 和滚动兼容矩阵，不能拿 mock 通过代替。

交付：无需 EmployeeLoop 模型即可由内部测试入口创建 Task，直接执行、回读轨迹、结果和 ArtifactRef。

## 8. D06：复制并修正 GawkBot 内核

依赖：D01、D04；先只接 fake 模型和 fake tools。

文件：新 `service/employeeloop/*`、LICENSE、SOURCE_MAP 与原测试。

- [ ] 固定复制来源并保留修改说明，不复制完整 Broker/产品依赖；移植路线沿 R5 的来源和许可约束。
- [ ] 最小轨迹必须实际经过迁入的 Tick、StreamLLM、ExecuteTool，不能只在目录存一份未调用源码。
- [ ] 红绿验证 ctx 取消能到 provider、阻塞 tool 不持有全局锁、多个 tool call 不丢、ToolCallID 对齐。
- [ ] 验证 EOF 无合法 disposition 不完成、journal 保存失败不提交效果、过期 generation 不提交。
- [ ] 复制 `prompt_builder.go` 的可用函数、首句/直接回应/避免重复 poll 片段及 `teamVoiceForSlug` 口吻；只适配平台、工具、角色和不适用的 Issue/wiki 强制项。
- [ ] 实现同一前台交互 `max_model_calls=3`；格式修复与请求重试共用计数，禁止独立分类/review/composer 调用，不能以内部重建 wake 重置预算。
- [ ] 验证简单回复第 1 轮直接发送；明确目标第 1 轮同时生成派发与接单文案，入队成功后发送，不等执行器启动。
- [ ] 验证一次取证后第 2 轮回复、两次必要读取后第 3 轮收束；超限没有第 4 次调用和盲目派发，重活交 executor。
- [ ] 统计第一条有用回复 P50/P95、首轮回复比例与调用次数；静态 prompt 字节稳定，无每次强制远端记忆检索/合窗延时。

验证：在 `server` 下运行 `go test -race ./internal/service/employeeloop -run 'TestTick|TestInterrupt|TestToolBatch|TestDisposition|TestJournal|TestFirstReply|TestModelBudget|TestPrompt' -count=1`。fake 测试严格计模型调用次数；真实模型时延单列证据，默认测试不得调用用户本机真实 Agent CLI。

## 9. D07：Event consumer、场域窗口与 Task mailbox

依赖：D02、D06。

文件：新 `employeeentry/*`；修改 `handler/event_admission.go` 和新 `employee_scene_entry.go`；保留 `eventrouter` 协议职责。

- [ ] 先测 ready receipt 之后 consumer 失败：不能因 receipt 已保存就声称业务受理；重投恢复同一 mailbox/acceptance。
- [ ] 实现原 owner 优先的持久消费记录；有结构绑定的混合项分别归属；父源全部有去向后才 ACK。
- [ ] 接消息窗口与逐句来源；窗口封存后新消息进入下一窗口，不覆盖已保存输入。
- [ ] worker 短事务 claim＋lease/generation；慢模型在事务外；提交前 fence。
- [ ] 租户 rebind、未知 kind/unmapped、native/Router ownership、纯观察和自己回声都沿现行合同处理。
- [ ] 两个副本和进程重启测试同输入只启动一个有效工作；不新增全局事件顺序承诺。

验证：`go test ./internal/eventrouter ./internal/employeeentry -count=1`，以及 handler 新 consumer 集成用例。带 PostgreSQL 的并发/恢复测试不得 skip。

## 10. D08：GawkBot 隔离记忆/学习、控制与执行信号

依赖：D05、D07。

文件：EmployeeBuilder、TaskWait、`service/employee_task_lifecycle.go`、新 `service/employeememory/*`、`employee_memory_state`/`employee_learning` 存储、Runtime control adapter。原 `service/scenememory` 保留给 Coordinator。

- [ ] 移植 scoped memory、LearningRecord、去重/衰减/supersedes、lookup/capture/promote、SessionRecovery 和异步 task distill；保留来源、测试，替换本地文件/wiki 存储依赖。
- [ ] 新旧 Loop 的内容、revision/cursor/lease、learning 和 reset 完全隔离；同一 scene_id 可有两套记忆，无自动复制、合并或回退读旧数据。
- [ ] scope 先做精确授权再检索；`user-stated`/verified 由真实证据决定，模型不能自封 trusted；完整 playbook synthesis 放后台第二增量。
- [ ] 前台只读已有短 brief；慢提炼/检索失败不阻塞回复。用 fake 慢 distill 验证首轮仍完成，纠正本轮立即生效。
- [ ] 结构化终态与任务状态在同事务持久化 signal/outbox；结果按 Run/attempt/generation 归属。
- [ ] needs_input 落真实 WaitID、allowed actors、问题和 revision；问人成功后释放执行资源。
- [ ] 只在经过核验的 backend 启用 live steer；其他 backend 排队或确认停止后重建，API 区分 accepted/applied/unsupported。
- [ ] 测停止→补充→迟到完成、回答错误 WaitID/actor、答案重投、provider 控制超时和 lease 抢占。

验收：一个 Task 能在服务重启和沙箱失效后继续推进；有 checkpoint 才称恢复，只有 ledger/正式产物时称重建；旧结果不会越过新授权约束。

## 11. D09：Notice、产物与可见性闭环

依赖：D08。

文件：新 `service/employee_notice.go`、发送 outbox 的既有公共接缝、任务/运行读 API；修改 legacy task_finished/scene routine 回报的 owner gate。

- [ ] milestone/needs_input/result 经明确 audience 和来源验证后形成 notice；直接使用合法主模型正文/已验证结果，Composer 只格式化发送，无额外润色模型。
- [ ] legacy task_finished、routine notice 与 Employee notice 对同一 Run 只有一个 delivery owner。
- [ ] 发送失败只恢复 delivery；未知发送结果先查证，重投不会重新计算业务结果。
- [ ] 本地产物上传、校验、manifest 提交后才发稳定 ArtifactRef；scope 不匹配读不到材料。
- [ ] 逐一检查 task list/messages/output/trajectory/download API 的 ACL，不能只新 API 收紧而旧 API 仍可绕过。
- [ ] 完成一个多人群场景：A/B 各自执行，A 修正，B 问人并获得回答，两份结果按各自 audience 交付。

验收分开记录：Host/PG 自动测试、fake model 轨迹、模型回放、真实钉钉发送回执/回读。工作台文字出现不能替代送达证据。

## 12. D10：配置开关和切换验收

依赖：D03、D09。UI 可提前在不可启用状态开发。

文件：`handler/agent.go`、Agent 查询和配置迁移、runtime replica/feature gate 装配；`packages/core/types/agent.ts`、`api/schemas.ts`、相关 mutation；`packages/views/agents/components/agent-message-settings.tsx`、`tabs/digital-employee-tab.tsx` 及四种 locale。

- [ ] 新 DTO 明确 coordination enabled/mode/revision；兼容限于旧 API 边界，不在内部双写两套配置真相。
- [ ] 设置页展示模式、是否对新工作生效、不可用原因、两边在途任务数和支持来源。
- [ ] 查看/重置记忆只作用当前选定 Loop；切回后看到旧 Loop 自己的记忆，不能隐式合并。
- [ ] 移除主动参与强制绑 Coordinator 的逻辑；旧用户确认/task_finished 选项只对其 owner 生效。
- [ ] Agent manager 权限、expected revision 冲突、旧字段/未知 enum、desktop 前后端错版分别测试。
- [ ] Coordinator→Employee→Coordinator 切换期间，新任务按新设置，原 task/run/wait 和重投按冻结 owner。
- [ ] 没有结构锚点的跨 lane 续接请求明确目标；不得双推理双派。回滚后仍能收尾已接受 Employee 任务。

验证：受影响前端单测、`pnpm typecheck`；模式 API/路由集成测试。UI 不乐观显示已切换，必须服务端保存成功再更新。

## 13. D11：接场域 Timer / Webhook 与资源事件

依赖：D10。

文件：现有 `service/autopilot.go`、scheduler/webhook admission、`handler/scene_routines.go`、`service/scene_routine.go`；新来源 adapter 与 fixture。

- [ ] 复用已有 trigger/cron 算法，以 trigger version＋scheduled occurrence / delivery ID 幂等；不造第二个 scheduler。
- [ ] 已有 routine 的配置/开始结束通知与 Loop owner 一起冻结，迁移 tick 不会旧链新链各派一次。
- [ ] Timer/webhook 显式绑定 scene、可用身份和 output policy；签名/调用方认证通过后才受理，actor 可为空。
- [ ] 不用触发器维护人或最后说话人冒充当前人类授权；Host principal 模型按真实来源补齐并做跨组织测试。
- [ ] calendar/approval 先接已验证的 Router/native schema；doc@ 等按真实 SDK 发布能力逐项登记。未支持事件可查 receipt/reason，无伪造会话。

验收：每个来源都有同源重投、绑定撤销、跨 org、控制台配置修改与回报去重用例。增加 category 不等于来源支持完成。

## 14. D12：完整 Capsule 与按需历史

依赖：D05、D09；不阻塞已具备正式产物保存的首版，但不得提前宣称完整恢复。

文件：Capsule manifest/object-store adapter、sandbox GC 接缝、Task history API/Compiler 增量读取。

- [ ] session log、workspace delta、临时产物一起发布 manifest，上传一半崩溃不会显示为可恢复。
- [ ] 正式 artifact promotion 保留 Run 来源且独立生命周期；临时日志/文件按 capsule 同一 tombstone 删除。
- [ ] Task 续接逐步取已授权历史和 ContextUsed；权限变化使旧材料不可复用。
- [ ] 完整恢复验证代码/文件 hash、输入/控制游标、principal 和 runtime/image 兼容；失败走可解释的重建。

## 15. 所有工作包的共同完成条件

- [ ] 先有可编译最小接口和行为断言红灯，再实现；编译错误、缺数据库和认证失败不算红灯。
- [ ] 回归范围按真实影响选择；测试通过记录实际命令、SHA 和关键结果，不能把计划中的命令写成已运行。
- [ ] PostgreSQL 权威、Tair 只唤醒；模型/provider 不持 DB 锁；租户/权限变化在使用时再检查。
- [ ] 更新当前合同、相关 source map 和必要 built-in Skill；不把历史 Plan 当新行为合同。
- [ ] 改 Runtime/Daemon 协议时跑仓库要求的本地/FC 滚动兼容与真实 Task canary。
- [ ] 提交按原子工作包，标题 `type(scope): 摘要`，正文真实换行；用 heredoc，提交后回读 `git log -1 --pretty=%B`。集成优先 rebase。

实施进度与实际验证记录见 employee-loop-delivery-log.md；未经验证的工作包保持未勾选。每批以最新 checkout 细化最小红绿步骤，建议符号只有实际落地后才视为已有 API。
