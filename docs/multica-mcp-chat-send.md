# Multica 自托管 MCP：Chat 续写协议

本文定义 Multica 服务端自托管 MCP 的 Chat 续写和 Agent 查询协议。它既支持用户从本机 MCP Client 以个人身份继续一个已有 Chat，也支持正在执行的 Chat A 把消息发送到已有 Chat B 并触发 B 的 Agent 继续执行，还提供受权限约束的 Agent 发现能力；Agent 侧不需要新增 `multica` CLI 命令，也不依赖第三方 MCP 托管。

Qoder、Claude Code 的安装配置、task token 限制和使用示例见 [Multica MCP 客户端接入指南](multica-mcp-client-setup.md)。数字员工绑定工具的服务端契约见 [Native MCP Direct Binding Contract](dingtalk-account-binding-router-contract.md#native-mcp-direct-binding-contract)。

## 1. 总体结构

Multica API 同时承担 MCP Server 和业务服务角色：

1. 本机 Codex、Qoder、Claude Code 等显式接入的 Client 使用已有的 `mul_` PAT；客户端不传 Workspace header。
2. Chat A 的任务被 Runtime claim 时，Multica 服务端生成已有的任务令牌 `mat_...`；该 token 固定绑定用户、Agent、Task 和 Workspace。
3. MCP Client 由使用方显式配置为连接 `https://<multica-api>/api/mcp`；Multica 不改写 claim 返回的 `mcp_config`，也不让沙箱默认使用该 MCP。
4. 调用 `chat_send_message` 时，PAT 分支以用户成员身份继续其已有 Chat；Task Token 分支核验源任务并保留 A→B 来源。两者都复用现有原子 Chat 发送事务创建消息和目标任务。

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
- 调用方本身支持 Streamable HTTP MCP，并显式传入 `mul_` PAT 或当前任务的 `mat_` 令牌。
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

本机 MCP Client 的 PAT 配置形如：

```json
{
  "mcpServers": {
    "multica": {
      "type": "http",
      "url": "https://api.example.com/api/mcp",
      "headers": {
        "Authorization": "Bearer mul_<redacted>"
      }
    }
  }
}
```

安全规则：

- 接受 Auth middleware 已验证的 `mul_` Personal Access Token 和标记为 `X-Actor-Source: task_token` 的 `mat_` Task Token；普通登录 Cookie、JWT 或其他 bearer 不能进入 MCP handler。
- PAT 分支的 `X-User-ID` 由 Auth middleware 按 token row 写入。Chat 根据 `session_id`、数字员工工具根据 `agent_id` 反查 Workspace，再由业务权限门校验 PAT 用户仍是成员。
- Agent 查询不接收 Workspace 参数。PAT 查询遍历该用户仍有成员关系的 Workspace，并复用现有 Agent 可见性规则；Task Token 查询固定在 token 的 Workspace。
- Task Token 分支的 `X-User-ID`、`X-Agent-ID`、`X-Task-ID`、`X-Workspace-ID` 均由服务端根据 token row 覆盖，客户端自报值不是授权依据。
- token 不放入 URL、工具参数、日志或工具返回值。
- Task Token 分支会重新查询源任务，核验源 Agent、源任务和 workspace 一致；PAT 分支按用户身份核验 Chat 所有权和目标 Agent invoke 权限。
- 浏览器请求若携带 `Origin`，只接受配置的 API、App 或 Frontend origin，避免 DNS rebinding 类跨源调用。
- 这是基于 Multica 已有 PAT/Task Token 的 bearer 模式，不是面向任意外部 MCP Client 的通用 OAuth 接入点。

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
| `session_id` | 是 | 有效 UUID；PAT 可指定本人已有 Chat；Task Token 必须指定源任务之外的另一个 Chat |
| `content` | 是 | trim 后非空；服务端原样保存，不擅自改写正文 |

### 目标校验

两种鉴权都必须满足：

- 目标 Chat 属于鉴权 Workspace，creator 是鉴权用户。
- 目标 Chat 处于 `active` 状态。
- 目标 Agent 未归档且绑定了 Runtime。
- 鉴权用户有权触发目标 Agent；private/public-to 规则继续使用现有 `canInvokeAgent` 判定。

Task Token 还必须满足：源任务是 `running` 或 `dispatched` 状态的 Chat Task；目标 Chat 不是源 Chat；源任务的持久化顶层 human originator 是权限判断和目标任务的用户来源。

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

- PAT 分支复用 `TaskService.SendDirectChatMessage`，以鉴权用户作为 initiator 和 `member` uploader，不伪造源 Agent 或源 Task。
- Task Token 分支复用 `TaskService.SendDirectChatMessageWithContext`（现有 direct-send 原子事务的带上下文入口），以源 Agent 作为 uploader，并继承源任务的 human originator。
- 两个分支都在同一事务内提交目标 Task、`role=user` 的 Chat message、message-task 绑定和 session touch。
- 事务提交后才广播 `task:queued` 并唤醒 Runtime。
- PAT 分支的 Chat WebSocket actor 是鉴权 member，目标 Task 不写入 Task-to-Task 来源字段。
- Task Token 分支的 Chat WebSocket actor 是源 Agent；目标 Task context 持久化 `mcp_forwarded_from_task_id`、`mcp_forwarded_from_chat_session_id` 和 `mcp_forwarded_from_agent_id`，用于跨 Chat 追溯来源；这些字段不包含 token。
- V1 工具明确标记 `idempotentHint=false`。客户端在超时后无条件重试可能产生重复消息；加入持久化 idempotency key 属于后续协议版本，不能在未升级协议前假设幂等。

### 失败语义

- HTTP 鉴权、release flag、Origin 和协议版本错误使用对应的 `4xx/503`。
- JSON-RPC 结构、方法名和参数错误使用 JSON-RPC error。
- 已通过协议校验、但 PAT 用户、源任务（仅 Task Token）或目标 Chat 不满足业务约束时，返回 MCP tool result `isError=true`，不创建目标消息或任务。
- 内部数据库错误只返回通用失败信息，详细错误仅写服务端日志且不包含 token。

## 6. Tools：`search_agents` 与 `list_agents`

`search_agents` 按 Agent 名称做忽略大小写的包含匹配：

```json
{
  "keyword": "探针"
}
```

`keyword` trim 后必须非空。`list_agents` 不接收业务参数，输入为 `{}`。

两个工具都只返回未归档的用户 Agent，结果结构一致：

```json
{
  "agents": [
    {
      "id": "<agent-uuid>",
      "workspace_id": "<workspace-uuid>",
      "workspace_name": "Workspace A",
      "workspace_slug": "workspace-a",
      "name": "Agent name",
      "description": "...",
      "instructions": "...",
      "avatar_url": null,
      "runtime_id": "<runtime-uuid>",
      "runtime_mode": "local",
      "status": "idle",
      "permission_mode": "private",
      "visibility": "private",
      "invocation_targets": [],
      "owner_id": "<user-uuid>",
      "max_concurrent_tasks": 1,
      "model": "",
      "thinking_level": "",
      "created_at": "<RFC3339 timestamp>",
      "updated_at": "<RFC3339 timestamp>"
    }
  ],
  "count": 1
}
```

- PAT：跨该用户的全部 Workspace 查询，但普通成员只能看到自己拥有或 invocation allow-list 允许查看的 Agent；Workspace owner/admin 沿用现有治理视图，可看到该 Workspace 的全部活跃 Agent。
- Task Token：只查询 token 固定的 Workspace，并沿用现有 Agent actor 的 Workspace 内协作可见性。
- 两个工具都是只读、幂等工具，不返回 `mcp_config`、`custom_env`、`custom_args`、`runtime_config`、Composio allow-list 或任何凭据。
- `structuredContent` 和 `content[0].text` 返回同一份结果，兼容不同 MCP Client。

## 7. 客户端配置边界与兼容

Multica 只提供服务端 endpoint，不拥有 Client 的 MCP 配置：

- 单任务 claim 和批量 claim 都只返回既有任务令牌，不向 `mcp_config.mcpServers` 增加 `multica`。
- Agent 已配置的 `mcpServers` 和 provider 扩展字段不会被本功能覆盖。
- 调用方可以自行使用 `multica` 作为 Client 侧 server name，但必须显式配置可信 URL 和 `Authorization` 请求头。
- flag 只决定 `/api/mcp` 是否接受调用；关闭 flag 不清理或改写任何 Client、Agent 或沙箱配置。

## 变更历史

| 日期 | 变更 | 原因 |
|---|---|---|
| 2026-08-07 | 新增 `search_agents` 和 `list_agents`，由服务端推导 Workspace，并按 PAT 用户可见性或 Task Token Workspace 返回非敏感 Agent 详情。 | 让通用 MCP Client 能先通过名称找到 Agent UUID、查看 Agent 元数据，再调用绑定或 Chat 等后续工具，同时避免重新引入客户端 Workspace header 或泄露 Agent 配置凭据。 |
| 2026-08-07 | PAT 客户端不再传 Workspace header；服务端从目标 Chat 或 Agent 反查 Workspace 并校验成员关系。 | Streamable HTTP Client 的连接初始化不携带业务资源，要求全局 Workspace header 会阻断 Codex 等通用客户端；资源级解析同时避免多 Workspace 用户产生默认选择歧义。 |
| 2026-08-07 | 增加 `mul_` PAT 鉴权；PAT 以用户成员身份继续已有 Chat，`mat_` Task Token 继续保留 A→B 固定任务身份和来源追溯。 | 支持 Qoder、Claude Code 使用已有个人令牌显式接入，同时不扩大运行中 Agent 的最小权限边界。 |
| 2026-08-07 | 移除 task claim 对 Multica MCP 的自动注入，保留服务端 endpoint、鉴权和工具能力。 | 自动改写沙箱 MCP 配置会影响现有 Runtime 启动；服务端能力发布不应让沙箱默认安装或使用。 |
| 2026-08-06 | 新增 Multica 自托管 Streamable HTTP MCP 与 `chat_send_message`，并在 claim 后以任务令牌动态注入。 | 让 Chat A 能把回答转交给已有 Chat B 并触发后续执行，同时把迭代和发布控制留在服务端，避免为新增 CLI 命令强制滚动 Agent 镜像。 |
