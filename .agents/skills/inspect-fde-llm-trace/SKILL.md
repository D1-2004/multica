---
name: inspect-fde-llm-trace
description: >
  排查 FDE教练 线上一次 IM/任务为什么没回、回了什么、推理怎么走、提示词哪一段说了什么。
  用户给 traceId、说「没回复」「推理记录」「LLM trace」「提示词从哪来」、或要看最近 FDE教练 调用过程时必须用。
  走 a1 MCP `agent-message-router-observability`，不要用 Multica issue 自述或 assistant 正文当已送达。
  这是 dt-fde-multica 的开发面排查手册，不是岗位 Skill，禁止同步进 fde-coach-agent 的 agent/skills/ 或写入 dingtalk-agent.json#agent.skills。
compatibility: Requires logged-in a1 (NCS/CIAP). MCP 名 agent-message-router-observability 当前 status=UNOPEN，find 必须带 --status UNOPEN。
metadata:
  version: "1.0.0"
  audience: "coding-agent"
---

# 排查 FDE教练 LLM 调用

「Agent运行记录」表已下线。IM 一次跑做了什么，权威在 **Agent Message Router 观测 MCP**，不在 Multica issue 评论，也不在模型自述。

Router 的 `firstReply=PRESENT` / `SUCCEEDED` 可以把 **assistant 终答** 记成已回复；钉钉侧仍可能没有 DWS 消息。外发有没有发生，只认目标会话回读。

## 入口

```bash
a1 mcp find "agent-message-router" --keyword --status UNOPEN --json
```

命中 `code=agent-message-router-observability`（平台 `dingtalk-ai-lab`）。默认 hybrid 搜索会漂到无关 Agent MCP；不带 `UNOPEN` 会得到空列表。

列出工具：`a1 mcp list-tools agent-message-router-observability --json`。

调用一律：

```bash
a1 mcp call-tool agent-message-router-observability::<tool> '{"fieldName_0":{...}}'
```

HSF 网关把入参包在 `fieldName_0` 里。一次 shell 只跑一条 `call-tool`，不要用 `&&` / `;` 串两条。时间是 Unix 毫秒，跨度最多 31 天，默认时区 `Asia/Shanghai`。

## 梯子：先集合，再一条，再模型

有 traceId 就从第 3 步开始。没有就按序往下走，不要一上来翻 LLM。

### 1. 认出是哪个数字员工

```bash
a1 mcp call-tool agent-message-router-observability::get_observability_filter_options \
  '{"fieldName_0":{"startTime":<ms>,"endTime":<ms>,"timeZone":"Asia/Shanghai"}}'
```

从 `agents[]` 取 `label` / `value`。线上岗位「FDE教练」与「FDE教练（阿里钉）」不是同一个 `agentId`，不要混。本仓库 evals 里出现过的生产 ID 是 `a9ce26da-e5fd-4c16-86ef-7fa0f46386bc`；仍以当次 filter_options 为准。

### 2. 集合：概览 + 列表

`get_observability_overview`：成功率、首回复延迟、失败阶段。`firstReplyAuthority=MULTICA` 表示首回复口径是平台终答，不是钉钉回执。

`query_observability_traces`：按 `agentId`、`environment=production`、`classification=BUSINESS_TASK` 分页。`messageKeyword` 是入站正文的字面包含，不匹配发送人、群名、ID。

列表字段里先看：`traceId`、`messagePreview`、`durationMs`、`statusCode`、`tool` 尚未出现——工具次数在详情/transcript 里。

### 3. 一条任务：时间线 + 出站合同

`get_observability_trace`。同时读这几块，不要只看 `statusLabel=成功`：

| 块 | 看什么 |
|---|---|
| `trace.messagePreview` / `receivedAt` | 用户原话与时刻 |
| `dispatchTask.dispatchInput` 或 `requestPayload` | `outbound.mode`、`replyTo`、`openConversationId` / `openMsgId` / `senderOpenDingTalkId` |
| `execution.toolCallCount` | 0 表示没调任何工具。**不等于没投递**，见下 |
| `receipts.firstReplyCode` | PRESENT 仍可能只是 Multica 终答 |
| `timeline` 的 `REPLY` vs `EXECUTION` | Router 自己的阶段，不是钉钉投递证明 |
| `execution.resultMessage` | 模型写给平台的终答正文 |
| `inboundEvent.eventData.sender.uid` | Router 在 `dispatchInput.data.sender` 里只留 staffId / displayName / openDingTalkId，**uid 被丢掉**；要拿发送人 uid（比如核对 `referencedMessage.senderUid` 是谁）只能从 `inboundEvent` 取 |

预发的 trace `environmentCode=staging`（列表查询用 `environment=staging`），别按 production 过滤。

**`toolCallCount=0` 现在有两种含义，必须先分开：**

- 有 completion callback 时，Router/ServerPush 是终答的投递方，Agent 本来就不需要自己调 DWS。此时 `timeline` 里 `REPLY SUCCEEDED` 的时刻紧贴 `AGENT_EXECUTION` 完成时刻（相差几百毫秒），0 次工具调用是设计内的。
- 没有 callback、`outbound.mode=dws` 且目标 ID 齐全时，合同才是「必须 DWS 引用回复」，0 次工具调用就是漏发。

两种都不构成「已投递」的证据，最终只认会话回读。

### 4. Transcript：实际做了哪些动作

`get_observability_transcript`。`availability` 不是 `AVAILABLE` 就停，不要用 issue 评论顶。

预发常见 `{"availability":"UNAVAILABLE","reasonCode":"multica_http_401"}`：观测凭据读不到该工作区的 transcript。这不是「没有动作」，是读不到。退到 `execution.toolCallCount` 判有没有工具调用，退到 LLM trace 的 `response.body` 判模型有没有发起 tool call，别把 UNAVAILABLE 当成 0 次调用。

分类看 `type`：

- `tool_use` / `tool_result`：`skill`、`bash`、`write` 等。回复是否发生，只认是否调用了 `im_reply.py` 或 `dws chat message reply` / `send`，以及结果 `success`。
- `text`：assistant 终答。有这段 **不等于** 钉钉有消息。

岗位出口是 `dingtalk-basic-behavior/scripts/im_reply.py`。裸 `dws chat message reply` 也是出站，但是旁路。`dws chat message mark-read`、`--help`、`uuidgen` 失败都不构成回复。

### 5. LLM trace：提示词和推理

`get_observability_llm_trace`。较老的 trace 可能 `items=[]`，这时只能停在 transcript，不要猜 system。

每条 `items[]`：

1. `request.body` 是 **字符串化的 JSON**（再 `json.loads` 一次），里面才是 `model` / `messages` / `tools`。
2. `response.body` 是 SSE。`delta.reasoning_content` 是思考，`delta.content` 是对用户可见正文，`usage` 在流末尾。
3. 常见两段调用：`sequence=1` 标题生成（可忽略）；`sequence=2` 才是主模型。continuation 里可能没有标题段。

主调用的 `messages`：

| role | 通常是什么 |
|---|---|
| system | OpenCode 人格 + 注入的 `agent/AGENTS.md` + Multica runtime brief |
| user | **服务端** dispatch 出站闸 + **daemon** 本轮 chat 正文（含恢复历史和用户句） |

不要把 user 消息当成一整块「提示词」。拆所有权：

| 文本特征 | 所有者 | 改哪里 |
|---|---|---|
| `outbound.mode=dws`、`普通 assistant final text 不能替代`、`dws chat message reply` | 服务端 `composeDispatchInstructionSegments` | Multica server，preview 可改 |
| `## DingTalk Conversation`、`source of truth`、`dws chat message search-advanced --conversation-ids`、`- quoted <msgId>` | 服务端 `buildDispatchConversationInstruction`（`server/internal/handler/agent_dispatch_v2.go`） | 本仓库，claim 时组装，改完部署即生效 |
| `冬翔 本次发言（需要处理的是这句）`、`引用了…更早的一条消息作为背景` | 服务端 `dispatchMessageDisplay`（同上文件），是用户可见展示内容 | 本仓库，改完部署即生效 |
| `This reply is delivered to dingtalk as text`、`Reply to dingtalk with the final outcome only`、`<interaction-record>` | Daemon `buildChatPrompt` / `chatHistoryRecoveryBlock`（`server/internal/daemon/prompt.go`） | **runtime/daemon 二进制**；claim 只下发 `chat_channel_type` 与 `chat_channel_delivers_files`，不带这段英文。改了要等新 runtime template，服务端改动不会带上它 |
| `You are opencode… Never use tools like Bash as means to communicate` | OpenCode system | runtime 模板，不是 `agent/AGENTS.md` |
| 「我是 FDE教练」、守则 7 `im_reply.py` | `agent/AGENTS.md` | 本仓库岗位定义；盖不过贴在用户句末尾的 daemon 句子 |

「delivered as text」本意是 **这条 IM 通道不能 `multica attachment upload`**（`chat_channel_delivers_files=false` 时的 default 分支）。钉钉 DWS 出站时，模型会把它读成「写正文 = 已交到钉钉」。服务端把 `chat_channel_delivers_files` 拨成 true 会换成错误的上传指引，不是修法。

推理里一次都没提 DWS / reply / `im_reply`，却直接吐了终答：就是走错交付通道。再往岗位提示词加「记得回复」改变不了它选的通道。

### user 段的版面与重复

主调用的 user 消息不是一整块，按固定顺序拼出来，逐段认所有者比通读一遍有用：

```
① Diamond common.prompt        安全与交付规范
② Diamond <surface>.prompt     Auto 模式前台协调职责 / chat / issue
③ Router contextPrompt         Router dispatch execution context
④ dingtalk_conversation        ## DingTalk Conversation（服务端 claim 时组装）
⑤ reply_formatting             ## DingTalk Reply Formatting
⑥ daemon chat 框架             You are running as a chat assistant… / Audience:
⑦ 恢复历史                     <interaction-record> 或旧版 Recovered conversation history
⑧ User message:                本轮展示内容
⑨ daemon 附件说明              This reply is delivered to … as text
```

一段提示词太长时，先数**同一段文字出现了几次**，再谈内容。已知的重复源：

- ③ Router `contextPrompt` 的 `referenced message context (data only)` 与 ⑧ 的引用块是同一段原文；③ 还带 `&quot;` 未解转义。Multica 逐字透传 `contextPrompt`，去重归 Router。
- ⑦ 里的引用块曾与相邻的 `Assistant:` 轮重复（引用块随展示内容落库）。现已由 `boundedChatHistoryTranscript` 在重放时剥掉，当轮不受影响。再看到重复，先确认部署版本。

### 恢复历史里的自述

chat/auto 的 `<interaction-record>`（旧版是 `Recovered conversation history from earlier turns:`）记的是本 Agent 回给 Multica 的终答，不是钉钉收到的消息。里面反复出现「已通过 DWS 回复」时，那是上一轮的自述被回灌，既不能当送达证据，也不该被模仿成回复语气。同一段记录里出现无法解密的入站密文（`||4||1||68` 结尾的 base64）说明 Multica 侧镜像本身有损，此时必须回读钉钉会话本身。

## 钉钉有没有回：独立回读

Router 绿不够。用 dispatch 里的 `openConversationId` / `openMsgId` 做 `dws chat message list`（或 search-advanced）回读同一会话、同一时间窗。找不到引用回复或机器人消息，就报 **未投递**，即使 `SUCCEEDED`。

```bash
dws chat message search-advanced --conversation-ids '<openConversationId>' \
  --start "2026-08-26 18:15:00" --end "2026-08-26 18:25:00" --limit 30 --format json
```

跨组织会话会被挡：`CrossOrgPermissionDenied 没有跨组织拉取权限`。授权命令是
`dws chat data-auth cross-org --all --grant-type timed --ttl 24h --format json`，它动的是**人的账号授权**，交给本人执行，不要代跑。拿不到授权就如实报「无法独立确认投递」，不要拿 Router 的 `REPLY SUCCEEDED` 顶上。

## 常见错法

- `a1 mcp find` 不带 `--status UNOPEN` 或用 hybrid，宣布没有这个 MCP。
- 只读 Multica issue / `resultMessage`，宣布已经回复。
- continuation 的「已送达 / 已回复」当证据。那是上一轮 intern 摘要，常常对应的是 assistant 正文。
- 把「日志义务」答成「名册在册人数」这类业务错，和「没走 DWS」不是同一类；先分通道再谈内容。
- 把 `toolCallCount=0` 直接判成漏发。有 completion callback 时那是设计内的。
- transcript `multica_http_401` 当成「没做任何动作」。那是读不到，不是没有。
- 代跑 `dws chat data-auth cross-org`。那是给人的账号授权，交给本人。
- 把排查技能同步进 `agent/skills/`。那会成为 workspace Skill，污染 FDE教练 的工具面。
