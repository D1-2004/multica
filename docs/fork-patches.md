# Fork PATCH 维护清单

本文记录 `D1-2004/multica` 相对 `multica-ai/multica` 的长期 PATCH。目标不是逐条复述 commit，而是明确每组 PATCH 的边界、依赖、验证入口和 upstream 冲突热点，让后续升级可以按层重放。

## 基线与迁移原则

- Upstream 基线：`multica-ai/multica` 的 `upstream/main`。
- Fork 集成分支不应混入已存在于 upstream 的提交；升级统一使用 `git rebase --rebase-merges upstream/main`，完成后用 `git range-diff` 核对。
- 已发布 migration 的完整 stem 不改名。迁移器按完整 stem 记账，改名会导致存量数据库重复执行。
- SQL 是事实源；`server/pkg/db/generated` 冲突先合并 `server/pkg/db/queries`，再运行 `sqlc generate`。
- 功能 PATCH 与纯修复 PATCH 分开，文档紧跟对应功能，不再通过临时 merge commit 汇合。
- 新增 fork commit 使用 `Fork-Patch: PXX` trailer；一个 commit 只属于一个 PATCH 层，避免跨域提交在 rebase 时形成不可拆分冲突。

## 标准升级入口

在隔离 worktree 中运行：

```bash
bash scripts/rebase-upstream.sh
```

脚本会拒绝 dirty worktree，自动 fetch、建立带时间戳的 backup、执行 `rebase --rebase-merges`、检查冲突标记，并输出 rebase 前后的 `range-diff`。冲突解决与测试仍按本文检查表执行。

新增 PATCH 的提交格式：

```text
feat(fork/dingtalk): <单一意图>

<背景、改动、验证>

Fork-Patch: P20
```

如果一个需求同时改身份、Channel 和 Runtime，应拆成 P10/P20/P40 三个可独立验证的 commit；共享 schema 先落独立基础 commit，再由各层依赖，禁止把多个业务域压成一个 merge commit。

## PATCH 分层

### P00：部署入口与登录策略

**提交范围**

- `e980db969` FDE Agent 单步 onboarding。
- `56871977c`、`e0caef979` 根路径直接进入登录/工作区。
- `cb9cfbd5a` self-host CLI 可达性探测。

**主要目录**

- `apps/web/app/(auth)`、`apps/web/proxy.ts`
- `packages/core/onboarding`、`packages/views/onboarding`
- `server/cmd/multica`

**依赖与冲突热点**

- `apps/web/proxy.ts` 会持续与 upstream 的路由迁移、locale header 和 legacy redirect 重叠。
- 建议保持为独立、最先重放的小 PATCH；proxy 测试同时覆盖 upstream redirect 与 fork root redirect。

### P10：DingTalk/Lark 身份登录

**提交范围**

- DingTalk OAuth、登录锁与配置加载：`54395825b`、`88b9ff7b8`、`28877f59d`。
- DingTalk/Lark 私网 agent 身份解析：`e40647be2`、`1858049ad`、`8d1c692aa`、`94241fba4`、`5737ae7e2`。
- Lark 前端登录与统一 provider allowlist：`71a22df50`–`a7e61e80b`。

**主要目录**

- `server/internal/handler/auth*.go`
- `server/internal/integrations/{dingtalk,lark}`
- `packages/core/config`、`packages/views/auth`
- Web/Desktop login entrypoints

**依赖与冲突热点**

- `server/cmd/server/router.go`：provider client 装配和 auth route allowlist。
- `packages/core/config/index.ts`：必须同时保留 upstream feature flags 与 fork `authConfigLoaded/loginProviders`。
- 后续应把 provider 初始化抽成 `registerIdentityProviders(...)`，让 router 只接收结果，降低每次 upstream router 重构的冲突面。

### P20：DingTalk Channel

**提交范围**

- 组织选人/工作区邀请：`39f4857f0`、`95c301018`、`1eb10afa7`。
- Bot device flow 与 Stream Mode：`a5b1bd47b`、`a5d791626`。
- 免绑定、卡片、会话命令和稳定性：`00bd8782f`、`fe0422d22`、`03c264110`、`fde6f8f68`、`4e97639ca`、`db0467eac`、`3b678a7f8`、`8db882e70`、`a6f7cb681`。
- 文档：`7f10cedc8`、`0bfd0e0a4`。

**主要目录**

- `server/internal/integrations/dingtalk`
- `server/internal/integrations/channel/engine`
- `server/internal/handler/dingtalk*.go`
- `packages/core/dingtalk`、`packages/views/settings/components/dingtalk-tab.tsx`

**数据库**

- `132_issue_origin_dingtalk_chat` 已在 fork 发布，必须保留 stem。
- 与 upstream `132_agent_task_queue_runtime_connected_apps` 同号，但迁移器以完整 stem 区分；两者已加入 legacy duplicate allowlist。

**依赖与冲突热点**

- Channel SQL 与 upstream Lark/Slack 共用 `channel.sql`；每次升级必须生成而非手改 `channel.sql.go`。
- Agent Integrations UI 已适配 upstream 新 Tab，不再向新版 inspector 强插旧布局。
- 后续建议将 DingTalk 路由、service 和 client 装配集中到 `integrations/dingtalk/module.go`，router 只调用一个注册函数。

### P30：Lark 动态运行卡片

**提交范围**

- `/issue` 动态卡片：`29bc319f1`。
- claim/send 可靠性与取消鉴权：`f1a5e25b2`。
- Chat task 卡片：`1622465d0`。
- 文档：`b659c5e73`。

**主要目录**

- `server/internal/integrations/lark/{run_card,outbound,card_action}.go`
- `server/pkg/db/queries/channel.sql`

**依赖与冲突热点**

- 与 upstream channel cleanup 查询发生同区域冲突；必须同时保留 session cleanup、card claim、failed-claim delete。
- `1622465d0` 明确退休旧 error card；升级时不要因选择 upstream side 把 `Patcher.fail` 复活。

### P40：FC E2B Runtime + DWS

**提交范围**

- Runtime 基础、前端和 Railway 启动：`6aee2949a`–`a2d2ae1c7`。
- Sandbox 复用：`e92d10b32`。
- DWS profile/auth：`9213053c6`、`1b9847ccd`、`739c62817`、`685bef711`。
- Chat pending/recovery：`7756cd397`、`fdae85878`、`3619b7df2`。
- Runner 稳定性与展示：`d9ad894a8`、`f9a260227`、`b15cf9245`、`6c990ff04`、`6e5fb8481`、`6a206e43a`。

**主要目录**

- `server/internal/service/fc_e2b.go`
- `server/internal/handler/runtime_fc_e2b.go`、`dws_auth.go`
- `server/internal/daemon`
- `packages/core/runtimes`、`packages/views/runtimes`

**数据库**

- `133_fc_e2b_sandbox_session`、`134_dws_auth_profile` 已发布，stem 不改。
- 分别与 upstream 133/134 同号，已加入 legacy duplicate allowlist。

**依赖与冲突热点**

- `TaskService` 必须同时保留 upstream Composio overlay 与 fork `RuntimeLauncher`。
- Chat claim 必须同时保留 upstream task-owned input batch 和 FC E2B cold-start history。
- Agent create 必须同时保留 upstream permission/invocation targets、Composio allowlist 与 fork DWS profile ownership validation。
- Runtime UI 已适配 upstream machine-centric 页面；FC E2B 创建入口只对 workspace owner/admin 显示。

### P90：通用可靠性修复

**提交范围**

- `eb1743822` 修复 completion 丢失导致 issue 卡在 `in_progress`。
- `14da290a4` PAT `last_used` 热行保护。
- `5e637a46f` terminal task report 重投。
- `09e1f233c` skill bundle 上限调整。
- `1017bc616` DingTalk allow-unbound/bot name。
- `f21d3b05a` Web 构建修复。

这组应保持在功能 PATCH 之后，并尽量逐条向 upstream 提交；一旦 upstream 合入，下一次 `range-diff` 应显示其被基线吸收，而不是继续留在 fork 栈。

### P95：当前 upstream 兼容层

**当前提交**

- 使用 `git log --grep='Fork-Patch: P95' upstream/main..HEAD` 查询，避免 rebase 后在文档里维护失效 hash。
- 当前内容包括新主线契约适配、132-134 migration 重号兼容，以及保证 PATCH 栈检查可执行的空白清理。

**维护规则**

- P95 不承载产品 feature，只记录“这一轮 upstream 升级迫使 fork 做的适配”。
- 下一次升级先 rebase P00-P90，再根据新 upstream 重做 P95；不要机械保留已失效的兼容代码。
- 当 upstream 已吸收某项兼容处理时，直接删除对应 P95 diff。这样 `range-diff` 中 P95 就是本轮需要人工复核的最小集合。

## 推荐重整后的重放顺序

1. P00 部署入口。
2. P10 身份 provider 与登录策略。
3. P20 Channel/DingTalk 基础、再叠 P30 Lark 卡片。
4. P40 FC E2B 基础、sandbox、DWS、最后 chat recovery。
5. P90 通用修复。
6. P95 当前 upstream 兼容层；每轮升级重新审视，不视为永久产品 PATCH。
7. 文档随各层提交，不使用跨层 merge commit。

## 每次 upstream 升级检查表

1. 创建 `backup/<branch>-before-upstream-<date>` 和隔离 worktree。
2. 运行 `bash scripts/rebase-upstream.sh`；脚本自动 fetch、备份、rebase 和输出 `range-diff`。
3. 冲突时先确认 PATCH 意图；禁止整文件选择 ours/theirs。
4. SQL 冲突后运行 `sqlc generate`，只保留事实源导致的 generated diff。
5. 运行 migration lint，确认已发布 stem 未改名、未新增 148 以下编号。
6. `git range-diff upstream/main...backup upstream/main...HEAD`，逐项解释 `!` 和被 upstream 吸收的 `<`。
7. 至少验证 DingTalk/Lark、FC E2B、daemon/task service、Web typecheck/build。
8. 更新本文的 upstream 基线、PATCH hash 和已被 upstream 吸收的条目。
