# Coordinator 场域记忆：预发 e2e 剧本

设计：`docs/plans/2026-09-02-coordinator-scene-memory-takeover.md`  
落地约束：`docs/plans/2026-09-02-coordinator-scene-memory-landing.md`  
dws 只负责拟人触发。闭环要自己把下面每一面都看完。怎么发消息：skill `dws-env`。怎么拉 Coordinator SLS：`inspect-coordinator-sls`。沙箱：`inspect-fde-llm-trace`。发布：`aone-deploy`。Multica Issue/任务：`dta-ops-multica`。

只在本地能 `as` 的三张号之间演：冬翔、东翔测试号、dxxh。不要须莫、**不要菲迪/教练**、不要第四人。

| 角色 | 钉钉 | 扮演 |
|---|---|---|
| 主角 | 冬翔（钉钉组织） | 委托人 |
| 测试号 | 东翔测试号（Think测试组织） | 被测 Agent（绑定号） |
| 配角 | dxxh（Think测试组织） | 被员工私聊的人；群里的人类 |

冬翔 → 测试号 单聊 cid：`cid+bEFv7ngm9n79Q1vL9HYJw==`。禁止 `+dm --to 东翔测试号`。

Agent：`e2293e9e-1e79-4926-b0e6-da4cb693add0`，workspace `sombrero-galaxy-zleb`。dws 保持预发。

---

## 观察面（dws 之外必须自己看）

触发永远是 dws。证据永远在别的系统。每一面只回答它能回答的问题，不要互相顶替。

| 面 | 看什么 | 能证明 | 不能证明 |
|---|---|---|---|
| **1. 钉钉回读** | `as` 同一身份 `chat message list` 该 cid | 人有没有把话发出去；员工/对方有没有一条可见消息 | Coordinator 读了什么；记忆写没写 |
| **2. Coordinator SLS** | 预发 tag `acni_ag_dt-fde-multica_default_prehost`；`inbound_coordinator_llm_request` / `_llm` / `_decided` | **下一轮** `user_prompt` 有没有召回记忆；`assoc_recall` 命中哪张卡；`decided` 的 action | 钉钉是否投递；沙箱工具是否真发 |
| **3. 预发库 `scene_memory`** | PolarDB `multica_pre`，按 `agent_id + scene_key=cid` | Text、revision、dirty/flushed、lease、blocked、`bootstrapped_at`、cursor | 下一轮模型有没有读到（那是 SLS） |
| **4. 预发库 `agent`** | 四开关列 | write/recall/ui/bootstrap 实际值 | UI 看起来开了但库没写 |
| **5. 后端 log tail** | `GET /api/internal/logs/tail?file=backend&contains=scene_memory` | MarkDirty / claimed / retry / block / flush 有没有发生、耗时 | prompt 内容 |
| **6. Multica Issue** | `issue-get` / `comment-list` / `issue-timeline` | 有没有建卡、评论打在哪张、有没有被纠正污染 | 记忆召回 |
| **7. assoc HTTP** | `GET /api/assoc/recall?conversation=` / `events` | 该 cid 绑了哪些 Issue | 记忆 Text |
| **8. Router 观测** | `environment=staging`，agentId=`e2293e9e-…` | 沙箱 Run 是否起来、dispatch 的 cid、工具有没有 `dws chat send` | Coordinator 上下文（禁止拿 LLM trace 顶 SLS） |
| **9. 任务轨迹** | `agent-tasks` / `task-trace` | Run 状态、失败原因、有没有 `assoc recall --conversation` | IM 送达 |
| **10. Workbench** | inbound Tab / 开关 | D 之后人看得见 | 召回（仍以 SLS 为准） |
| **11. 流水线** | 预发部署 SUCCESS | 当前二进制/迁移已上去 | 功能正确 |

预发库从 env-vars 的 `DATABASE_URL` 来，先 unset 代理。log tail 用 `MULTICA_LOG_TAIL_TOKEN`。Router 预发是 `staging` 不是 production。

### 实现必须打的点（B/C 合入时写进 slog，否则闭环看不全）

SLS/log 里要能搜到（event 名写全称）：

| event | 关键字段 |
|---|---|
| `scene_memory_mark_dirty` | workspace/agent/scene_key/dirty_revision/idempotency |
| `scene_memory_claimed` | 已有 |
| `scene_memory_flush_commit` | scene_key、memory_revision、cursor、耗时 |
| `scene_memory_recall_injected` | scene_key、memory_revision、code_points；**不要**把 Text 打进 SLS |
| `scene_memory_reset` | scene_key、旧 revision |

`user_prompt` 仍是召回的主证据；上面这些用来证「写路径发生了」，和 prompt 对得上。

### 闭环顺序（每一步剧本都走完）

```text
Aone 预发部署 SUCCESS
  → 读库确认开关
  → dws 触发（本轮）
  → 钉钉回读 sendStatus + 会话 list
  → 本轮 SLS decided（排除误伤）
  → 库 dirty/flush（记忆类要等）
  → log tail 有 claimed/commit
  → dws 触发（下一轮）
  → 下一轮 SLS user_prompt / assoc_recall   ← 召回证据在这里
  → 若 decided=issue：Multica issue + assoc + Router trace + 钉钉回读员工是否真发给了 dxxh
```

记忆类：本轮只证明「没当事项」+「库写上了」。**召回只认下一轮 `user_prompt`。**  
事项类：本轮记下 `issue_id`。**续接只认下一轮 `assoc_recall` result。**  
员工口头回复和 Issue 自述都不是送达、也不是召回。

---

## P1 冬翔私聊员工：口径进入下一轮上下文

前置：write + recall 打开。切片 C。

**本轮（写入）** 冬翔 → 测试号：「GoalMate 是工具，不是数字员工。下次别搞错。」

| 面 | 过线 |
|---|---|
| 钉钉 | SUCCESS；测试号有回复（只证明活着） |
| 本轮 SLS `decided` | **不得** `issue_comment_add` |
| 库 | 该 `scene_key` dirty 上涨；Flush 后 `memory_text` 含「工具」，`memory_revision` +1 |
| log | `mark_dirty` 然后 `flush_commit` |

本轮 `user_prompt` 可以还没有这条口径。

**下一轮（验证召回）** 冬翔 → 测试号：「GoalMate 是什么」

| 面 | 过线 |
|---|---|
| 下一轮 SLS `user_prompt` | 有场域记忆段，口径是「工具 / 不是数字员工」 |
| 下一轮 `scene_memory_recall_injected` / revision | 非空，且对得上库里的 revision |
| 下一轮 `assoc_recall` | cid 是这条单聊；没有因为这句纠正带上旧 Issue |
| 下一轮 `decided` | 不是把「是什么」评到旧事项 |

问候对照：冬翔 → 测试号「你好」→ `decided=reply` 无 issue_id。不拿问候当记忆证据。

---

## P2 冬翔让员工去问 dxxh：下一轮事项召回

**本轮（下单）** 冬翔 → 测试号：「帮我私聊 dxxh，问他明天上午有没有空。就说是冬翔让你问的。」

| 面 | 过线 |
|---|---|
| 本轮 SLS `decided` | `action=issue`，purpose 是问 dxxh 空闲。记下 `issue_id` |
| Multica | `issue-get` 能拿到这张卡；评论里没有把 GoalMate 纠正写进去 |
| assoc | 冬翔这条 cid 绑上这张卡 |
| Router `staging` | 出现对应 BUSINESS_TASK；dispatch 带出站 cid |
| 钉钉 `as 配角` | 测试号真的问了明天上午。记下 **测试号↔dxxh** cid |
| 库 | 冬翔 cid 的 Text 不出现在 dxxh cid 那一行 |

**下一轮（事项召回）** dxxh → 测试号：「明天上午可以，十点吧」

| 面 | 过线 |
|---|---|
| 下一轮 SLS `assoc_recall` | conversation = dxxh 这条 cid；result 命中记下的 `issue_id` |
| 下一轮 `user_prompt` | **没有** 冬翔单聊的「GoalMate 是工具」 |
| 下一轮 `decided` | `issue_comment_add` 到那张卡 |
| Multica `comment-list` | 新评论在那张卡上，内容与「十点」对应 |
| 任务轨迹 | 若又开 Run，`task-trace` 不是把记忆当 issue_id |

---

## P3 还是 dxxh，第二件事：下一轮必须召回新卡

**本轮** 冬翔 → 测试号：「另开一个新 issue：让 dxxh 把 GoalMate 的使用说明发我。」

记下新 `issue_id`，必须 ≠ P2。

**下一轮** dxxh → 测试号：「说明今晚发你」

| 面 | 过线 |
|---|---|
| 下一轮 `assoc_recall` | 新卡，不是 P2 问空闲 |
| `decided.issue_id` | 新卡 |
| Multica | 评论在新卡；P2 那张卡没有这条「说明」 |
| 库 | 冬翔 cid 的 Text 不被 P3 改写 |

---

## P4 两个群：下一轮上下文不串

`as 配角` 建两个群，群主 dxxh，只拉测试号。消息必须 @ 测试号。

**本轮** dxxh → 群 A @测试号：「这个群里 GoalMate 是报表工具。」等 Flush。库里群 A 行有「报表工具」。

**下一轮** dxxh → 群 B @测试号：「GoalMate 是什么」

| 面 | 过线 |
|---|---|
| 群 B SLS `conversation_id` | 群 B |
| 群 B `user_prompt` | **没有**「报表工具」 |
| 库 | 群 B 行 ≠ 群 A 行（不同 `scene_key`） |

**再下一轮** 冬翔 → 测试号单聊：「GoalMate 是什么」→ 冬翔 cid 的 `user_prompt` 仍是 P1「不是数字员工」，不是「报表工具」。

---

## P5 `/reset-memory`：下一轮上下文里口径没了

**本轮** 冬翔 → 测试号：「/reset-memory」

| 面 | 过线 |
|---|---|
| 库 | 冬翔 cid：Text 空，`bootstrapped_at` 在，cursor≈now |
| assoc | 冬翔 cid 无 waiting 卡 |
| log | `scene_memory_reset` |
| 本轮 SLS | 可以没有 LLM。不当召回证据 |

**下一轮** 冬翔 → 测试号：「GoalMate 是什么」→ `user_prompt` 没有「工具 / 不是数字员工」。

对照下一轮：dxxh 或群 A 再问一句，那些 cid 的 `user_prompt` 还在。

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
