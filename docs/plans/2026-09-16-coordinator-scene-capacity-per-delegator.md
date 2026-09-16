# 场景容量改为「场景 × 委托人」，并给等待加上限

关联义务：`COORD.F09`（收集/容量/等待反馈）、`COORD.F01`（可信身份决定归属）。
对照契约：`f09_contrast`、`f09_scene_capacity_is_per_delegator`、`f09_capacity_wait_has_deadline`。

## 1. 触发证据

正式环境 `oa测试群`（`cidjpTppGMtZhM+I4mgBKSoqw==`，Agent `3141dfdb-d567-46ca-93d4-a754292fc16e`）。代成俊在 67 秒内连发三条 VOC 诉求，Coordinator 三条都判为工作：

| 时间（2026-09-15） | coord_trace_id | 内容 |
| --- | --- | --- |
| 21:09:13 | `0827acf3…` | 账号被封希望短信告知原因 |
| 21:09:55 | `459346e8…` | 千问办公无法单独关闭 |
| 21:10:20 | `94909f13-0fa5-47ba-9011-58b7f7409d0e` | 群文档封面占版面，希望有统一设置入口 |

第三条判完即被拒：`dispatch rejected with HTTP 409: {"error":"scene already has two in-flight matters"}`，随后 11 次 `inbound_coordinator_job_parked`（5 秒一次，21:10:20 → 21:11:17），用户收到的是「这次请求已保存，但新的执行还没开始。当前处理名额已满，空出名额后会继续。」57 秒后名额空出才开跑，21:13:59 结果送达。

同一天正式环境的 24 小时窗口里，park 事件 500 条样本中 489 条是同 Issue busy、11 条是场景容量；单个 job `f04aaec8-d9d7-4a16-8962-d7b897c1b207`（单聊「云欢」）从 09:01:10 park 到 09:21:23，共 20 分钟。park 会把 `attempt_count` 减一，因此等待本身没有上限。

三个问题：

1. 容量按「场景」计。群里一个人的两件长任务会让同群其他人的新请求一起排队。
2. 等待文案用的是内部概念（名额），用户既看不懂也无法判断要等多久。
3. `deferred`（排期或等外部输入）和长期不推进的行同样占名额，park 又没有上限，极端情况下场景可以被一直堵住。

## 2. 本次合同变化

- **容量维度**：`SceneDelegatorMaxInFlightMatters = 2`，按 `workspace × agent × scene × 委托人` 计。归属取 assoc `task_person` 边，人按 `assoc_person_alias` **双向**归一（输入 → canonical person_key → 该人的其它别名），因为 assoc 每次事件按当次输入重选 canonical key，同一个人可能在旧事项上以 staffId、在新消息里以 uid 出现。合窗里每位发言人各自结算自己的新增事项，分组也走同一份归一闭包，避免同一个人因两句话带的标识不同而拿到两份预算。单窗口一次最多起两项（`SceneWindowMaxItems`）不变。
- **续办归属**：续办已有事项成功后，按当前窗口的委托人补建 `task_person` / `task_scene` 关联。否则准入按乙的预算放行、执行却仍记在甲名下，乙可以逐个续办他人的历史事项绕过自己的上限。
- **在飞判定**：只含 `queued/dispatched/running/waiting_local_directory`；`deferred` 与 `fire_at` 在未来的行不占名额；非 running 的行超过 2 小时不再占名额，running 由 daemon 心跳自证存活，长跑合法占用（真正卡死仍由 `cmd/server/runtime_sweeper.go` 判失败）。
- **无归属兜底**：没有任何 `task_person` 边的在飞事项计入每个委托人；委托人身份不可信时整轮退回按场景计数。缺失身份不凭措辞或显示名归属。
- **等待上限**：同一窗口等待容量最多 10 分钟（从 job 创建起算），到期后本轮通过 worker context 的容量豁免直接执行，记 `inbound_coordinator_scene_capacity_waived`。豁免只针对场景容量；同 Issue busy 的 park 是重复执行保护，不因等待时长放行（`IssueCommentService` 事务内的 active-task 检查仍是最终保护）。
- **重试前置检查**：合窗保留首位发言人作为 command sender，所以重试时不再只看这个人，而是按持久化计划里未完成项各自的委托人判断：只要还有人有名额就进入逐项准入，不把甲的满额算到乙头上。

已知边界：准入仍是整窗裁决——同一个 collect 窗口（4 秒静默 / 12 秒上限内）里如果甲满额、乙有名额，整窗一起等到甲腾出名额或触发 10 分钟豁免。事故里的常见形态是各自独立的窗口，不受此影响；逐项部分放行需要改动整窗去向语义，本次不做。
- **等待文案**：改为「这条我记下了，还没开始做。你前面交代的事我还在处理，忙完接着做这条。」（同事项 busy 为「同一件事上一轮还没结束，结束后接着做。」）。不出现名额、槽位、队列等内部说法。真正开始执行仍由该工作原本的接单回复回传，不另发「开始处理」，避免同一件事两条通知。
- **滚动发布**：新的 park 原因保留旧前缀 `scene already has two in-flight matters`，所以旧二进制也认得新二进制停放的 job；新二进制用 `in-flight matters` 子串识别两种写法。混跑期间旧副本仍按旧的场景总量限制执行、也没有 10 分钟豁免，不能宣称新语义在滚动过程中立即一致。

## 3. 实现位置

- `server/internal/service/inboundcoord/window.go`：容量常量。
- `server/internal/handler/coordinator_capacity.go`：委托人解析、名额计算、豁免 context、等待到期判定。
- `server/internal/handler/inbound_coordinator_job.go`：park 前置检查与豁免注入。
- `server/internal/handler/agent_dispatch_v2_handler.go`：按委托人结算窗口新增事项的准入。
- `server/internal/handler/coordinator_wait.go`：等待说明文案。
- `server/pkg/db/queries/inbound_coordinator_job.sql` 及生成代码：`CountActiveDelegatorTasksForConversation`、`ResolveAssocPersonKeys` 与共用的在飞判定。

该文件的生成代码是手工维护的紧凑版本（仓库既有约定），本次用同一份 `.sql` 跑 sqlc 生成后逐条比对确认两者语义一致，参数顺序为 `$1 workspace`、`$2 agent`、`$3 conversation`、`$4 stale_after_secs`、`$5 person_keys`。

## 4. 评审与验证状态

Codex 评审（2026-09-16）提出 2 High、2 Medium：续办事项归属、alias 单向归一、同窗分组按原始首个 key、重试前置检查用错委托人。四项均已修复，对应测试见下。

- Host 协议测试：`TestCoordinatorSceneCapacityIsPerDelegatorAndBounded`、`TestSceneCapacityFollowsThePersonAcrossIdentifiers`、`TestCoordinatorFreshWindowJudgedAtCapacity`、`TestCoordinatorWaitTextExplainsTheWaitWithoutInternalVocabulary`、`TestSceneDelegatorDoesNotInferIntent`、`TestSceneDelegatorGroupsOneSpeakerOnce`、`TestSceneCapacityReasonMatchesLimit` 本地通过（2026-09-16）。
- `go test ./internal/handler`：61 项失败，与同一数据库上 HEAD 基线逐项一致（本地库陈旧与既有 dispatch prompt 精确匹配用例），本次改动未新增失败。
- `go test ./internal/service/inboundcoord/...` 通过；`python3 scripts/check-coordinator-policy.py` 结构检查通过。
- 预发真实会话验收：见下节，未跑完不得记为通过。

## 5. 预发验收

2026-09-16 15:09–15:19，预发群「Multica 预发群测试」（`cidVaO557dsSgYcgnvRNbwY4g==`，Agent `e2293e9e-1e79-4926-b0e6-da4cb693add0`），冬翔与 dxxh(SixSix) 真实对话，服务端为本次改动部署后的预发（run 3108479444，release 分支含本 CR 的合并提交 `6b6b8644`）。

| 时间 | 发言人 | coord_trace | 结果 |
| --- | --- | --- | --- |
| 15:09:39 | 冬翔 | `e4ceb69e` | issue，task `11e4f185` 入队 |
| 15:09:55 | 冬翔 | `ae036d10` | issue，task `5d42d5fe` 入队（冬翔名额用满） |
| 15:10:38 | 冬翔 | `eef25125` | 判为 issue 后按容量 park，原因 `scene already has two in-flight matters for this delegator` |
| 15:10:51 | 数字员工 | — | 群里引用回复冬翔：「这条我记下了，还没开始做。你前面交代的事我还在处理，忙完接着做这条。」 |
| 15:11:30 | dxxh | `bfe2f1b2` | **不受冬翔满额影响**：直接 issue，task `085450a9` 入队，15:11:46 回「收到，我来处理。」 |
| 15:17:30 | — | — | dxxh 的 task 完成，冬翔那条**继续等待**（别人的完成不释放我的名额） |
| 15:19:19 | — | — | 冬翔自己的 task `5d42d5fe` 完成 |
| 15:19:21 | — | `eef25125` | 排队的窗口自动开跑，task `813f69c3` 入队，job completed；全程 8.5 分钟未丢消息 |

结论：按「场景×委托人」计容量、新的等待文案、以及等待结束后自动接着做，都在真实预发链路上验证通过。旧代码下 dxxh 15:11:30 那条会和冬翔一起排队。

未在预发覆盖（保持 `not_run`）：10 分钟等待上限的豁免路径（本次等待 8.5 分钟未触发）、`deferred`/未来 `fire_at` 与超期未推进行的在飞判定（仅本地数据库用例覆盖）、alias 跨标识归一（同上）。
