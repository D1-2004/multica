# Agent Message Router 只读观测契约

本文定义 Router 读取 Multica 执行任务（task）摘要和 Transcript 的最小服务到服务契约。实现基线为 `origin/develop@e89dc37838bcce417f9e2ff51ee4acdd2de794b3`。

## 设计结论

- 复用现有 Agent dispatch endpoint 凭证，不新增 Aone 配置。
- 凭证主体绑定 Multica 的一个 `workspace_id + agent_id`，不是全局 Router 身份。
- 每次读取都先校验 endpoint Bearer，再要求目标 task 同时属于该工作区和智能体。
- 只读取现有 `agent_task_queue`、`task_usage` 和 `task_message`。不新增表、不复制 Transcript、不改变采集或保留期。
- 不修改、匿名化或放宽现有用户 API 和守护进程 API。
- `AGENT_MESSAGE_ROUTER_SERVICE_CREDENTIAL` 是 Multica 调 Router 的反向服务凭证，不用于本契约。

## 鉴权契约

Router 已通过 `MESSAGE_ROUTER_AGENT_DISPATCH_KEYS` 持有与 Multica 相同的版本化 keyring。Multica 使用 `MULTICA_AGENT_DISPATCH_KEYS` 和 `MULTICA_AGENT_DISPATCH_CURRENT_KEY_ID` 校验同一凭证。

现有 `dispatchUrl` 或 canonical dispatch Path 为：

```text
/api/webhooks/agent-dispatch/{endpointId}
```

`endpointId` 格式为 `{keyId}_{base64url-random}`。Router 继续使用现有 `MulticaDispatchCredentialProvider` 派生 Bearer：

```text
base64url-no-padding(
  HMAC-SHA256(masterKey[keyId], "multica-agent-dispatch:v1:" + endpointId)
)
```

请求头：

```http
Authorization: Bearer <derived-delivery-secret>
Accept: application/json
```

endpoint ID 只是 locator，不是 secret。主密钥和派生 Bearer 不进入 URL、数据库、task 审计快照或日志。

## 执行摘要

### 请求

```http
GET {dispatchUrl}/tasks/{taskId}/summary
```

无请求体。

### 响应

```json
{
  "task_id": "4d493f23-bd85-4fb7-bd1a-c3bba4efbe91",
  "status": "completed",
  "created_at": "2026-07-29T06:00:00.123Z",
  "dispatched_at": "2026-07-29T06:00:01.123Z",
  "started_at": "2026-07-29T06:00:02.123Z",
  "completed_at": "2026-07-29T06:00:05.123Z",
  "duration_ms": 3000,
  "provider": "anthropic",
  "model": "claude-sonnet-4",
  "input_tokens": 101,
  "output_tokens": 29,
  "cache_read_tokens": 17,
  "cache_write_tokens": 3,
  "message_count": 3,
  "tool_call_count": 1,
  "usage_details": [
    {
      "provider": "anthropic",
      "model": "claude-sonnet-4",
      "input_tokens": 101,
      "output_tokens": 29,
      "cache_read_tokens": 17,
      "cache_write_tokens": 3
    }
  ],
  "transcript_available": true
}
```

字段规则：

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `task_id` | string | Multica task UUID。 |
| `status` | string | 原样返回 `agent_task_queue.status`。Router 负责公开状态映射。 |
| `created_at` | RFC3339Nano string | task 创建时间，UTC。 |
| `dispatched_at` | RFC3339Nano string or null | 未派发时为 `null`。 |
| `started_at` | RFC3339Nano string or null | 未开始时为 `null`。 |
| `completed_at` | RFC3339Nano string or null | 未结束时为 `null`。 |
| `duration_ms` | long or null | 仅当 `started_at` 和 `completed_at` 都存在且顺序有效时计算；运行中不推断。 |
| `provider` / `model` | string or null | 仅有一条 usage 时返回该行；多模型或无 usage 时为 `null`。历史空字符串原样保留。 |
| token 汇总字段 | long or null | 有 usage 时对所有行求和；无 usage 时为 `null`，不以 `0` 冒充已采集。 |
| `message_count` | int | 当前 `task_message` 行数。 |
| `tool_call_count` | int | 当前 `task_message.type = "tool_use"` 的行数。 |
| `usage_details` | array | 按现有 `task_usage` 行返回；无 usage 时为 `[]`。 |
| `transcript_available` | boolean | task 通过本契约鉴权且 `task_message` 可读时为 `true`，与当前是否已有消息无关。 |

## Transcript 分页

### 请求

```http
GET {dispatchUrl}/tasks/{taskId}/messages?since=<seq>&limit=<limit>
```

无请求体。

查询参数：

| 参数 | 必填 | 规则 |
| --- | --- | --- |
| `since` | 否 | 非负 32 位整数；只返回 `seq > since`。省略时从最早消息开始。 |
| `limit` | 否 | `1..200`，默认 `100`。 |

### 响应

```json
{
  "items": [
    {
      "task_id": "4d493f23-bd85-4fb7-bd1a-c3bba4efbe91",
      "issue_id": "b79d108e-25be-40e1-91cd-ced89c69be7d",
      "seq": 12,
      "type": "tool_use",
      "tool": "Search",
      "input": {
        "query": "safe"
      },
      "created_at": "2026-07-29T06:00:04.123Z"
    }
  ],
  "next_cursor": "12"
}
```

- `items` 复用现有 `TaskMessagePayload`：`task_id/issue_id/seq/type/tool/content/input/output/created_at`。
- `created_at` 保持现有 RFC3339/RFC3339Nano 格式；Router 对外 LWP 继续转换为 epoch 毫秒。
- `content`、`input`、`output` 来自已经过现有 `redact.Text` / `redact.InputMap` 持久化链路的 `task_message`，读取时不建立第二套脱敏规则。
- 有下一页时，`next_cursor` 是本页最后一条消息的十进制 `seq` 字符串；没有下一页时为 `null`。
- Router 下一页将 `next_cursor` 原样作为 `since`。

## 错误契约

错误响应沿用 Multica：

```json
{
  "error": "task not found"
}
```

| HTTP | `error` | 条件 |
| --- | --- | --- |
| 400 | `invalid taskId` | `taskId` 不是 UUID。 |
| 400 | `invalid since parameter` | `since` 非整数、负数或超出 32 位范围。 |
| 400 | `invalid limit parameter` | `limit` 非整数或不在 `1..200`。 |
| 401 | `invalid dispatch credentials` | Bearer 缺失/非法、endpoint 格式非法、key 不存在或 endpoint 不存在。 |
| 404 | `task not found` | task 不存在、不属于 endpoint 工作区，或不属于 endpoint 智能体。统一 404，避免跨租户/跨智能体探测。 |
| 500 | `failed to resolve dispatch endpoint` | endpoint 归属查询发生存储故障。 |
| 500 | `failed to resolve task` | task 归属查询失败。 |
| 500 | `failed to get task usage` | usage 查询失败。 |
| 500 | `failed to get task message summary` | 消息计数查询失败。 |
| 500 | `failed to list task messages` | Transcript 查询失败。 |

## Router 接入方式

1. 从当前 Trace / `task_audit` 取得 `externalTaskId` 和 `agentId`。
2. 用现有 `agent_delivery_target` 解析该智能体的 canonical dispatch Path；不要给 `dispatch_task` 增加观测字段。
3. 用现有 `MulticaDispatchCredentialProvider.resolveDispatchUrl(...)` 得到 allowlist 校验后的 Multica URL。
4. 用原始 dispatch URL/Path 对应的 endpoint 调 `credentialFor(...)`，设置相同 Bearer。
5. 摘要请求追加 `/tasks/{externalTaskId}/summary`；Transcript 请求追加 `/tasks/{externalTaskId}/messages`。
6. `MulticaObservabilityHttpClient` 目前只有 `externalTaskId` 入参，Router 侧需要增加 `agentId` 或已解析 dispatch target 上下文；不能把本契约降级为 taskId-only 的全局读取凭证。
7. Router 映射建议：
   - `cachedInputTokens <- cache_read_tokens`
   - `cacheWriteTokens <- cache_write_tokens`
   - `usageDetails <- usage_details`
   - RFC3339Nano 时间先解析成 `Instant`，对 Dashboard/LWP 再输出 epoch 毫秒
   - `transcriptAvailable <- transcript_available`
8. 如果历史审计行没有可解析的 Agent target，Router 应只让摘要/Transcript 节点降级，不影响普通 Trace；不要回退到用户 JWT、守护进程 token、数据库直连或 `AGENT_MESSAGE_ROUTER_SERVICE_CREDENTIAL`。

## 配置影响

无新增配置。继续使用已存在的：

- Multica：`MULTICA_AGENT_DISPATCH_KEYS`、`MULTICA_AGENT_DISPATCH_CURRENT_KEY_ID`
- Router：`MESSAGE_ROUTER_AGENT_DISPATCH_KEYS`、`MESSAGE_ROUTER_MULTICA_DISPATCH_ORIGIN`、`MESSAGE_ROUTER_MULTICA_DISPATCH_HOSTS`

## 历史记录

- 2026-07-29：新增 Agent-scoped task 摘要和 Transcript 分页只读契约。原因：Router 观测后台已有 task 外部 ID，但现有用户/Daemon messages API 的身份边界不能由 Router 满足；复用现有 endpoint-specific dispatch HMAC/Bearer 可以在不新增配置、不扩大到全局服务权限、不改变采集与存储语义的前提下，补齐最小只读观测闭环。
