# 表情回复事件订阅与消费对接方案（Multica 侧）

日期：2026-08-17　分支：`feature/20260817_30789954_listen_emoji_msg_1`
上游文档：dm-bind `2026-08-17-multica-emoji-reaction-docking.md`、Router `multica-emotion-reply-dispatch.md`（+ `gateway-emotion-reply-dispatch.md`）

## 链路与分工

```
dm-bind 绑定页 ──LWP bindDingTalkAccount──> Router ──> Gateway 落 bubble_emotion 订阅
     │  source.subscriptionConfig.emojiReactionCids（禁 *）
     │  enabledDomains += "emotion_reply"（须含 channel，且 channel 非全量 *）
     │
     └──POST callbackUrl──> Multica 回调入库（本文 A 线）
                             message_scope_detail.emoji_reaction_cids + emoji_conversations

事件中心 ──MetaQ──> Gateway ──HSF──> Router（归一化为 channel/emotionReply，窗口聚合）
                                     └──HTTP Dispatch Command 2.0──> Multica（本文 B 线）
                                           messages[] 中带 reaction 子对象的条目即表情反应
```

绑定上行（dm-bind→Router）不经过 Multica，Multica 无透传改动。Multica 只做两件事：**回调入库**、**dispatch 消费**。

## A. 绑定回调解析与持久化

回调新字段契约（dm-bind 侧已落地）：

- `message_binding.message_scope_detail.emoji_reaction_cids`：`string[]`，可选，缺省=`[]`；**禁通配符 `*`**；元素非空、≤512、无空白字符
- `message_binding.emoji_conversations`：与 `conversations` 同结构（cid/name/avatar_media_id/avatar_url）；identity 模式（message skipped）时为 `[]`
- `direct_cids`/`group_cids` 同时为空仍拒绝，emoji 桶不参与该判断；修改绑定全量覆盖；存量记录缺字段按 `[]` 处理
- `subscriptions[]` 可能新增 `{domain:"emotion_reply"}` 条目——现有 `validBindingSubscriptions` 只要求 channel 在且 sourceID 匹配主 source，`emotion_reply` 天然通过；`EnabledDomains` 落库自动带上，**无需改动**

改动点：

1. `server/internal/integrations/agentmessagerouter/dingtalk_account_config.go`
   - `DingTalkMessageScopeDetail` 增加 `EmojiReactionCids []string \`json:"emoji_reaction_cids,omitempty"\``
   - `DingTalkAccountConfig` 增加 `EmojiConversations []DingTalkConversationSnapshot \`json:"emoji_conversations,omitempty"\``
   - `normalizeDingTalkMessageScopeDetail`：emoji 桶缺省归一化为 `[]`，逐元素过 `validScopeCID` 且显式拒绝 `*`
   - 会话快照校验抽共享 helper（现为 `normalizeDingTalkConversationBindingForVersion` 内联循环），`EmojiConversations` 复用；`Validate()` 覆盖新字段，存量记录（无字段）天然通过
   - `PublicDingTalkMessageScopeView` 增加 `emoji_reaction_cids`（v1 记录升格为 `[]`）；`PublicDingTalkBindingOutcome` 增加 `emoji_conversations`；`PublicBinding()` 填充——满足"回调原样回显"验收
2. `server/internal/integrations/agentmessagerouter/completion.go`（+ `service.go` 的 `completeCallback`）
   - `MessageBindingResult` 增加 `EmojiConversations`
   - `validateCompleteBindingParams`：success 分支校验 emoji_conversations；skipped 分支要求其为空（identity 模式兼容）；failed 分支与 conversations 同规则
   - `validatedMessageBinding` 携带 emojiConversations，`completeCallback` 与 `CompleteBinding` 失败回执路径写入 config（全量覆盖，无增量合并）
3. 前端（仅类型/schema 透传）
   - `packages/core/types/dingtalk-account-binding.ts`：`DingTalkMessageScopeSubscription` 加 `emojiReactionCids: string[]`；`DingTalkMessageRouteOutcome` 加 `emojiConversations`
   - `packages/core/api/schemas.ts`：subscription schema 加 `emoji_reaction_cids`（缺省 `[]`），outcome 加 `emoji_conversations`
   - `packages/views` 绑定卡渲染本期不改（上游文档无要求，如需展示另起迭代）

## B. Dispatch 消费（Router → Multica webhook）

契约要点：

- `event.domain=channel` 不变；顶层 `event.type` 纯表情窗口为 `emotionReply`、混合窗口可能仍为 `message.created`——**识别只看条目级 `messages[].reaction`，不看顶层 type**
- reaction 条目：`{ emotionName, emotionTypeV2, emotionVersion, action: "add"|"remove", operateTime }`；条目的 `messageId/openMsgId/text` 指向**被反应的消息**；`occurredAt` 是表情操作时间；data 层 `sender` 是贴/移除表情的人
- 幂等、`completionCallback`、`outbound.replyTo=latest_message`（回复目标即被反应消息）全部复用，无改动

改动点（`server/internal/handler`）：

1. `agent_dispatch_v2.go`
   - 新增 `DispatchMessageReaction` 类型；`DispatchMessage` 增加 `Reaction *DispatchMessageReaction \`json:"reaction,omitempty"\``
   - `validateChannelMessageCreated` 放行 `channel/emotionReply`（仅限 `source.type=digital_employee`）；reaction 条目校验：`action ∈ {add,remove}`、`emotionName` 非空且长度受限；`openMsgId` 仍必填；**text/附件要求对 reaction 条目放宽**（被反应的可能是纯附件消息，AI 可读内容可为空，渲染时回退为"一条消息"）
   - `NewDispatchPromptBuilder` 注册 `("channel","emotionReply","digital_employee")`，复用 channel 渲染函数
   - `buildDingTalkChannelDisplay` 按条目分流：reaction 条目渲染为 `对消息"{text 摘要}"贴上/移除了表情 {emotionName}`（sender 由既有头部携带，语义同 Router 文档建议）；普通条目不变
   - `applyDingTalkDispatchPromptForClaimWithFeatureFlags` 的 `channelMessage` 条件放行 `emotionReply`——否则表情事件的 claim 投影会丢掉 DWS outbound 运行时指令
   - `dispatchIssueTitle`：窗口首条目为 reaction 时，摘要采用「对消息"…"的表情回复」形式（默认方案，可评审）
2. `agent_dispatch_v2_handler.go`
   - `createAgentDispatchChatV2` 的 `dispatchText` 拼装：reaction 条目改用渲染句而非 `text` 原文——否则"被回复消息的原文"会被当成用户输入注入 chat 会话
3. 幂等/上下文：无需改动
   - `dispatchWindowIdempotencyKey` 对 messages JSON 做哈希，reaction 字段自然入键；add/remove 是独立事件、键不同，符合"remove 不抵消 add"
   - `dispatchRuntimeContext` 全量持久化 event.data，reaction 随 `dispatch_event_data` 进任务私有上下文

## 明确不改

- Multica → Router 绑定上行（dm-bind 直连 Router LWP；MCP `bind_digital_employee_to_multica_agent` 本期不加 emoji 参数）
- 解绑链路（Router 只收敛本地状态，不删上游订阅规则）
- `schemaVersion` 仍为 2；`DispatchEventData.Reaction`（data 级 legacy 透传字段）与本次无关，不动
- Router 状态表情（处理中/完成）闭环由 Router 自闭环，Multica 不参与

## 已知边界（接受）

- chat surface 下入站去重以"被反应消息 openMsgId"为键：同一消息跨窗口的多次表情操作可能被判重丢弃。Router 窗口防抖是主幂等层，此为小概率边缘，接受
- 表情反应是弱信号，Agent 静默并正常回报完成是合法结果（prompt 不强制响应）

## 测试

- `message_scope_detail_test.go`：emoji 桶校验（含 `*` 拒绝、非法 cid 拒绝、缺省=`[]`、direct+group 双空仍拒绝）
- `completion`/`service` 测试：success 落库并回显、identity skipped 携带 `emoji_conversations:[]` 不报错、replaceExisting 全量覆盖
- `agent_dispatch_v2_test.go`：`emotionReply` 校验通过、reaction 条目校验、混合窗口渲染、纯表情窗口渲染、claim 指令重建放行 `emotionReply`
- `packages/core/api/schemas.test.ts`：新字段解析与缺省

## 联调验收（对齐上游清单）

绑定线：含 `emojiReactionCids:["cid_a"]` 的绑定落库并在回调响应回显；缺省/`[]` 不报错；含 `*` 回调返回 400；replaceExisting 全量替换；identity 模式不报错。
消费线：纯表情 dispatch（`type=emotionReply`）prompt 正确渲染且 `completionCallback` 正常回报；混合窗口两类条目均能解析；`action=remove` 不报错。
