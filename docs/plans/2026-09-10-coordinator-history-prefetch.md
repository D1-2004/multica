# Coordinator 历史预取、轻量问候免审与代问答复转告

policy_version `2026-09-10.9`，装配版本 `20`（合并 须莫 的 `codex/coordinator-history-fix-20260910`：DWS 历史环境隔离、跨组织授权续期、按需历史与时间判断之后）。现行合同见 [inbound-coordinator-loop.md](../inbound-coordinator-loop.md)。

## 触发事实（正式，2026-09-10 17:03–17:06，FDE教练 a9ce26da）

冬翔 → 菲迪 → 须莫 的代问链路：

| 时间 | 会话 | 事件 | trace |
| --- | --- | --- | --- |
| 17:03:08 | 冬翔 | 「给须莫发个消息，问他晚上几点出发」→ start_work，6.4s（round 3.4s + 审核 3.0s） | `31d82ba330634f4cbaf91117ba41b575` |
| 17:03:52 | 须莫 | 沙箱 42.8s / 10 次模型调用发出提问；又给冬翔回了一次「已经帮你问须莫了」（与协调层接单重复） | 同上 agent_task |
| 17:03:59 | 须莫 | 「6 点」→ clarify「你是想确认…还是要我同步给冬翔」，7.0s。`history_status=not_loaded`，模型没看到自己 7 秒前的提问；`dialogue` 模块因 history 未加载而未装配 | `bf0bcb544bed486ab4fbb16454dabe67` |
| 17:05:57 | 须莫 | 「回复冬翔」→ 主模型两次提出方向错乱的 continue_work（「我去问冬翔晚上几点出发」），审核两次驳回，第 4 轮退化为 report_status「冬翔委托的询问事项已完成，任务状态为 completed」；16.9s、7 次模型调用；冬翔始终没收到「6 点」 | `39427c330a2b4182a5b10fe44503355f` |

同日预发 `cb7dacb19ec246c584971e148ea8c11f`：群消息「帮我算 37×49」8 轮 19s，历史读取失败后模型连续 6 次重复 `context_read(kind=history)`，最终 silence。

同日 89 条单轮扫描（正式 49 / 预发 40）：耗时中位数 5.1s，P90 13.2s，>8s 占 23.6%；每轮模型调用均值 4.48、最少 2；失败标签：慢>15s 19、为发一条消息派沙箱 15、重复确认 13、协议空转 10、bot 互刷 9、内部术语泄漏 6、审核拦掉正确方案 3。

## 改动

1. **Host 历史预取**（`history_prefetch.go`，`loop.go`）：数字员工/机器人入站、CID 与 DWS 身份齐全、history 未加载、非主动群未@消息时，与 assoc 预取并行读取有界钉钉历史，上限 2.5s。成功/空按原 history 快照登记；超时保持 not_loaded，其它失败 unavailable。首个模型请求同时装配 `dialogue`。
2. **重复读历史直接复用**：同一轮再次 `context_read(kind=history)` 且快照已在，返回复用提示，不重读、不覆盖成功快照；basis=answer 前置反馈路径不变。
3. **轻量问候免审（已撤回）**：曾实现单聊短问候的 `acknowledge(greeting|thanks)` 跳过 finish_check。Codex 审查指出 Host 无法证明 ≤40 字的消息不夹带请求（「你好，帮我查一下昨天的日志」被模型标成 greeting 就会直接吞掉），与「不漏请求」优先级冲突，撤回；审查成本改由更小的审查模型/更短审查输入解决。
4. **规则**：`voice@3` 进度不用平台状态值/ID；`channel@2` 单聊对方纯确认/收尾不再回；`inbound@21` 代问对象的答复是同一件事的推进，须转告委托人才算完；`finish_check_work@16` 该转告判 same_deliverable。
5. **执行器任务书**：接待文案已由 Host 发出，执行器不再向委托人发确认/进度，只在有结果或失败时回报。

## 验证状态

| 层 | 状态 |
| --- | --- |
| 结构检查 `scripts/check-coordinator-policy.py` | PASS_STRUCTURAL_ONLY |
| `go test ./internal/service/inboundcoord/` | 通过（新增 `TestDecidePrefetchesDWSHistoryBeforeFirstModelCall`、`TestDecideRepeatedHistoryReadReusesPrefetchedSnapshot`；`TestDecideDWSHistoryTimeoutStillRunsLLM` 的 loader 次数按预取语义改为 2） |
| Codex 审查 | 4 项发现：免审吞请求（撤回）、超时被判 unavailable（已按预取 context 状态判定）、任务书断言已送达（已改为 Host 负责）、旧 trace 证据版本被改写（已恢复 .4） |
| 预发 e2e（冬翔 → 测试号 → dxxh 同款代问链路） | 通过，见下 |

## 预发 e2e 回填（2026-09-10 18:50–18:54，run 3107698664，agent e2293e9e）

| 时间 | 会话 | 事件 | 证据 |
| --- | --- | --- | --- |
| 18:50:03 | 冬翔→测试号 `cid+bEFv7ngm9n79Q1vL9HYJw==` | 「给 dxxh 发个消息，问他今晚几点出发。E2E-RELAY-7731」→ 18:50:21 回「我会向 dxxh 发送消息，询问今晚几点出发。」 | trace `5ffa7faf935a482ab629004f7bf08a5a`：history_prefetch loaded 2161ms，1 轮，start_work，审核 allow，8.5s |
| 18:51:27 | 测试号→dxxh `cidY2dbfQCZpRueOTXtopDhjvML5zzQGOkDHSQfIeaPP4g=` | 沙箱发出「冬翔托我问你一下：今晚几点出发？时间定好后回复我，我转告冬翔。」 | 同上 agent_task |
| 18:52:54 | dxxh | 「6 点」→ 18:53:29 回「收到，我会把“6 点”转告给冬翔。」 | trace `9d420681c16f4b55af5bf607cbbed9dd`：history_prefetch loaded 835ms、dingtalk_history_count=10，continue_work(basis=answer)，审核 allow（「Relay reply advances same deliverable」），无 clarify；24.9s，其中 round.1 模型延迟 17.5s |
| 18:54:10 | 冬翔 | 收到「你让我问 dxxh 今晚几点出发，他回复了：6 点出发。」 | 钉钉回读（as 主角） |

正式环境 17:03 链路中断掉的「答复转告委托人」一环在预发闭合。遗留：转告仍走沙箱（约 40s）；模型侧延迟波动不由本改动控制。

## 第二轮：重试预算与工具契约（policy 2026-09-10.9 / assembly 20）

冬翔明确「工具报错后机械重试到 8 轮是不对的」，并要求「上下文里先把工具需要什么参数、怎么调用定义充分，不然都走 hint 会浪费」。

触发证据（当日正式 trace 的重复错误统计）：

| trace | 重复错误 | 次数 |
| --- | --- | --- |
| `342b8b1cfe8040a29f79e4a613a59ecf` | `decline constraint_quote must quote an actual supplied boundary` | 35 |
| 同上 | 把 `decline` 当工具名调用 | 9 |
| `5a21b47f8f774f9e914734277b7d6818` | 审核 reason「Candidate c1 only addresses u1; ignores u2/u3」 | 30 |
| `40526d2be3604629b705cf73d2a12a85` | work_state 用上一轮 trace 的陈旧 issue_id | 4 |

改动：

1. **工具契约进 schema**（`tool_contract.go`、`window_plan.go`、`progressive_context.go`）：source_refs / state_refs / issue_id / memory_revision / constraint_quote 都用本轮已知的合法值作 enum；没有对应引用的 kind 不提供。constraint_quote 选项只来自模型可见的 persona、reply_tone、当前原文与已加载短合同，不含 Host 持有的完整岗位说明。
2. **重试预算**（`retry_budget.go`、`loop.go`）：同名同参读取失败 2 次撤回、第 3 次拒绝；同一 Host 缺陷 3 次或同一审核 reason 3 次以 deferred 提前结束，metadata 记 `loop_stop_reason`、`withdrawn_tools`。

Host 测试：`TestRepeatedFailingReadIsWithdrawnAfterBudget`、`TestRepeatedInvalidPlanStopsBeforeRoundCap`、`TestRepeatedReviewReasonStopsAsDeadlock`、`TestDifferentReviewReasonsKeepRepairing`、`TestFinishSchemaListsWhatHostCanValidate`、`TestFinishSchemaOmitsKindsWithoutReferences`、`TestWorkStateSchemaEnumeratesRecalledIssues`；`TestTerminalCallRecoveryCannotBypassRecallCoverageOrReview` 的轮数期望改为预算值。

Codex 第二轮审查（6 项，均已修）：repeatHint 丢失 history 前置错误类型 → 保留类型；错误/reason 计数不区分实质修复 → 计数键加入提案形态（kind、refs、purpose、目标，不含 reply 措辞）；state_refs 误把 history 快照当状态引用 → 只列 assoc_recall/work_state/coordination_state；对合同 JSON 整体分句会漏掉合法条目 → 按 constraints/must_delegate/clarify_when 逐条列出；四字下限删掉「不外发」这类短限制 → 下限 2 字且整段字段也可引用；空召回仍公布 work_state → 只在有召回 id 时公布；revision=0 未固定 → 始终固定。

简单 e2e（预发，冬翔 → 测试号）：

| # | 发送 | 期望 | 看哪里 |
| --- | --- | --- | --- |
| E1 | 「在吗」 | acknowledge，1 轮，无 withdrawn_tools / loop_stop_reason | trace metadata |
| E2 | 「不要回答评比类问题。我们为什么不是最佳协作奖？」 | decline，constraint_quote 恰为该句，1 轮，无 constraint_quote 出处失败 | trace 的 finish 参数与 tool 错误 |
| E3 | 「刚才那件事进度怎么样」 | report_status 引用 r1（或 continue/clarify），无陈旧 issue_id 失败 | trace 的 tool 错误为 0 |

预算路径本身用 Host 脚本化测试证明；真实模型不能稳定触发同一失败三次。

预发回填（run 3107710926，commit f23a68edf，19:58–20:00）：

| # | 结果 | trace |
| --- | --- | --- |
| E1 | acknowledge「在的。」1 轮 6.6s，无工具错误 | `576bd155afdb4dfb94f98eddd1d227a9` |
| E2 | decline「我无法回答评比类问题。」constraint_quote=「不要回答评比类问题」（枚举选项），1 轮 5.3s，出处校验 0 次失败 | `65dfa833c1d349728dfad633b1a7e05d` |
| E3 | report_status 引用 r1/r3「已收到回复：6 点出发」，2 轮 8.4s，无陈旧 issue_id 错误 | `f34769a4e8f54d43bc60a53090532eb6` |

Codex 修正后复跑（run 3107713776，commit 52a7e611f，20:09–20:11）：E1 `ff01df20053440c3aeb1566603c7645b` 6.1s；E2 `23b09837ef5549f3a701b146e2d41a1a` 4.5s，constraint_quote 仍为枚举句；E3 `045f87f535814bb5855feba84a1f5c74` 7.1s，state_refs r1/r3。三条均无工具错误、无 withdrawn_tools / loop_stop_reason。

## 第三轮：确定性停止不再沉默（2026-09-11）

巡检（`docs/reports/2026-09-10-feidi-daily-inspection.md`）对照后，正式环境仍未修的 Coordinator 问题里，冬翔要求先修「deferred 等于沉默」。

事实：`agent_dispatch_v2_handler.go` 对 deferred 返回 503，`inbound_coordinator_job.go` 按 1/2/4/8/16/16 秒重投最多 6 次后 fail，用户什么都收不到。`342b8b1cfe8040a29f79e4a613a59ecf`（景霖群里 @ 问评比）6 次 deferred、48 次 generation、零出站。第二轮的重试预算只把每次从 8 轮压到 3 轮，仍是 6 次同样失败。

改动（`loop_stop_fallback.go`、`loop.go`、`coordinator.go`）：轮数耗尽改记 `rounds_exhausted`；`rounds_exhausted` / `repeated_invalid_plan` / `review_deadlock` 三种确定性停止在 Decide 层转成终态：被 @ 或单聊 → 固定文案回复「这条我没接住，麻烦再说一遍或者换个说法，我再看。」，未被 @ → silence；不带工作项、不经审核，Reason 保留停止原因，trace `loop_stop_fallback`。模型/审核/存储错误和 task_finished 仍 deferred 交 worker 重试。Host 测试 `TestDeterministicLoopStopRepliesWhenAddressed`、`TestDeterministicLoopStopSilencesUnaddressedTurn`、`TestTransientLoopFailureStaysDeferred`、`TestTaskFinishedLoopStopStaysDeferred`；`TestDecideTraceRecordsExhaustedRoundsAsLoopError` 改为期望兜底回复且根 span 仍为 ERROR。真实模型无法稳定触发三次同一失败，兜底路径不做线上 e2e。

### 审核放行错误提案的两个 case（未修）

- `bf0bcb544bed486ab4fbb16454dabe67`「6 点」：审核输入里 `history_status=not_loaded`，但 `read_evidence.r1` 已给出 issue「向须莫发送消息询问晚上出发时间」`status=todo`、`waiting_on` 含本会话。审核仍 allow clarify，reason「Intent ambiguous; clarify is appropriate.」。`finish_check@9` 写明「A necessary clarify question handles that request this turn: allow it」，把 clarify 当成永远安全的动作；没有规则说「窗口原文正是本场景 waiting_on 事项的答复时，clarify 是覆盖缺陷」。
- `39427c330a2b4182a5b10fe44503355f`「回复冬翔」：候选 report_status「冬翔委托的询问事项已完成，任务状态为 completed」，state_refs=r2（问须莫的沙箱任务 completed）。审核 allow，reason「No new work requested」——把「回复冬翔」这个请求当成 context update，把「提问任务完成」当成「委托事项完成」。history 未加载，看不到「6 点」。

两条共同点：审核缺证据时按提案自身通顺度放行。方向：history 未加载且召回项 waiting_on 含本场景时，clarify / report_status 一律 revise 并要求补读；report_status 的 state_refs 若是执行任务状态而非交付状态，不能作「事项完成」证据。

### 探针互刷 trace 的真正原因（`5a21b47f8f774f9e914734277b7d6818`）

不是「缺发送方 bot 标记」。窗口 u1「本次没有生成有效回答…」、u2「默认响应者：default。至少保留一位。 !dev」、u3「收到，我这边也没有新的待办…」都由夏东翔账号发出（`sender_id` 同一个人），Host 无法按发送方区分。第 1、2 轮 Host 以「window has unhandled source」驳回；第 3 轮模型提案 a1 acknowledge(u1)、a2 ignore(u2, reason 系统配置指令)、a3 acknowledge(u3)，Host 覆盖校验通过；审核却返回 `missing_source_refs=[u2,u3]`、reason「Candidate c1 only addresses u1; ignores u2 and u3」。审核只按 `candidate_quote_ref=c1` 这一条回复判覆盖，无视 candidate.actions 里 a2/a3 的存在；模型随后 5 次原样重交（审核缓存命中），耗尽 8 轮，6 次 job 重投共 30 次同 reason。方向：Host 把按 uN 计算的覆盖表（source_ref → action_ref/kind）写进审核输入，`missing_source_refs` 的 schema 说明改为「已有动作但不足以处理该请求的 uN」，并对「reason 声称 ignores uN 而 uN 实际有非 ignore 动作」的裁决按无效审核处理。

### 审核轮前缀缓存（第二步，2026-09-11）

冬翔看了 `d60c8a6368874ef3ae6c97410ed47db0`（正式，「Hi」→「在的。」6.1s：主判断 2.3s / 8.7k token，审核 3.1s / 9.7k token，两次都无 cache_read）后要求先做「让审核轮的 prompt cache 命中」。

对已拉的正式 trace 统计 cache_read：同一 trace 内 round.2 起命中 4.6k–5.1k（system prompt），审核只在 `39427c33` 的 finish_check.2 命中 1152 token，正好是主判断与审核共用的 core 模块；跨轮从未命中，包括 `bf0bcb54` 与 `39427c33` 这对间隔 2 分钟、system prompt 哈希完全相同（9e9e31b7）的轮次。原因：审核背景是一个 map 序列化的 JSON，键按字母排序，`conversation_id`、`history_before` 排在 `job_policy` 前面，前缀在几百 token 处就断。

改动：审核请求拆成 system、Agent 配置段、本轮段、提案段四条消息（见合同文档「审核请求的分段」），字段不增不减。测试 `TestFinishCheckConfigurationSegmentIsStableAcrossTurns`：同一 Agent 两轮不同会话/历史/记忆/读取，前两段字节相同，本轮字段不泄漏进配置段。

待验证：模型网关的前缀缓存是否跨请求生效。主判断的 system prompt 跨轮哈希相同却也没命中，说明网关侧缓存可能是实例本地或存活期很短；预发上连续两轮看 finish_check 的 cache_read 即可判断，不命中就要走显式缓存标记（取决于网关是否支持）。

## 未做与建议

- 转达不做原语（冬翔 2026-09-11 决定）：代问答复若还涉及事项推进，仍走沙箱 Issue；纯代为通知的场景很少，不为它加 Host 动作。
- 审核缺证据放行（上节两个 case）与审核按单条候选判覆盖（5a21b47f）未修；需要更短、更固定的审核输入，把 Host 已算出的事实（覆盖表、waiting_on、delivery 状态）写进去。
- 执行器不回读外部状态就报完成（签名「已更新」、日报「Token 任务未执行」）是执行器任务书问题。
### 记忆 worker 无限重试（已修）

`4aceb4ae2f5f9342a88525fc28e5dd47`：口香糖小队 flush attempt 130，`dws_history_range` 报 `server_error_code=130003`。用教练身份在正式环境复现：`OpendId is not in conversation`，菲迪已不在该群，任何区间都同样报错，不是抖动。原 worker 只对 AUTH/ROUTE_INACTIVE/CONFIG 封禁，其余一律 5s→15min 退避重试，attempt_count 只在成功时归零，所以每天约 96 次白跑。

改动（`dwsclient/client.go`、`scenememory/model.go`、`flush.go`、`worker.go`）：130003 分类为终态 `NOT_IN_CONVERSATION`，首次即封禁；其它 `category=api, reason=business_error` 的历史拒绝在 `attempt_count` 达到 12（约 1.5h 退避）后封禁；超时/传输错误仍无上限退避。封禁时打 error 级 SLS `scene_memory_blocked`（scene、agent、attempt、error_code、history_error）。解封沿用已有机制：该场景来新的入站触发时 `UpsertSceneMemoryDirty` 清 `blocked_at`，即重新进群后自动恢复。

Codex 审查 7 项后的修正：`attempt_count` 是「自上次提交分页以来的领取次数」而非按错误码的连击，文档与注释按此表述（12 次无进展且最新一次是业务拒绝才封）；跨组织授权拒绝 `CrossOrgPermissionDenied` 排除在上限外（Coordinator 下次读取会续期）；`BlockSceneMemory` 加 `dirty_revision = lease_target_dirty_revision` 守卫，flush 期间来了新触发则封禁为空操作，不会把刚唤醒的场景再封回去；只在 Block 真正写入后才打 `scene_memory_blocked`，丢 lease 不告警；flush trace 在决定封禁时记 `block_decided/block_attempt/block_terminal` 并保持 error 级；dws CLI 超时时即使 stdout 已有业务错误 JSON 也按超时分类。未做：核对查询身份与收信身份是否一致（130003 也可能来自查错账号），trace 里目前没有实际查询 UID。

测试：`TestClassifyHistoryMapsMembershipLossToTerminalCode`、`TestBlockAfterFailureCapsRepeatingBusinessErrors`（纯函数）；`TestBlockSkipsClaimSupersededByNewTrigger`、`TestBlockStillWorksWithoutNewTrigger`（需 DB，用 `multica_coordinator_progressive_0907` 跑通全套 109 个用例）。
