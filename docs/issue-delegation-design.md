# Chat → Issue Surface Handoff 设计

## 1. 结论

`multica issue delegate` 不是第三套任务协议，也不是普通 Issue CRUD 的包装。
它是一个 task-scoped 的 surface handoff：

1. Router 仍按现有 Agent Dispatch V2 把消息投递到 Chat；
2. Chat Agent 判断需要后台执行后调用 delegation 工具；
3. Multica 根据可信的源 Chat `task_id` 读取服务端私有投递参数；
4. 将 `surface.type` 从 `chat` 改为 `issue`，再交给现有 Issue Dispatch
   的创建或 follow-up materializer；
5. Issue/comment、目标 task、task lineage 和 handoff update outbox 原子落库；
6. 新 Issue task 接管最终完成责任，源 Chat task 只释放当前 session，
   Router 中的外部任务仍保持执行中。

Agent 只决定业务路由：创建还是续写、标题、任务内容和目标 Agent。它看不到
也不能提交 `ContextToken`、completion callback 或其他私有投递参数。

## 2. 设计目标

- 所有外部消息默认先进入 Chat，不增加同步的轻重任务分类器。
- Agent 可以先理解任务或执行廉价步骤；确认会长期占用当前运行后再委派。
- 创建 Issue 时复用现有身份解析、Issue 创建、task 入队、Daemon claim、
  completion outbox 和 callback worker。
- 在一个数据库事务内完成业务载体、后台 task 与控制权交接记录；事务失败时
  Chat 仍持有控制权，不留下孤儿 Issue 或评论。
- 支持选择另一名专业 Agent 执行，同时保持 Router 所校验的源 Agent 身份。
- 新 Issue 带有源 Chat session 的可检索 metadata 和标签。
- 不新增 delegation 业务表，不维护第二套 preparing/active/completed 状态机。
- 使用独立的非终态 execution update 通知 Router 已转入后台，不复用或放宽
  terminal callback。

以下内容不在本次范围：

- Issue follow-up 在 active task 存在时的合并或排队策略；
- Issue 结果重新注入父 Chat 成为一条 assistant 消息；
- 依赖标题实现服务端强一致语义路由。

## 3. 标识与所有权

| 标识 | 所有者 | 用途 |
| --- | --- | --- |
| `source_task_id` | 源 Chat task | 工具权限、私有上下文来源、completion root |
| `chat_session_id` | Chat session | 语义路由候选集、Issue 管理 |
| `target_task_id` | Issue task | 后台执行、重试、日志和 terminal run |
| `source_agent_id` | 源 Chat task | Router callback 的 `agentId` |
| `target_agent_id` | Issue assignee | 后台沙箱实际运行身份 |

`source_agent_id` 与 `target_agent_id` 可以不同。前者证明回调属于 Router
最初接受的 endpoint，后者决定谁执行 Issue。二者不能混用。

## 4. 数据模型

不新增 `issue_task_delegation` 业务表。目标 task 使用已有字段：

```text
source Chat task
    └── parent_task_id ← target Issue task
```

目标 task 的私有 context 额外写入：

```json
{
  "dispatch_delegated_from_task_id": "<source_task_id>",
  "dispatch_surface": {
    "type": "issue"
  }
}
```

`parent_task_id` 让现有 completion lineage 能从目标 task 回到源 task；
`dispatch_delegated_from_task_id` 用于区分 delegation child 与普通 retry
child，并支持查询、取消和顺序重试。

新 Issue 使用现有 `metadata`、label 和 `issue_to_label`：

```json
{
  "multica.chat_session_id": "<chat_session_id>",
  "multica.delegated_from_task_id": "<source_task_id>",
  "multica.delegated_from_agent_id": "<source_agent_id>"
}
```

label name 受 32 字符限制，使用：

```text
chat:<sha256(chat_session_id) 前 20 个十六进制字符>
```

完整 session ID 保存在 metadata 和 label description。Agent 精确检索使用
metadata，label 主要用于 UI 过滤。

可靠投递使用技术 outbox `task_execution_update_outbox`。它不表示第二套任务
状态，只保存一次 `delegated_to_issue` 交接事件的不可变快照和投递状态：

```text
root_task_id        = source Chat task
target_task_id      = target Issue task
callback_url        = Router /execution-update
request_id          = multica-handoff:<source_task_id>
status              = queued | delivered | dead_letter
```

`root_task_id` 和 `request_id` 均唯一，同一个源 task 的顺序重试只会复用同一
交接；不同的 Issue/task 不能覆盖已经提交的交接。

## 5. 私有上下文转移

服务端从源 task context 读取并保留：

- `agent_identity_context_token` 及过期时间、来源；
- dispatch source、domain、event data 和 outbound；
- dispatch endpoint、幂等键和未知的兼容字段；
- terminal completion callback URL、execution update callback URL 与
  target identity；
- `parent_ref` 等 Router 关联信息。

转换只做两件事：

1. `dispatch_surface.type = issue`；
2. 增加 `dispatch_delegated_from_task_id`。

之后使用同一个 `BuildDispatchPrompt` 重建 Issue runtime/workflow prompt。
因此目标 task 获得 Issue 的双落点规则，而不会继续使用 Chat prompt。

转换发生在服务端内存和 task 私有 context 中。CLI 请求、CLI 输出、Issue
description、comment、metadata 和日志均不得包含 token 或 callback。

## 6. 创建流程

1. task-token 中间件写入可信的 `X-Task-ID`、`X-Agent-ID` 和 workspace。
2. handler 校验 `source_task_id == X-Task-ID`，并确认源 task 属于 Chat。
3. 如果源 task 已有带 delegation marker 的 child，返回已有 Issue/task，
   支持工具超时后的顺序重试。
4. 从源 task 重建 Issue surface 的 Dispatch Command 和私有 context。
5. 以源 task 的 human originator 校验目标 Agent 是否可调用。
6. 使用 Agent 提供的稳定标题和完整任务描述，调用与 Agent Dispatch V2
   共用的 Issue create 参数构造器和 `IssueService.Create`。
7. `IssueService` 锁定仍为 active 的源 task，并在同一事务中写入 Issue、
   metadata、label、`parent_task_id = source_task_id` 的目标 task，以及
   `delegated_to_issue` update outbox。
8. 事务提交后才发布 task 入队事件和返回 `release_parent: true`；Chat Agent
   简短确认后结束本轮。

事务提交是唯一的控制权边界：

- 提交前任一步失败：所有写入回滚，Chat 仍负责通过原 terminal callback
  闭环；
- 提交成功：Issue 已可独立运行，源 Chat 的 terminal callback 被 lineage
  抑制，只需释放 session。

创建模式允许 Agent 根据语义路由结果创建同标题 Issue，不用 workspace 级
标题去重覆盖 Agent 已完成的判断。

## 7. 续写流程

1. 校验目标 Issue 与源 task 同 workspace，且负责人是可运行 Agent。
2. 从源 task 重建相同的 Issue surface 私有 context。
3. 使用与 Agent Dispatch V2 相同的 `IssueCommentService.CreateExternalFollowUp`。
4. 服务在一个事务中锁定源 task，创建评论、关联附件、创建
   `parent_task_id = source_task_id` 的 target task，并写 handoff update
   outbox；提交后才发布评论和 task 事件。

当前 `CreateExternalFollowUp` 在 Issue 已有 active Agent task 时返回
`409 issue_dispatch_pending`。是否将不同外部任务合并到 Multica 自己的评论
队列，是独立的 Issue Dispatch 能力演进；本次 handoff 不再私建一条合并链路，
也不把排队逻辑搬到 Router。

## 8. 控制权交接与最终回调

Router 下发两个明确的 callback：

```json
{
  "url": "/api/v1/dispatch-tasks/<id>/execution-result",
  "updateUrl": "/api/v1/dispatch-tasks/<id>/execution-update"
}
```

`url` 只接收最终 `completed` / `failed`；`updateUrl` 只接收非终态
`delegated_to_issue`。Multica 不根据字符串替换推导 update URL。

原子事务提交后，completion worker 先投递 handoff update：

```json
{
  "requestId": "multica-handoff:<source_task_id>",
  "agentId": "<source_agent_id>",
  "externalTaskId": "<source_task_id>",
  "updateType": "delegated_to_issue",
  "occurredAt": 1785376800000,
  "extension": {
    "issueId": "<issue_id>",
    "issueIdentifier": "MUL-123",
    "targetTaskId": "<target_task_id>",
    "targetAgentId": "<target_agent_id>"
  }
}
```

Router 校验源 Agent 和外部 task 映射，把快照写入
`dispatch_task.metadata.executionHandoff`，但不修改 task 的 terminal 状态。
相同 request/payload 可重复确认，不同 handoff 返回冲突。

源 task 在工具成功后正常结束。completion resolver 的规则是：

- terminal task 本身就是 root，且 root 已有 child：不创建 outbox；
- terminal task 是 child：沿 `parent_task_id` 找到 root，创建一次 outbox。

目标 Issue task 完成时：

```text
root_task_id     = source Chat task ID
terminal_task_id = 实际结束的 Issue task / retry task ID
callback         = root task 私有 context 中的原 callback
agent_id         = root task 的 source Agent ID
external_run_id  = terminal Issue task ID
```

callback body 使用源 Agent ID，所以跨 Agent 委派仍通过 Router 现有 endpoint
快照校验；无需在 Router 放宽校验，也无需依赖 Issue label 传递安全身份。

`task_completion_outbox.root_task_id` 的现有唯一约束继续保证一次 terminal
结果。若 handoff update 仍为 `queued`，同一 root 的 terminal completion
不可被 claim；因此即使目标 Issue 极快完成，Router 也会先看到交接，再看到
最终结果。可重试错误按 outbox 退避重试；明确不可重试的协议错误进入
dead letter 后解除 terminal 阻塞，避免永久卡住外部任务。Router
`updateUrl` 已随请求下发但 callback 命中旧 Pod 时可能短暂返回 404；该状态
按 rolling 窗口中的可重试错误处理，不提前丢弃 handoff。

## 9. 取消

通过源 task ID 取消时：

1. 查询带 delegation marker 的 active child；
2. 结束仍在运行的源 Chat task；
3. 沿 lineage 取消目标 Issue task 及其 active retry；
4. 非叶子 task 不提前上报，最后被取消的 active 叶子 task 的 trigger 沿
   lineage 找到 root callback；
5. outbox 使用 root Agent ID，terminal task ID 仍为实际被取消的叶子 task。

迁移 `252_issue_delegated_task_completion` 只替换 migration 203 已有的取消
trigger：

- 任意 task 有 child 时，取消该 task 不提前生成 callback；
- 取消 active 叶子 task 时，使用 root task 和 root Agent 生成 callback。

这是对现有 completion lineage 的修正，不新增业务表。

## 10. Agent 工具协议

创建：

```bash
multica issue delegate \
  --title "采集 2026-07-30 科技新闻并生成消息卡片" \
  --description-file ./delegation.md \
  --assignee-id <agent-id> \
  --output json
```

续写：

```bash
multica issue delegate \
  --issue <issue-id> \
  --content-file ./follow-up.md \
  --output json
```

CLI 默认使用 Daemon 注入的 `MULTICA_TASK_ID`。显式 `--task-id` 必须与它一致。

API：

```http
POST /api/issue-delegations
Authorization: Bearer mat_...
Content-Type: application/json
```

创建请求：

```json
{
  "source_task_id": "源 Chat task UUID",
  "mode": "create",
  "title": "稳定的任务语义标题",
  "description": "交给目标 Agent 的完整任务说明",
  "assignee_id": "目标 Agent UUID",
  "status": "todo",
  "priority": "none"
}
```

成功响应：

```json
{
  "issue_id": "Issue UUID",
  "issue_identifier": "MUL-123",
  "target_task_id": "Issue task UUID",
  "trigger_comment_id": "续写时返回",
  "queued": true,
  "release_parent": true
}
```

普通 member token、PAT 或伪造的 `X-Task-ID` 不能调用。只有
`release_parent: true` 才表示 Issue/comment、后台 task 与 handoff update
已经原子提交，源 Chat 可以安全释放。

## 11. 可观测性与发布

结构化日志记录：

- `source_task_id`
- `source_chat_session_id`
- `issue_id`
- `target_task_id`
- `trigger_comment_id`
- create / continue outcome

日志不记录 token、callback 或完整 task context。

发布需要：

1. 执行 migration 252，更新取消 trigger；
2. 执行 migration 253，创建 execution update outbox；
3. 先发布兼容版 Multica server 与随镜像分发的 CLI；普通 Dispatch 继续允许
   缺少 `updateUrl`，只有实际调用 delegation 时才保守拒绝释放 Chat；
4. Multica 全量后再发布 Router，使其支持 `/execution-update` 并下发
   `completionCallback.updateUrl`；
5. 不新增 Daemon 协议字段，也不新增 runtime 环境变量。

## 12. 变更历史

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-07-29 | 初版提出独立 delegation 表、状态机和 completion resolver | 当时按“多个外部任务可合并到一个 Issue task 且分别回调”建模，导致实现超出 surface handoff 本身 |
| 2026-07-30 | 重构为 Chat Dispatch → Issue Dispatch 桥接；删除 delegation 表和独立状态机，复用 task lineage、Issue materializer、completion outbox；callback 使用 root Agent ID | 澄清后确认两种 surface 已共享完整身份、创建和回调链路，新工具只需从可信源 task 取参数并切换 surface；同时保留 Router 的 Agent 校验 |
| 2026-07-30 | 增加原子控制权边界、`completionCallback.updateUrl`、可靠 execution update outbox 及 update-before-terminal 顺序保证 | Issue 创建成功不能等价于外部任务完成；必须让 Chat 在失败时继续负责闭环，并在成功后把后台 Issue 快照可靠通知 Router，同时避免极快的 Issue 终态越过 handoff |
