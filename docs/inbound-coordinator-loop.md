# Inbound Coordinator Loop

快循环（Event Loop）的合同。对照实现阅读：`server/internal/service/inboundcoord/`。
沙箱慢循环的 Chat 提示词不在这里，见 [钉钉入站 Chat 提示词规格](dingtalk-inbound-chat-prompt-spec.md)。
Dispatch 执行面见 [Agent Dispatch V2 Execution Contract](agent-dispatch-v2-execution-contract.md)。
场域图见 [Event / Scene / Issue-Task 关联](plans/2026-08-31-event-scene-issue-task-assoc.md)。

状态：本文同时写 **现行实现** 和 **关键设计**。缺口集中在文末「做不到」。

---

## 1. 循环要干什么

Coordinator Loop 是入站消息进沙箱之前的短接待。它不是第二个 Agent 运行时。

三件事，按这个顺序：

1. **按基础人设快速接待。** `finish.text` 要像同事在 IM 里说话，不像工单机器人。人设只定声音，不定路由。
2. **把场域和事情连上。** 场域是钉钉 `openConversationId`（`cid…`），事情是 Issue。图查询是 `assoc_recall`；绑边是 `assoc_bind`，由模型注入 `purpose`（交付物短句）和 `intent`（ask/confirm/notify/lookup/wait/other）。新建事项先 bind（不带 issue_id）再 `finish action=issue`。event-stream 命中的卡片 `matched_via=event` 只是候选，要 bind 才算关联。时间用卡片上的 `last_touched_age` / `last_comment_age`，不要让模型自己算。
3. **需要持续跟的，交给 Issue。** 联系人、DWS、搜索、文件、写入、评论线程、追踪，都不是这轮直接做完的事。`action=issue` 开新 Issue 或续已召回的 Issue，沙箱再跑慢循环。

双循环：

| 循环 | 延迟 | 上下文 | 出口 |
|---|---|---|---|
| Coordinator（快） | 45s 墙钟，最多 8 轮 tool call | 人设、本场景最近 IM、图召回 | `reply` / `issue` / `silence` |
| Sandbox Task（慢） | 一次 Issue 任务 | Agent Instructions、Diamond、Router Context、DWS 回读命令 | 评论、出站、状态、产物 |

快循环不暴露 DWS / 搜索 / 新闻给模型。服务端在第一轮模型之前自己拉当前 cid 的钉钉历史。沙箱 ContextToken 不用。

### 1.1 Router 接单与短循环执行分离

Router 的 HTTP 请求不等待 DWS 或模型。对带 `completionCallback` 的
`channel/message.created` Chat / auto / Issue 入站，Multica 在一个数据库事务里写入：

1. dispatch acceptance 的 `202 {"status":"accepted"}`；
2. 一条持久化 `inbound_coordinator_job`；
3. 一个只读 Coordinator Chat 和本轮用户消息。

事务提交后立即把 202 回给 Router，后台 worker 再执行 DWS 拉取、最多 8 轮工具调用、
Issue/评论写入和原有 callback。每个副本有 8 个并发 worker；PostgreSQL
`FOR UPDATE SKIP LOCKED` 让每条消息独立领取，1 分钟 lease 负责进程重启后的恢复，
可重试错误最多 6 次。Router 的 10 秒读超时因此不再包住短循环，也不会因为
acceptance 仍为 pending 而进入 409 重试。

每个入站短循环对应一个 Multica Chat，会话级显示 `Coordinator` 标签。接单提交后
会话立即出现在列表；执行完成后，assistant 行保存 `message_kind=coordinator`，并按普通
Chat timeline 展示 DWS 历史拉取、模型判断、tool use、tool result 和最终文本。这个 Chat
只展示短循环记录，不接受用户继续输入。

---

## 2. 人设放在哪里

产品入口是 Agent **Instructions** 页的三个独立字段，不是混在 System Prompt 里。

| 字段 | UI | 库 | 给谁用 | 是否改路由 |
|---|---|---|---|---|
| 基础人设 `persona` | Instructions 页 | `agent.persona`，最多 400 字 | Coordinator `finish.text` 的「我是谁」 | 否 |
| 回复语气 `reply_tone` | 同上 | `agent.reply_tone`，最多 200 字 | Coordinator `finish.text` 怎么说 | 否 |
| System Prompt `instructions` | 同上下方 | `agent.instructions` | **沙箱**工作规则 | 否 |

抽取按钮把 `instructions` 里的声音抽到人设/语气，不改路由合同。

空人设、空语气时，快循环默认「简洁同事」：短句、不客套、不重复用户的话。禁止「收到 / 正在处理 / 稍等 / 好的我马上」。

### 2.1 快循环提示词怎么拼

两段，都在 `inboundcoord`：

```
system  ← inboundcoord/prompt.go  systemPrompt
          路由、工具、assoc_recall 读法、finish 口径。不含某 Agent 的人设。

user    ← buildUserPrompt(turn)
          source / addressed / chat_type
          agent_name
          agent_persona          ← 人设（clip 400，与 UI 一致）
          agent_reply_tone       ← 语气（clip 200，与 UI 一致）
          identity_note
          conversation_id / person_id
          agent_instructions     ← 工作规则 clip 400；不得改 action，不得抄进回复
          recent_multica_history ← 仅网页 Chat，最近 4 条
          recent_dingtalk_history← 钉钉：服务端 DWS 拉的最近 10 条（去掉本轮）
          current_message
```

模型：`qwen3.7-plus`，thinking 关，`tool_choice=required`，温度 0.3，最多 512 completion tokens。最后一轮只能 `finish`。裁决只认 `finish`，不认模型自由文本。

人设出现在 **user 段的 `agent_persona` / `agent_reply_tone`**，由 system 规定「只定声音、不定 action」。不要把人设写进 system 常量，也不要写进沙箱 Diamond。

### 2.2 谁把人设填进 Turn

| 入站 | 代码 | 人设 |
|---|---|---|
| 网页 Chat | `chat.go` → `TurnFromChatSession` | `GetAgentVoice`，有 |
| 机器人 Channel Engine | `channel/engine/router.go` → `TurnFromChatSession` | 有 |
| 数字员工 Dispatch V2 | `decideDispatchCoordinator` 手拼 Turn 后 `FillVoice` | `GetAgentVoice`，有 |

### 2.3 沙箱里的人设

沙箱 **不读** `persona` / `reply_tone`。它读 `agent.instructions`（claim 时进 `## Agent Identity` 运行时简报），外加 Diamond、`## Scene graph`、`## DingTalk Conversation`。

两套声音不要混：快循环用基础人设接待；慢循环用 System Prompt 办事。

---

## 3. 工具面（OpenAI tool call）

快循环的工具是 Chat Completions `tools[]` + `tool_choice=required`。参数 JSON Schema 必须能被模型直接填。不要把 CLI 字符串塞进这一层。

### 3.1 现行工具

定义在 `inboundcoord/loop.go` `coordinatorToolDefs`。

**`assoc_recall`** — 场景图上的事情索引，精排只发生在模型读完 tool result 之后。

```json
{
  "since": "24h | 48h | 7d | RFC3339",
  "conversation_id": "cid…",
  "person_id": "钉钉 uid，可选排序信号，禁止编造",
  "issue": "Issue UUID",
  "q": "purpose 关键词，只过滤本场景；不能用来丢掉 conversation_id",
  "limit": "默认 20，最大 50"
}
```

入站有 cid 时，服务端始终把本轮场域 cid 填进 `assoc_recall`（模型漏传也会补上）。`q` 只在该 cid 上过滤 purpose，不再变成全 Agent 时间窗关键词搜索。用户点名的 cid 必须原样传入。`person_id` 只加权，不和 cid 做 AND 过滤。无入站 cid（网页 Chat）且只有 `q` 时，才退回时间窗搜索。

返回必须是模型能直接精排的 JSON，不是表行转储。见第 5 节。

**`assoc_bind`** — 把 cid 绑到事情上。

```json
{
  "conversation_id": "cid…",
  "issue_id": "来自 assoc_recall 的 Issue UUID；省略则只给入站 Event 打标",
  "evidence_id": "openMsgId",
  "person_id": "uid",
  "purpose": "可交付短语，例如 向冬翔确认今天吃什么",
  "kind": "single | group"
}
```

**`finish`** — 唯一合法裁决。

```json
{
  "action": "reply | issue | silence",
  "text": "用户看见的那一句",
  "look_into": "action=issue 时要核对的可交付短语",
  "issue_id": "续旧事时从 assoc_recall 原样拷贝的 UUID",
  "reason": "短理由，用户语言"
}
```

`issue_id` 必须出现在本轮 `assoc_recall` 结果里。数字员工单聊命中恰好一个 open/waiting 的 `outreach` / `waiting_on` 时，必须 `action=issue` 且带上该 `issue_id`，不能用一句「好的」收掉。

### 3.2 Issue 查询与评论（已挂上 Loop）

快循环能在 **不启动沙箱** 的情况下看事情、在 Issue 下留一条接待记录。查询和评论走同一套 tool call 协议，参数与沙箱 CLI / HTTP 对齐，但入口是 Loop tools，不是 Bash。

**`issue_get`**

```json
{
  "issue_id": "UUID，必须来自 assoc_recall 或本轮已验证的 id"
}
```

返回：`id`、identifier、title、status、assignee、updated_at、description 摘要。用于精排和续旧，不是把整份 Issue 正文灌进第一轮 user。

**`issue_comment_list`**

```json
{
  "issue_id": "UUID",
  "thread": "可选，评论 id",
  "since": "可选 RFC3339",
  "tail": "默认 20，硬顶 50",
  "roots_only": "布尔，先扫线程再展开"
}
```

返回紧凑评论：author、created_at、clipped body、thread id。给精排和「这事已经说到哪了」。

**`issue_comment_add`**

```json
{
  "issue_id": "UUID",
  "content": "外呼回信原文和发送人上下文",
  "reply_text": "给当前 IM 说话人的短确认",
  "parent": "可选，挂到已有评论"
}
```

用途：把外呼回信作为 **成员评论** 写回原 Issue，并立即走现有评论自驱机制排下一轮 Issue task。成功调用本身就是终点：`reply_text` 经当前 dispatch callback / Stream 回给正在说话的人，不再多跑一轮 `finish`，也不再 `action=issue` 二次入队。Issue 已有活动任务时不写评论，当前 dispatch 返回冲突并重试。

`issue_id` 必须来自本轮 `assoc_recall`，且该 Issue 在本 workspace、指派给本 Agent。Agent 评论只会留下记录，不能用于这条路径。

DWS、搜索、文件、联系人目录继续 **不** 进 Loop。需要它们就 `action=issue`。

---

## 4. 连场域和事情

Scene = `openConversationId`，且必须 `cid` 前缀。内部 `uid:uid` pair、staffId、userId、openDingTalkId 都不是场域。空 cid 不写场景边，Task/Issue 仍可建。

入站数字员工：Event.Data 优先，缺 cid 再从 Router Context `current message context (data only):` 补。Chat 引擎会话 id 用过滤后的 cid；没有 cid 才保留 pair 做路由。

```text
入站 message.created
  → 记 Event(inbound, evidence_id=openMsgId)
  → Coordinator：DWS 拉本 cid 历史 → assoc_recall → （可选）assoc_bind → finish
  → reply     一句 IM，无沙箱
  → silence   群闲聊，网页 Chat 禁止
  → issue     无 issue_id：新建 Issue + Task，Associate 本 cid
              有 issue_id：普通续旧路径；外呼回信优先走 issue_comment_add
```

出站（沙箱 `dws chat message send` 成功回执）`assoc_bind` / `BindOutbound`。回信进新 cid 时，靠出站边召回，不靠 Multica `chat_session` UUID。

「新事还是旧事」不在图层分类。图只给候选；Coordinator 看 `purpose` 和证据再 `finish`。

---

## 5. Recall 必须是 AI 原生（给 LLM 精排）

图层做 **粗召回**：时间窗 + cid / issue / q。排序信号是 recency × status × rel × person 命中，给截断用，**不是**语义答案。

精排是 Coordinator 读 tool result。所以 `assoc_recall` 的 JSON 必须是模型可消费的事情卡片，而不是行转储。

现行 `Result`：

```json
{
  "read_this": "items are candidates, not a verdict. …",
  "since": "…",
  "until": "…",
  "conversation_id": "cid…",
  "q": "可选",
  "items": [
    {
      "issue": "uuid",
      "issue_id": "uuid",
      "purpose": "向须莫v6确认今晚几点打球",
      "intent": "ask",
      "intent_label": "向某人询问一件事",
      "status": "waiting",
      "on_this_scene": true,
      "why_listed": "graph link on the inbound scene",
      "matched_via": "scene",
      "last_touched_age": "17小时前",
      "last_comment": "…",
      "last_comment_age": "16小时前",
      "conversations": [
        { "conversation_id": "cid…", "kind": "dm", "rel": "outreach", "rels": ["outreach"] }
      ],
      "people": [{ "person_id": "…", "name": "冬翔" }],
      "waiting_on": [{ "conversation_id": "cid…" }]
    }
  ],
  "events": [
    {
      "direction": "inbound",
      "text": "…",
      "when": "刚刚",
      "age": "刚刚"
    }
  ],
  "events_note": "scene IM evidence, not the matter index."
}
```

`purpose` 必须是可交付短语（最少 8 字，拒绝「帮我看看」）。精排靠它，不靠 recency score（score 不进 JSON）。`matched_via=window` / `on_this_scene=false` 不能当成当前会话的事。events 最多 8 条有正文的最近证据。

**不要**把召回结果预写成 `related_tasks:` 塞进第一轮 user。`injectRelatedTasks` 会让模型不走 tool 就答题；system 已经禁止凭 `related_tasks` 作答。Decide() 当前也没有调用它。召回只通过 `assoc_recall` 的 tool result 进入对话。

沙箱慢循环用同一份 JSON 合同：MCP `assoc_recall` / CLI `multica assoc recall`。Loop 与沙箱只换入口，不换字段。

---

## 6. 引用和消息历史：两路并行，不要当成一份

快循环和慢循环各自拉 IM。不要假设 Coordinator 看过的历史会进沙箱，也不要假设 Router Context 进了 Loop。

### 6.1 Coordinator（服务端，第一轮模型之前）

钉钉：独立 Agent Identity context → 每请求 `DWS_CONFIG_DIR` → `dws chat message list`。去掉本轮 `openMsgId`，保留更早的最多 10 条，时间正序。每条正文 clip 160 字；有 `quotedMessage` 时内联：

```
- 发送人: 正文
  引用消息（原发送人）：引用正文
```

网页 Chat 不拉钉钉。失败则 fail-open 进沙箱，不用 Multica transcript 或 Router 窗口顶替。

### 6.2 Sandbox Agent（claim / per-turn 指令）

独立三路，见 Chat 提示词规格：

| 来源 | 内容 |
|---|---|
| Router `contextPrompt` | 透传，含 current / referenced JSON（常带 HTML 转义） |
| `dispatchMessageDisplay` | 用户可见：本轮正文 + 引用头 80 字，无 id |
| `## DingTalk Conversation` | 按 `openMsgId` 写好的 `dws chat message list` / `list-by-ids` |

沙箱被明确要求：需要引用全文或窗口里没有的内容时自己回读。平台投递的旧轮次不是送达证据，也不是可模仿的语气。

### 6.3 观察时看什么

排「没带上引用 / 没带上前文」或「为什么接到旧事项」时，两路都要看：

1. Loop（SLS `dt-fde-multica-sls` / `application-log`，slog 文本在 `content`）：
   - 索引：`coord_trace_id`、`conversation_name`（群名或单聊发送人）、`conversation_id`、`sender_name`、`agent_name`、`current_message`。
   - `inbound_coordinator_llm_request`：`user_prompt`、人设、钉钉历史条数。
   - `inbound_coordinator_llm`：每一轮 tool 的 `arguments` 和 `result`。
   - `inbound_coordinator_llm_finish` / `inbound_coordinator_decided`：`action` / `issue_id` / `text`。
   - 本地查：`scripts/query-coordinator-sls.sh --env pre --name 冬翔`（Normandy，不要走阿里云 AK）。
2. 沙箱：Router observability 的 `contextPrompt`；task instruction 里的 DWS 命令有没有被执行。

Loop 的 10 条 clip 历史 **不是** 沙箱的权威会话。沙箱的 Router 窗口 **不是** Loop 的召回依据。

### 6.4 `/reset-memory`

入站正文第一个 token（可带一个前导 `@提及`）是 `/reset-memory` 时，Dispatch V2 在 Chat / Issue / continuation 分流之前拦截：

- 关掉这个 cid 上所有未关闭的事项边（outreach / waiting_on / task_scene / spawned_from，以及该场景 Event 的 `event_of`）。
- 去掉该场景 Event 上的 `task_id`。Event 正文保留。
- 不进 Coordinator，不进沙箱。回一句「已清理这个会话上的事项关联」。
- 之后 `assoc_recall` 这个 cid 不应再召回旧事项。其它 cid 不受影响。

这和 Router `/reset`（ForceFresh 沙箱会话）不是同一条命令。

---

## 7. `finish` 之后发生什么

| action | IM | 平台 |
|---|---|---|
| `reply` | `text` 经 dispatch callback / Stream 发出 | 无沙箱。网页 Chat 记 `message_kind=coordinator` |
| `issue` 无 `issue_id` | 先回一句正在核对什么（`execution-update` 冻结 ack） | 新建 Issue + Task，Associate 本 cid |
| `issue_comment_add` | `reply_text` 回当前说话人 | 成员评论写入原 Issue，并自驱下一轮 Issue task |
| `issue` 有 `issue_id` | 非外呼回信的普通续旧 | 原 Issue 上追加 follow-up 任务 |
| `silence` | 不回 | 无沙箱。网页禁止 |
| 内部 `continue` | 走原入队 | LLM / DWS 历史 / 开关失败时的 fail-open |

`reply` 禁止能力拒绝（「我看不到联系人」）。Loop 做不了的查找或动作必须 `issue`。

---

## 8. 源码地图

| 主题 | 位置 |
|---|---|
| 路由 system prompt、user 拼装 | `server/internal/service/inboundcoord/prompt.go` |
| tool 定义、轮次、finish 校验 | `loop.go` |
| Turn / Decide / DWS 历史接入 | `coordinator.go` |
| `assoc_recall` / `assoc_bind` 实现 | `tools.go` → `server/internal/assoc` |
| 钉钉历史 + 引用内联 | `dws_history.go` |
| DE Dispatch 入站 | `handler/agent_dispatch_v2_handler.go` `decideDispatchCoordinator`；`/reset-memory` 在 `tryDispatchResetMemory` |
| 持久化接单、并发 worker、Coordinator Chat | `handler/inbound_coordinator_job.go` |
| 网页 / 机器人 Turn | `handler/chat.go`、`integrations/channel/engine/router.go` |
| 人设 API / 页 | `handler/agent_voice.go`、`packages/views/agents/.../instructions-tab.tsx` |
| 沙箱场景图指令 | `handler/agent_dispatch_v2.go` `dispatchSceneGraphInstruction` |
| 沙箱 Issue 评论 CLI | daemon `multica issue comment list/add` |

---

## 9. 现在做不到的

对照第 1 节的三件事。

### 接待（人设）

- `instructions` 仍被 clip 进 Loop user。产品定义它是沙箱工作规则；进 Loop 会干扰路由，尽管 system 说「不得改 action」。

### 连场域和事情

- `injectRelatedTasks` 与「只通过 tool result 精排」冲突，且 Decide() 未调用。不要复活成第一轮记忆。
- 网页 Chat 没有 cid。网页接待绑不了钉钉场域；只有沙箱后来出站成功才能 bind。
- 图粗排没有 embedding。v1 故意如此；语义精排 = Coordinator 读卡片。旧 Event 在写入 body 之前仍可能没有正文，本轮会用 DWS 历史 / current_message 按 evidence_id 补上。

### 引用 / 历史

- Loop 失败（DWS 身份不完整、list 失败）会 **整段跳过** Coordinator，用户失去人设接待。
- Coordinator Chat 在短循环完成后一次性写入完整 timeline；当前不逐步流式刷新每个工具调用。
- 两路历史不对齐：Loop 10×160 字；沙箱是 Router 窗口 + 按需 DWS。Agent 可能再拉一遍 Loop 已经看过的引用。
- 数字员工 Event 里的 pair 与 Context 里的 cid 并存。场域必须用 cid；pair 只用于没有 cid 时的会话路由。

### 刻意不做（仍归沙箱）

- Loop 里调 DWS 发消息、搜联系人、读文件、跑 skill。
- 用 `chat_session` UUID 当场域键。
- 入站 ACK 路径写关联边。
- 把 Coordinator 扩成第二个沙箱。
