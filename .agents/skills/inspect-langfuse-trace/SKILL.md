---
name: inspect-langfuse-trace
description: >
  用业务 id 在 Langfuse 里找到并读懂一次交互：Coordinator 短循环回合、Scene Memory 刷新、Agent 任务（含沙箱模型调用）。
  用户给了 cid/openConversationId、钉钉 uid、Multica user id、workspace_id、agent_id、issue_id/task_id、coord_trace_id/job id、
  chat session id、evidence_id、scene_key，或说「Langfuse 上怎么查」「这次调用的 trace」「token 用量」时必须用。
  走 scripts/langfuse_lookup.py（Langfuse 公开 API），不要凭 UI 截图猜。
compatibility: Requires LANGFUSE_PUBLIC_KEY / LANGFUSE_SECRET_KEY / LANGFUSE_BASE_URL in the shell environment (never in the repo). Unset HTTP proxy. Python 3.9+, stdlib only.
metadata:
  version: "1.0.0"
  audience: "coding-agent"
---

# 用 id 查 Langfuse trace

预发和正式各自往同一个 Langfuse 项目投递，按 `environment`（`pre` / `production`）区分。
key 只从环境变量读；这个 Langfuse 只能直连，每条命令先：

```bash
unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy
export LANGFUSE_PUBLIC_KEY=… LANGFUSE_SECRET_KEY=… LANGFUSE_BASE_URL=https://unify-aipilot.dingtalk.com
LF=.agents/skills/inspect-langfuse-trace/scripts/langfuse_lookup.py
```

可选 `LANGFUSE_QUERY_ENVIRONMENT=pre` 让 trace 列表只看一个环境。加 `--json` 拿原始 JSON。

## 这台 Langfuse 的查询边界（实测，别按官方文档假设）

| 能力 | 结果 |
|---|---|
| `GET /api/public/traces/{id}` | 可用，id 是 32 位 hex |
| `sessionId=` / `name=` / `environment=` / `tags=`（多 tag 为 AND） | 可用 |
| `tags=` 里带冒号的值（`source:web`） | **匹配不到**，所以导出侧全部用 `key-value` 形式 |
| `userId=` | **查不到**：trace 列表不落 user id（详情页有）。用 `user-<id>` tag 或 idx 事件 |
| `filter=`（JSON 过滤 metadata 等） | 接受参数但**不生效** |
| `GET /api/public/observations?name=` | 精确匹配可用，名字里 `=` `+` 中文都行，`:` 和空格不行 |

因此导出侧给每条 trace 加了两层索引：

1. 静态 tag：`inbound_coordinator` / `scene_memory` / `agent_task`、`source-*`、`kind-*`、`runtime-*`、`provider-*`、`channel-*`、`agent-<agent_id>`、`workspace-<workspace_id>`、`user-<id>`，任务自有 trace 还有 `task-<task_id>`、`issue-<issue_id>`。
2. 索引事件：只给 tag、session、trace id 都覆盖不到的 id 建索引，每个 id 一个零时长 DEBUG event，名字 `idx.<key>.<value>`（`value` 中冒号和空白换成 `_`），统一挂在根 observation 下的一个 DEBUG `index` 节点里（节点 metadata 列出全部 id，树上只占一行），用 observations `name=` 精确命中后取 `traceId`。Coordinator 回合索引 `evidence_id`、`chat_session_id`（有会话时）和 `user-` tag 之外的用户 id；记忆刷新索引 `scene_memory_id`、`coord_trace_id`（job id 不同时再加 `job_id`）；任务索引 `runtime_id`、`session_id`、`parent_task_id`、`autopilot_run_id`、`trigger_comment_id`，加入 Coordinator trace 的任务再加 `task_id`、`issue_id`。`key` 子命令对 tag / session / trace id 覆盖的 key 会自动改走对应查法，所以下表任何 key 都能 `key <key> <值>`。

## id → 查法

| 手里的 id | 命令 | 说明 |
|---|---|---|
| `coord_trace_id`（SLS）/ Coordinator job id / 任务 chat trace id | `python3 $LF trace <id>` | 去掉横杠就是 trace id；数字员工回合的 job id 与 coord_trace_id 相同 |
| 非聊天任务的 `task_id` | `python3 $LF trace <task_id>` 或 `python3 $LF key task_id <task_id>` | 任务自有 trace 时 trace id = task id；Coordinator 派生任务与回合共用 trace，用 `key` |
| `issue_id` | `python3 $LF key issue_id <issue_id>` | 任务自有 trace 还可 `tag issue-<issue_id>` |
| `openConversationId` / cid / scene_key | `python3 $LF session '<cid>'` | 一个会话的 Coordinator 回合、记忆刷新、任务都在这个 session；`+`/`=` 原样传 |
| Web chat session id | `python3 $LF session <chat_session_id>` 或 `key chat_session_id <id>` | Web 回合 session 就是 chat session id |
| 钉钉用户 uid（`Dv6…`）/ dws_uid | `python3 $LF tag user-<uid>` 或 `key person_id <uid>` / `key dws_uid <uid>` | 详情里 `userId` 也是它，但列表过滤用 tag |
| Multica user id | `python3 $LF tag user-<uuid>` 或 `key user_id <uuid>` | Web 回合；任务用 `key initiator_user_id` / `key originator_user_id` |
| `agent_id` | `python3 $LF tag agent-<agent_id>` 或 `key agent_id <agent_id>` | `agent_name` 只在 metadata，UI 里可看不可过滤 |
| `workspace_id` | `python3 $LF tag workspace-<workspace_id>` | 配合 `--json` 再按 metadata 过滤 |
| `evidence_id`（openMsgId） | `python3 $LF key evidence_id '<openMsgId>'` | 仅 Coordinator 回合 |
| `scene_memory_id` / 触发 job | `key scene_memory_id <id>` / `key job_id <id>` | 记忆刷新 |
| 只知道时间和智能体 | `python3 $LF recent inbound_coordinator 20` | 列表里读 `agent`、`conversation`、`action` |

## 从一个 trace 跳到相关 trace

```bash
python3 $LF related <任意上述 id>
```

它把同一 id 的 trace、`idx.coord_trace_id` / `idx.job_id` / `idx.task_id` / `idx.issue_id` / `idx.chat_session_id` 命中的 trace、以及 `task-<id>` / `issue-<id>` tag 命中的任务自有 trace 按时间排在一起：

- Coordinator 回合 → 派生任务：同一个 trace（Web、渠道引擎、Router 派发三条路径都把回合 trace id 写成任务 chat trace），根 observation `inbound_coordinator` 与 `agent_task` 并排，沙箱 `llm.call.N` 在任务根下。
- Coordinator 回合 ↔ Scene Memory 刷新：刷新 trace 的 `coord_trace_id` / `idx.coord_trace_id` 指向触发它的回合（数字员工回合 = job id）。
- 任务 → Issue：`metadata.issue_id` / `idx.issue_id`；Issue 的后续评论任务各自有 `task_id`。

## 读一条 trace

`python3 $LF trace <id>` 依次打印：摘要（name/tags/session/agent/action/status）、metadata、根 input/output、每个 observation 一行（类型、名字、level、模型、usage、延迟、输入输出前缀），最后列出 `idx.*` 键。

- `inbound_coordinator`：根 input 是入站原话，output 是裁决（`action`/`user_text`/`issue_id`）；`coordinator.round.N` 是每轮模型调用；工具 observation 名字就是工具名；`dws_chat_history` 是钉钉历史预读。
- `scene_memory_flush`：`dws_history_range` → `memory_flush.round.N` → `memory_flush_commit`（accepted/reason）；根 output 有 `new_memory_revision` 和替换后的文本。
- `agent_task`：根 input 是 Issue 标题/描述与触发，output 是结果；`llm.call.N` 是沙箱真实模型请求响应（含 usage）；工具名来自 transcript；`thinking` / `assistant_text` 是合并后的段落。

## 注意

- 用 Langfuse UI 时：trace 详情右侧 "Metadata" 里的 key 就是上表的 id；列表页只有 tags / session / name / environment 过滤可靠。
- 旧 trace 不会按新规则回填；2026-09-04 之前的预发数据 tags 仍是冒号形式。
- 正式环境投递依赖 `LANGFUSE_*` 变量随发布生效；沙箱模型 body 依赖镜像 `llm_trace_v1` 能力。
