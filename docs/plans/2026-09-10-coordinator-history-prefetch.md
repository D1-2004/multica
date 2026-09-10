# Coordinator 历史预取、轻量问候免审与代问答复转告

policy_version `2026-09-10.5`，装配版本 `16`。现行合同见 [inbound-coordinator-loop.md](../inbound-coordinator-loop.md)。

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
| 预发 e2e（冬翔 → 测试号 → dxxh 同款代问链路） | 见本文末尾回填 |

## 未做与建议

- 协调层没有「转达」原语：代问答复仍要再派一次沙箱（每次 30–40s）。建议给 Host 增加受限的同场景/委托人回传动作，或让执行器在同一任务里等待答复后转告。
- 审核一致性、工具报错后的机械重试（8 轮上限）与 bot 对 bot 刷屏没有 Host 级刹车，本轮只以规则和免审窄化，未改协议。
- 入站事件没有「发送方是数字员工」标记，无法在 Host 层识别 bot 互刷。
