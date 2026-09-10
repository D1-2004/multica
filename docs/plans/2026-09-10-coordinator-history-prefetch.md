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

## 未做与建议

- 协调层没有「转达」原语：代问答复仍要再派一次沙箱（每次 30–40s）。建议给 Host 增加受限的同场景/委托人回传动作，或让执行器在同一任务里等待答复后转告。
- 重试预算与工具契约（第二轮）只消除「同一错误反复」和「模型猜合法值」；审核对不同提案给出前后矛盾的裁决（a0871239）仍未处理，需要更短、更固定的审核输入或更小的审核模型。bot 对 bot 刷屏没有 Host 级刹车。
- 入站事件没有「发送方是数字员工」标记，无法在 Host 层识别 bot 互刷。
