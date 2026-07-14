# Chat Session 回复模板设计

## 目标

让沙箱调用者通过稳定的 `session_key` 幂等创建/定位 Chat Session、投递消息，并按 `task_id` 查询每个 Turn 的状态和最终回复。Session 可绑定固定回复模板；Turn 完成后，由发起方沙箱内安装的后处理脚本使用本地 DWS 登录态发送最终回复，中间流式内容不触发回信。

## 核心边界

- Multica Server 只保存模板名、结构化配置、Turn 状态与最终回复，不保存或执行 Shell。
- 回复脚本安装并运行在发起方沙箱内，因此能复用该沙箱的 DWS 登录态。
- Provider Streaming 与 WebSocket 只降低等待延迟，不承担可靠性；可靠事实源是 `agent_task_queue` 与最终 `chat_message`。
- 每个 Turn 创建时快照 Session 的模板与配置，后续修改 Session 不会改变旧 Turn 的回信目标。
- 模板只接收 terminal Turn，`queued/dispatched/running` 及中间消息全部忽略。

## API

### SessionKey

`session_key` 在 `(workspace_id, creator_id)` 内唯一，最大 200 字符。

- `PUT /api/chat/sessions/by-key/{sessionKey}`：幂等创建或返回 Session。首次请求需要 `agent_id`；重复请求的 agent 不一致时返回 409。
- `GET /api/chat/sessions/by-key/{sessionKey}`：按 key 查询 Session。
- `POST /api/chat/sessions/by-key/{sessionKey}/messages`：按 key 投递消息，响应继续返回 `task_id`。

原有 UUID API 保持兼容；`POST /api/chat/sessions` 增量接受 `session_key`、`reply_template`、`reply_config`。

### Turn

- `GET /api/chat/sessions/{sessionId}/turns/{turnId}`：返回 Turn 状态、最终 reply、error、时间和回复模板快照。
- terminal 状态为 `completed`、`failed`、`cancelled`。
- `completed` 一定包含普通回复或 `no_response` outcome；失败包含服务端持久化的失败 assistant outcome。

## 沙箱模板

CLI 提供：

```text
multica chat reply-template install <name> --script <path>
multica chat reply-template list
multica chat reply-template remove <name>
```

脚本以可执行文件形式安装到 `~/.multica/chat-reply-templates/<name>/run`。执行时 stdin 是完整 Turn JSON；环境变量包含 Session/Turn 标识。脚本退出码 0 表示发送成功，非 0 原样返回错误且不把 Turn 改成失败。

`chat start/send --wait` 等待终态；Session 带 `reply_template` 时自动等待并执行对应本地模板。DWS 模板配置沿用最近派发 Issue 的结构化字段：

```json
{
  "mode": "reply",
  "openConversationId": "...",
  "openMessageId": "...",
  "senderOpenDingTalkId": "...",
  "aiTag": true
}
```

## 前端

新建 Session 时可选择固定回复模板并编辑结构化 JSON 配置。前端仅保存模板名/配置，不上传脚本；脚本是否已安装由具体沙箱负责。

## 安全

- Server 永不执行模板。
- CLI 直接执行已安装文件，不经 `sh -c`，模板名拒绝路径分隔符。
- reply config 必须是 JSON object，并限制体积。
- SessionKey 通过 URL decode 后校验长度；所有查询仍受 workspace、creator 与 agent 权限约束。
