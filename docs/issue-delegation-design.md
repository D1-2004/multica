# Chat → Issue Surface Handoff 设计

## 1. 结论

`multica issue delegate` 不是第三套任务协议，也不是普通 Issue CRUD 的包装。
它是一个 task-scoped 的 surface handoff：

1. Router 仍按现有 Agent Dispatch V2 把消息投递到 Chat；
2. Chat Agent 判断需要后台执行后调用 delegation 工具；
3. Multica 根据可信的源 Chat `task_id` 读取服务端私有投递参数；
4. 将 `surface.type` 从 `chat` 改为 `issue`，再交给现有 Issue Dispatch
   的创建或 follow-up materializer；
5. Issue/comment、目标 task 或评论排队归属、callback 所有权和 handoff
   update outbox 原子落库；
6. Issue task 接管最终完成责任，源 Chat task 只释放当前 session；
7. 多个源 Chat task 的评论可以合并进同一个 Issue task，Issue task 结束时
   按实际交付评论分别回调每个外部任务。

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
- 续写评论复用 Multica 现有 comment coalescing、排队和 completion
  reconciliation，不在 Router 重复实现 Issue 队列。
- 保持每条外部评论自己的 callback；一个物理 Issue task 可以消费多条评论并
  扇出多个 terminal callback。
- 使用独立的非终态 execution update 通知 Router 已转入后台，不复用或放宽
  terminal callback。

以下内容不在本次范围：

- Issue 结果重新注入父 Chat 成为一条 assistant 消息；
- 依赖标题实现服务端强一致语义路由。
- 单条评论级取消；多个 callback 共享物理 task 后，取消其中一个外部任务不能
  直接取消整个物理 task，需另行定义 comment obligation 的取消语义。

## 3. 标识与所有权

| 标识 | 所有者 | 用途 |
| --- | --- | --- |
| `source_task_id` | 源 Chat task | 工具权限、私有上下文来源、completion root |
| `chat_session_id` | Chat session | 语义路由候选集、Issue 管理 |
| `target_task_id` | Issue task | 后台执行、重试、日志和 terminal run |
| `comment_id` | Issue comment | 一条续写输入及其 callback 的稳定关联键 |
| `source_agent_id` | 源 Chat task | Router callback 的 `agentId` |
| `target_agent_id` | Issue assignee | 后台沙箱实际运行身份 |

`source_agent_id` 与 `target_agent_id` 可以不同。前者证明回调属于 Router
最初接受的 endpoint，后者决定谁执行 Issue。二者不能混用。

## 4. 数据模型

不新增 `issue_task_delegation` 或 comment-callback 映射表。首次创建 Issue
仍使用已有 task lineage：

```text
source Chat task
    └── parent_task_id ← target Issue task
```

续写已有 Issue 时，一条源 Chat task 产生一条 member 评论：

```text
comment.id             = comment_id
comment.author_type    = member
comment.source_task_id = source Chat task ID
```

`comment.source_task_id` 原有语义是“产生该评论的 task”。Agent 回复评论使用
`author_type = agent` 且 `source_task_id = Issue task ID`；委派输入评论使用
`author_type = member` 且 `source_task_id = source Chat task ID`。现有
originator 解析、回复查询和 UI retry 均已按 author/type 约束，两种记录不会
混淆。

同一个物理 Issue task 通过已有字段记录本轮真正收到的输入：

```text
target task.delivered_comment_ids = [comment A, comment B, ...]
```

因此完整关联为：

```text
comment.id
    → comment.source_task_id
    → source Chat task.context.completion_callback
```

`parent_task_id` 仍只表达物理 task 的单父 lineage：首次创建的目标 task，或
一次由首条续写评论创建的新目标 task，可以指向一个源 Chat task；后续评论
合并进该 task 时不改写 parent。terminal resolver 会把 lineage root 和
`delivered_comment_ids` 对应的其他 source task 合并、去重，不能试图在
单值 `parent_task_id` 中保存多个 callback 所有者。

目标 task 的私有 context 额外写入：

```json
{
  "dispatch_delegated_from_task_id": "<source_task_id>",
  "dispatch_surface": {
    "type": "issue"
  }
}
```

`parent_task_id` 让首次创建和首条续写仍能沿现有 completion lineage 回到源
task；
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
- `external_identity.dws` 稳定 DWS 身份描述；
- dispatch source、domain、event data 和 outbound；
- dispatch endpoint、幂等键和未知的兼容字段；
- terminal completion callback URL、execution update callback URL 与
  target identity；
- `parent_ref` 等 Router 关联信息。

首次创建，或一条续写评论需要创建新的物理 Issue task 时，转换只做两件事：

1. `dispatch_surface.type = issue`；
2. 增加 `dispatch_delegated_from_task_id`。

之后使用同一个 `BuildDispatchPrompt` 重建 Issue runtime/workflow prompt。
因此目标 task 获得 Issue 的双落点规则，而不会继续使用 Chat prompt。

如果评论合并进已有 queued task，不覆盖该 task 的私有 context。该物理
task 继续使用创建它时的第一份 ContextToken、稳定 DWS 描述和 runtime context；后续
评论的 callback 留在各自 source Chat task context 中，不拼接进目标 task。

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
3. 服务锁定源 task，在同一事务中创建
   `author_type = member, source_task_id = source task` 的评论并关联附件。
   相同 `source_task_id` 的工具重放先返回已有评论和 target task，不重复追加
   指令。
4. 将评论交给普通 UI 评论使用的同一套 enqueue/coalescing 决策：
   - 已有 queued task：把评论加入它的 `coalesced_comment_ids`；
   - 只有 running task：创建 queued successor；
   - task 已进入 dispatched、无法修改 claim payload：保留评论，由现有
     completion reconciliation 在本轮结束后创建 successor。
5. 若本条评论创建了新的物理 target task，它可以
   `parent_task_id = source_task_id`，并使用本条评论的 ContextToken；若合并
   到已有 task，则复用已有 target task 和第一份 ContextToken。
6. 写入本 source task 的 handoff update outbox；提交后才发布评论、task
   事件并返回 `release_parent: true`。

评论持久化就是 callback 控制权边界。提交前失败时，源 Chat task 仍自行
terminal callback；提交成功后，源 Chat task 的直接 terminal callback 必须
被抑制，即使它没有成为 target task 的 `parent_task_id`。completion resolver、
completion reconciliation 和取消 trigger 均通过以下事实识别已交接：

```sql
EXISTS (
  SELECT 1
  FROM comment
  WHERE author_type = 'member'
    AND source_task_id = source_chat_task.id
)
```

被 target task 的 `delivered_comment_ids` 实际消费前，该 source callback
保持待完成，不能因为前台 Chat task 已释放而提前上报。

## 8. 控制权交接与最终回调

Router 下发两个明确的 callback：

```json
{
  "url": "/api/v1/dispatch-tasks/<id>/execution-result",
  "updateUrl": "/api/v1/dispatch-tasks/<id>/execution-update",
  "telemetryUrl": "/api/v1/dispatch-tasks/<id>/llm-traces",
  "telemetryToken": "<task-scoped-capability>",
  "telemetryExpiresAt": 1786377600000
}
```

`url` 只接收最终 `completed` / `failed`；`updateUrl` 只接收非终态
`delegated_to_issue`。Multica 不根据字符串替换推导 update URL。三个
telemetry 字段是可选的一组；Chat → Issue 交接时作为私有 task context 原样
继承，使最终执行 task 使用同一个 Router task-scoped LLM trace 能力。

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
- terminal task 是已产生委派 member 评论的源 Chat task：不创建 outbox；
- terminal task 是 child：沿 `parent_task_id` 找到 root，创建一次 outbox。

目标 Issue task 完成时先解析两个 callback 来源：

```text
1. parent_task_id lineage 的 root（首次创建或首条续写）
2. terminal task.delivered_comment_ids 中每条 member 评论的 source_task_id
```

按 source task ID 去重后，为每个来源分别创建 completion outbox：

```text
root_task_id     = 对应的 source Chat task ID
terminal_task_id = 同一个实际结束的 Issue task / retry task ID
callback         = 各 source task 私有 context 中的原 callback
agent_id         = 各 source task 的 source Agent ID
external_run_id  = terminal Issue task ID
```

对于评论 callback，`result_message` 优先使用本 terminal task 对该评论所在
thread 发布的 Agent 回复；同一 thread 的多条输入共享该 thread 的合并回复；
没有回复时使用 task `result_message/output` 兜底。callback 继续使用
`externalTaskId = source task ID`、评论终态 request ID 和
`executionResult.terminalTaskId`，不扩展 Router 既有 terminal 协议。

只有 `comment_id = ANY(delivered_comment_ids)` 的 source task 可以在本轮
收到 callback。计划合并但没有进入 claim payload 的评论必须等待 successor，
不能使用 `trigger_comment_id/coalesced_comment_ids` 提前完成。completion
reconciliation 必须覆盖 completed 和最终 failed 的交付缺口，避免
前台已释放、评论却没有后继执行的孤儿状态。

callback body 使用各 source Agent ID，所以跨 Agent 委派仍通过 Router 现有
endpoint 快照校验；无需在 Router 放宽校验，也无需依赖 Issue label 传递安全
身份。

`task_completion_outbox.root_task_id` 的现有唯一约束继续保证一次 terminal
结果；多行可以共享同一个 `terminal_task_id`。若某个 source 的 handoff
update 仍为 `queued`，该 source 的 terminal completion
不可被 claim；因此即使目标 Issue 极快完成，Router 也会先看到交接，再看到
最终结果。

评论扇出的 terminal callback 只尝试一次。成功后标记 `delivered`；任何
HTTP、网络或协议失败直接标记 `dead_letter` 并退出投递队列，不做退避重试。
使用 `multica-comment-terminal:<source_task_id>` request ID 前缀区分这一投递
策略，保留 dead-letter 行仅用于审计。handoff execution update 仍保留现有
rolling-deploy 兼容重试，避免旧 Pod 的短暂 404 让 Router 永远看不到
“后台处理”状态。

## 9. 取消

首次创建仍可通过源 task ID 沿 lineage 取消。多个 source Chat task 的评论
共享一个物理 Issue task 后，`parent_task_id` 只代表物理 lineage，不能把
任一 comment source 的取消直接解释为“取消整个 target task”。

本次只保证 terminal callback 闭环：

1. 取消物理 target task 时，对已交付评论的 source callback 分别上报
   `executionStatus=canceled`，并保留 `failureReason=cancelled` 作为诊断信息；
2. 单独取消一个 comment source、取消尚未交付的评论，或让共享物理 task
   继续执行，留给后续
   comment-level cancellation 设计。

迁移 `9030_issue_delegated_task_completion` 只替换 migration 203 已有的取消
trigger：

- 任意 task 有 child 时，取消该 task 不提前生成 callback；
- 取消 active 叶子 task 时，使用 root task 和 root Agent 生成 callback。

这是对现有 completion lineage 的修正，不新增业务表。

迁移 `9032_delegated_comment_completion_fanout` 再扩展同一个 trigger：取消
物理叶子 task 时，按 `delivered_comment_ids → comment.source_task_id`
分别写入评论 callback；没有评论映射时保持 migration 255 的单 lineage
行为。

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

1. 执行 migration 255，更新取消 trigger；
2. 执行 migration 256，创建 execution update outbox；
3. 执行 migration 257，启用取消终态的评论 callback 扇出；
4. 先发布兼容版 Multica server 与随镜像分发的 CLI；普通 Dispatch 继续允许
   缺少 `updateUrl`，只有实际调用 delegation 时才保守拒绝释放 Chat；
5. Multica 全量后再发布 Router，使其支持 `/execution-update` 并下发
   `completionCallback.updateUrl`；
6. 不新增 Daemon claim 协议字段；LLM trace capability 只通过 cloud sandbox
   task exec 环境传入 runtime runner。

## 12. 变更历史

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-08-14 | task completion outbox 和取消触发器新增 `canceled` 终态，reconciliation 同步按该值回调；既有 `failed/cancelled` 历史行不回填 | 取消是独立业务终态，不应继续被 Router 计入失败数和失败率；保留 failureReason 便于诊断但不再决定统计分类 |
| 2026-07-29 | 初版提出独立 delegation 表、状态机和 completion resolver | 当时按“多个外部任务可合并到一个 Issue task 且分别回调”建模，导致实现超出 surface handoff 本身 |
| 2026-07-30 | 重构为 Chat Dispatch → Issue Dispatch 桥接；删除 delegation 表和独立状态机，复用 task lineage、Issue materializer、completion outbox；callback 使用 root Agent ID | 澄清后确认两种 surface 已共享完整身份、创建和回调链路，新工具只需从可信源 task 取参数并切换 surface；同时保留 Router 的 Agent 校验 |
| 2026-07-30 | 增加原子控制权边界、`completionCallback.updateUrl`、可靠 execution update outbox 及 update-before-terminal 顺序保证 | Issue 创建成功不能等价于外部任务完成；必须让 Chat 在失败时继续负责闭环，并在成功后把后台 Issue 快照可靠通知 Router，同时避免极快的 Issue 终态越过 handoff |
| 2026-07-30 | 续写改为复用普通评论队列；以 `comment.source_task_id` 关联每条外部评论和各自 source Chat callback，按 `delivered_comment_ids` 在一个物理 Issue task 结束时扇出多个 terminal callback；评论 callback 失败后直接丢弃 | UI 已证明多个评论会合并为一次 Agent turn、再按 thread 分别回复；单值 `parent_task_id` 无法表达多个 callback 所有者，评论映射可以在不新增业务表、不修改队列调度算法的前提下保留每个外部任务的闭环 |
| 2026-07-30 | 增加续写幂等返回、claim 竞争后的私有上下文继承、failed completion reconciliation，以及 migration 257 的取消回调扇出 | 工具重放、评论晚于 claim 和物理 task 失败/取消都是可能产生重复指令或孤儿 callback 的边界，需要在不改队列调度算法的前提下补齐闭环 |
| 2026-08-07 | 私有继承 `completionCallback` 的 telemetry URL/token/expiry，并由 task exec 环境交给 runtime | Chat → Issue handoff 不能打断同一 Router task 的推理链路；能力凭证必须避免进入用户内容、Daemon claim 和日志 |
| 2026-08-08 | telemetry URL 改为与 execution callback 共用 Router Base URL 的相对路径；runtime 收到的是 Multica 绝对 HTTPS relay URL，Multica 再向 Router 内网转发 | 云沙箱可访问 Multica 控制面但不保证能访问 Router 内网入口，交接后的 trace 也必须复用同一条可达通路 |
