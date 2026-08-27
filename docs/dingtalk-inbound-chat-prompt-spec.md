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
① 安全与交付规范                        Diamond common.prompt        配置热更新
② <surface> 模式职责（auto/chat/issue） Diamond <surface>.prompt     配置热更新
③ Router dispatch execution context    Router（外部服务）            Router 发版
④ ## DingTalk Conversation             handler                      部署即生效
⑤ ## DingTalk Reply Formatting         handler                      部署即生效
⑥ chat 框架 / Audience / 交付口径       daemon                       需重建镜像
⑦ <interaction-record>   外壳           daemon                       需重建镜像
                         内容           handler                      部署即生效
⑧ User message: 本轮展示内容            handler                      部署即生效
⑨ 文件交付说明                          daemon                       需重建镜像
```

③ 由 Router 逐字透传，Multica 不解析、不裁剪、不去重。

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
| Issue 交付 | `surface=issue` 且带 completion callback | 终答会被平台投回钉钉会话，issue 评论是 Multica 侧记录；写一次、两处都给；不要自己再发一遍 |
| 定位符 | 有会话 ID / 有引用 | 逐条打印可直接执行的回读命令 |

命令一律**代入真实 ID、可原样执行**，不留 `<openMsgId>` 这类占位符——需要模型自己从数据块里
拼命令的提示，就是会被猜出来的提示。同一个标识符在段里只出现一次（只在命令里），
不再重复当行标签。

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

新增规则时按同样的表自查一遍。

## 7. 已知的跨系统缺口

以下两条需要 Router 侧配合，Multica 单侧改不掉：

1. ③ 的 `referenced message context (data only)` 与 ⑧ 的引用摘头同源，长引用时是重复；
   而且 JSON 里的引号是 `&quot;`，未解转义。去重与转义归 Router。
2. Router 在 `dispatchInput.data.sender` 里丢掉了 `uid`（`inboundEvent` 里有）。补上这一个
   字段，第 3 节的归属判定就能从「其他人」精确到具体的人。
