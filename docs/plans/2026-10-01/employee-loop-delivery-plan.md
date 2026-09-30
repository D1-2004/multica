# EmployeeLoop R5 实施拆解

> 面向实施 Agent：按 `subagent-driven-development` 或 `executing-plans` 逐任务执行；任务完成前保持 checkbox 未勾选。每个提交只包含本任务的已验证改动。

**Goal:** 交付独立 EmployeeLoop，以钉钉场域事件驱动持续任务、默认 RunOnly 执行和及时场域回报，并保留可独立降级的旧 Coordinator 灰度通道。

**Architecture:** 统一认证/事件受理入口冻结 legacy/new lane；新 lane 复制并改造 GawkBot Go Tick 内核，不调用旧 Coordinator，也不再串联分类模型。Builder 编译可信来源、历史和任务上下文；HostGuard 做轻量确定性守卫，ReplyComposer 只表达已验证事实；能力、执行、DWS 连接和 outbox 继续复用现有服务。

**Tech Stack:** Go 1.26.1、PG/sqlc、Redis/Tair、DWS Go SDK、现有 TaskService/daemon/DSH、既有 Web/desktop 配置入口。

基线：`origin/develop@7c91589854e06d6907cce18b944b46b66a34a553`；GawkBot `71e82a1809565281cbd0bf8185d3c125b715d934`；DWS 同步源 `ae29c2914dd9108e7ce451329bceeca411464b6b`。实施先 rebase 最新 develop；能力分支单独整合，不恢复已撤回的关键词授权和唯一候选自动续接。

本文件对应主方案R5。旧Coordinator仅为独立灰度通道；普通新路径默认RunOnly、无独立finish_check，按统一场域与Task/Run接口适配。

## 实施约束与共同夹具

- EmployeeTask 是持续目标；ExecutionRun 是一次执行；ExecutorQueueTaskID 指向既有队列任务。Issue 仅是跨场域/复杂多阶段协作的可选投影，创建它不扩大结果 visibility。
- 新 lane 默认 `finish_check_calls=0`；不复制旧 12 秒独立审核流程。旧 lane 保留自身合同；业务明确要求的人工批准绑定具体定义 revision，不启用第二个分类 LLM。
- source-specific Builder 挂在 Employee 上；消息、日程、文档、审批资源不强制使用聊天 CID。日程/审批默认企业场域；timer/webhook 明确绑定 enterprise/group/personal 场域。
- 共享 sandbox 不共享 Task、nativeSession、credential 或私有文件权限；当前 runtime 做不到隔离时，不承接跨 principal 的私有并行工作。
- TDD 每题先建可编译的最小接口和夹具，再获得行为断言的红灯；编译错误、缺 DB、认证失败不算有效红灯。绿灯只实现该断言所需逻辑，不写镜像实现的测试。
- 每题测试只用 fake 模型、fake backend、httptest DWS/provider；不得解析或执行用户安装的 Agent CLI。真实 canary 单独授权、单独记录。

共同 fixture 保存于 `server/internal/employeeevent/testdata/`，使用普通自然语言和稳定测试 ID；字段最少为：

```json
{"case":"approval_scope_is_enterprise","source_event_id":"event-1","source_kind":"dws","provider_key":"user_oa_approval_task_created","workspace_id":"test-ws","employee_id":"employee-e","subscribed_principal":"employee-account","actor_ref":null,"scene_kind":"enterprise","scene_ref":"enterprise-1","origin_ref":"resource-1","occurred_at":"2026-10-01T01:00:00Z","history_before":"2026-10-01T01:00:00Z","definition_revision":1,"grant_revision":1,"generation":1,"expected":{"lane":"new","dispatch_count":1,"finish_check_calls":0}}
```

生产 UUID 解析测试另用 canonical UUID fixture；上例描述语义，不能把测试 ID 发到业务服务。按题补 `source_refs/allowed_actors/question_ref/result_version/audience/anchor/control_seq/provider_outcome`；expected 检查权威记录与调用次数，不检查“收到”等固定关键词。

## 13 个可独立评审的小任务

### 任务 1：冻结入口契约与 lane 选择

**责任文件：**新增 `server/internal/employeeevent/ingress.go`、`ingress_test.go`、`testdata/dispatch_v2.json`；修改 `server/internal/handler/agent_dispatch.go`、`agent_dispatch_v2_handler.go`、`agent_dispatch_acceptance.go`。
**依赖：**无；交付入口接口与 fake store，不改变现网默认 lane。

- [ ] 红：同事件两副本分别请求legacy/new；重试时开关变化；同群mixed-lane的无引用续接语义命中另一owner。断言只冻结一个lane、HTTP replay相同、结构锚点归原owner；无法证明的跨lane接续澄清而不重派，不运行第二圈。
- [ ] 绿：认证后保存source receipt；结构锚点先归Task owner，无锚点按SceneDefaultOwner，再冻结lane/config revision和原wire/callback；恢复只读取冻结 lane。legacy 和 new 分派互斥，新失败不自动执行 legacy。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/employeeevent -run 'TestIngressLane|TestWireReplay' -count=1`；红阶段断言失败，绿阶段 PASS。

**兼容样本：**7 类 Dispatch 2.0、legacy、mentions null/[]、reaction/quote、controls、callback 缺省；保留 responsePolicy v1 literal 和 summary retired/statistics 专项回执。

### 任务 2：复制内核并改名，确保只有一条模型驱动循环

**责任文件：**新增 `server/internal/service/employeeloop/{loop,types,tools,journal,queues}.go`、`loop_test.go`、`SOURCE_MAP.md`、许可通知；复制上游 `internal/bot` 核心及对应 fake 测试。
**依赖：**任务 1 的新 lane 接口；新包先只做 fake Tick。

- [ ] 红：完整 tool batch、EOF 无提交、Interrupt 遇阻塞 tool、旧 generation 回调。断言无合法 finish 不完成、取消不中途卡 mutex、只有一个主模型驱动器。
- [ ] 绿：命名使用 EmployeeLoopState/EmployeePhase/EmployeeTool/ModelChunk/EmployeeJournal；给 StreamFn 传 ctx，锁外调用 tool/emit，保留原生 ToolCallID 和完整 batch，terminal disposition 明确。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test -race ./internal/service/employeeloop -run 'TestTick|TestInterrupt|TestTerminal' -count=1`；绿阶段 PASS。

**范围：**来源账本记录固定 SHA、原文件/符号、目标文件、修改原因；不复制 GawkBot 整套 Broker、SQLite、UI 或 provider 启动器。

### 任务 3：持久 mailbox、日志与三种任务 ID

**责任文件：**新增 `server/internal/employeetask/{model,store,mailbox}.go`、`store_integration_test.go`；新增 `server/pkg/db/queries/employee_task.sql`、`employee_event.sql`；在 `server/migrations/` 分配未占用 `9000+` 迁移编号。
**依赖：**任务 1–2；仅增加新 lane 数据权威，不回填全部历史 Issue。

- [ ] 红：两个 PG connection 认领同 mailbox、lease steal、进程在 intent commit 后崩溃、callback 重投。断言一个 EmployeeTask/ExecutionRun/队列映射，旧 owner 提交被拒绝。
- [ ] 绿：PG 保存事件、任务、run、journal、effect intent；短事务认领与 revision/generation CAS，模型/provider 在锁外。Redis仅唤醒，不能成为任务真相。
- [ ] 验证：`make sqlc`；`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/employeetask -run 'TestMailbox|TestLease|TestCrashReplay' -count=1`；使用现有 worktree DB、fake backend，绿阶段 PASS。

**迁移门槛：**无 FK/cascade；每个索引独立单语句 `CREATE [UNIQUE] INDEX CONCURRENTLY`；显式租户删除与关系清理。

### 任务 4：DWS 原生事件入口与订阅配置

**责任文件：**修改 `server/internal/dwseventsource/source.go`、`source_test.go`；新增 `server/internal/employeeevent/dws.go`、`dws_test.go`；复用 `server/pkg/dws/events`、`internal/connmgr`、`internal/dwsclient`。
**依赖：**任务 3 的 durable receipt；每个已注册事件可独立接通。

- [ ] 红：group/target/roles/filter 不同却被合并；handover 双连接重投；Typed 失败但 Data/Body 非空。断言 scope fingerprint 不混、PG受理一次、未知/坏数据留证据而非伪造空事件。
- [ ] 绿：Consumer 扩完整 SubscriptionSpec 和 typed Event handle；保留 Native.Key/ID/Data/Body、订阅主体与 actor分离，Handle commit 后才 ACK，未知 key 不绕过 SDK registry。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/dwseventsource ./internal/employeeevent -run 'TestSubscription|TestNativeEvent|TestHandoff' -count=1`；fake ticket/WS，绿阶段 PASS。

**范围：**SDK28目录与旧CLI27目录分开；calendar/doc@ 的未发布 key 先在 dws-for-tag 上游登记后 sync，不本地修改 vendored SDK。

### 任务 5：TaskDefinition、ledger、human note 与依赖 lane

**责任文件：**新增 `server/internal/employeetask/{definition,ledger,dependency,lane}.go`及对应测试；更新任务 2 的来源账本，映射上游 normalize、ContextUsed、humanNotePacketBlock、dependency result 与资源 lane 符号。
**依赖：**任务 3；任务装配逻辑不依赖模型。

- [ ] 红：重复 normalize、多人human note（stop→普通补充→迟到完成）、依赖结果迟到、同资源并发写、不同资源独立。断言定义归一幂等，note按append-only ID/seq逐条消费，未解除的restrictive fence独立保留，旧依赖不能推进当前 generation。
- [ ] 绿：复制可复用任务流程并适配PG，排除上游note单槽覆盖和整槽清空；限制只允许有授权对应clear/resume解除；ledger保来源/ContextUsed/权限版本；同一任务主 lane 与资源冲突规则分开，Actor亲近度只排序候选。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/employeetask -run 'TestDefinitionNormalize|TestHumanNote|TestDependency|TestResourceLane' -count=1`；绿阶段 PASS。

**范围：**依赖结果只向允许 audience 注入；不把本地 ring buffer、英文 follow-up 前缀或30秒最近人规则搬成授权门槛。

### 任务 6：Employee Builder 与 source-specific 上下文

**责任文件：**新增 `server/internal/service/employeeloop/{builder,history,scope}.go`、`builder_test.go`及 `server/internal/handler/employee_builder.go`；修改 `server/internal/handler/agent_builder.go` 的配置装配边界，新增独立 Employee Builder 配置，不复用旧合同判定对象。
**依赖：**任务 4–5；可独立以 fake history 验证，不启动执行器。

- [ ] 红：同ID历史重复、原水位后消息倒灌、跨场域引用、订阅主体被当actor、同名不同UID。断言准确去重、Watermark/Scope不扩大、当前原文限制不丢、第二分类模型调用数=0。
- [ ] 绿：Builder挂Employee，按 source schema 编译 current event、已定义任务、history-by-ID、记忆revision、有效grant与执行上下文；读失败保 unavailable，缺actor不补猜。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/service/employeeloop -run 'TestBuilder|TestHistoryWatermark|TestScope' -count=1`；绿阶段 PASS。

**范围：**原生DWS业务payload保持原shape；敏感工具正文不进共享Scene摘要，资源Scene不强制聊天CID。

### 任务 7：轻量 HostGuard 与单次主模型计划

**责任文件：**新增 `server/internal/service/employeeloop/{guard,plan,policy}.go`、`guard_test.go`、`testdata/semantic_cases.json`；迁移纯 helpers/voice规范，legacy `inboundcoord` 运行包不变。
**依赖：**任务 2、5–6；新 lane 主模型已有结果后只做确定性提交守卫。

- [ ] 红：唯一旧任务的新样本请求、状态询问、别人@、伪造work/run/grant/recipient、遗漏source。断言无错误dispatch/outbox，合法续接必须同交付物且有实质推进。
- [ ] 绿：HostGuard检查 schema、scope、已验证actor、target ownership、当前grant、source覆盖、revision/generation、动作幂等；不请求旧 finish_check，不隐藏新增岗位分析步骤。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/service/employeeloop -run 'TestHostGuard|TestPlanCoverage|TestContinuation' -count=1`；trace断言 `finish_check_calls=0`、`builder_classifier_calls=0`、模型回合仅由一个Tick驱动。

**边界：**模型语义回放单列；结构guard无法证明业务正确性，高风险操作用明确且有版本的人工批准，不暗加12秒审核模型。

### 任务 8：默认通用 RunOnly，Issue 为可选投影

**责任文件：**新增 `server/internal/service/employee_execution.go`、`employee_execution_test.go`、`server/internal/employeetask/issue_projection.go`；修改 `server/internal/service/autopilot.go`、`server/internal/service/task.go`、`server/internal/handler/daemon.go`、`server/pkg/db/queries/agent.sql` 的实际 seam。
**依赖：**任务 3、5、7；这是第一条端到端执行切片的核心。

- [ ] 红：普通明确工作误建Issue、run-only claim依赖伪造Autopilot、跨task串行组误挡、Issue投影扩大visibility。断言默认无Issue，三个ID关联正确，claim/workspace/attribution从不可变run取得。
- [ ] 绿：抽通用RunOnly enqueue/claim/complete；调用现有NotifyTaskEnqueued与launch lease；Task/nativeSession/credential绑定task、run和principal；Issue仅在跨场域/复杂协作定义中显式选用，ACL仍按原scope/输出策略。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/service ./internal/handler -run 'TestEmployeeRunOnly|TestEmployeeClaim|TestIssueProjectionVisibility' -count=1`；fake executable/launcher，绿阶段 PASS。

**范围：**先迁新EmployeeTask来源，未迁Autopilot/Issue仍走自身域服务；不一次性重构所有公共API。

### 任务 9：timer/webhook 的场域绑定与无人触发

**责任文件：**新增 `server/internal/employeeevent/{timer,webhook}.go`及测试；修改 `server/internal/scheduler/jobs_autopilot.go`、`handler/autopilot_webhook.go`、`service/autopilot.go` 的适配点。
**依赖：**任务 4、6、8；受信定义直达不增加分类 LLM。

- [ ] 红：两副本同planned_at、旧定义重试、签名payload改scene/uid/recipient、默认日程/审批落个人。断言一次run，企业默认正确，个人例行无delegation被拒绝。
- [ ] 绿：规则绑定 enterprise/group/personal SceneRef、run_as/grant、定义revision和output policy；actor=system/service；每次firing重查授权，不把owner当个人凭据主体。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/employeeevent ./internal/scheduler ./internal/handler -run 'TestTimerOccurrence|TestWebhookScope|TestEnterpriseDefault' -count=1`；绿阶段 PASS。

**范围：**UTC planned_at与显式时区；overlap skip/coalesce/queue，等待deadline不套latest-only；模板参数仍受HostGuard。

### 任务 10：SceneNotice、ReplyComposer 与及时 outbox

**责任文件：**新增 `server/internal/service/employeeloop/{notice,reply_composer}.go`及测试；修改 `service/dingtalkresponse/{service,worker,sandbox}.go`、`handler/dingtalk_response.go`、`pkg/protocol/messages.go` 的受版本保护字段。
**依赖：**任务 3、7–8；关键成果/问题在运行中立即可交付，不等终态。

- [ ] 红：A委托/B答问/E订阅；stage v1 delivered 后result v2；Compose后撤权；provider已调用但响应丢。断言正确A/anchor、v1不覆盖v2、撤权调用数0、unknown仅reconcile。
- [ ] 绿：执行器提报 SceneNotice(kind/ref/version/audience)→事实/授权校验→ReplyComposer→已有场域outbox；固定目标与允许事实，voice/persona只改措辞，发送前复查grant/run/policy版本。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/service/employeeloop ./internal/service/dingtalkresponse -run 'TestSceneNotice|TestNoticeAudience|TestResultCoverage|TestSendTimeRevocation' -count=1`；绿阶段 PASS。

**边界：**业务结论来自执行证据，原始thinking/tool输出不群发；question记录先入库。迁新字段不重解释旧delegated_to_issue/response receipt语义。

### 任务 11：运行控制、等待与共享 sandbox 隔离

**责任文件：**新增 `server/internal/employeetask/control.go`、`control_test.go`；修改 `server/pkg/protocol/messages.go`、`server/internal/daemon/daemon.go`、`server/pkg/agent/agent.go`、`server/pkg/agent/dsh_native.go`、`server/internal/dshhost/session.go` 的受信输入与quiescent seam。
**依赖：**任务 8、10；只为试点backend打开已验能力。

- [ ] 红：活needs_input恢复误Start第二run、cancel/completed竞争、暂停无停止证据、共享sandbox读他人session/credential。断言Control既有handle、旧终态fence、无quiescent不复用writer。
- [ ] 绿：durable command/seq/expected revision，persisted/received/applied/quiescent区分；unsupported明确；同sandbox仍独立Task/nativeSession/credential/受限工作区。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/employeetask ./pkg/agent ./internal/daemon -run 'TestEmployeeControl|TestQuiescent|TestSharedSandboxIsolation' -count=1`；使用测试创建的fake程序，绿阶段 PASS。

**边界：**后台CLI取消不等于零外部副作用；不能隔离私有文件的runtime拒绝跨principal复用，不能以子目录名充当权限。

### 任务 12：Capsule、正式产物与留存

**责任文件：**新增 `server/internal/executioncapsule/{manifest,store,promotion,gc}.go`及测试；修改 `server/internal/daemon/gc.go`、`server/internal/handler/dsh_trajectory.go`、`server/internal/handler/dsh_trajectory_store.go` 对应保存/清理边界。
**依赖：**任务 3、8、11；独立于第一条RunOnly切片上线。

- [ ] 红：上传半失败、GC与恢复/promotion竞争、session-log缺批、撤权后resume。断言不发布不完整manifest，deleting阻断恢复，正式产物独立，旧私有现场不可注入。
- [ ] 绿：staging→hash验证→PG发布完整revision；log/增量workspace/临时产物统一生命周期，promotion复制校验后独立ACL/ref；终态GC等待完整checkpoint确认。
- [ ] 验证：`cd server && MULTICA_RUN_REAL_AGENT_SMOKE=0 go test ./internal/executioncapsule ./internal/daemon -run 'TestCapsule|TestPromotion|TestRetention' -count=1`；fake对象存储，绿阶段 PASS。

**边界：**native session ID仅优化；FC token/计算期限前checkpoint，续租失败可恢复停止，不宣称本机无绝对超时等于FC无限运行。

**拆分交付：12A最低结果/产物保存进入M2：Run结果先入DB，本地文件上传+hash校验+稳定ArtifactRef/Scene ACL成功后才GC；测试上传失败保目录且不发布临时链接。12B完整workspace/nativeSession Capsule恢复留M3；M2活handle/resume只开放已验证backend。**

### 任务 13：配置入口、灰度降级与证据交付

**责任文件：**修改 `server/pkg/runtimeconfig`、`cmd/server/runtime_config.go`、Employee Builder handler；涉及UI仅改 `packages/core` API/schema和`packages/views`配置组件；更新主方案、HTML、使用文档和源码映射。
**依赖：**任务 1–10可开始试点；11–12通过后才宣称可交互/可恢复长任务。

- [ ] 红：开关热变更让同事件换lane、缺新协议runtime仍接新命令、降级后旧run丢Notice。断言新受理切lane、在飞run沿冻结owner结算，旧协议拒绝新kind，UI不暴露尚未验能力。
- [ ] 绿：按workspace/employee/source灰度；保留独立legacy Coordinator lane，标注退出条件。Builder选择与场域/能力引用版本化，默认新RunOnly/轻量HostGuard明确显示。
- [ ] 验证：定向Go/TS检查通过后运行 `python3 scripts/check-coordinator-policy.py` 验legacy合同、`pnpm typecheck`；不得把结构PASS当新Loop模型/渠道验收。

**提交：**每题完成后提交原子commit；标题 `type(scope): 摘要`，用heredoc真实换行正文；优先rebase，返回 `git log -1 --pretty=%B`。部署/发信单独按授权执行。

## 里程碑、降级条件与真实验收

| 里程碑 | 依赖 | 可独立交付的结果 |
| --- | --- | --- |
| M1：内核与可靠事件 | 1–4 | fake完整链及DWS受理持久化；默认仍legacy，未声称已执行真实任务 |
| M2：新通道RunOnly试点 | 5–10、12A、13 | 一个员工/一个backend/已注册来源，默认无Issue，运行中Notice正确回场域；这是最小可用交付 |
| M3：交互与恢复闭环 | 11–12 | 真实控制ACK/quiescent、权限隔离、完整Capsule恢复和删除；逐backend开放 |

自动停止新受理灰度的条件：重复Executor派工/外部副作用、actor或audience串用、私有数据泄漏、lease失效后仍提交、发送时撤权被绕过、Notice丢失或错误覆盖最终结果；当前run先fence/停止并保留证据，不对同事件重新走legacy。普通性能退化按明确阈值降新受理比例，不牺牲授权与送达真实性。

真实canary单独验证：两副本重启/交接；同群多人含同名；timer/webhook企业与个人边界；关键阶段→问题→回答→最终结果的实际DWS回读；共享sandbox负向文件/credential可见性；本机/FC old-new协议矩阵；cancel后quiescent；超过FC token/计算期限前续租与checkpoint；Capsule删除/promotion并发。

涉及daemon/FC时使用 `fc-runtime-dev-loop`，固定镜像/commit/配置与canary任务ID；真实Agent仅在明确授权后使用 `agentintegration` 标签及 `MULTICA_RUN_REAL_AGENT_SMOKE=1`，不得把fake green或结构检查作为真实结果。

性能证据按 ingress_commit→builder_done→plan_commit→queued→runtime_ready→claimed→first_notice→completed 分段，比较相同定义/模型/runtime的warm/cold、样本数、p50/p95、模型调用数/token。用户报告median 9.3s vs24.5s、约快2.63倍是需求输入，非本轮实测、非默认SLO或已验证归因。

本轮仅设计文档；所有checkbox均未完成，没有实施业务源码、部署、发信或运行真实canary。先交付可review的完整方案/HTML/本拆解，再由根任务按已授权流程提交develop及钉钉本人。
