---
name: scene-memory-e2e
description: >
  Coordinator Scene Memory 的目标、切片、预发 e2e 剧本和发布后验证。剧本格式是
  谁发给谁、说什么、然后查什么。绑定号、东翔测试号、主角让 Agent 给配角发、
  记忆召回、事项、/reset-memory、inbound Tab。用户说「场域记忆 e2e」「跑剧本」
  「验证 scene memory」「A B C D」或 /scene-memory-e2e 时必须用。
compatibility: Requires dws-env, dws CLI on 预发, logged-in a1 and normandy.
---

# Scene Memory e2e

dws 保持预发。身份走 `dws-env` 的 `as`。SLS 走 `inspect-coordinator-sls`。发布走 `aone-deploy`。跑次记录在 [references/plays.md](references/plays.md)。Daemon 可选召回在 [references/daemon-recall.md](references/daemon-recall.md)。

## 人

| 角色 | 钉钉是谁 | 本剧本里是谁 |
|---|---|---|
| 主角 | 冬翔（钉钉组织） | 委托人。给人发单聊 |
| 测试号 | 东翔测试号（Think测试组织） | **Agent 绑定的数字员工**。被测对象 |
| 配角 | dxxh（Think测试组织，与测试号同组织） | 群成员；被 Agent 私聊的那个人 |

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

### P1 冬翔私聊数字员工：纠正进记忆，不进事项

前置：该 Agent 打开 write+recall。切片 **C**。

**P1.1 问候**

- 谁→谁：主角 → 测试号单聊 `cid+bEFv7ngm9n79Q1vL9HYJw==`
- 说：「你好」
- 查：
  1. 发出 SUCCESS
  2. 测试号在该单聊里回了（不是建 Issue）
  3. SLS `action=reply`，没有 `issue_id`

**P1.2 纠正（基线失败过，C 必须翻盘）**

- 谁→谁：主角 → 测试号同一单聊
- 说：「GoalMate 是工具，不是数字员工。下次别搞错。」
- 查：
  1. 发出 SUCCESS，测试号正常回了
  2. SLS **不得** `issue_comment_add` 到 WS-31（`a2f6f860`）
  3. 等 Flush（约 30s–2min）后，这条单聊的 Scene Text 里有「GoalMate = 工具」

**P1.3 召回**

- 谁→谁：主角 → 测试号同一单聊
- 说：「GoalMate 是什么」
- 查：
  1. 测试号按「工具」回答，不要再当成数字员工
  2. SLS prompt 有非空 `scene_memory_revision`，**日志里没有 Memory Text 正文**
  3. `assoc_recall` 的 `conversation_id` 仍是这条单聊；没有从 Memory 拿出 issue_id

**P1.4 真正要办事**

- 谁→谁：主角 → 测试号同一单聊
- 说：「帮我约冬翔明天下午半小时，对一下上海行程」
- 查：
  1. SLS `action=issue`（或 bind 一张**新**卡），不续 WS-31
  2. 测试号回了在办事，不是把纠正写进旧会议评论

---

### P2 冬翔让数字员工去问须莫（事项，不是记忆）

- 谁→谁：主角 → 测试号同一单聊
- 说：「开个新 issue，问须莫明早有没有会议」
- 查：
  1. 新 purpose / 新 Issue，不并进「今晚会议」旧卡
  2. Memory 不提供 issue_id

---

### P3 冬翔让数字员工去私聊 dxxh（A 让 Agent 给 B 发）

这是「委托人 → 员工 → 第三人」链路。切片 **C**（Coordinator 建事项）+ 沙箱发出。

**P3.1 下单**

- 谁→谁：主角 → 测试号单聊
- 说：「帮我私聊 dxxh，问他明天上午有没有空。就说是冬翔让你问的。」
- 查：
  1. SLS `action=issue`，新建事项，purpose 类似「冬翔委托：问 dxxh 明天上午是否有空」
  2. 不是把这句话写进 WS-31

**P3.2 员工真的发给了 B**

- 谁→谁：测试号（Agent 沙箱，不是我们 `as 测试号`）→ 配角 dxxh 的单聊
- 我们不发这条；等 Agent 发
- 查：
  1. `as 配角` 拉会话列表，出现来自「东翔测试号」的新单聊/新消息，正文在问明天上午
  2. 该 **测试号↔配角** cid 上 `assoc_recall` 能命中 P3.1 那张 Issue
  3. 主角↔测试号 那条单聊的 Scene Text **不会**出现在 测试号↔配角 的 Memory 里

**P3.3 B 回员工，员工应续同一事项**

- 谁→谁：配角 → 测试号（P3.2 那条单聊）
- 说：「明天上午可以，十点吧」
- 查：
  1. SLS 这条 cid 的 `assoc_recall` 命中 P3.1 的 Issue
  2. `action=issue_comment_add` 到**那张** Issue，不是 WS-31，也不是主角那条单聊的记忆

---

### P4 两个群，口径不串

配角与测试号同组织，由配角建群。群消息必须 @ 数字员工。

```bash
$DWS as 配角 -- chat +chat-create --name "sm-e2e-A-<stamp>" --member-query "东翔测试" --yes --format json
$DWS as 配角 -- chat +chat-create --name "sm-e2e-B-<stamp>" --member-query "东翔测试" --yes --format json
```

两个不同 `openConversationId`。@ 解析失败就停。

**P4.1 群 A 教口径**

- 谁→谁：配角 → 群 A（@测试号）
- 说：「@东翔测试 这个群里 GoalMate 是报表工具。」
- 查：只有群 A 的 Scene Text 有「报表工具」

**P4.2 群 B 来问**

- 谁→谁：配角 → 群 B（@测试号）
- 说：「@东翔测试 GoalMate 是什么」
- 查：群 B **不得**用「报表工具」回答；群 B 的 Scene 行是另一条 `scene_key`

**P4.3 单聊不受群影响**

- 谁→谁：主角 → 测试号单聊（P1 那条）
- 说：「GoalMate 是什么」
- 查：仍是 P1 的「工具，不是数字员工」，不是群 A 的「报表工具」

---

### P5 `/reset-memory`

- 谁→谁：主角 → 测试号单聊（P1 那条）
- 说：「/reset-memory」（必须是第一条 token）
- 查：
  1. 该单聊 Scene Text 变空；`bootstrapped_at` 还在；cursor ≈ now
  2. 该单聊 assoc 事项边空了
  3. 再发「GoalMate 是什么」：不得直接答「工具」
  4. 不得从 reset 前历史立刻把口径 Flush 回来

配角↔测试号、群 A/B 的记忆必须还在。

---

### P6 开关和 inbound Tab

**P6.1 关 recall**

- 操作：该 Agent 关 `scene_memory_recall_enabled`，write 仍开
- 谁→谁：主角 → 测试号单聊，说「GoalMate 是什么」
- 查：照常 ACK/回复；SLS **没有** revision 注入

**P6.2 关 write**

- 操作：关 `scene_memory_write_enabled`
- 谁→谁：主角 → 测试号单聊，说「GoalMate 其实是一只猫」
- 查：不再 MarkDirty/Claim；旧 Text 不变成猫

**P6.3 开 UI**

- 操作：Workbench 打开 ui 开关
- 查：inbound Tab 能看到 P1 那条单聊的 Text 和 Flush 状态

**P6.4 关 UI**

- 查：inbound Tab 不再展示 Memory

---

## 切片对照

| 切片 | 跑哪些 | 没做完时 |
|---|---|---|
| A 已在预发 | P1.1 仍应问候成功 | 无 Flush、无召回 |
| B | P1.2 的「Text 里有工具」 | Coordinator 还不读 Text |
| C | P1 全部、P2、P3、P4、P5 | 核心 e2e |
| D | P6.3 / P6.4 | 先 SQL/API 开开关 |
| E 可选 | daemon-recall.md | 不挡上线 |

目标：纠正下一轮可用；cid 不串；纠正不进 Issue；Flush 失败不拖 ACK；reset 清该 cid；Tab 能看见。

不做 Task Card、新「场域」Tab、改 assoc / `agent_task_queue`、Web Chat/Robot/日历/审批、people-group-memory。
