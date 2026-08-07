# Multica MCP 客户端接入指南

本文说明如何在 Codex、Qoder 和 Claude Code 中显式接入 Multica 自托管 MCP。Multica 只提供服务端 endpoint，不会在执行任务（task）claim 时改写智能体或沙箱的 MCP 配置。

协议和业务约束分别见：

- [Multica 自托管 MCP：Chat 续写协议](multica-mcp-chat-send.md)
- [DingTalk account binding Router contract](dingtalk-account-binding-router-contract.md#native-mcp-direct-binding-contract)

## 1. 接入参数

| 参数 | 值 |
| --- | --- |
| Server name | `multica` |
| Transport | Streamable HTTP，客户端配置值为 `http` |
| 预发 endpoint | `https://pre-fde-workbench.dingtalk.com/api/mcp` |
| 正式 endpoint | `https://fde-workbench.dingtalk.com/api/mcp` |
| 本地客户端鉴权 | `Authorization: Bearer mul_<personal-access-token>` |
| Multica 任务鉴权 | `Authorization: Bearer mat_<current-task-token>`；工作区和 Agent 由服务端从 token 解析 |

预发和正式环境的 URL、执行任务、工作区和 token 必须属于同一个环境。不要用预发环境生成的 token 调用正式 endpoint，反之亦然。

服务端还必须开启 release flag：

```bash
FF_MULTICA_MCP_CHAT_SEND=true
```

虽然 flag 名保留了第一期的 `chat_send` 命名，它当前控制整个 `/api/mcp` endpoint，包括 Chat 续写和数字员工绑定工具。

## 2. 两种鉴权模式

Multica MCP 接受已有的 `mul_` Personal Access Token（PAT）和 `mat_` Task Token，不接受普通登录 Cookie、JWT 或 OAuth 登录态直接调用。

| 行为 | `mul_` PAT | `mat_` Task Token |
| --- | --- | --- |
| 适用场景 | 用户在本机 Codex、Qoder、Claude Code 中长期显式配置 | 正在运行的 Multica Agent 短期使用 |
| 身份 | PAT 所属用户 | 固定的用户、Agent、Task、Workspace |
| Workspace | Chat 按 `session_id`、数字员工工具按 `agent_id` 反查，并校验 PAT 用户成员关系 | 服务端按 token row 覆盖客户端请求头 |
| Chat 发消息 | 只能向该用户创建的已有 Chat 发消息，并按 `member` 权限触发 Agent | 只能从活跃 Chat Task 转到该用户创建的另一个 Chat，按 `agent` 身份执行并记录来源 |
| 数字员工工具 | 必须传 `agent_id`，且用户对该 Agent 同时具有 manage 和 invoke 权限 | 不传 `agent_id`；只能操作 token 绑定的 Agent |
| Agent 查询 | 跨用户加入的全部 Workspace，返回该用户当前可见的活跃 Agent | 只返回 token 所在 Workspace 的活跃 Agent |
| 生命周期 | 由用户在 Multica 的 PAT 管理界面创建、续期和撤销 | 最长 24 小时，Task 完成、失败或取消后主动撤销 |

PAT 代表用户本人，但不绕过业务权限：服务端仍校验 Workspace 成员关系、Chat 所有权、Agent invoke 权限以及数字员工 manage-plus-invoke 权限。客户端不能通过 `agent_id` 或请求头切换成另一个用户。

本机接入前，在 Multica Web 的 **Settings → Personal Access Tokens → New token** 创建 PAT；完整 token 只在创建时展示一次。客户端不需要配置 Workspace header。

Task Token 仍由 Multica 在任务 claim 时生成，并作为 `MULTICA_TOKEN` 注入当前 Agent 进程；Multica 不会自动生成或改写 MCP 客户端配置。它适合 A 对话把回答转交给 B 对话等任务内协作，不应作为本机长期凭据。

## 3. 在 Codex 中配置

Codex 的 `--bearer-token-env-var` 参数要求填写环境变量名，不能直接填写 `mul_...` token。先确保启动 Codex 的进程能够读取 `MULTICA_PAT`：

```bash
export MULTICA_PAT="mul_<personal-access-token>"
```

然后重新添加 MCP server：

```bash
codex mcp remove multica
codex mcp add multica \
  --url "https://pre-fde-workbench.dingtalk.com/api/mcp" \
  --bearer-token-env-var MULTICA_PAT
codex mcp list
```

对应的 `~/.codex/config.toml` 应类似：

```toml
[mcp_servers.multica]
url = "https://pre-fde-workbench.dingtalk.com/api/mcp"
bearer_token_env_var = "MULTICA_PAT"
enabled = true
```

这里的 `MULTICA_PAT` 是环境变量名。不要写成 `bearer_token_env_var = "mul_..."`，否则 Codex 会尝试读取一个名为 `mul_...` 的环境变量，最终不会发送 Authorization。Codex Desktop 修改环境变量后需要完全退出并重新启动；正式环境把 URL 换成 `https://fde-workbench.dingtalk.com/api/mcp`。

## 4. 在 Qoder 中配置

### Qoder IDE

1. 打开 Qoder Settings。
2. 进入 MCP → My Servers，点击 Add。
3. 添加以下配置，并把 URL 和 PAT 替换成当前环境的值：

```json
{
  "mcpServers": {
    "multica": {
      "type": "http",
      "url": "https://pre-fde-workbench.dingtalk.com/api/mcp",
      "headers": {
        "Authorization": "Bearer mul_<personal-access-token>"
      }
    }
  }
}
```

4. 保存后确认 `multica` 旁显示连接图标，并展开检查工具列表。

Qoder 会自动识别 Streamable HTTP。旧版 Qoder 若只在界面中显示 SSE 选项，也应把上述 endpoint 填入远程 endpoint 配置；Qoder 会自动探测 Streamable HTTP。

### Qoder CLI

先在当前 shell 中提供 endpoint 和 PAT：

```bash
export MULTICA_MCP_URL="https://pre-fde-workbench.dingtalk.com/api/mcp"
export MULTICA_PAT="mul_<personal-access-token>"
```

然后添加到当前项目的 local scope：

```bash
qodercli mcp add --transport http --scope local \
  multica "$MULTICA_MCP_URL" \
  --header "Authorization: Bearer $MULTICA_PAT"
```

local scope 的配置文件是：

```text
${project}/.qoder/settings.local.json
```

也可以直接把与 Qoder IDE 相同的 `mcpServers` JSON 写入这个文件。不要把真实 `mul_` PAT 写入项目级 `.mcp.json`，因为项目级配置通常会提交到代码仓库。添加或修改后执行：

```bash
qodercli mcp list
```

如果 Qoder CLI 已经启动，在交互会话中执行：

```text
/mcp reload
```

写操作默认应保留人工确认。若确实要配置权限，工具名格式为 `mcp__multica__<tool-name>`；不要默认放行 `mcp__multica__*`，因为其中包含发消息、绑定和解绑操作。

## 5. 在 Claude Code 中配置

### 方式 A：CLI 添加到 local scope

先在当前 shell 中提供 endpoint 和 PAT：

```bash
export MULTICA_MCP_URL="https://pre-fde-workbench.dingtalk.com/api/mcp"
export MULTICA_PAT="mul_<personal-access-token>"
```

然后添加 MCP server：

```bash
claude mcp add --transport http multica \
  --scope local \
  "$MULTICA_MCP_URL" \
  --header "Authorization: Bearer $MULTICA_PAT"
```

`local` scope 保存在本机 `~/.claude.json` 的当前项目配置中，不会写入代码仓库。该命令会把当时的 header 值保存到本机配置；PAT 续期或撤销后，需要删除并重新添加，或直接更新本机配置。

检查连接：

```bash
claude mcp list
claude mcp get multica
```

在 Claude Code 会话内也可以执行 `/mcp` 查看状态和工具。

### 方式 B：在 `.mcp.json` 中引用环境变量

Claude Code 支持在 `.mcp.json` 的 URL 和 headers 中展开环境变量：

```json
{
  "mcpServers": {
    "multica": {
      "type": "http",
      "url": "${MULTICA_MCP_URL:-https://pre-fde-workbench.dingtalk.com/api/mcp}",
      "headers": {
        "Authorization": "Bearer ${MULTICA_PAT}"
      }
    }
  }
}
```

这份配置没有写入 token 本身，可以作为项目模板；启动 Claude Code 前仍必须设置当前的 `MULTICA_PAT`。项目级 `.mcp.json` 会触发 Claude Code 的 workspace trust 和 MCP server 审批流程。

### Task Token 的短期动态刷新

Claude Code 还支持 `headersHelper`。如果已有一个本机私有程序能够安全取得当前 task token，可以改为：

```json
{
  "mcpServers": {
    "multica": {
      "type": "http",
      "url": "https://pre-fde-workbench.dingtalk.com/api/mcp",
      "headersHelper": "/absolute/path/to/get-multica-mcp-headers"
    }
  }
}
```

该程序必须在标准输出返回 `{"Authorization":"Bearer mat_..."}`，且不能把 token 写入日志。`headersHelper` 只解决任务运行期间刷新 header 的问题；日常本机接入优先使用 PAT。

## 6. 可用工具和使用示例

当前 endpoint 暴露 6 个工具：

| 工具 | 作用 | 关键输入 |
| --- | --- | --- |
| `chat_send_message` | 向已有 Chat 发送消息并触发其智能体继续执行 | `session_id`、`content` |
| `get_digital_employee_binding` | 查询并对账数字员工绑定 | PAT：`agent_id`；Task Token：无 |
| `bind_digital_employee_to_multica_agent` | 把 DWS 创建的数字员工绑定到 Multica Agent | PAT：`agent_id`；两者：`tenant_id`、`digital_employee_id` |
| `unbind_digital_employee` | 解绑数字员工 | PAT：`agent_id`；Task Token：无 |
| `search_agents` | 按名称关键词查询可见 Agent 的非敏感详细信息 | `keyword` |
| `list_agents` | 获取调用方当前可见的全部活跃 Agent | 无 |

可以直接在 Codex、Qoder 或 Claude Code 中用自然语言指定工具和参数，例如：

```text
使用 Multica 的 search_agents 查找名称中包含“探针”的 Agent，并返回详细信息。
```

```text
使用 Multica 的 list_agents 列出我当前可以查看的所有活跃 Agent。
```

```text
使用 Multica 的 get_digital_employee_binding 查询 agent_id=<agent-uuid> 的数字员工绑定。
```

```text
使用 Multica 的 bind_digital_employee_to_multica_agent，把 DWS 返回的数字员工绑定到指定 Agent：
tenant_id=<organization-id>
digital_employee_id=<digital-employee-id>
agent_id=<agent-uuid>
其余参数使用默认值。
```

绑定工具的默认值为：

- `surface_type=auto`
- `message_scope=direct_only`
- `enabled_domains=["channel"]`

把 Chat A 中收到的回答转交给 Chat B：

```text
使用 Multica 的 chat_send_message，把下面的内容发送到 session_id=<target-chat-session-uuid>，让该 Chat 继续执行：

<要转发的完整回答>
```

PAT 调用 `chat_send_message` 时，目标必须是同一工作区内由 PAT 用户创建的活跃 Chat，且该用户有权触发目标 Agent；消息按用户本人发送，不写入 A→B Task 来源字段。Task Token 调用时，源执行任务还必须是活跃 Chat Task，目标必须是另一个 Chat，并记录 A→B 来源。该写操作不是幂等的，客户端超时后不要无条件重试，否则可能产生重复消息。

## 7. 验证顺序

1. 先在客户端确认 `multica` 状态为 connected。
2. 确认能发现上述 6 个工具。
3. 先调用只读的 `list_agents`，再用 `search_agents` 验证名称包含匹配和 Agent 详情；这两个工具不需要 Workspace header。
4. 调用只读的 `get_digital_employee_binding`；PAT 配置需传上一步返回的目标 `agent_id`。
5. 再根据需要验证绑定、解绑或 Chat 续写，并在 Multica 中确认对应数据和后续 task 已创建。

也可以绕过客户端做最小协议探测：

```bash
curl --fail-with-body --silent --show-error \
  -H "Authorization: Bearer $MULTICA_PAT" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"manual-check","version":"1.0"}}}' \
  "$MULTICA_MCP_URL"
```

成功响应中应包含 `serverInfo.name=multica`。这个命令只验证 endpoint 和鉴权，不执行任何业务写入。

## 8. 常见问题

| 现象 | 常见原因 | 处理方式 |
| --- | --- | --- |
| `401 Unauthorized` | token 缺失、格式错误、过期或已撤销 | 更新 `mul_` PAT；任务内调用则换成当前活跃 Task 的 `mat_` token |
| `403 Multica MCP requires a task token or personal access token` | 使用了登录 Cookie、JWT、OAuth token 或其他 bearer | 使用 Multica `mul_` PAT 或 Task claim 生成的 `mat_` token |
| Codex 显示连接失败 | 把真实 token 写进了 `bearer_token_env_var`，或 Codex 进程读不到所配置的环境变量 | 该字段只填 `MULTICA_PAT`，设置环境变量后完全重启 Codex |
| Workspace 相关 tool error | `session_id` / `agent_id` 不存在、跨环境，或 PAT 用户已不是目标 Workspace 成员 | 检查资源 ID、PAT 所属环境和成员关系；不需要增加 Workspace header |
| `403 untrusted MCP Origin` | 浏览器型客户端发送了未受信任的 `Origin` | 使用配置的 Multica origin，或检查服务端 Public/App/Frontend URL 配置 |
| `503 Multica MCP is not enabled` | release flag 未开启 | 开启 `FF_MULTICA_MCP_CHAT_SEND=true` |
| 客户端 connected 但调用失败 | PAT 缺少 `agent_id`，或工具的工作区、Agent、Chat、绑定约束不满足；Task Token 也可能已结束 | 根据 tool result 检查参数和权限，必要时更新 token |
| Qoder 看不到新工具 | 配置未重载或不在 Agent mode | 执行 `/mcp reload`，切换到 Agent mode |
| Claude 看不到 server | 环境变量未设置、项目配置未批准或连接失败 | 执行 `claude mcp list`、`claude mcp get multica` 或 `/mcp` |

## 9. 安全要求

- 不要把 `mul_` PAT 或 `mat_` Task Token 提交到 Git，也不要放进 URL、聊天正文、截图或日志。
- 优先使用本机 scope、私有环境变量或受控的 header helper。
- 不要把一个环境的 token 转发到另一个环境。
- 不要默认放行写工具；绑定、解绑和 Chat 发消息应保留客户端确认。
- PAT 具有用户身份权限，应设置合理有效期并及时撤销不再使用的客户端凭据。
- Task 结束后应删除本地保存的旧 Task Token，避免把鉴权失败误判为 MCP 协议问题。

## 10. 客户端官方文档

- [Qoder IDE MCP](https://docs.qoder.com/user-guide/chat/model-context-protocol)
- [Qoder CLI MCP servers](https://docs.qoder.com/cli/mcp-servers)
- [Qoder Agent SDK MCP integration](https://docs.qoder.com/cli/sdk/mcp)
- [Claude Code MCP](https://code.claude.com/docs/en/mcp)

## 变更历史

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-08-07 | 增加 `search_agents` 和 `list_agents` 的权限边界、用法及验收步骤。 | 让客户端无需预先取得 Workspace ID 或 Agent UUID，就能发现当前可见的 Agent 并继续调用其他 MCP 工具。 |
| 2026-08-07 | 增加 Codex 配置说明，并移除 PAT 客户端的 `X-Workspace-ID`；服务端改为按 `session_id` / `agent_id` 反查 Workspace。 | 通用 MCP Client 不应承担 Multica Workspace 上下文；资源 ID 已能唯一确定 Workspace，服务端可在同一处完成成员和权限校验。 |
| 2026-08-07 | 增加 `mul_` PAT 本地接入方式，并区分 PAT 用户权限与 `mat_` Task Token 固定任务权限。 | 让 Qoder、Claude Code 可以使用已有个人令牌长期显式接入，同时保留运行中 Agent 的最小权限和 A→B 来源语义。 |
| 2026-08-07 | 新增 Qoder、Claude Code 的显式安装、鉴权、验证和使用说明。 | 服务端 MCP 不再自动注入沙箱，需要给使用方一份与当前 task-scoped 鉴权和 4 个工具一致的客户端接入文档。 |
