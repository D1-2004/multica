# Chat Session CLI and Source Install Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 CLI 能按 Chat Session 管理与发送消息，将同一 Session 的等待消息合并进一个 queued collector，并让 fork/official CLI 都可从源码安装和更新。

**Architecture:** 直接 Chat 发送事务先锁 Session，再复用现有 queued direct-chat task 或创建新 task；消息继续使用 `task_id/chat_input_task_id` 形成 collector 输入批次。CLI 仅封装既有 Chat REST API。安装器和 `multica update` 共享 `fork/official` source registry，源码构建成功后才原子替换 CLI 并持久化来源。

**Tech Stack:** Go/Cobra、PostgreSQL/sqlc、Bash installer、GitHub source checkout、Go unit/integration tests。

---

### Task 1: Session collector 数据路径

**Files:**
- Modify: `server/pkg/db/queries/chat.sql`
- Modify: `server/pkg/db/generated/chat.sql.go`（由 sqlc 生成）
- Modify: `server/internal/service/task.go`
- Test: `server/internal/handler/chat_input_ownership_test.go`

- [ ] **Step 1: 写首次发送与 queued collector 复用的失败测试**

在 `chat_input_ownership_test.go` 增加：首次 `SendDirectChatMessage` 返回新 task 且 `Collected=false`；第二次发送返回同一个 task、`Collected=true`，数据库只有一个 queued task，两条 user message 均绑定该 task。

- [ ] **Step 2: 运行测试确认 RED**

Run:

```bash
set -a; source ../.env.worktree; set +a
go test ./internal/handler -run 'TestSendDirectChatMessageCollectsQueuedMessages' -count=1
```

Expected: FAIL，因为 `Collected` 不存在或第二次发送创建了新 task。

- [ ] **Step 3: 添加锁与 collector 查询**

在 `chat.sql` 增加：

```sql
-- name: LockChatSessionForDirectSend :one
SELECT id FROM chat_session WHERE id = $1 FOR UPDATE;

-- name: GetQueuedDirectChatCollector :one
SELECT * FROM agent_task_queue
WHERE chat_session_id = $1
  AND status = 'queued'
  AND chat_input_task_id IS NOT NULL
ORDER BY created_at ASC, id ASC
LIMIT 1;
```

运行 `make sqlc`，只保留事实源导致的 generated diff。

- [ ] **Step 4: 最小实现 collector 复用**

扩展 `DirectChatSendResult`：

```go
Collected bool
```

在事务开始锁 Session；查询 queued collector，存在则复用，不存在才执行 `CreateChatTask + SetChatTaskInputOwnerSelf`。新 message 的 `TaskID` 始终使用选定 task id。

- [ ] **Step 5: 运行测试确认 GREEN**

Run: Task 1 Step 2 同一命令。
Expected: PASS。

- [ ] **Step 6: 写 active task 后只创建一个下一 collector 的失败测试**

把首 task 更新为 `running`，连续发送两条消息；断言第一条创建第二个 queued task，第二条复用它，总 task 数为 2。

- [ ] **Step 7: 运行 RED/GREEN 并提交**

实现应由 Step 4 自然满足；先确认新测试在错误实现下能失败，再恢复实现并运行：

```bash
go test ./internal/handler -run 'TestSendDirectChatMessageCollectsQueuedMessages|TestSendDirectChatMessageCreatesCollectorBehindRunningTask' -count=1
```

Commit:

```text
feat(fork/chat): 合并 Session 等待消息到 queued collector

Fork-Patch: P50
```

### Task 2: 并发安全与 API 响应

**Files:**
- Modify: `server/internal/handler/chat.go`
- Test: `server/internal/handler/chat_input_ownership_test.go`
- Test: `server/internal/handler/chat_test.go`

- [ ] **Step 1: 写并发发送失败测试**

并发调用两次 direct send，使用 channel 同时起跑；断言 Session 中只有一个 queued task，两个结果中一个 `Collected=false`、另一个 `true`。

- [ ] **Step 2: 运行测试确认 RED 后使其 GREEN**

Run:

```bash
go test ./internal/handler -run 'TestSendDirectChatMessageConcurrentCollector' -count=10
```

Session row lock 应保证通过；若测试暴露事务隔离问题，只在查询/事务边界内修复，不增加新表。

- [ ] **Step 3: 写 handler 响应失败测试**

断言 `SendChatMessageResponse` JSON 包含：

```json
{"message_id":"...","task_id":"...","created_at":"...","attachment_ids":null,"collected":true}
```

- [ ] **Step 4: 追加 collected 字段并验证**

在响应 struct 增加 `Collected bool`，取自 service result。

Run:

```bash
go test ./internal/handler -run 'TestSendChatMessage.*Collected|TestSendDirectChatMessage' -count=1
```

- [ ] **Step 5: 提交**

```text
feat(fork/chat): 暴露消息 collector 状态

Fork-Patch: P50
```

### Task 3: CLI Chat Session manage 命令

**Files:**
- Modify: `server/cmd/multica/cmd_chat.go`
- Create: `server/cmd/multica/cmd_chat_test.go`

- [ ] **Step 1: 为 list/get/messages 写失败测试**

使用 `httptest.Server` 断言：

- `chat list` 请求 `GET /api/chat/sessions`；
- `chat get abcd` 先 list 解析 Session 前缀，再请求 canonical ID；
- `chat messages <id>` 请求 `GET /api/chat/sessions/<id>/messages`；
- JSON 输出保持服务端字段，table 输出含 ID/Agent/Status/Title 或 Role/Content。

- [ ] **Step 2: 运行测试确认 RED**

```bash
go test ./cmd/multica -run 'TestChat(List|Get|Messages)' -count=1
```

Expected: FAIL，因为命令尚未注册。

- [ ] **Step 3: 实现只读 manage 命令**

在 `cmd_chat.go` 注册 `list/get/messages`，复用 `newAPIClient`、`cli.PrintJSON`、`cli.PrintTable`。新增 `resolveChatSessionID`，完整 UUID直通，短前缀通过 Session list 唯一解析。

- [ ] **Step 4: 验证 GREEN**

运行 Step 2 命令并确认 PASS。

- [ ] **Step 5: 为 start/send 与 stdin 写失败测试**

覆盖：

- `start --agent Lambda "hello world"` 解析 agent，POST create 后 POST message；
- `send <session> "hello world"` 请求既有 Session；
- message 为 `-` 时读取多行 stdin；
- 空参数/空 stdin 本地失败；
- start 的 send 失败错误包含已创建 Session ID；
- 输出包含 `session_id/message_id/task_id/collected`。

- [ ] **Step 6: 运行测试确认 RED**

```bash
go test ./cmd/multica -run 'TestChat(Start|Send)' -count=1
```

- [ ] **Step 7: 实现 start/send 并验证 GREEN**

实现共享 `readChatMessage` 与 `sendChatSessionMessage`；不引用 Issue command 或 issue API。

Run:

```bash
go test ./cmd/multica -run 'TestChat' -count=1
```

- [ ] **Step 8: 提交**

```text
feat(fork/cli): 增加 Chat Session manage 命令

Fork-Patch: P50
```

### Task 4: fork/official source registry 与源码更新器

**Files:**
- Create: `server/internal/cli/source_update.go`
- Create: `server/internal/cli/source_update_test.go`
- Modify: `server/cmd/multica/cmd_update.go`
- Modify: `server/cmd/multica/cmd_update_test.go`

- [ ] **Step 1: 写 source registry 与持久化失败测试**

期望：`fork` → `https://github.com/D1-2004/multica.git`, default ref `develop`；`official` → `https://github.com/multica-ai/multica.git`, default ref `main`；未知 source 报错；`~/.multica/update-source` 原子保存/读取。

- [ ] **Step 2: 运行测试确认 RED**

```bash
go test ./internal/cli -run 'Test(SourceSpec|UpdateSource)' -count=1
```

- [ ] **Step 3: 实现 registry 与配置文件**

定义：

```go
type SourceSpec struct { Name, RepoURL, DefaultRef string }
func ResolveSource(name string) (SourceSpec, error)
func LoadUpdateSource() (string, error)
func SaveUpdateSource(name string) error
```

- [ ] **Step 4: 写源码 checkout/build 原子安装失败测试**

用临时 HOME、目标 binary 和 PATH 中的 fake `git/go` 覆盖：首次 clone、后续 fetch、ref 覆盖、build 失败不替换旧 binary、成功替换并保留 executable mode。

- [ ] **Step 5: 运行测试确认 RED**

```bash
go test ./internal/cli -run 'TestBuildAndInstallSource' -count=1
```

- [ ] **Step 6: 实现 BuildAndInstallSource**

源码目录为 `~/.multica/source/<source>`；checkout 到 `origin/<default>` 或用户 ref；从 `server` 执行 `go build -ldflags ... ./cmd/multica`，输出到目标目录临时文件，成功后调用现有 `replaceBinary`。

- [ ] **Step 7: 为 update flags 写失败测试并实现**

`multica update --source fork|official --ref <ref>` 使用 source updater；成功后才 SaveUpdateSource。无 flags 且存在来源文件时沿用；没有来源文件时保留现有 release/Homebrew 路径。

Run:

```bash
go test ./cmd/multica ./internal/cli -run 'Test.*Update|Test(SourceSpec|UpdateSource|BuildAndInstallSource)' -count=1
```

- [ ] **Step 8: 提交**

```text
feat(fork/cli): 支持 fork 与官方源码更新

Fork-Patch: P50
```

### Task 5: shell 源码安装器

**Files:**
- Modify: `scripts/install.sh`
- Modify: `scripts/install.test.sh`

- [ ] **Step 1: 重写 installer 测试为源码安装期望并确认 RED**

用 fake `git/go` 断言：默认 clone fork/develop；`--source official` clone official/main；`--ref` 覆盖；成功写 `~/.multica/update-source`；build 失败保留旧 binary 和旧来源；缺少 git/go 给出明确错误。

Run:

```bash
bash scripts/install.test.sh
```

Expected: FAIL，当前脚本仍走 Homebrew/Release。

- [ ] **Step 2: 实现 source 参数和构建安装**

删除默认 CLI 安装中的 brew/release 分支，加入 source registry、clone/fetch、detached checkout、`go build` 临时输出、原子移动和来源写入。`--with-server` 同样使用所选 repo/ref。

- [ ] **Step 3: 验证 GREEN 与 shell 语法**

```bash
bash -n scripts/install.sh
bash scripts/install.test.sh
```

- [ ] **Step 4: 提交**

```text
feat(fork/install): 默认从 fork 源码安装 CLI

Fork-Patch: P50
```

### Task 6: 文档、PATCH 分层与完整验证

**Files:**
- Modify: `docs/fork-patches.md`
- Modify: `apps/docs/content/docs/chat.mdx`
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-07-12-chat-session-cli-source-install.md`

- [ ] **Step 1: 文档化 P50 与命令**

记录 Chat manage/collector 边界、安装命令：

```bash
curl -fsSL https://raw.githubusercontent.com/D1-2004/multica/develop/scripts/install.sh | bash
curl -fsSL https://raw.githubusercontent.com/D1-2004/multica/develop/scripts/install.sh | bash -s -- --source official
```

- [ ] **Step 2: 运行事实源/生成代码检查**

```bash
make sqlc
git diff --check
git status --short
```

确认 sqlc 二次运行无新 diff、没有冲突标记、仅计划内文件变化。

- [ ] **Step 3: 完整 Go 与 shell 验证**

```bash
set -a; source .env.worktree; set +a
(cd server && go test ./...)
bash scripts/install.test.sh
```

- [ ] **Step 4: 前端契约验证**

```bash
pnpm typecheck
pnpm test
```

新增 JSON 字段必须不破坏 Web/Mobile schema 与测试。

- [ ] **Step 5: 构建真实 CLI 并做 help smoke**

```bash
make build
server/bin/multica chat --help
server/bin/multica chat start --help
server/bin/multica update --help
```

- [ ] **Step 6: 请求代码审查并修复 Critical/Important**

审查范围为设计提交之后到 HEAD，重点检查 collector 并发、CLI API 路径与原子二进制替换。

- [ ] **Step 7: 回填计划结果并提交**

```text
docs(fork): 记录 Chat Session CLI 与源码安装

Fork-Patch: P50
```

### Task 7: 推送并创建 Draft PR

**Files:**
- None

- [ ] **Step 1: 最终审计**

逐项核对 spec：Session manage、collector、fork 默认源码安装、official source、update source persistence、测试与文档均有直接证据。

- [ ] **Step 2: 推送分支**

```bash
git push -u origin codex/chat-session-cli
```

- [ ] **Step 3: 创建以 develop 为 base 的 Draft PR**

PR 标题：`feat(cli): 支持 Chat Session 管理与源码安装`

正文包含行为、collector 并发语义、安装来源、兼容性、测试证据与已知限制；不合并 PR。
