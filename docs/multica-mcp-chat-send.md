# Multica 自托管 MCP：Chat 续写协议

本文定义 Multica 服务端自托管 MCP 的第一期协议。目标是让正在执行的 Chat A 把一条消息发送到已有 Chat B，并触发 B 的 Agent 继续执行；Agent 侧不需要新增 `multica` CLI 命令，也不依赖第三方 MCP 托管。

## 1. 总体结构

Multica API 同时承担 MCP Server 和业务服务角色：

1. Chat A 的任务被 Runtime claim 时，Multica 服务端生成已有的任务令牌 `mat_...`。
2. MCP Client 由使用方显式配置为连接 `https://<multica-api>/api/mcp`；Multica 不改写 claim 返回的 `mcp_config`，也不让沙箱默认使用该 MCP。
3. 显式接入的 Client 使用 claim 返回的任务令牌连接 Multica MCP；令牌只放在 `Authorization` 请求头中。
4. Agent 调用 `chat_send_message`，Multica 核验源任务和目标 Chat 后，复用现有原子 Chat 发送事务创建消息和目标任务。

这是一条服务端受控、客户端显式选择的能力。工具定义、校验规则和业务行为都随 Multica API 发布；部署服务端 MCP 不会改变现有 Agent 或沙箱的启动配置。

## 2. 发布开关与运行条件

能力默认关闭，由后端 release flag `multica_mcp_chat_send` 控制 `/api/mcp` 的协议发现和工具执行。该开关不修改 task claim 响应。

全局开启：

```bash
FF_MULTICA_MCP_CHAT_SEND=true
```

或在 `MULTICA_FEATURE_FLAGS_FILE` 中配置：

```yaml
multica_mcp_chat_send:
  default: true
```

显式接入还必须满足以下条件：

- 调用方能够访问 Multica API 的 `/api/mcp` 地址。
- 调用方本身支持 Streamable HTTP MCP，并显式传入当前任务的 `mat_` 令牌。
- 沙箱和 Agent 的既有 `mcp_config` 保持不变；是否安装和使用 Multica MCP 由调用方负责，不能依赖 claim 自动注入。

建议滚动顺序：先发布服务端且保持 flag 关闭，确认 API 与数据库基线正常，再开启 flag。开启或关闭 flag 都不会改变沙箱启动配置；已经排队的目标 Chat 任务是正常业务数据，不会因关 flag 被删除。

## 3. MCP 传输协议

- Endpoint：`/api/mcp`
- Transport：MCP Streamable HTTP，stateless request/response 模式
- JSON-RPC：`2.0`
- 首选协议版本：`2025-06-18`
- 兼容协议版本：`2025-03-26`；后续请求未带 `MCP-Protocol-Version` 时按该版本兼容
- `POST`：承载单条 JSON-RPC request 或 notification
- `GET`：返回 `405 Method Not Allowed`；V1 不提供 server-initiated SSE stream
- 支持的方法：`initialize`、`notifications/initialized`、`ping`、`tools/list`、`tools/call`
- 不支持 JSON-RPC batch

当前实现参考 MCP 官方 [Streamable HTTP transport](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)、[lifecycle](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle) 和 [tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools) 规范。

## 4. 鉴权与安全边界

显式接入的 MCP Client 配置形如：

```json
{
  "mcpServers": {
    "multica": {
      "type": "http",
      "url": "https://api.example.com/api/mcp",
      "headers": {
        "Authorization": "Bearer mat_<redacted>"
      }
    }
  }
}
```

安全规则：

- 只接受 Auth middleware 标记为 `X-Actor-Source: task_token` 的 `mat_` 任务令牌。
- `X-User-ID`、`X-Agent-ID`、`X-Task-ID`、`X-Workspace-ID` 均由服务端根据 token row 覆盖，客户端自报值不是授权依据。
- token 不放入 URL、工具参数、日志或工具返回值。
- MCP handler 会重新查询源任务，核验源 Agent、源任务和 workspace 一致。
- 浏览器请求若携带 `Origin`，只接受配置的 API、App 或 Frontend origin，避免 DNS rebinding 类跨源调用。
- V1 是 Multica Runtime 与 Multica API 同一信任域内的预置 bearer 模式，不是面向任意外部 MCP Client 的通用 OAuth 接入点。

## 5. Tool：`chat_send_message`

### 输入

```json
{
  "session_id": "<target-chat-session-uuid>",
  "content": "要发送给目标 Chat 的完整消息"
}
```

| 字段 | 必填 | 规则 |
|---|---:|---|
| `session_id` | 是 | 有效 UUID；必须是另一个 Chat，不能等于源任务所在 Chat |
| `content` | 是 | trim 后非空；服务端原样保存，不擅自改写正文 |

### 目标校验

调用成功前必须同时满足：

- 源任务是 `running` 或 `dispatched` 状态的 Chat 任务。
- 目标 Chat 与源任务属于同一 workspace。
- 目标 Chat 的 creator 是任务令牌绑定的 Runtime owner。
- 目标 Chat 处于 `active` 状态。
- 目标 Agent 未归档且绑定了 Runtime。
- 源任务的顶层 human originator 有权触发目标 Agent；private/public-to 规则继续使用现有 `canInvokeAgent` 判定。

### 成功结果

```json
{
  "session_id": "<target-chat-session-uuid>",
  "message_id": "<created-message-uuid>",
  "task_id": "<queued-target-task-uuid>",
  "trace_id": "<chat-trace-id>",
  "created_at": "<RFC3339 timestamp>"
}
```

结果同时放在 MCP tool result 的 `structuredContent` 中，并以 JSON 文本形式放入 `content[0].text`，兼容尚未读取 structured output 的客户端。

### 写入语义

- 复用 `TaskService.SendDirectChatMessageWithContext`（现有 direct-send 原子事务的带上下文入口），目标 task、`role=user` 的 Chat message、message-task 绑定和 session touch 在同一事务内提交。
- 事务提交后才广播 `task:queued` 并唤醒 Runtime。
- Chat WebSocket 消息的 actor 是源 Agent，目标 task 的 human originator 继承自源任务。
- 目标 task context 持久化 `mcp_forwarded_from_task_id`、`mcp_forwarded_from_chat_session_id` 和 `mcp_forwarded_from_agent_id`，用于跨 Chat 追溯来源；这些字段不包含 token。
- V1 工具明确标记 `idempotentHint=false`。客户端在超时后无条件重试可能产生重复消息；加入持久化 idempotency key 属于后续协议版本，不能在未升级协议前假设幂等。

### 失败语义

- HTTP 鉴权、release flag、Origin 和协议版本错误使用对应的 `4xx/503`。
- JSON-RPC 结构、方法名和参数错误使用 JSON-RPC error。
- 已通过协议校验、但源任务或目标 Chat 不满足业务约束时，返回 MCP tool result `isError=true`，不创建目标消息或任务。
- 内部数据库错误只返回通用失败信息，详细错误仅写服务端日志且不包含 token。

## 6. 客户端配置边界与兼容

Multica 只提供服务端 endpoint，不拥有 Client 的 MCP 配置：

- 单任务 claim 和批量 claim 都只返回既有任务令牌，不向 `mcp_config.mcpServers` 增加 `multica`。
- Agent 已配置的 `mcpServers` 和 provider 扩展字段不会被本功能覆盖。
- 调用方可以自行使用 `multica` 作为 Client 侧 server name，但必须显式配置可信 URL 和 `Authorization` 请求头。
- flag 只决定 `/api/mcp` 是否接受调用；关闭 flag 不清理或改写任何 Client、Agent 或沙箱配置。

## 变更历史

| 日期 | 变更 | 原因 |
|---|---|---|
| 2026-08-07 | 移除 task claim 对 Multica MCP 的自动注入，保留服务端 endpoint、鉴权和工具能力。 | 自动改写沙箱 MCP 配置会影响现有 Runtime 启动；服务端能力发布不应让沙箱默认安装或使用。 |
| 2026-08-06 | 新增 Multica 自托管 Streamable HTTP MCP 与 `chat_send_message`，并在 claim 后以任务令牌动态注入。 | 让 Chat A 能把回答转交给已有 Chat B 并触发后续执行，同时把迭代和发布控制留在服务端，避免为新增 CLI 命令强制滚动 Agent 镜像。 |
