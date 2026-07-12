# Chat Session CLI 与源码安装设计

## 目标

为 Multica CLI 补齐以 Chat Session 为中心的管理能力，使调用者可以不创建 Issue，直接创建会话、查看会话与消息、向既有 Session 发送消息。连续消息在同一 Session 内合并到一个尚未执行的 collector task，避免每条消息各启动一次 Agent。

同时修复 fork 安装脚本始终指向官方发行版的问题：fork 默认从 `D1-2004/multica` 源码构建 CLI，仍可显式选择 `multica-ai/multica` 官方源码；安装来源会持久化，后续手工更新不会把 fork 覆盖成官方二进制。

## 范围

### Chat CLI

保留现有 Channel 只读命令：

```text
multica chat history
multica chat thread [id]
```

新增 Session 管理命令：

```text
multica chat list
multica chat get <session-id>
multica chat start --agent <agent-name-or-id> [--title <title>] <message|->
multica chat send <session-id> <message|->
multica chat messages <session-id>
```

- `start` 创建 Session 并发送首条消息，返回 `session_id`、`message_id`、`task_id` 和 `collected`。
- `send` 仅依赖 Session，不创建或解析 Issue。
- `message` 为 `-` 时从 stdin 读取，支持多行内容；其他情况将剩余位置参数用空格连接。
- Session ID 接受完整 UUID 或 `chat list` 输出的最短四位 UUID 前缀。
- Agent 接受完整 UUID或现有 `resolveAgent` 支持的名称匹配。
- `list/get/messages` 默认表格输出，`--output json` 提供自动化稳定格式；`start/send` 默认 JSON，避免脚本解析人类文案。

本次不增加交互式 REPL、等待 Agent 完成、附件上传、archive/delete/rename，也不改变现有 Channel history/thread 语义。

### Session collector

直接 Chat 发送仍通过 `POST /api/chat/sessions/{sessionId}/messages`。服务端在一个事务内：

1. `FOR UPDATE` 锁定 Chat Session，串行化同一 Session 的并发发送。
2. 查找该 Session 最早的 `queued`、且拥有 `chat_input_task_id` 的 direct-chat task。
3. 找到时复用它，把新 user message 的 `task_id` 绑定到 collector task，不再创建 task。
4. 未找到时按现有逻辑创建 task，并令 `chat_input_task_id = id`。
5. task 处于 `dispatched/running/waiting_local_directory` 时不可再收集；此时创建一个新的 queued collector。

Claim 路径已经按 `chat_session_id` 串行化并按 `priority DESC, created_at ASC` 取任务，所以一个 Session 最多同时执行一轮，queued collector 会在当前轮结束后继续。Claim 读取 `ListChatInputMessages(chat_input_task_id)`，因此 collector 启动前绑定的所有 user messages天然作为同一输入批次执行。

响应新增 `collected: boolean`：新建 task 为 `false`，复用 queued collector 为 `true`。字段为向后兼容追加，旧 Web/Mobile 客户端忽略即可。

外部 Channel 使用独立的 `EnqueueChatTask` 路径，不进入该 collector；Issue、Autopilot 和评论触发逻辑均不变化。

## 源码安装与更新来源

### 安装脚本

fork 中的 `scripts/install.sh` 新增：

```text
--source fork      # 默认：D1-2004/multica，分支 develop
--source official  # multica-ai/multica，分支 main
--ref <ref>        # 可选覆盖默认分支/tag/commit
```

安装流程使用 `git clone/fetch` 和本机 Go toolchain，在 `~/.multica/source/<source>` 维护源码 checkout，通过 `go build` 写入临时文件后原子安装到 `MULTICA_BIN_DIR`、`/usr/local/bin` 或 `~/.local/bin`。不读取 GitHub Release，不使用 Homebrew，不下载托管 CLI 二进制。

源码、分支和 repo 定义集中在小型 source registry 中，shell 与 Go 使用相同的两个稳定标识 `fork`、`official`。安装成功后将标识写到 `~/.multica/update-source`。

Windows PowerShell 安装器保持现状；本 PR 的源码安装优化仅覆盖用户当前使用的 macOS/Linux shell 安装路径。Windows 不伪装支持未验证的源码替换。

### 手工更新

`multica update` 增加：

```text
--source fork|official
--ref <ref>
```

- 有 `--source` 时切换并持久化来源。
- 无 `--source` 时优先读取 `~/.multica/update-source`。
- 没有来源文件的历史安装继续走原有官方 Release/Homebrew 更新逻辑，保证向后兼容。
- source 模式复用源码 checkout，fetch 指定 ref、构建并原子替换当前可执行文件。
- source build 使用 git describe/commit/date 作为版本信息，因此 daemon 的官方 release 自动更新轮询会按现有规则跳过；源码更新只由用户显式执行。

## 错误处理

- Session/Agent 不存在、无权限、archived 或 Agent 无 Runtime 时沿用服务端现有 4xx。
- `start` 创建 Session 后发送失败时，错误必须包含新 Session ID，便于用户用 `chat send` 重试。
- 空消息和空 stdin 在本地拒绝，不发请求。
- collector 查找与消息写入共享事务；并发发送不会产生多个 queued collector。
- 源码安装缺少 `git` 或 `go` 时直接给出可操作错误，不回退官方二进制。
- source/ref 无效、checkout/build 失败时保留旧 CLI，不覆盖现有可执行文件，也不更新来源文件。

## 测试

- DB/service：首次发送创建 task；第二条消息复用 queued collector；active task 后创建一个新 collector；多个后续消息复用该 collector；并发发送最多形成一个 queued collector。
- Handler：`collected` 响应正确且保持既有字段。
- CLI：list/get/start/send/messages 的路径、请求体、stdin、短 ID、表格/JSON及失败提示。
- Installer：fork 默认值、official 显式选择、ref 覆盖、缺少工具、成功写入来源；所有网络与 build 用 stub 隔离。
- Update：历史安装保持官方 release；来源文件选择 source updater；`--source` 切换并持久化；失败不写来源。
- 回归：相关 Go 包、shell installer tests、`go test ./...`、`pnpm typecheck`。

## 提交与发布

- 分支：`codex/chat-session-cli`
- 产品代码使用新的 `Fork-Patch: P50`（Chat Session 管理）trailer；安装来源属于同一 CLI manage 交付，不修改 upstream migration 编号。
- 完成后仅推送分支并创建以 `develop` 为 base 的 Draft PR，不直接合并或推送 `develop`。
