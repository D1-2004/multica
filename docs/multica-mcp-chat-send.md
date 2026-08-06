# Multica 自托管 MCP：Chat 续写协议

本文定义 Multica 服务端自托管 MCP 的第一期协议。目标是让正在执行的 Chat A 把一条消息发送到已有 Chat B，并触发 B 的 Agent 继续执行；Agent 侧不需要新增 `multica` CLI 命令，也不依赖第三方 MCP 托管。

## 1. 总体结构

Multica API 同时承担 MCP Server 和业务服务角色：

1. Chat A 的任务被 Runtime claim 时，Multica 服务端生成已有的任务令牌 `mat_...`。
2. 服务端把 `https://<MULTICA_PUBLIC_URL>/api/mcp` 动态合并到该任务的 `mcp_config.mcpServers`。
3. Runtime 使用现有 managed MCP 配置能力连接 Multica MCP；令牌只放在 `Authorization` 请求头中。
4. Agent 调用 `chat_send_message`，Multica 核验源任务和目标 Chat 后，复用现有原子 Chat 发送事务创建消息和目标任务。

这是一条服务端受控能力。工具定义、校验规则和业务行为都随 Multica API 发布；Agent 镜像只需已经具备仓库现有的远程 MCP 客户端能力。

## 2. 发布开关与运行条件

能力默认关闭，由后端 release flag `multica_mcp_chat_send` 同时控制“claim 时发现工具”和“执行工具”两处边界。

全局开启：

```bash
FF_MULTICA_MCP_CHAT_SEND=true
```

或在 `MULTICA_FEATURE_FLAGS_FILE` 中配置：

```yaml
multica_mcp_chat_send:
  default: true
```

还必须满足以下条件：

- `MULTICA_PUBLIC_URL` 是 Runtime 可访问的 Multica API 绝对地址；未配置时不注入 MCP。
- FC/E2B、ASB 等不可变 Cloud Sandbox manifest 必须声明 `mcp` capability；旧镜像未声明时不注入。
- 本地 Runtime 必须已经支持现有 managed `mcp_config` 通道。协议不要求新增 CLI 子命令，但无法让一个本身没有 MCP 客户端的旧 Runtime 获得 MCP 能力。

建议滚动顺序：先发布服务端且保持 flag 关闭，确认 API 与数据库基线正常，再开启 flag。回滚时先关闭 flag；已经排队的目标 Chat 任务是正常业务数据，不会因关 flag 被删除。

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

claim 响应内的 canonical MCP 配置形如：

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

## 6. 配置合并与兼容

Multica 使用保留 server name `multica`。claim 时合并遵循：

- 保留 Agent 已配置的其他 `mcpServers`。
- 保留 MCP 文档中非 `mcpServers` 的 provider 扩展字段。
- 若 Agent 自己配置了同名 `multica`，服务端条目覆盖它，防止把任务令牌发往非 Multica URL。
- 单任务 claim 与批量 claim 使用完全相同的注入逻辑。
- flag 关闭、Public URL 缺失、token 不是 `mat_`，或 Cloud Sandbox 未声明 `mcp` capability 时，claim 保持原配置不变。

## 变更历史

| 日期 | 变更 | 原因 |
|---|---|---|
| 2026-08-06 | 新增 Multica 自托管 Streamable HTTP MCP 与 `chat_send_message`，并在 claim 后以任务令牌动态注入。 | 让 Chat A 能把回答转交给已有 Chat B 并触发后续执行，同时把迭代和发布控制留在服务端，避免为新增 CLI 命令强制滚动 Agent 镜像。 |
