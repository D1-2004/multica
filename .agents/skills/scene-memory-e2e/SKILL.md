---
name: scene-memory-e2e
description: >
  Coordinator Scene Memory 的目标、切片、预发 e2e 剧本和发布后验证。剧本格式是
  谁发给谁、说什么、然后查什么。只在冬翔、东翔测试号、dxxh 三张号之间演。
  记忆召回、让员工私聊 dxxh、群隔离、/reset-memory。用户说「场域记忆 e2e」
  「跑剧本」「验证 scene memory」或 /scene-memory-e2e 时必须用。
compatibility: Requires dws-env, dws CLI on 预发, logged-in a1 and normandy.
---

# Scene Memory e2e

dws 保持预发。身份走 `dws-env` 的 `as`。SLS 走 `inspect-coordinator-sls`。发布走 `aone-deploy`。跑次记录在 [references/plays.md](references/plays.md)。Daemon 可选召回在 [references/daemon-recall.md](references/daemon-recall.md)。

## 人（只这三张号，本地都能 as）

| 角色 | 钉钉是谁 | 本剧本里是谁 |
|---|---|---|
| 主角 | 冬翔（钉钉组织） | 委托人 |
| 测试号 | 东翔测试号（Think测试组织） | **Agent 绑定的数字员工** |
| 配角 | dxxh（Think测试组织，与测试号同组织） | 被员工私聊的人；群里的人类 |

硬约束：每一步的发送方和接收方只能是这三个人。不要须莫、菲迪、约冬翔自己、或任何第四人。群也只拉 **dxxh + 东翔测试号**。

禁止 `+dm --to 东翔测试号`。主角 → 测试号 用已有单聊 cid：`cid+bEFv7ngm9n79Q1vL9HYJw==`。

```text
DWS='python3 $HOME/.agents/skills/dws-env/scripts/dws_env.py'
发:  $DWS as <角色> -- chat +messages-send --as user --chat-id <cid> --text '…' --yes --format json
查发出: 同一身份 +messages-query-send-status，必须 SUCCESS
查回复: 同一身份把该 cid 最近消息拉出来，看测试号有没有回
查裁决: scripts/query-coordinator-sls.sh --env pre --cid '<cid>'
```

每步都是：**谁 → 发给谁 → 说什么 → 等 SUCCESS → 查这几样**。

---

## 剧本

全程只有冬翔、测试号、dxxh。

### P1 冬翔私聊员工：纠正进记忆，不进事项

前置：write+recall 打开。切片 **C**。单聊 cid：`cid+bEFv7ngm9n79Q1vL9HYJw==`。

**P1.1 问候**

- 谁→谁：冬翔 → 测试号单聊
- 说：「你好」
- 查：发出 SUCCESS；测试号回了；SLS `action=reply`，没有 `issue_id`

**P1.2 纠正（必须当记忆，不当事项）**

- 谁→谁：冬翔 → 测试号同一单聊
- 说：「GoalMate 是工具，不是数字员工。下次别搞错。」
- 查：
  1. 测试号正常回了
  2. SLS **不得** `issue_comment_add` 到任何已有 Issue
  3. 等 Flush 后，这条单聊的 Scene Text 有「GoalMate = 工具」

**P1.3 召回**

- 谁→谁：冬翔 → 测试号同一单聊
- 说：「GoalMate 是什么」
- 查：按「工具」答；SLS 有非空 `scene_memory_revision`，日志里没有 Text 正文；`assoc_recall` 不从 Memory 拿出 issue_id

---

### P2 冬翔让员工去私聊 dxxh（A 让 Agent 给 B 发）

**P2.1 下单**

- 谁→谁：冬翔 → 测试号单聊
- 说：「帮我私聊 dxxh，问他明天上午有没有空。就说是冬翔让你问的。」
- 查：SLS `action=issue`，新建事项，purpose 类似「冬翔委托：问 dxxh 明天上午是否有空」。纠正口径不得写进这张卡。

**P2.2 员工真发给了 dxxh**

- 谁→谁：测试号（Agent 沙箱自己发，不是 `as 测试号`）→ dxxh 单聊
- 我们不发；等 Agent
- 查：
  1. `as 配角` 会话列表里有「东翔测试号」来问明天上午
  2. 这条 **测试号↔dxxh** cid 的 `assoc_recall` 命中 P2.1 那张 Issue
  3. 冬翔↔测试号 的 Scene Text 不出现在 测试号↔dxxh 的 Memory 里

**P2.3 dxxh 回员工，应续同一事项**

- 谁→谁：dxxh → 测试号（P2.2 那条单聊）
- 说：「明天上午可以，十点吧」
- 查：SLS `assoc_recall` 命中 P2.1 的 Issue；`issue_comment_add` 到**那张**卡，不是冬翔那条单聊里的旧评论

---

### P3 同一对 dxxh，第二件事必须新开事项

- 谁→谁：冬翔 → 测试号单聊
- 说：「另开一个新 issue：让 dxxh 把 GoalMate 的使用说明发我。」
- 查：
  1. 新 purpose / 新 Issue，**不并进** P2.1「问空闲」那张卡
  2. Memory 不提供 issue_id
  3. 若 Agent 再私聊 dxxh，`as 配角` 能看到新问题（使用说明），且这条出站 cid 的 recall 是新卡不是 P2.1

可选跟一拍：dxxh → 测试号「说明今晚发你」→ 评论打在 P3 新卡上，不是 P2.1。

---

### P4 两个群（只含 dxxh 和测试号），口径不串

`as 配角` 建群，群主是 dxxh，只拉测试号。必须 @ 测试号。

```bash
$DWS as 配角 -- chat +chat-create --name "sm-e2e-A-<stamp>" --member-query "东翔测试" --yes --format json
$DWS as 配角 -- chat +chat-create --name "sm-e2e-B-<stamp>" --member-query "东翔测试" --yes --format json
```

**P4.1** dxxh → 群 A @测试号：「这个群里 GoalMate 是报表工具。」→ 只有群 A 的 Text 有这句话。

**P4.2** dxxh → 群 B @测试号：「GoalMate 是什么」→ 群 B 不得用「报表工具」。

**P4.3** 冬翔 → 测试号单聊：「GoalMate 是什么」→ 仍是 P1「工具，不是数字员工」，不是群 A 口径。

---

### P5 `/reset-memory`（只清冬翔这条单聊）

- 谁→谁：冬翔 → 测试号单聊
- 说：「/reset-memory」
- 查：这条单聊 Text 空、assoc 边空；再问 GoalMate 不得直接答「工具」；**测试号↔dxxh 单聊和群 A/B 的记忆还在**

---

### P6 开关和 inbound Tab

**P6.1** 关 recall，冬翔 → 测试号：「GoalMate 是什么」→ ACK 正常，SLS 无 revision。

**P6.2** 关 write，冬翔 → 测试号：「GoalMate 其实是一只猫」→ 旧 Text 不变成猫。

**P6.3** 开 UI → inbound Tab 能看到冬翔这条单聊的 Text。

**P6.4** 关 UI → inbound Tab 不展示 Memory。

---

## 切片对照

| 切片 | 跑哪些 | 没做完时 |
|---|---|---|
| A 已在预发 | P1.1 仍应问候成功 | 无 Flush、无召回 |
| B | P1.2 的「Text 里有工具」 | Coordinator 还不读 Text |
| C | P1–P5 | 核心 e2e |
| D | P6.3 / P6.4 | 先 SQL/API 开开关 |
| E 可选 | daemon-recall.md | 不挡上线 |

目标：纠正下一轮可用；cid 不串；纠正不进 Issue；Flush 失败不拖 ACK；reset 清该 cid；Tab 能看见。

不做 Task Card、新「场域」Tab、改 assoc / `agent_task_queue`、Web Chat/Robot/日历/审批、people-group-memory。
