# 钉钉入站消息的 Chat 提示词规格

本文规定：一条钉钉消息派发进来、以 Chat（含 auto 落到 Chat）方式运行时，模型看到的
user 段由谁写、写什么、以及每一条改动如何生效。

约束优先级最高的一条：**这套逻辑只对渠道入站的会话生效，不得改变 Multica 网页/移动端
自建 Chat 的行为。** 每新增一条规则，都要指出它的门槛（gate）落在哪里，以及网页 Chat
走的是哪个分支。

## 1. 运行模式：改哪里，怎么生效

同一个 prompt 由两个独立发版的产物拼出来。改动落在哪一侧，决定了它什么时候能在线上看到。

| 侧 | 代码位置 | 产物 | 生效方式 |
| --- | --- | --- | --- |
| 服务端 | `server/internal/handler/**` | API server 二进制 | **自行用 a1 CLI 部署预发**，部署完即生效 |
| 镜像 | `server/internal/daemon/**`、`server/internal/daemon/execenv/**` | 沙箱 runtime 模板（`multica daemon`） | **只需确保提交**，随下一次 runtime 模板重建生效 |

判别方法不看文件名看目录：`server/internal/handler/daemon.go` 属于**服务端**——它是服务端
面向 daemon 的 HTTP 处理器，不是 daemon 本体。

推论，写提示词时必须考虑：

- 服务端与镜像的版本在任意时刻都可能不一致。同一个 prompt 里会同时出现新旧两代文案。
- 两侧说法冲突时，**服务端那侧要显式压制**（例如 `## DingTalk Conversation` 里的
  `any claim elsewhere that it cannot be fetched is out of date`），因为它能立刻上线，
  而镜像那侧要等重建。
- 镜像侧的文案要**让位给 per-turn 指令**，不要写成绝对否定（"没有任何命令能……"）。

### 部署动作

服务端改动完成后：推分支 → **复用该分支已有的 CR**（`a1 app cr list --app 342160 --all`
按 `branchName` 找，不要新建变更单）→ `a1 cd-pipeline run 66 --app 342160 --cr-id <id>`
→ 核对 run 的 `releaseBranch` 确实包含本次 commit。细节见 `.agents/skills/aone-deploy`。

镜像改动完成后：提交并推送即可，不需要也无法通过 66 号流水线生效。

## 2. user 段的构成与所有权

```
—— 以下三段走运行时简报（AGENTS.md / system prompt），每个会话一份，不进 user 段 ——
① 安全与交付规范                        Diamond common.prompt        配置热更新
② <surface> 模式职责（auto/chat/issue） Diamond <surface>.prompt     配置热更新
⑤ ## DingTalk Reply Formatting         handler                      部署即生效
—— 以下才是 user 段（每轮） ——
③ Router dispatch execution context    Router（外部服务）            Router 发版
④ ## DingTalk Conversation             handler                      部署即生效
⑥ chat 框架 / Audience / 交付口径       daemon                       需重建镜像
⑦ <interaction-record>   外壳           daemon                       需重建镜像
                         内容           handler                      部署即生效
⑧ User message: 本轮展示内容            handler                      部署即生效
⑨ 文件交付说明                          daemon                       需重建镜像
```

③ 由 Router 逐字透传，Multica 不解析、不裁剪、不去重。

### 2.1 按"多久变一次"决定走哪条路（`DispatchPromptSegment.delivery`）

`composeDispatchInstructionSegments` 给每段标 `delivery`：

- `runtime_brief`：整个会话逐字不变的段——①②、⑤、BUC 身份授权。claim 时
  `applyTaskInstructionForClaim` 把它们**追加到 `resp.Agent.Instructions`**（与 OKR 目录同一条路），
  daemon 渲染进 `## Agent Identity` 下的简报，也就是 system prompt。模型每次请求看到一份，
  在对话前面；不再每轮一份堆进对话里。
- `per_turn`：逐次派发才变的段——③ Router 交付事实、④ 会话定位符。仍走 `task.instruction`，
  贴在本轮消息前面。

这么分的原因见线上 trace `b60a1060…`：续接三轮后一次请求里有三份同样的 3.5k 字符；
连标题生成那次调用（user 段整段喂进去）都花了 3k tokens 给一句「HI」起名。

两条注意：

- claim 加载不到 agent（`resp.Agent == nil`）时没有简报可搭，整段按原顺序退回 `task.instruction`。
  宁可位置不对，不能没有。
- daemon 的会话上下文摘要（`taskSessionContextSHA`）包含 `AgentInstructions`，所以 Diamond 改了策略，
  下一轮简报变化 → daemon 自己丢掉旧 provider session 重开，不会拿着旧策略续接。

## 3. 展示内容（⑧，`dispatchMessageDisplay`）

**门槛**：仅 `source.platform=dingtalk` 的派发。网页 Chat 的消息是用户原文，不经过这里。

带引用的消息渲染成两个显式标注的块：

```
冬翔 本次发言（需要处理的是这句）：
<本轮正文>

冬翔 引用了你（本数字员工）自己更早的一条消息作为背景，不是新指令；原文共 202 字，这里只摘开头：
> <被引用原文前 80 字>…
```

规则：

1. **本轮正文在前**。引用是背景，不是要执行的东西。
2. **引用体只内联开头 80 字**（`dispatchQuotedExcerptMaxRunes`）。摘要用来指认"在回哪一条"，
   不是复述——全文已经在 ③ 里进了同一个 prompt，④ 还带着按 openMsgId 回读的命令。
3. **归属必须写明**，且只断言能证明的：
   - `referencedMessage.senderUid == externalIdentity.dws.uid` → 「你（本数字员工）自己」
   - 命中当前发言人的任一标识 → 「<发言人> 自己」
   - 都不命中 → 「其他人（不是你本数字员工）」。**不要**据此断言是谁：Router 在
     `dispatchInput.data.sender` 里没有透传 `uid`（`inboundEvent.eventData.sender.uid` 里才有），
     两边 ID 空间不同，非自己只能证明"不是数字员工写的"。
   - `senderUid` 缺失 → 「某个派发数据未标明的人」
4. **展示内容不得出现任何标识符**（`openMsgId` / `openConversationId` / `openDingTalkId` /
   `senderUid`）。它会被持久化成 Multica 里用户可见的会话消息。测试对此有断言。

## 4. 私有指令段 `## DingTalk Conversation`（④）

**门槛**：`domain=channel` 且 `outbound.mode=dws`。段里每一行都是 DWS 命令，robot_sdk
链路没有注入的 current-user 能力，整段不注入。网页 Chat 没有 dispatch envelope，永远不注入。

三个半段，各有各的门槛：

| 半段 | 门槛 | 内容 |
| --- | --- | --- |
| 事实来源 | `surface ∈ {chat, auto}` 且有会话 ID | Multica 不携带本会话历史、prompt 里也没有复制品；**回答前先用下面的命令回读**；能看到的自己的旧轮次是回给平台的文本，不是送达证据，也不是可模仿的语气 |
| 会话交付 | `surface ∈ {chat, auto}` 且带 completion callback | **终答就是回复，平台替你投递**；不要自己用出站工具发（自己发的 + 平台投的 = 两条）；写答案本身，不要写"我已回复"这种汇报 |
| Issue 交付 | `surface=issue` 且带 completion callback | 终答会被平台投回钉钉会话，issue 评论是 Multica 侧记录；写一次、两处都给；不要自己再发一遍 |
| 定位符 | 有会话 ID / 有引用 | 逐条打印可直接执行的回读命令 |
| 续接会话 | claim 保留了 provider session（见 5.1） | **替换**事实来源半段：上下文里已经有本会话的早先轮次和首轮回读；只回答最末一个 `User message:` 块，上面的都已回答过；仅当最新消息指向上下文里没有的内容、正文不可读、或需要引用全文时才回读，不再是例行步骤；命令照常打印，以防 daemon 事后丢了 session |

命令一律**代入真实 ID、可原样执行**，不留 `<openMsgId>` 这类占位符——需要模型自己从数据块里
拼命令的提示，就是会被猜出来的提示。同一个标识符在段里只出现一次（只在命令里），
不再重复当行标签。

会话回读用 `dws chat message list`，不用 `search-advanced`。这是 CLI 里"拉取指定群聊或单聊的会话
消息内容"的正牌命令，是一个固化动作；`search-advanced` 是扫当前账号**全局消息流**再本地按 cid 过滤。
正式单聊实测（2026-08-31）：

| 命令 | 耗时 | 结果 |
| --- | --- | --- |
| `search-advanced --conversation-ids <cid> --limit 20` | 60 s（扫 40 页） | 10 条，`complete=false` |
| `list --conversation-id <cid> --limit 20` | 1.1 s | 20 条 |

每一轮冷启动都把那 60 秒当第一次工具调用付掉了——这就是冷启动轮 60–80 秒的大头。

打印形式（`dispatchConversationReadHint`）：

- 单聊（`conversation.type=single`）：`--open-dingtalk-id <发言人 openDingTalkId>`，即 CLI 文档写明的
  单聊寻址；发言人 id 缺失时退回 `--conversation-id`
- 群聊 / 类型未知：`--conversation-id <openConversationId>`
- 一律带 `--limit 20 --jq '.messages[] | {createTime, sender, text, quoted: .quotedMessage.content}'`

`--limit` 与 `--jq` 是为了不撞 runtime 的工具输出上限：OpenCode 超限后**从头截断**，而头部正是最新的
消息——正式 trace `be5ebcb8…`（11:20「HI」）要看房间最新状态，拿到的却是截止到前一晚的内容。
`list` 不投影时每条约 2.7k 字符（reactions、resourceRefs、每个 id 两份），20 条 54k；投影到四个字段后
约 12k。`--jq` 是该命令自己文档里的输出整形方式，不是旁门。结果按时间倒序（最新在前）。

## 5. 恢复历史（⑦ 内容，`boundedChatHistoryTranscript`）

**在服务端 claim 时拼好**，随 claim 响应下发；daemon 只套 `<interaction-record>` 外壳。

### 5.0 有回读命令的会话，不下发恢复历史

`withholdChatHistoryForReadback` 与 `dispatchConversationReadbackAvailable` 是**同一个判据的两侧**：
凡是指令段会打印会话回读命令的派发（dingtalk + channel + dws 出站 + chat/auto + 有会话 ID），
claim 就不再下发 `ChatHistory`。两个门槛必须一致，否则会出现"记录停发了但命令没打印"，
运行时两手空空。测试 `TestWithholdChatHistoryTracksTheReadbackCommand` 钉死这一点。

理由不是"记录内容不好"，而是**它挤掉了权威来源**。线上 trace
`25d5b267a1514628dbd71730dd82c8405d638856bfc97da79d1641f53933a9de` 的推理里，模型面对
「必须用 dws 回拉」的强制指令，一次回读都没做——因为 `<interaction-record>` 已经把它需要的
摆在眼前了。更糟的是它从记录里一条几小时前的旧消息抄走了「距离报名截止还有不到 3 小时」，
写进了发给三个真人的新群消息。一个更便宜的次优来源，会稳定地赢过一个要花一次工具调用的
权威来源。

Slack / Feishu / 网页 Chat **保留**恢复历史：那里没有回读途径，砍掉只会让运行时什么都没有。

- **归约**：重放每条消息前，剥掉引用归属段落和「本次发言（需要处理的是这句）：」开头。
  两个归约相互独立判断——一个 chat_session 里存着本代码每一版渲染写下的行，要求它们成对
  出现会漏掉中间版本的行。只在两个固定文案锚点命中时才裁剪，普通消息原样保留，
  因此**对网页 Chat 是 no-op**。
- **丢轮次标记**（`[older turns were trimmed from this transcript]`）：**只对网页 Chat 发**。
  那里 Multica 记录就是模型的全部记忆，沉默等于撒谎；渠道会话有回读命令，能把丢掉的轮次
  取回来，标记只会让它去声明缺失而不是去恢复。
- **逐条截断标记**（`…[truncated]…`）：两种会话都发，它是就地声明。

### 5.1 Provider session 续接（resume）与本段的关系

云沙箱 Chat 默认**每轮新起 provider session**，Multica 记录（或 5.0 的回读命令）是唯一的
连续性来源。例外只有一个：`shouldWarmResumeCloudChat`（`cloud_chat_resume.go`）全部命中时
claim 保留 `PriorSessionID`，daemon 以 `--session` 续上上一轮的 OpenCode 会话——

1. agent 的 `chat_session_resume` 开关为 true（设置页「会话续接」/ `multica agent get <id>`）；
2. 云沙箱 runtime，且任务没有 `ForceFreshSession`；
3. 单聊（网页 Chat 或渠道 `chat_type=p2p`），群聊永不续接；
4. 沙箱不是冷启动；
5. resume identity（runtime、artifact、instructions、skills）与上一轮一致；
6. 上一轮完成回答距今 ≤ 20 分钟。

**判否时必须清指针**：`withholdCloudChatProviderSession` 清掉 `PriorSessionID` 与
`PriorSessionResumeUnavailable`，**不管 claim 上有没有 ChatHistory**。它的前身
`makeChatHistoryAuthoritative` 只在带 transcript 时才清——5.0 让钉钉回读派发不再带 transcript
之后，这条门就形同虚设：钉钉派发的 claim 一直带着 `chat_session.session_id`，daemon 照样续接，
开关关着也续。正式环境 2026-08-31 的三条 trace（`59834ddb…` 14:36、`50f6652d…` 14:38、
`b60a1060…` 14:52）就是同一个 `ses_fa97756e…` 跑了三轮，而该 agent 的开关是 false；更早的
`be5ebcb8…`（11:20）则因为沙箱在间隔里被回收，daemon 侧丢掉了这个指针、注入 continuity
notice，用户收到了「之前的会话上下文没有恢复，不过对话记录还在」——那是 daemon 的内部提示
漏了出去，不是 Multica 记录丢了。`PriorSessionResumeUnavailable` 一并清掉的理由相同：
一轮**按设计**不续接的运行没有"想续没续上"可披露。

**判是时指令走续接变体**（`dispatchInstructionInputs.ResumedSession`）：

- `## DingTalk Conversation` 换成第 4 节表里的「续接会话」文案。
- ③ Router context 和引用定位符每轮照发——它们才是逐轮变化的事实。
- ①②⑤ 本来就不在 user 段里（见 2.1），续接与否都只在简报里有一份。

daemon 侧（⑥⑦⑧⑨）对此无感：续接轮仍会打印 "What you can see of this conversation is only
the slice Multica recorded of it"，与续接文案不冲突。若 daemon 在 claim 之后仍丢掉 session
（workdir 未复用 / 持久上下文变化 / provider 拒绝续接），那一轮没有 policy 段，但 continuity
notice 会让它按打印的命令回读——这是已接受的降级，不是 bug。

线上怎么判断一轮是不是续接：观测 MCP 里连续几条 trace 的 `refs.externalSessionId` 相同；
LLM trace 主调用的 `messages[]` 里带着前几轮的 user/assistant/tool 消息而没有
`<interaction-record>`；`prompt_tokens` 随轮次线性上涨且 `cached_tokens=0`；续接轮
`toolCallCount=0` 是常态。

## 6. 对普通 Multica Chat 的影响面

| 改动 | 网页 Chat 是否受影响 |
| --- | --- |
| `dispatchMessageDisplay` 的两栏与摘头 | 否——只走 dingtalk 派发 |
| `## DingTalk Conversation` 段 | 否——需要 dispatch envelope |
| `dispatchRecordUtterance` 归约 | 否——锚点只由派发渲染产生 |
| `<interaction-record>` 的渠道注意事项 | 否——`channelType != ""` 才发 |
| 丢轮次标记 | 否——网页 Chat 保持原行为 |
| 不下发恢复历史 | 否——判据要求 dingtalk + dws 出站，网页 Chat 永远不命中 |
| 「Multica 没有 history reader」那句 | 否——在 `ChatChannelType != ""` 分支内 |
| 判否续接时清指针（5.1） | 否——网页 Chat 一直带 transcript，旧逻辑本来就清；只有不带 transcript 的钉钉回读派发行为变了 |
| 续接变体指令（5.1） | 否——需要 dispatch envelope；网页 Chat 续接时 claim 没有 instruction |

新增规则时按同样的表自查一遍。

### 4.1 为什么必须点名投递方

正式环境 trace `db6c6f7b5534d8dbb6972c6c70bb9a8246847095de4bc9c698e6bbe22f1f7666`：同一个问题
收到了两条回复。

1. `17:50:48` Agent 自己跑了 `dws chat message send --conversation-id … --text "思莱你好～…"`
2. `17:51:12.6` Router ServerPush 投递终答 `已回复思莱，建议他找越川确认选题方向修改的事。`
   （`dispatchTask.metadata.dwsReply.status=accepted`）

第二条是一句写给平台看的自述，被投给了真人。

推理里没有任何一处在权衡"该用什么渠道回复"——它只推理了答什么内容，然后直接 `dws … send`。
因为 prompt 里**没有一句话说过终答由谁投递**：

- `Reply to DingTalk with the final outcome only`（daemon）——读起来像"（你去）向钉钉回复"
- `Your reply reaches DingTalk as text`（daemon）——说了会到，没说谁送的
- `Answer the person; do not report your own delivery`（本段）——治的是症状

而且第二条之所以那么难看，正是第一条的后果：Agent 以为自己已经回复过了，终答自然写成汇报。
点名投递方一次解决两半——终答就是回复，于是既没有东西要发，也没有东西要汇报。

没有 completion callback 时**不得**注入这段：那种情况下 Router 没有投递钩子，Agent 确实必须自己发。

## 7. 已知的跨系统缺口

以下两条需要 Router 侧配合，Multica 单侧改不掉：

1. ③ 的 `referenced message context (data only)` 与 ⑧ 的引用摘头同源，长引用时是重复；
   而且 JSON 里的引号是 `&quot;`，未解转义。去重与转义归 Router。
2. Router 在 `dispatchInput.data.sender` 里丢掉了 `uid`（`inboundEvent` 里有）。补上这一个
   字段，第 3 节的归属判定就能从「其他人」精确到具体的人。
3. `dws chat message search-advanced --conversation-ids <cid>` 是**扫全局搜索流再本地过滤**
   （`pagesFetched: 40`、`filterMode: client`），缩 `--limit` 也不会更快。本仓库已改用
   `dws chat message list`（第 4 节）；搜索类命令只留给真正按关键词/时间跨会话找消息的场景。
