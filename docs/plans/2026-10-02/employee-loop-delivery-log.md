# EmployeeLoop R2 实施与交付记录

用户于 2026-10-02 授权开发、分批交付到 `feat/tag-multitenant`、部署预发并验证。实现基线更新为远端 `78bfa78fa`；隔离开发分支 `codex/employee-loop-r2`。仅部署预发 pipeline 66，不发布正式。

## 批次与状态

| 批次 | 内容 | 状态 |
| --- | --- | --- |
| B0 | R2 方案归档；修复新库历史迁移依赖顺序 | 已推送并完成预发部署 |
| B1 | EmployeeTask 目标/追加记录/Run 持久化 | 已推送并完成预发部署 |
| B2 | Work Packet、Issue/Direct 接缝 | Direct 文本执行及权限回归通过、复审中；Compiler / IssueBackend 开发中 |
| B3 | GawkBot 内核、首轮回复与 3 调用上限 | 内核已推送并完成预发部署；生产消费另批接线 |
| B4 | 隔离 memory/learning、入口及回报闭环 | 隔离记忆基础验证通过、准备交付；入口及回报开发中 |
| B5 | 配置开关、预发真实场景与 Runtime 验证 | 配置开关本地验证通过；真实账户验证等待预发 PAT 刷新 |

## B0 已观察的证据

- 目标分支更新：远端从 `7d1a398bb` 前进到 `78bfa78fa`；采用新基线，未覆盖他人提交。
- 本地 Go：1.26.1；独立 PostgreSQL 17 测试数据库 `employee_loop_r2_test`，不使用预发业务库跑测试。
- 原始 `go run ./cmd/migrate up` 在 `271_task_completion_canceled_status` 失败：其依赖的 `task_completion_outbox` 到 9025 才创建。
- 新排序回归用例观察红灯：271 小于 9025。修复迁移移到 9540，并按仓库约束保留旧 271 alias；新增真实 PG 回归先复现 down 后旧 alias 残留，再修复成功撤销时一次删除当前及旧名称记录。
- 修复后本地 migrate up 完成；scene/eventrouter 的实际 PG 测试和迁移定向回归通过。
- `cmd/migrate` 整包测试通过。`internal/migrations` 整包存在原分支多组 9093/922x/926x/929x/930x 重复编号 lint 失败；本批没有隐瞒或扩大修改范围，不宣称全库测试全部通过。
- 开始工作时预发实例 `3110298726` 是其他已提交改动的运行；其构建、部署与集成测试均成功，停在人工预发验证。不能当作本任务部署证据。

后续每批记录确切 commit、测试命令/结果、审查结果、预发实例/发布 SHA、健康与业务验收。未发生的模型、Runtime、真实发送验证保持未完成。

- B0 两阶段审查通过；真实 9540 SQL 在本地事务内执行 down/up 后回滚，约束从 completed/failed 恢复到 completed/failed/canceled。

## B1 本地验证

- 新增 `employeetask` 目标、追加账本、Run/队列映射与可选 Issue 绑定；9600–9609 为独立并发索引迁移，无 FK。
- 先在已连通并迁移的隔离 schema 中观察 8 项明确行为断言失败，再实现到通过。最终覆盖 12 项顶层用例及 7 项删除竞争子例。
- `go test -race ./internal/employeetask -count=1 -v`、`go vet ./internal/employeetask` 通过，无跳过。
- 工作区删除新增真实记录夹具，先复现 Task/entry/Run 残留，再加同事务清理。清理隔离、事务回滚和两个已有工作区用例共 4 项 race 通过。
- 独立审查发现删除竞争：没有 FK 的新表不会自动获得父行锁。已补全部写入先取 workspace FOR KEY SHARE，并用两连接验证删除先行/写入先行，复审通过。
- 本机原 55439 是另一个会话的进程，期间关闭。本任务已创建自己的 PostgreSQL 17 cluster `/private/tmp/employee-loop-r2-pg`，端口 55462；重新全量迁移及全部定向测试通过。
- 本批还没有用户入口、实际派发或运行流量；同 revision retry/完整控制/等待属于后续接线。

## 预发交付进展

- B0 已推送目标分支，commit `685f68c73`；基线同时保留其他人的 `d11fe440b`（FC 场域/触发者复用）。
- 触发 pipeline 66 / CR 36355253，实例 `3110299956` 后被外部操作取消。新实例 `3110300031` 正在构建，最终验收需核对实际 revision。
- 预发 DB 直连演练因网络超时未完成；没有执行或提交预发迁移 SQL。正式迁移仅由既有 Aone release order 自动执行；后续以部署及服务日志验证。

## B1 预发与 B3 内核证据

- B1 干净交付分支 rebase 后提交为 `97cb044ae`，目标 `feat/tag-multitenant` 已推送。pipeline 66 实例 `3110300728` 的构建、部署、集成测试均 SUCCESS，停在人工预发验证；bootstrap 回读 `9600_employee_task` 已应用。
- GawkBot 固定提交的内核、队列、工具注册、会话和 prompt/voice 片段实际移植，LICENSE/NOTICE/SOURCE_MAP 随代码保留。
- 内核支持首轮直接回复、首轮 Host 已受理派发后回复、原生工具 ID 与完整批次、3 次真实请求预算、ctx 取消；不调用旧 finish_check/composer。
- 独立审查两次捕获并复现部分成功问题：多个 terminal 提前退出、后续 effect 无 receipt 错误被整体成功覆盖。均新增行为反例红绿修复，逐项 outcome/receipt 保留；独立 overlay、race/vet 复审通过。
- 可选 `employeeintegration` 模型 smoke 需要显式环境开关，默认不联网；本地 HTTP fixture 验证两场景与真实请求总数上限。尚未声称真实模型或 Host 副作用验收。
- Runtime 预检：默认预发 profile 返回 401 invalid token，已请用户本机刷新登录；私有 canary/真实 FC 与本地 Daemon 验证待有效 PAT。

## B3 部署及 B4 隔离记忆基础

- B3 开发提交 `54c4afe33` 经干净交付分支集成为 `6e1cf1fce`，已推送目标分支。实例 `3110304218` 的构建、部署和集成测试均 SUCCESS，后在人工验证阶段被外部取消；后继实例 `3110304361` 同样完成上述三阶段，停在人工验证。没有把取消状态误记成整条流水线成功。
- 新记忆包实际移植固定 GawkBot 的 learning、scope matching、recovery、distill 和 lookup/capture/promote 记录逻辑；独立表 9620–9624，与旧 Coordinator memory 无共享内容、revision 或 reset。
- 两阶段审查中的三个反例已修复：中文标题碰撞、reset 后改变模型 key 重放复活、模型冒充 Host Task/Run 来源；真实 PG race 回归通过。
- 独立提交快照在新建 `employee_memory_delivery_test` 数据库完成全量迁移；记忆整包真实 PG race、vet，以及 workspace 删除隔离/回滚回归通过。新增表的删除跟随 workspace 锁和事务。
- 扩展执行的全库 deletion manifest 检查暴露原分支大量已有未分类表；本批新增两表均已登记。该总表审计没有通过，不把定向通过描述成全库通过。
- 当前批次是存储及机制基础，不代表后台消费、模型提炼、实际内容晋级或真实发送已完成。实际模型、FC canary、持久设备滚动验证仍单独记录。

## B2 Direct 文本执行与配置基础

- Direct 采用独立 EmployeeTask/Run 加既有 queue，没有 Issue 或永久 Autopilot；幂等键恢复同一执行，按 Task 串行而不同 Task 可并发。排队输入只保留最小重放事实，不冻结完整 Autopilot 配置。
- claim/reclaim/recover 在选队列前校验服务器生成的 Runtime 白名单；Direct 回调验证 mdt 的真实 Runtime 绑定或真实 PAT Runtime owner。WebSocket 保留服务端认证种类；管理员读取权不能升级为执行权。
- 读取、列表、轨迹与实时事件遵守 Direct 私有范围；机器凭据不能借关联用户权限。已接受的执行结果保存在队列和 Run，未映射用户不向 workspace 广播原文。
- 能力 `employee-direct-v1` 为新增 wire gate，旧 Daemon/FC r1 不消费 Direct prompt；旧 Issue/Chat/Autopilot 语义保留。FC r2 候选仍待精确源码构建及真实账户 canary。
- API/UI 新增 Coordinator/Employee 处理方式；旧启用字段保留独立语义，未知值拒绝，模式和响应/主动参与设置同事务保存。此批 `EmployeeLoopReady` 尚未装配，因此 Employee 不可启用，生产入口将另批交付。
- 独立审查和干净索引快照验证：服务端与 CLI 构建成功；handler Direct/claim/配置/WS、service Direct/claim/实时、daemon/execenv/middleware 的定向 race 通过，无跳过；daemonws 整包及六个受影响包 vet 通过。首轮构建的公共 Go proxy 超时，通过镜像下载缺失依赖后构建成功。
- core 全量 1,895 测试通过；设置和详情页 40 项定向测试通过；core/views typecheck 通过。Coordinator checker 为 `PASS_STRUCTURAL_ONLY`，没有冒充模型行为验收。
- 一次扩大执行的 views 全量结果为 4,496 通过 / 8 失败：原有 Builder payload 断言 2 项、DSH 状态 2 项、ja/ko DSH 与 ASB locale parity 4 项。对应组件/测试源码与本批前 HEAD 相同，缺词在 HEAD 也存在；该全量套件未通过。
- 全量 sqlc 仍被既有 `agent.sql` 中 ambiguous id 阻塞；本批 claim/recovery 完整查询通过官方 sqlc 的窄配置生成受影响块。未手写生成 SQL，也不宣称全量生成门禁通过。
- 当前交付边界为文本执行、结果/轨迹与不可启用的配置入口。Compiler、IssueBackend、场域消费、最终回报、文件产物、等待及连续控制仍在后续批次；真实模型/真实 FC/本地设备滚动验收待完成。

## D04 Work Object Compiler

- 直接移植 GawkBot 的 `normalizeTaskDefinition`、`taskDefinitionPacketLines` 和工作包组装代码，固定源码及 LICENSE 一并保存。
- Goal 必填，交付物/成功条件/访问请求可省略；完整保留纠正和约束，AccessNeeded 不变成授权。所有材料先验证 exact scope/principal，ContextUsed 仅记录实际渲染的引用。
- 历史 unavailable/empty/available/truncated 明确区分；编译为纯函数，不调用模型、不推进游标、不消费纠正或解除停止。
- 岗位 Instructions 完整加入稳定 system 前缀，窗口和记忆仍为独立数据。独立审查和干净提交快照的 compiler/prompt race、vet 通过。
- 消费 Host 的实际 Compiler 接线将在场域入口批次交付。

## Runtime 候选准备与限制

- Direct 已推送为 `7781227e119b93646bbd6c0b309dfcf2318d74dd`，预发实例 `3110305603` 已启动。隔离记忆实例 `3110304772` 的构建、部署和集成测试均成功，停在人工验证。
- Runtime 专用分支 `codex/employee-loop-runtime-20261002` 新增独立候选 YAML，固定上述 Multica commit；35 项离线构建契约/SDK 桩测试通过。后续记录真实 Aone 构建、Template 和 sandbox smoke，不将离线测试当作 canary。
- 本机安装 CLI 为 0.3.43，不支持 `daemon probe-runtimes`，既有 compare helper 因非 JSON 输出终止。当前源码编译的 arm64 CLI 构建成功，但执行被 AMFI 签名校验终止（exit 137），对本任务临时产物重新 ad-hoc 签名仍未解除。没有关闭系统保护或替换用户已安装 CLI；本地真实 Daemon 验证尚未通过。

## B4 场域入口消费基础

- 新 `employeeentry` 保存 owner、窗口、lease/generation、模型请求/成功或失败、工具回执及结果；9640–9646 与工作区清理同批交付。
- EventRouter 首次 receipt 与消费记录通过 storage-only hook 同事务提交；旧 receipt 不因模式切换重新交给 Employee。父 workspace 锁覆盖事件写入与删除竞争。
- GawkBot 内核经真实 worker 调用：首轮回复/派发，3 次累计请求预算跨进程保留，Quiet 明确落账，自发消息零模型。逐句身份和可信场域/租户按当前证据校验。
- 独立复现并修复：失败模型轮未记账导致恢复路径漂移、Direct commit 后 journal 取消丢失 receipt、300 KiB 合法输入永久 pending。超限现在无需模型只生成一次明确反馈。
- Compiler 已接入真实 Direct 入队：原消息证据、完整 packet 和实际 ContextUsed 入库。生产 `EmployeeLoopReady` 仍关闭，等待最终结果通知链；不会仅凭 Runtime capability 放开开关。
- 独立审查通过；干净索引快照在实际 PostgreSQL 执行 employeeentry/eventrouter/handler 定向 race 全部通过，无跳过；server 构建及范围 vet 通过。公开合同新增 `docs/employee-loop.md`。
- Direct 预发实例 `3110305603` 构建、部署、集成测试 SUCCESS；回读 `/api/config` 200、fence=normal，两活副本状态正常；bootstrap 确认 9620 与 9630 已应用。
- Runtime 初次构建 `77158514` 在 Linux root 下的假执行器测试失败；已以开发提交 `0339c0333` / 交付 `e65efb8f5754dab67875b55a4a504b9b874e5799` 修复仅测试环境。专用候选继续以新的不可变 pin 重建，未使用失败产物切换。

## D10 双 Loop 记忆管理

- 新界面 list/detail/reset 显式指定 Loop，缓存键含 Loop；切换会关闭旧确认框，陈旧模式或 tenant/revision 写入返回冲突。
- Employee 页面只展示 scene 学习条目，不聚合私人记忆、不走 Coordinator 的整段编辑或关联清理；旧省略 loop GET 保持 Coordinator API 兼容。
- 重置在 workspace→agent mode→memory revision 的同事务中执行，保留去重墓碑；两个 Loop 的内容和 reset 互不影响。
- 独立审查发现并修复两个界面反例：旧章节过滤隐藏合法 Employee 内容、滚动旧响应把其他 Loop 摘要放入列表。
- 独立复审及干净索引快照真实 PG 管理/重置 race 通过；42 个界面、新 core 3 个及旧 client 7 个测试通过，core/views typecheck 和范围 vet 通过。
- 无新增 CLI `--loop` 标志；当前管理能力沿既有 HTTP 路由显式 query 参数提供。入站 `/reset-memory` 的新 Loop 分派在后续消费批次接线。

## 场域复用纠正与同步要求

- 用户明确要求按会话作为场域复用。核对远端：统一目录按 workspace/agent/provider/tenant/namespace/openConversationId 解析 scene_id；d11fe440b 以 scene 加可选个人维度复用，d97927ad6 将单聊配置链接也统一到场域。
- staffId 缺失时使用该场域的公共桶；同群共享符合现行合同，不意味着跨会话混用。此前拟议的 Direct 全部退出场域复用未提交，已撤回。
- 新增真实 PG 回归：同群不同 UID 保留场域复用、不同单聊 cid 分离、相同外部 cid 的不同租户分离、已核实 staffId 作为附加个人维度。测试通过；只读 overlay 恢复被撤回方案时观察到明确失败，证明该回归能保护用户确认的行为。
- 用户要求每批提交后由子代理核对/同步目标分支。branch_sync 已交付 D10 为 0e0a3d0630b983473a748ef02dfdf5454b2e626e，并保留 d97927ad6、934ffd146、246fdf0c5；原始 checkout 已快进，未跟踪 .omx/ 保留。
- 最终验收按用户指定冬翔→Qwen-DWS 单聊及冬翔→各种TAG群做真实 IM 测试。当前仅完成账号/会话只读定位，尚未发送新测试消息或完成 Employee E2E；旧历史消息不作为本次验收证据。

## D03 Coordinator IssueBackend

- Coordinator 的新建/续接复用原 Issue/Comment 服务，Task 定义、Issue 绑定与实际 queue/Run 记录同事务；提交后广播与唤醒，不增加第二次派单。
- 忙时 follow-up 合并与已有自动重试按真实队列、parent/retry lineage 恢复对应关系，保留原重试预算。观察层遇到尚在运行的较新 Run 只延后映射，不能撤销旧后端已合法接受的队列。
- 独立审查捕获并修复三项并发反例：最老 Issue 被锁后其他候选饥饿；较新 Run 活跃时观察器回滚旧重试；等待 Task 锁期间旧 version 引发 CAS 冲突。最后一项由主线程在真实两连接测试中复验。
- Direct 新 Run 的最终门保留在 StartRun：失败/取消的旧执行缺少终止证据时，修正或迟到结果也不能绕过恢复边界；成功后明确 Resume 和已存在 Run 重放正常。
- 干净提交快照的真实 PG task-domain 整包、IssueBackend/FollowUp/Coordinator plan 定向 race、server build 和范围 vet 通过。Policy checker 为结构检查通过，不是模型行为认证。
- 新远端另加入 human Issue steer、专用 CancelAgentTaskForSteer 与进程停止确认屏障；交付子代理会在最新基线保留合并回调和提交后 NotifySteerPredecessor→NotifyTaskEnqueued 顺序，并重新验证，不能用旧分支测试替代整合验证。
