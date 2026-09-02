# Coordinator 场域记忆：预发 e2e 剧本

设计：`docs/plans/2026-09-02-coordinator-scene-memory-takeover.md`  
落地约束：`docs/plans/2026-09-02-coordinator-scene-memory-landing.md`  
怎么发消息 / 怎么拉 SLS：skill `dws-env`、`inspect-coordinator-sls`。本文件只写 **演什么、下一轮看 SLS 的哪一段**。

只在本地能 `as` 的三张号之间演：冬翔、东翔测试号、dxxh。不要须莫、菲迪、第四人。

| 角色 | 钉钉 | 扮演 |
|---|---|---|
| 主角 | 冬翔（钉钉组织） | 委托人 |
| 测试号 | 东翔测试号（Think测试组织） | 被测 Agent（绑定号） |
| 配角 | dxxh（Think测试组织） | 被员工私聊的人；群里的人类 |

冬翔 → 测试号 单聊 cid：`cid+bEFv7ngm9n79Q1vL9HYJw==`。禁止 `+dm --to 东翔测试号`。

Agent：`e2293e9e-1e79-4926-b0e6-da4cb693add0`，workspace `sombrero-galaxy-zleb`。dws 保持预发。

---

## 怎么验证（先看这个）

记忆、事项、隔离、reset **都不看这一轮员工口头回了什么当证据**。要再发一轮，用 **下一轮 Coordinator** 的 SLS 看上下文有没有召回。

一次 `Decide()` 用 `coord_trace_id` 串起来。预发：

```bash
unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy
scripts/query-coordinator-sls.sh --env pre --cid '<cid>'
scripts/query-coordinator-sls.sh --env pre --trace '<coord_trace_id>'
```

在结果里找到 `current_message` = **下一轮那句话** 的那次，不要拿本轮教口径那次的 trace 当召回证据。

| 看哪条 event | 看什么 | 用来证 |
|---|---|---|
| `inbound_coordinator_llm_request` | `user_prompt` | 下一轮模型实际吃到的上下文：有没有场域记忆段、口径是不是上一轮教的 |
| 同上 | `scene_memory_revision`（或 prompt 里同等字段） | 开了 recall 时应非空；关 recall / reset 后应没有或未注入 |
| `inbound_coordinator_llm` | `assoc_recall` 的 arguments / result | 事项召回：cid 对不对、命中哪张 Issue、有没有被 Memory 污染 |
| `inbound_coordinator_decided` | `action` / `issue_id` | 纠正不该 comment 旧卡；办事才 issue/bind |

员工 IM 回复只证明链路活着，**不能**代替 `user_prompt` 召回。Router LLM trace 是沙箱，不是 Coordinator 上下文。

教完后要等 Flush（约 30s–2min）再发下一轮，否则下一轮 prompt 里还没有新 Text。

---

## P1 冬翔私聊员工：口径进入下一轮上下文

前置：write + recall 打开。切片 C。

**本轮（写入）** 冬翔 → 测试号：「GoalMate 是工具，不是数字员工。下次别搞错。」

本轮 SLS 只用来排除误伤：`decided` **不得** `issue_comment_add` 到任何已有 Issue。本轮 `user_prompt` 可以还没有这条口径。

**下一轮（验证召回）** 冬翔 → 测试号：「GoalMate 是什么」

下一轮 SLS（`current_message` 含「GoalMate 是什么」）：

1. `user_prompt` 里出现场域记忆，且口径是「工具 / 不是数字员工」
2. `scene_memory_revision` 非空
3. `assoc_recall` 的 `conversation_id` 是这条单聊；result **没有**因为这句纠正就带上某张旧 Issue
4. `decided` 不是把「是什么」评到旧事项上

问候对照（可选）：冬翔 → 测试号「你好」。下一轮仍问候的话，`decided=reply` 且无 `issue_id`。不拿问候当记忆证据。

---

## P2 冬翔让员工去问 dxxh：下一轮事项召回

**本轮（下单）** 冬翔 → 测试号：「帮我私聊 dxxh，问他明天上午有没有空。就说是冬翔让你问的。」

本轮 SLS：`decided action=issue`，新建事项，purpose 能看出来是问 dxxh 空闲。记下 `issue_id`。

等 Agent 自己发给 dxxh（不是 `as 测试号`）。`as 配角` 能看见东翔测试号来问明天上午。记下 **测试号↔dxxh** 的 cid。

**下一轮（验证事项召回）** dxxh → 测试号（刚那条单聊）：「明天上午可以，十点吧」

下一轮 SLS（这条 **测试号↔dxxh** cid，`current_message` 含「十点」）：

1. `assoc_recall` arguments 的 conversation 是 dxxh 这条 cid，不是冬翔那条
2. `assoc_recall` result 命中本轮记下的那张 Issue
3. `decided` 是 `issue_comment_add` 到 **那张** Issue
4. `user_prompt` **没有** 冬翔单聊里「GoalMate 是工具」那段记忆（场域不串）

---

## P3 还是 dxxh，第二件事：下一轮必须召回新卡

**本轮** 冬翔 → 测试号：「另开一个新 issue：让 dxxh 把 GoalMate 的使用说明发我。」

本轮 SLS：新 `issue_id`，和 P2 不同。

**下一轮** dxxh → 测试号（员工为这件事联系他的那条单聊）：「说明今晚发你」

下一轮 SLS：

1. `assoc_recall` result 是 P3 新卡，**不是** P2「问空闲」那张
2. `decided` 的 `issue_id` 是新卡
3. `user_prompt` 仍不串冬翔单聊的 GoalMate 记忆；P3 这句话也不该改写冬翔那条单聊的 Text

---

## P4 两个群：下一轮上下文不串

`as 配角` 建两个群，群主 dxxh，只拉测试号。消息必须 @ 测试号。

**本轮** dxxh → 群 A @测试号：「这个群里 GoalMate 是报表工具。」等 Flush。

**下一轮（隔离）** dxxh → 群 B @测试号：「GoalMate 是什么」

群 B 这一轮 SLS：

1. `conversation_id` 是群 B
2. `user_prompt` **没有**「报表工具」
3. 若有记忆段，也不是群 A 那份 revision

**再下一轮** 冬翔 → 测试号单聊：「GoalMate 是什么」

冬翔单聊这一轮 SLS：`user_prompt` 仍是 P1「工具，不是数字员工」，不是群 A「报表工具」。

---

## P5 `/reset-memory`：下一轮上下文里口径没了

**本轮** 冬翔 → 测试号：「/reset-memory」

本轮可以没有 Coordinator LLM（命令短路）。不要拿本轮当召回证据。

**下一轮** 冬翔 → 测试号：「GoalMate 是什么」

下一轮 SLS：

1. `user_prompt` **没有** P1 写入的「工具 / 不是数字员工」
2. 没有仍指向旧 Text 的 `scene_memory_revision` 注入（空 Text 的空壳可以有，但不能带旧口径）
3. `assoc_recall` 这条单聊上没有 P2/P3 的 waiting 卡

对照：dxxh 单聊或群 A 再问一句，那些 cid 的下一轮 `user_prompt` **还在**，证明只清了冬翔这条。

---

## P6 开关：下一轮还读不读记忆

**关 recall** 后 冬翔 → 测试号：「GoalMate 是什么」

这一轮就是验证轮 SLS：`user_prompt` 无场域记忆段 / 无 revision 注入。ACK 仍在。

**关 write** 后 冬翔 → 测试号：「GoalMate 其实是一只猫。」等一会儿，再发下一轮「GoalMate 是什么」。

下一轮 SLS：`user_prompt` **没有**「猫」；若 recall 仍开，最多还是关 write 之前的旧口径。

**UI**（切片 D）：inbound Tab 能看见冬翔这条单聊的 Text。关 ui 后 Tab 不展示。UI 不是召回证据，召回仍看 SLS。

---

## 切片和剧本

| 切片 | 用哪条「下一轮 SLS」过线 |
|---|---|
| A | P1 问候链路活着即可；没有记忆注入 |
| B | 还不能证召回；只能确认 Flush 后库里有 Text |
| C | P1 下一轮 prompt 有口径；P2/P3 下一轮 assoc_recall 对卡；P4/P5 下一轮不串 / 清空 |
| D | P6 UI；开关对下一轮 prompt 的影响在 C 已验 |
| E 可选 | 沙箱 Run 里 `assoc recall --conversation <cid>`；Memory GET 另议 |

---

## 旧基线（含须莫，不再重跑）

| 日期 | 谁→谁 | coord_trace_id | 结论 |
|---|---|---|---|
| 2026-09-02 | 冬翔→测试号 你好 | `7bd5ee60-…` | reply 无 issue |
| 2026-09-02 | 冬翔→测试号 GoalMate 纠正 | `b4db44f2-…` | **失败**，当事项评掉 |
| 2026-09-02 | 冬翔→测试号 约会议 | `49133eb5-…` | `action=issue` |
| 2026-09-02 | 冬翔→测试号 须莫 | `7f5655d7-…` | 新 bind。新剧本不再打须莫 |
