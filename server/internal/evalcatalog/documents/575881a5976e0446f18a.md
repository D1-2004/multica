# DeepSeek Harness (DSH) 在 FDE 工作台 / dt-fde-multica 上的支持方案评估

收件人：冬翔（dt-fde-multica owner）
日期：2026-09-05
输入：`scratchpad/dsh-brief.md` + 10 份研究报告 + 160 条对抗性核验（87 条 claim：77 confirmed / 5 disputed / 5 refuted）
锚点：fork `HEAD = 75c0a055f`(= origin/develop)、`upstream/main = 7a438bd5b`、merge-base `b904e6b71`(2026-08-12)、镜像仓库 `multica-fc-hermes-runtime master d86436d`、bundle 仓库 `multica-ai/dsh-multica-runtime e29aae2`

标注约定：**[V]** = 报告中有直接命令/源码证据；**[V!]** = 该结论经过对抗性核验并被确认；**[争议]** = 核验中被 refuted/disputed，本文采用修正后的说法；**[I]** = 推断/估算，未执行验证。所有人日均为 **[I]**。

---

## 1. 一句话结论与推荐路线

**一句话**：不要为了 DSH 做全量 upstream sync——上游的 DSH 能力是一个 30 文件的提交（`4f10a944b`，+985/-14），定向移植只有 **1 个文本冲突**；而全量 sync 要付 **235 个冲突文件 / 1008 个冲突 hunk**，其中只有 **7 个文件（3.0%）** 与 DSH 有关 **[V!]**（merge-strategy §2.1-2.3）。真正的长杆不是 Go 代码，而是 `--profile multica` bundle 的**许可与供应**，以及 DSH 本体（0.1.2-rc.1 已 latest、0.1.3-alpha.1 预告破坏性 session v2）的**版本节奏**。

**推荐路线（四条并行线，按优先级）**：

| 优先级 | 线 | 内容 | 人日 [I] | 为什么现在做 |
| --- | --- | --- | --- | --- |
| **P0** | **A. bundle 供应决策** | 决定 `multica` profile bundle 来源：①催上游发布（issue #6936）②自研 ~560 行 TS ③基于 MIT 的 `dsh-profile-multica@0.1.0` fork。落到内网新仓库 + anpm 私有 scope。 | 2–3（上游发布则 0） | 它是 P1 的硬前置：`agents_probe.go` 只有在 `dsh --profile multica --probe` 返回 protocol 1 时才注册 dsh **[V]**；也是唯一的法务风险项 **[V!]** |
| **P0** | **D. 云镜像升级到 0.1.2-rc.1** | `DSH_VERSION` + tarball SHA256 + lockfile 重生成 + stderr 错误提取修复 + telemetry 双保险 + web_fetch 网络姿态决策 | 1–3（含一次 candidate CI 与 canary） | **唯一能触达 Aone 托管产品**的路径（Aone 容器不跑 daemon **[V!]**）；0.1.2 已改 telemetry 与 web_fetch 默认值，越晚升越容易和 0.1.3 撞车 |
| **P1** | **B. 定向移植原生本地 backend（b 档）** | `4f10a944b` + DSH 相关进程树 ownership 语义 + fork 自有 `9xxx_runtime_profile_add_dsh` + 云/本地边界回归测试 | 5–8 | 用户目标 1（daemon 支持 DSH）；成本比全量 sync 低一个数量级，可 `git revert` 单提交回滚 |
| **P2** | **C. DSH 插件动态组装（Phase 1：镜像烘焙 catalog + 每 Agent 选择）** | `--patch` overlay 组装 + `agent.runtime_config.dsh` 选择 + Agent 详情页新 tab + Runtime 只读目录卡 | 8–14（Multica 侧 ~7.5、镜像侧 ~2.5） | 用户目标 2；零迁移、零新页面路由、web/desktop 同时生效 **[V!]** |
| **P3** | **E. 工作区级开源插件导入（Phase 3）** | 复制上游 plugin 生命周期切片的**形状**（publish→consent→install→config→upgrade），存储走 OSS + PG 元数据 | 6–9（或 9–14 若完整移植上游切片） | 用户目标 2 的后半段；等 Phase 1 跑通再做，且**用 fork 自有表名** `dsh_plugin_*`（§3.7） |

**明确不做**：①全量 `git merge upstream/main`（§3）；②自研同协议 Go backend（被定向移植支配）；③复用 DSH 自带 Web UI 作为插件管理界面（§6.1）；④为 DSH 移植上游整套 Multica plugin 平台（~40 文件 + ~30 迁移，对 DSH 目标复用率仅 ~30% **[V]**）。

**时间线 [I]**：P0 两条线并行 → 第 1 周末可在开发机跑通 `probe / list-models / stdio`（local-daemon 报告已在本机实测通过 **[V!]**）；P1 第 2–3 周合入；P2 第 4–6 周。首个可用的"本地 daemon 原生 DSH" 约 **1–1.5 周**（Go 与 bundle 并行），对照全量 sync 的 **3–5 周** **[V]**（merge-strategy §7）。

---

## 2. 现状对比表

### 2.1 三方能力对照

| 维度 | **fork（dt-fde-multica develop）** | **upstream（multica-ai/main）** | **DSH 本体** |
| --- | --- | --- | --- |
| DSH 运行形态 | 仅**云沙箱**（FC/E2B + ASB），xumo 2026-08-14 引入（`0de291676`、`4549f3ff7`） | 仅**本地 daemon 原生**（`4f10a944b`，2026-08-13，#6923） | 自带 `web / headless / sdk / sdk-minimal / acp` 五个 shipped profile **[V]** |
| 协议 | 镜像内 875 行 Python adapter 把 OpenCode `run --format json` 翻译成 `dsh --profile headless --patch <tmp>/multica.patch.json -- <task>`，再 tail `session.jsonl` 反投影成 OpenCode 事件 **[V]**（`scripts/multica-dsh:777-795, 511-687`） | `dsh --profile multica --stdio` 的 JSONL v1：`execute`/`cancel` 入，`ready/session/text/thinking/tool_call/tool_result/usage/result/protocol_error` 出 **[V]**（`protocol.ts:65-120`） | Cordis「一切皆插件」；profile = `$DSH_HOME/profiles/<name>/package.json` 的 `dsh.profile.bundles` **有序**列表 **[V!]** |
| provider 注册 | `FCE2BSupportedProviders` 含 `dsh`（`fc_e2b.go:390`）；`usesOpenCodeA2AInboundAdapter` 覆盖 `opencode,dsh,opencode-v2`（`fc_e2b.go:515-527`） | `agent.SupportedTypes` 25 条，含 dsh/mcode/dim/zeroclaw/codearts | — |
| 本地 backend | **无**：`server/pkg/agent/dsh.go` 不存在、`SupportedTypes` 20 条无 dsh（`agent.go:282-304`）、`agents_probe.go` 无 `MULTICA_DSH_PATH` 探测 **[V!]** | 有：`dsh.go` 474 行 + `dsh_test.go` + `agents_probe_dsh_test.go` + `dsh_integration_test.go` | — |
| session resume | 云路径由服务端强制清空 `PriorSessionID`（`handler/daemon.go:3037-3041`）。**[争议→已澄清]** 唯一生产调用传的是 `service.CloudSandboxRuntimeProvider(runtime)`（`daemon.go:2980`），非 cloud 返回空串，**本地 resume 并未被破坏** **[V!]**（refuted claim 57；confirmed 58、76） | 支持：`resume_session_id` → `agents.resume`，cwd 校验，`resume_rejected` 透传 **[V]** | session 落盘 `<root>/<cwd编码>/<session-id>/session.jsonl`，`SESSION_FORMAT_VERSION = 0` **[V!]** |
| thinking / 模型目录 | adapter 拒绝 `--variant`，无 thinking 控制 **[V]**（`multica-dsh:87-98`） | `--list-models` → `models` 帧；`reasoning_effort` 按 `llm.resolveModelInfo()` 校验 **[V]** | `--list-models` 输出 `provider/model` URL-escape id **[V!]** |
| MCP | 镜像 `multica-mcp-bridge.mjs` + `multica-managed-mcp` 行 + `headless-runner.inject` 就绪屏障；secret 走 `{"__jsExpr": process.env[...]}` **[V!]** | `execute.mcp_servers[]` 走 stdin；stdio + streamable-http，SSE 拒绝 **[V]** | `@deepseek-ai/dsh-mcp-client` 的 `apply(ctx, config)`，0.1.1→0.1.2 契约未变 **[V!]** |
| usage / 计费 | adapter 不发 `step_finish.tokens` → **DSH 任务不记 usage** **[V]** | `usage` 帧按 `provider/model` 聚合（`dsh.go:403-415`） | `assistant/message.data.usage` 带 5 类 token **[V]** |
| trajectory | 有：PUT `/api/tasks/{id}/dsh-trajectory`，AES-256-GCM 落 OSS，只读查看器 **[V]**；校验要求 header `version==0`、`delegationDepth==0` | 无 | — |
| 迁移 | `runtime_profile_protocol_family_check` 白名单 20 项（最后一次改动是 `254_runtime_profile_add_reasonix`），**不含 dsh、不含 opencode-v2**，无 9xxx override **[V!]**；`9064_agent_task_dsh_trajectory` 用了 `REFERENCES ... ON DELETE CASCADE`，违反 CLAUDE.md 无外键规则 **[V!]** | `313_runtime_profile_add_dsh`；后续 342/370/403/441 整体替换同一约束 | — |
| 版本 | 镜像 pin `DSH_VERSION=0.1.1-rc.2`（`Dockerfile:81`），tarball SHA256 `47ec05f4…6057` | 文档写 `dsh plugin --profile multica add <bundle>`（bundle 未发布） | npm dist-tags：`alpha=0.1.2-alpha.5, latest=next=0.1.2-rc.1`（2026-09-03T06:21Z）；**`0.1.3-alpha.1` 只在 GitHub（published_at 2026-09-04T11:34:32Z），npm 404，无 release asset，子包也没有** **[V!]** |
| 插件生态机制 | `--patch` 每任务插行 = 已有的动态组装雏形 **[V]** | Multica plugin v2（manifest / 托管产物 / consent / hooks），**与 DSH 插件无关** **[V!]** | `dsh plugin --profile X add <npm｜/abs/path｜./x.tgz｜github:…#sha>` → 转发 pnpm → reconcile `bundles` **[V!]** |
| GUI | Agent tab / Runtime 详情 / 自定义 runtime profile 对话框等原语齐全，**无插件页** | `plugins-tab.tsx` 721 行 + 完整生命周期后端，flag `plugins_v1` 默认 false **[V!]** | Settings→Plugins 只有**只读** inventory + 4 个 host 插件的配置，**无任何安装/启停 API**（`loader/create`、`pluginInventory/add` 实测 404）**[V!]** |

### 2.2 三个反直觉但要记住的事实

1. **fork 的云 DSH 其实是以 OpenCode provider 在跑**：沙箱 runner 设 `MULTICA_OPENCODE_PATH=/usr/local/libexec/multica-dsh`，并把 daemon 的 `--provider dsh` 改写成 `opencode` **[V]**（`scripts/multica-fc-hermes-runner:182-188, 1090-1100`）。所以云侧 provider 语义（capability / adapter / A2A inbound）与未来的本地原生 backend 语义是**两套协议共用一个 `dsh` 标签**——移植时最需要钉死的边界（§3.6 Step 2）。
2. **DSH 子包的 `dist-tags.latest` 竟是 `0.0.1-rc.1`**，只有 `next` 才是 `0.1.2-rc.1` **[V!]**（claim 16 code 核验）。任何镜像/lockfile 工作必须**显式 pin 版本**，绝不能按 `latest` 解析子包。
3. **`agent.runtime_config` 已原样透传到沙箱内 daemon 的 claim payload** **[V!]**（claim 32），所以"插件选择"不需要新的 server→sandbox 通道，也不需要改 FC launcher 的 env allowlist。

---

## 3. 合并 / 支持方案对比矩阵

### 3.1 四个方案的定义

| # | 方案 | 具体内容 |
| --- | --- | --- |
| 1 | **定向移植** | port `4f10a944b`（30 files, +985/-14）+ `e3ec3f8b5` 的 DSH 文档段（23 files, +81/-18）+ `46b5d9e6d` 的 DSH 相关进程树语义（33 files, +790/-91，只取 DSH 部分）；upstream `313` 换成 fork 自有 `9xxx` |
| 2 | **全量 sync** | `git merge upstream/main`：470 commits、2938 files、+295963/-40069 |
| 3 | **自研同协议** | 不抄上游代码，按 JSONL v1 协议自写 `dsh.go` |
| 4 | **只走云路径** | 不做本地 daemon；只升级镜像 + 用 `--patch` 做插件组装 |

### 3.2 冲突证据（全部 [V!] 可复现）

```
$ git merge-tree --write-tree HEAD upstream/main      # exit 1, tree df3e5a5a2
235 个冲突路径 = 210 内容冲突 + 2 添加/添加 + 23 修改/删除
$ git grep -c "^<<<<<<<" df3e5a5a2 → 212 个文件 / 1008 个 hunk
DSH 相关 = 7 / 235 = 2.98%
```

> ⚠️ 复现提醒：本机 git 消息是中文（`冲突（内容）`），`grep CONFLICT` 会**返回 0** 并静默漏报全部冲突 **[V]**（merge-strategy §2.1）。

1008 个 hunk 的构成 **[V]**：247 个是 sqlc 生成文件（应 `make sqlc` 重生成而非手工合并）、123 个在测试文件、79 个在 locale JSON，剩 **~559 个需手工合并的生产 hunk**。热点：`server/pkg/db/queries/*.sql` 16 个文件（`agent.sql.go` 单文件 154 hunk）、`packages/views` 54 文件 202 hunk、`apps/docs` 23 文件 134 hunk、`server/internal/integrations` 22 文件 96 hunk（几乎全是钉钉——fork 重写 77 files/+15248 对上游重写 24 files/+2708）。

定向 cherry-pick 的冲突面 **[V!]**：

| 模拟对象 | 冲突文件数 |
| --- | --- |
| `4f10a944b`（DSH 本体） | **1** —— `server/internal/daemon/daemon.go`，1 个 hunk，就是 `runtimeDisplayNameOverrides`（`:5234-5250`）的 gofmt 对齐 + fork 多一个 `opencode-v2` 条目 |
| `e3ec3f8b5`（文档/logo） | 6 |
| `46b5d9e6d`（进程树） | 11（其中 6 个是 fork 没有的 backend 的 modify/delete） |
| `3b0842368`（fixed_args 前缀） | 11 |
| `603519d15`（删测试） | 85 —— **不要移植这个提交** |

**[争议]** gui-upstream-plugins-tab 报告称"`daemon.go` 是最热冲突文件，因此必须先移植 DSH backend"——**被 refuted**：最热的是 `packages/core/api/client.ts` 与 `schemas.ts`（7 个相关提交中 6 个冲突），`daemon.go` 只在 2 个提交里冲突且两处互不相干 **[V!]**（claim 45）。**排序理由改为功能性**：claim payload 里的 `DshBundles` 字段在 `server/pkg/agent/dsh.go` 存在之前没有消费者。

### 3.3 迁移编号与规则

- fork 现状：upstream 段最高 `272`，fork 自有段 `9000–9127`（126 个 stem，最新 `9127_scene_memory_pending_from`），共 440 stem **[V!]**。
- upstream 新增：**166 个 stem**（不是 167——`362_agent_task_durable_work_dir` 被上游改名成 376，add-history 重复计数）**[V!]**，范围 273–450。
- 名称层面：stem、数字前缀、索引名、表名、函数/触发器名、ADD COLUMN **全部无冲突** **[V!]**。
- 本次要新增：`server/migrations/9128_runtime_profile_add_dsh.{up,down}.sql`，单个 `DO $migration$ … DROP CONSTRAINT IF EXISTS … ADD CONSTRAINT … NOT VALID … END $migration$;`，把 20 项白名单扩成 21 项（加 `dsh`，**不加 opencode-v2**）。无外键、无索引、无 extension，因此不触发 `CREATE INDEX CONCURRENTLY` 例外；`NOT VALID` 沿用 fork 254 的历史容忍模式，本迁移不做 VALIDATE **[V]**（port-plan §5.2/5.3）。down 只收窄白名单、保留历史 dsh 行（但那些行之后不可再被更新）。
- 编号占用：9128 在快照时空闲，但 gui-fork-ui 的"runtime 默认插件集"表、plugin-assembly 的 Phase 3 表也都想要 9128/9129 —— **实施时按当时空闲号顺序分配，绝不覆盖他人迁移**。runner 按**完整文件名 stem** 记账，改名 = 重跑，所以任何可能被重放的迁移必须幂等 **[V]**。
- 9128 是全新迁移而非重命名，因此**不需要** `cmd/migrate` 的 alias，也不需要 `make sqlc`。

**[争议] 迁移能否单独成一个提交？** port-plan §5 说可以作为独立第二提交；核验指出 `server/pkg/agent/agent_supported_types_test.go:36-53` 把 `SupportedTypes` 与迁移白名单**按数量与成员逐一钉死**，纯代码提交会 `t.Fatalf("SupportedTypes has 21 entries, migration whitelist has 20")` **[V!]**（claim 59 external）。→ **本文采纳：`SupportedTypes` 增项与 9128 必须在同一个提交**。（另一半仍成立：探测注册的 builtin dsh 写 `agent_runtime` 表、不读 `runtime_profile`，所以没有该迁移也能跑本地任务 **[V!]**。）

### 3.4 全量 sync 才会遇到的语义级坑（名称检查看不见）

1. **`issue_origin_type_check` 值集合冲突（阻断级）[争议→采纳修正]**：upstream `366_issue_origin_telegram_chat` 重建该约束时**丢掉了 fork 专有值 `agent_mcp`**（由 `9050_issue_origin_type_fork_compat` 断言、`9051` validate、`handler/agent_mcp_issue.go:28` 正在写）。迁移按文件名字典序执行，已迁移的线上库里 9050/9051 已记录不会重跑 → 366 最后落地 → `367` 的 `VALIDATE CONSTRAINT` 在任何存在 `origin_type='agent_mcp'` 行的库上**硬失败，直接中断 `src/main.sh` 的 `migrate up`**；若恰好没有这类行则静默通过，随后 Hosted Agent MCP 建 issue 在运行期报错 **[V!]**（claim 65 双 lens 一致）。现有防线 `repairIssueOriginTypeConstraintHook`（`cmd/migrate/main.go:67-73`）只挂在 `260_..._validate` 上，**没有 366/367 条目**。
2. `382_remove_dingtalk_group_routing_bindings` 会 **DELETE** fork 的 `channel_chat_session_binding` / `channel_outbound_card_message` 行 **[V]**；`379` 批量 UPDATE `agent_task_queue` 中 queued/dispatched/running 的行 **[V!]**。
3. `603519d15` **删掉** CLAUDE.md 依赖的迁移编号 lint（fork 184 行 → upstream 67 行）**[V]**，而合并时"take upstream"是最诱人的解法。
4. 8 个 fork 已删除的钉钉文件会被 merge-tree **复活**（保留上游版本）**[V]**。
5. `protocol_family` 白名单方向相反：upstream `313.down` 与 fork 254 逐字相同，`441.up` 是 fork 的**严格超集** **[V!]**，所以这一处全量 sync 不会丢东西，本次的 9128 未来也能干净合并。

### 3.5 部署 fence 影响

| 方案 | fence | 说明 |
| --- | --- | --- |
| 1 定向移植 | **不需要** | 9128 是 `runtime_profile` 小表上的单条原子 DDL；老 binary 因 `SupportedTypes` 无 dsh 而不会写 `dsh`，前后向单调；fence 触发器安装器（`cmd/migrate/main.go:499-507`）对"不建表/不删表"的迁移是 no-op **[V]** |
| 2 全量 sync | **需要，且是用户可见停机** | CLAUDE.md 强制 `normal → draining → frozen → normal`。`draining` 期间只有 `/api/daemon/*` 与 `/health`、`/readyz`、`/healthz`、`/health/realtime`、`/api/internal/logs/tail`、`/api/internal/deployment-fence` 还服务（`fence.go:167-199`），其余 503；`frozen` 连 `/api/daemon/*` 也 503，且要求**每副本 ack + 7 个工作计数器归零 + 所有业务表带 fence 触发器**（`fence.go:343-402`）**[V]**。**[争议]** 核验指出 fence 是**流程强制而非代码强制**（`src/main.sh:472-483` 在任何 fence 状态都会跑 `migrate up`），真正必须 fence 的理由是 `382`/`379` 会改动线上业务数据 **[V!]** |
| 3 自研 | 同 1 | — |
| 4 只走云 | 无 DB 变更 | 走镜像 candidate / stable 发布通道 |

### 3.6 决策矩阵

| 维度 | 1 定向移植 | 2 全量 sync | 3 自研 | 4 只走云 |
| --- | --- | --- | --- | --- |
| 触及代码 | 30 + ~10 文件，~1.1k 行 | 2938 文件；235 冲突文件 / 1008 hunk | ~500 行新 Go + 测试 | 0 个服务端文件 |
| 已知编译问题 | 1 处：初版 `dsh.go` 把 `*exec.Cmd` 传给 `signalProcessGroup`/`waitProcessGroupGone`，fork helper 收 `*os.Process`（`proc_other.go:35,44`；`proc_windows.go:49,56`）→ 改传 `cmd.Process` **[V!]**（上游是在 `e7e01638a` 改的签名，fork 从未合入） | 大量 | 无 | 无 |
| 合并风险 | **低**（1 个文本冲突） | **高**：钉钉文件复活；上次同步曾静默丢掉 fork 路由/枚举/fence 状态；`382` 删 fork 数据；lint 被删；`issue_origin_type_check` 会硬失败 | 中（无上游测试兜底，协议漂移全自担） | **最低** |
| 人日 [I] | **3–5**(a 档) / **5–8**(b 档，含进程树) / **7–11**(c 档，含自定义 fixed_args profile) | **10–20** 到可部署可验证（参照 2026-08-12 的 599 commit 同步：1 天合并 + 5 天 10 个修复提交 **[V!]**，而今天的坑更多） | 6–10 | **1–3**（镜像侧） |
| + bundle 前置 | +2–3（或 0） | 同左 | 同左 | **0**（云路径用 DSH 自带 headless profile） |
| 首个可用本地 DSH | **~1–1.5 周** | 3–5 周 | ~2 周 | 从不（定义外） |
| 长期漂移 | 低：`dsh.go` 上游一共只有 3 个提交（`46b5d9e6d`、`3b0842368`、`4f10a944b`）**[V!]** | 合并瞬间为零，随后重新长出；且白背 plugin 平台、Public API v1、4 个在 fork 跑不了的 provider | **最高**：无法与上游 diff | 漂移在 adapter 与 DSH 协议之间 |
| 回滚 | `git revert` 单提交；9128 的 down 只收窄白名单，历史行仍可读 | **没有回滚**：167 个迁移已应用（含 `382` 的 DELETE、`344` 的 DROP），只能向前修 | 同 1 | 镜像 stable rollout 回滚端点（`router.go:1902-1913`） |
| 目标 1 本地 daemon DSH | ✅ | ✅（要等整棵树稳定） | ✅ | ❌ |
| 目标 1 云 DSH | 今天已有 | 今天已有 | 今天已有 | 今天已有 |
| 目标 2 插件可选配 | 需另做 §5 设计 | ❌（上游是 Multica 插件系统，不是 DSH 插件系统 **[V!]**） | ❌ | **最接近**：`write_patch()` 已是动态组装机制 |
| 目标 3 同步上游能力 | 部分（仅 DSH） | ✅（这是它的目的） | ❌ | ❌ |

**结论**：**方案 1（b 档）+ 方案 4 并行**。方案 2 单独立项、择无发布窗口期做，预算 ≥3 周 + 一次 fence 窗口 + §3.4 的 5 个坑各自的前置修复。方案 3 被支配：fork 与上游同一份 Multica License，直接复制 `dsh.go` 合法、更便宜、测试更全 **[V]**；**真正要手写的是 TypeScript bundle**，那里许可才真的挡路。

取 **b 档**而非 a 档的理由：a 档明确无法承诺 Windows 进程树回收，而 cancel/timeout 的正确性恰恰是长驻 agent backend 最容易出事的地方 **[V]**。c 档（用 `fixed_args` 选不同 DSH profile）延后，因为它的形态取决于 §5 选哪种组装方案。

### 3.7 推荐方案的执行步骤与验证命令

**Step 0 — 钉输入（0.5 d）**
- pin `@deepseek-ai/dsh = 0.1.2-rc.1`；子包按显式版本或 `next`，**绝不用 `latest`** **[V!]**。
- 决定 bundle 出口并**立刻并行启动**（§4.4）。
- 确认当时空闲的 fork 迁移号：`ls server/migrations | grep -E '^9[0-9]{3}_' | sort -n | tail`。

**Step 1 — backend + probe（2–3 d）**：按 port-plan §3.1/3.2 移植 30 个路径；`dsh.go` 三处 `signalProcessGroup` / 一处 `waitProcessGroupGone` 改传 `cmd.Process`；`daemon.go` 冲突取 fork 语义全集（保留 `opencode-v2`，DSH 只留一次）；`scripts/agent-cli-command-names.txt` 加 `dsh`（**注**：`4f10a944b` 自己就改了这个文件，原样移植即满足 guard **[V!]**）。不要顺带引入 dim/mcode/zeroclaw/codearts。

```bash
cd server && gofmt -l ./pkg/agent ./internal/daemon && go vet ./pkg/agent/... ./internal/daemon/... && go build ./...
../scripts/go-test-with-agent-cli-guard.sh -- go test -count=1 -p 2 -parallel 2 ./pkg/agent -run 'Dsh|SupportedTypes|Thinking' -v
../scripts/go-test-with-agent-cli-guard.sh -- go test -count=1 ./internal/daemon -run 'Dsh|DSH|RuntimeProfile|ModelList|AgentCLIGuard|DefaultAgentCommand' -v
../scripts/go-test-with-agent-cli-guard.sh -- go test -count=1 ./internal/daemon/execenv -run 'RuntimeConfig|Sidecar|Skill' -v
```

**Step 2 — 云/本地共存边界回归（0.5–1 d）**：把下列事实钉成测试——本地 dsh 保留 `PriorSessionID`；云 dsh 仍被清空；云 run-once 仍把 dsh 映射到 OpenCode adapter（`run_once.go:55,86-96`）；把 `handler/dsh_trajectory.go:210-214` 收窄成 `service.CloudSandboxRuntimeProvider(runtime)=="dsh"`；轨迹按钮仍只看真实 artifact 标志（`dsh-trajectory-button.tsx:59`）。

```bash
cd server && go test ./internal/handler -run 'DSH|Dsh|SessionContract|Trajectory|AgentA2A' -count=1 -v
```
> ⚠️ **已知陷阱**：`go test ./internal/handler` 在 Postgres 不可达时 `TestMain` 直接 exit 0，会打印 `ok` 但**一个测试都没跑** **[V]**。必须在 `-v` 输出里确认 `--- PASS`。

**Step 3 — 迁移 + SupportedTypes（同一提交，0.5 d）**

```bash
cd server && go test ./internal/migrations -count=1 -v
cd server && go run ./cmd/migrate up
# 上预发前的只读核对：
#   SELECT conname, convalidated, pg_get_constraintdef(oid) FROM pg_constraint
#     WHERE conrelid='runtime_profile'::regclass AND conname='runtime_profile_protocol_family_check';
#   SELECT protocol_family, count(*) FROM runtime_profile GROUP BY 1;
# 并在预发 PolarDB 的回滚事务里验一次 DDL（本地 superuser Postgres 复现不了它的权限模型）
```
> ⚠️ **已知陷阱**：全新数据库会死在 `271_task_completion_canceled_status`（它 ALTER 的表由 `9025` 创建）；先手工跑 9025 再 `migrate up`。

**Step 4 — 前端/文档增量（0.5 d）**：只取 `e3ec3f8b5` 的 DSH 段落（四语 `environment-variables` / `install-agent-runtime` / `providers`、landing、logo、README/CLI_AND_DAEMON/SELF_HOSTING）。**必须改写上游"npm 装上就能用"的安装文案**——那个包 404 **[V!]**。landing 的"本地 N 个工具"数字按 fork 实际 protocol family 数重算或直接去掉。

```bash
pnpm typecheck && pnpm test && pnpm lint
```

**Step 5 — DSH 进程树 ownership（b 档，2–3 d）**：只为 DSH 实现 `startDshProcess / signalDshProcess / waitDshProcessGone / releaseDshProcess`（新增 `dsh_process_{other,windows}.go` + 两个测试文件），Windows 复用上游 Job Object 算法但用独立 owned map 以避免未来同步冲突；execute 写失败、正常结束、discovery 结束三处都 release；保留 `cancel → 3s TERM → 2s KILL` 的宽限期。**不要**把 `launch.go` / `startOwnedProcessTree` 铺到所有 backend（那是 `3b0842368`+`46b5d9e6d` 的 50+33 文件跨模块重构，另立项目）。

```bash
cd server && go test ./pkg/agent -run 'Dsh|ProcessGroup|Cancel' -count=1 -race -v
```

**Step 6 — 真实验收（1–2 d，依赖 Step 0 的 bundle）**

```bash
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY
dsh --profile multica --probe        # {"v":1,"type":"probe","runtime":"dsh",...,"protocol_version":1}
dsh --profile multica --list-models
make daemon
make cli MULTICA_ARGS="daemon probe-runtimes"   # 必须是 FORK 二进制报 "dsh":1，不是本机那个 upstream 0.4.33
```
产品内：本地 dsh runtime 跑任务 → 流式 `text/thinking/tool_call/tool_result` → 第二轮 resume（第一轮埋 nonce 记忆词，第二轮**不重新喂词**）→ 中途 cancel 且无孤儿进程 → 挂一个 stdio MCP → `{workDir}/.dsh/skills` 里的 skill 生效。**同一轮必须再跑一个云 DSH 任务**，证明 OpenCode adapter 与轨迹查看器没坏——这是让定向移植"便宜"的验收条件，也是全量 sync 白送不了的。
另外补三组兼容矩阵：新 server + 旧 daemon、新 server + 新 daemon、新 server + 现网云镜像 **[V]**（port-plan §8.3）。

**Step 7 — 合入前广验证 + 部署**

```bash
pnpm typecheck && pnpm test && make test && make check
```
部署：普通 Aone 滚动发布——推特性分支，复用该分支已有 CR id（一分支一 CR），然后 `a1 cd-pipeline run 66 --app 342160 --cr-id <id>`。**本次变更不需要 fence 切换**。

---

## 4. DSH 升级与镜像 / bundle 同步计划

### 4.1 `0.1.1-rc.2` → `0.1.2-rc.1` 逐项变更（全部实测）

| # | 项目 | 结论 | 要不要改 |
| --- | --- | --- | --- |
| (a) | **build-time patch 锚点** | `dsh-terminal-bash/lib/index.js` 的 `TERMINAL_BEFORE`、`dsh-bash-local/lib/index.js` 的 `BASH_LOCAL_ENV_BEFORE` / `BASH_LOCAL_SPAWN_BEFORE` 三个锚点在 0.1.2-rc.1 里**各命中恰好 1 次**，且被替换区域与 0.1.1-rc.2 **逐字节相同**；在副本上跑 `patch_terminal`/`patch_bash_local` 均 OK **[V!]** | `patch-terminal-bash-dws-identity.py` **不用改** |
| (b) | **cordis rows** | `agent-default-model {provider, model}` 未变；`llm-pi-ai` 的 `PiAiProviderProfile` 保留 `apiKeyEnv/displayName/api/baseURL/models/defaultInput/headers`，只新增可选 `modelOverrides/compat/transport/timeoutMs/retryPolicy`；`session-persistence-jsonl` 的 `root` 仍必填、`packChunks` 默认 true、`compression` 默认 zstd → **fork 那三个 key 依然有效且依然必要**；`headless-runner` 的 `inject` 覆盖行为不变 **[V!]** | adapter `write_patch` **不用改结构** |
| (b2) | **base 层默认值漂移** | ① `session-telemetry-otel.mode` 默认从 `DISABLED` 翻成 **`FEEDBACK_ONLY`**（实际落在 0.1.2-alpha.2）；② `tool-web.fetch` 从 `false` 翻成 **`true`** 并新增 `web-fetch-http` 行（落在 0.1.2-alpha.4）→ 模型默认拿到公网 `web_fetch`（有 SSRF 防护，但 adapter 是 `danger-full-access`，不会再问）；③ `hmr` 在 base 层就 disabled；④ 新增 `session-log-deepseek`（默认 off）与 `plugin-package-inventory-deepseek`（默认 on，但只影响 `deepseek-official` adapter 的请求，fork 走 `deap` 不触发）；⑤ `storage/storage-json/storage-domain/session-projection-cache` 进入 base（写在每任务临时 home 里，随之删除）；⑥ 新增 `acp/sdk/sdk-minimal` 三个 profile 模板 **[V!]** | telemetry 与 web_fetch **必须显式决策**（见 4.2） |
| (c) | **session 文件** | 文件名仍是 `session.jsonl`，布局仍是"每 project/session 一个目录"，`SESSION_FORMAT_VERSION` 两版都是 **0**，落盘 `HeaderLine` 字段集**完全相同** → fork 的 `dsh_trajectory.go:66` 与 `trajectory-model.ts:43` 的 `version==0` 校验**不用改** **[V!]**。补充：**事件行变了**——0.1.2 新增 `encodeProvenanceForStorage`，`sourceEventSeqs` 会被 run-length 编码成 `[start,end]`，使 `trajectory-model.ts:23` 的 `number[]` 类型描述不准确（无运行期影响，因为该字段从未被读）；`todo/write` 从 `SessionEventMap` 移除（投影器本来也没处理）**[V!]** | 服务端 **0 改动**；可顺手修类型注释 |
| (d) | **MCP client** | `dsh-mcp-client@0.1.2-rc.1` 的 `apply(ctx, config)` 签名与 `Config = StdioConfig \| StreamableHttpConfig` 未变，只新增更宽松的 `ConfigInput` 并去掉 bridge 未用的 `./invariant` 子路径导出；镜像自带的 `multica-mcp-bridge-smoke-test.mjs` **原样在两个版本上都通过**（streamable HTTP roundtrip ok, exit 0）**[V!]** | bridge 与冒烟测试 **不用改** |
| (e) | **环境变量** | `DSH_PERMISSION_MODE` 语义不变（`danger-full-access` → sandbox 关、approval never）；`DSH_TELEMETRY_MODE` 被 `session-telemetry-otel.mode` 读取（fork 用它，任何版本都生效）；`DSH_TELEMETRY_DISABLED` 由 **launcher** 处理，只要非空（包括 `0`/`false`）就在启动时注入 `{id: session-telemetry-otel, disabled: true}`（upstream daemon 用它）**[V!]** | **两个都设**（双保险） |
| (f) | **headless CLI 与 patch 文件格式** | `dsh --profile <name> [--patch <path>]... [--dump-config] [args...]`；`--patch` 可重复且是单值收集器；`--patch` 文件一律按 YAML（JSON_SCHEMA + `!!js` tag）解析，**JSON 被接受、扩展名无关**；`!!js` 构造 `{__jsExpr: "<expr>"}`，与 adapter 写的纯 JSON 等价 **[V!]**。0.1.2 新增 `anchorInsertedPluginNames`（把 `./`/`../` 的 insert name 相对 patch 文件重锚），fork 用的是绝对路径 `/opt/multica-dsh/...`，不受影响 **[V!]** | 不用改 |
| (g) | **node-pty** | 两版都是 `1.2.0-beta.15`，唯一依赖方 `dsh-subprocess-local`，install script 集合不变 **[V!]** | 源码重编译块保留 |
| (h) | **stderr reasoning 噪音（真回归）** | 0.1.2 的 headless **无条件**把推理流式写到 stderr（`streamReasoning`，0.1.1 没有）。adapter 失败路径取 stderr 末 4096 字符当错误消息（`multica-dsh:823-826, 844-846`）→ 错误信息变成大段模型思维链。**[V! 精确化]** 终止行 `dsh: <code>: <message>` 是在推理结束**之后**写的，所以尾切片的**结尾仍是真错误**，问题是被稀释（57KB 推理的模拟里真错误约占 1%）；**只有当 turn 以 `aborted`/`blocked`/`max-tokens`/`interrupted` 或早期崩溃结束时，根本没有 `dsh:` 行**，此时报出的错误是**纯推理文本**，且 adapter 的 `or "DSH headless failed"` 兜底不会触发（stderr 非空）**[V!]** | **必须改**：抽取最后一条 `dsh: <CODE>: ` 行（不能只匹配 `^dsh: `，推理文本可伪造该前缀），无匹配再回退尾切片；保留 `redact_dsh_error` |
| (i) | **SHA256 / 包体** | `dsh-0.1.2-rc.1.tgz` = `ca370668053ad6d0ac325e919ef5f65de53de00b7bad78008e6fb422dfce3530`（15248 bytes / 10 files），npmmirror、`npm pack`、registry.npmjs.org 三源一致，且与 npm 官方 `dist.shasum=fef213043313affc36ca2226d2637ad483b5e3f6` / `sha512-RPq48Tzx…` 对得上 **[V!]**。**坑**：`registry.npmmirror.com/.../dsh-<v>.tgz` 返回 **302** 到 `cdn.npmmirror.com`，不跟随重定向会拿到 99 字节的文本体；Dockerfile 的 `curl -fsSLo` 会跟随，所以现有写法安全 **[V!]** | `Dockerfile:81` → `0.1.2-rc.1`，`Dockerfile:83` → 新 SHA |
| (j) | **lockfile** | 重生成后：lockfileVersion 3、**583** 个包（原 512）、**230** 个 `@deepseek-ai/*`（原 204）、`hasInstallScript` 集合不变、`node-pty` 与 `@modelcontextprotocol/sdk 1.30.0` 不变、`@earendil-works/pi-ai` 0.82.1→0.84.4；`npm ci --omit=dev --ignore-scripts` 装 523 个包、`dsh --version` = `0.1.2-rc.1` **[V!]**。**[V! 修正]** 523 是 darwin-arm64 的数字；**Linux 构建机上应是 526**（多 8 个 linux-only 可选二进制、少 5 个 darwin 的）。验收文案要写"583 lock / **526 installed on the Linux builder**" | 重生成 + 修正验收数字 |

### 4.2 镜像仓库改动清单（`dingtalk-ai-lab/multica-fc-hermes-runtime`）

**必须（否则构建断或行为退化）**
1. `Dockerfile:81` → `ARG DSH_VERSION=0.1.2-rc.1`；`Dockerfile:83` → `ARG DSH_NPM_TARBALL_SHA256=ca3706…3530`。
2. `dsh-runtime/package.json` → `"@deepseek-ai/dsh": "0.1.2-rc.1"`，并用 `npm_config_registry=https://registry.npmmirror.com npm_config_replace_registry_host=always npm install --package-lock-only --ignore-scripts --no-audit --no-fund` 重生成 lockfile。
3. `scripts/multica-dsh` 的 stderr 错误提取（4.1(h)），并在 `tests/multica_dsh_test.py` 加一条用例：假 `dsh` 往 stderr 写 `dsh: reasoning:\n<text>\n` 再写 `dsh: X: real error\n`，断言错误里含 `real error`、不含推理。
4. 文档：`CLAUDE.md:58` 组件表、`README.md:189/:293` 的 `DSH_VERSION` 默认值，并在 `README.md` 变更历史与 `CLAUDE.md` 协议变更历史各加一条（版本、stderr 推理、telemetry 默认翻转、web_fetch 默认开）。**manifest version 保持 7、provider 与 capability 标签不变** → 指纹 `a2eb67817f146ef4` 不变 → 模板解析与服务端配置**不用动**。

**建议（策略/健壮性）**
5. `scripts/multica-dsh:763-769` 增加 `"DSH_TELEMETRY_DISABLED": "1"`（launcher 级硬关，与上游一致）。
6. **网络姿态显式决策**：要维持 0.1.1 行为就在 `write_patch` 加 `{"id": "tool-web", "config": {"fetch": false, "searchTimeoutMs": 60000}}`（注意 patch 是**整块替换 config**，两个 key 都要写全 **[V!]**）；否则就在文档里写明"DSH 任务可访问公网 URL（受沙箱出网策略约束）"。
7. `Dockerfile:551` 之后加构建期守卫：`DSH_HOME=$(mktemp -d) DSH_TELEMETRY_DISABLED=1 dsh --profile headless --help >/dev/null`，再用一个样例 JSON patch 跑 `--dump-config | grep -q 'provider: deap'`（离线可跑，已验证）；`scripts/runtime-smoke-test:269-272` 同步加 `--help` 检查。
8. 可选收益：让 adapter 从 `assistant/message.data.usage` 生成带 `tokens` 的 `step_finish`，daemon 就能记 DSH 的 usage（`opencode.go:527-556` 会累加）**[V]**。

**CI 现实**：`.aoneci/*` 的四条流水线跑的是同一份 6 文件白名单，**三个 DSH 测试都不在 CI 里**（`multica_dsh_test.py`、`dsh_install_contract_test.py`、`dsh_dws_identity_patch_test.py`）**[V!]**；同样漏掉的还有 `dws_install_contract_test.py`、`provider_http_proxy_test.py`——是白名单陈旧而非针对 DSH。**[V! 修正]** 但 DSH 并非完全无门禁：`build-image` 阶段执行 `Dockerfile:531-554`，会校验 tarball SHA256、断言 `dsh --version`、用真实 pty spawn 验证重编译的 node-pty、并跑 bridge 冒烟（真起一个 streamable-HTTP MCP server）。**建议把三个 DSH 测试加进 CI 白名单**，这是本次最便宜的质量提升。

**发布路径**：内网仓库开分支（不动 master）→ 复制 `.aoneci/runtime-a2a-providers-fc-candidate.yaml` 成分支专属 candidate 流水线 → `fc_runtime_dev.py doctor --profile pre-fde` → `cutover --runtime-ref <branch> --runtime-commit <40> --multica-ref <40> --provider dsh --visibility private --profile pre-fde` → canary（1 个真实 DSH 任务看 `dsh_trajectory_available` 与查看器、1 个 A2A inbound 任务走 `MULTICA_DEAP_DWS_TOKEN`、1 个带托管远程 MCP 的任务、1 个故意失败的任务验证新的错误提取）→ 合 master 触发 `runtime-master-release.yaml` → `POST /api/runtimes/fc-e2b/stable-releases` 及 rollout 端点；ASB 走 `runtime-asb-release.yaml` **[V]**。

### 4.3 服务端 / 前端要不要改？

- **为 0.1.2-rc.1 升级本身：不需要任何服务端改动** **[V!]**。`validateDSHTrajectory`（`version==0`、`delegationDepth` 必填）与 `parseDSHTrajectory` 仍匹配 0.1.2-rc.1 的落盘格式；capability、provider 集合、指纹、`com.multica.runtime.*` 标签都没变。
- **为 0.1.3+ 提前准备**：把 `dsh_trajectory.go:66` 与 `trajectory-model.ts:43` 改成**显式允许版本列表**（`format_version` 列已经存着到达的版本号），并给查看器做按版本的事件投影——**等真的拿到 v2 样本再做**。
- 顺手清一个 bug：`packages/core/agents/mcp-support.ts:8-26` 的 `MCP_SUPPORTED_PROVIDERS` 漏了 `"dsh"`，导致 DSH 云 Agent 在产品里**看不到 MCP tab**，而镜像、沙箱内 daemon、adapter 三层全都实现了托管 MCP，且镜像构建会因 MCP roundtrip 失败而 fail **[V!]**（claim 35）。这是"能力已发布但产品够不着"的一行修复（另建议对齐 `pi` 的 `capabilities.includes("mcp")` 校验）。

### 4.4 bundle：编译实测、许可、归属与分发

**编译与运行实测（全部 [V!]）**
- 只把 `package.json` 里 10 个 `@deepseek-ai/*` peer/dev 范围抬到 `^0.1.2-rc.1`（cordis `^4.0.2`、plugin-loader `^1.0.3`）并重生成 `pnpm-lock.yaml`，`src/`、`tests/`、`cordis.patch.yml`、`tsconfig*.json` **逐字节不变**，即可 `pnpm typecheck`（strict、`skipLibCheck:false`）exit 0、`pnpm test` 14/14 通过、`pnpm build` 出 `dist/`。范围抬升是**必须**的：semver 下 `^0.1.0-rc.6` 永远匹配不到 `0.1.2-rc.1`。
- 但 14 个测试几乎不碰 DSH API（只碰 `scrubbedParentEnv`），真正的兼容证据是 strict typecheck + 一次真实 `dsh --profile multica --stdio` 跑通（stdout 只有协议帧）。
- `dsh plugin --profile multica add /abs/x.tgz` 安装后 `--probe` / `--list-models` / `--stdio` 全部正常；upstream 自带的 `multica 0.4.33` CLI 在设了 `MULTICA_DSH_PATH` 后 `daemon probe-runtimes` 报 `"dsh":1`。
- **两个必须修的 drift**：
  1. `cordis.patch.yml` disable 的行 id 是 `telemetry-otel`，而该行**自 DSH a2d0f7f41（2026-08-12）起就叫 `session-telemetry-otel`** —— 也就是说这个 patch **对 bundle 声称验证过的 0.1.0-rc.6 起就一直是 no-op**，只是 0.1.2 把默认值翻成 `FEEDBACK_ONLY` 后才从"无害"变成"真会开 telemetry" **[V!]**。fork 副本要改成 `session-telemetry-otel`（想兼容更老版本可两个 id 都留，未知 id 只 warn）。
  2. bundle 只设了 `session-persistence-jsonl.root`，于是走库默认 `compression: zstd` + `packChunks: true`，落盘是 `session.jsonl.zstd` + 打包行 —— fork 的轨迹管线（glob `*/*/session.jsonl`、服务端要求明文 NDJSON 且逐行 `seq`/`time`）**吃不下** **[V!]**。fork 副本必须补 `compression: none, packChunks: false`（镜像的 Python adapter 早就自己这么写了）。
- 其他建议修：`patchReload` 在自定义 profile 默认是 `live`（会额外起 HMR 实例与两个文件监视器），烘焙的 profile 模板改 `startup`；`PLUGIN_VERSION` 是硬编码字符串，建议在 `probe`/`ready` 帧里加 `dsh_version`（Go 端 `json.Unmarshal` 忽略未知字段，向后兼容）；`TurnEndReason` 新增的 `blocked` / `interrupted` 目前都落到 `DSH_TURN_FAILED`，应给独立 error code；给 `apply()` 加 stdout 劫持（把非协议写入转到 stderr），防止第三方插件 `console.log` 污染协议流。

**许可（这是唯一的法务阻塞项，[V!] 双 lens 一致）**
- bundle 仓库无 LICENSE / COPYING / NOTICE，`"license": "UNLICENSED"`、`"private": true`，README 自称"Private, out-of-tree runtime bridge"；GitHub 显示 license = null。
- 上游 `multica-ai/multica` 的 LICENSE（Multica License）**不覆盖**它：`git grep -n "@multica-ai/dsh-runtime\|dsh-multica-runtime\|dsh-external" upstream/main` 返回空，上游代码里只有泛指的 `dsh plugin --profile multica add <bundle>`。
- 更关键：维护者自己那条未合并的 `agent/prepare-npm-release`（bundle 仓库 PR #1）**在准备 npm 发布的同时仍保留 `UNLICENSED`**，RELEASING.md 还写着"确认 UNLICENSED 是否仍是预期的许可策略" → **即使上游发布到 npm，也不自动解除限制** **[V!]**。
- ⇒ fork **不得**原样 vendor 进内网仓库或烘焙进对外分发的镜像。

**三条出口（按推荐顺序）**
1. **要授权 / 催发布**（upstream issue #6936 已在请求发布）。若以 MIT 或 Multica License 发布，就当依赖消费，fork 的定制以一层薄 overlay bundle 承载（§5 已验证 overlay 可覆盖任意行）。
2. **自研**：协议本身是 fork 已按 Multica License 拿到的（`dsh.go`），插件约 560 行 TS，形状在 bundle 报告 §2-3 有完整规格；预算 **2–3 天**含测试。**不要逐字抄上游文本**。
3. **基于 MIT 的 `dsh-profile-multica@0.1.0`**（作者 zhcai，2026-08-18 发布，MIT，代码与上游不同但 wire 协议逐字段一致）fork 之：保留其 LICENSE、加 fork 自有的行。**[争议]** 核验者认为这条路能把"bundle 是关键路径"这个判断推翻——`dsh plugin --profile multica add dsh-profile-multica` 一小时内就能让 probe 通过；但它 peer pin 在 `^0.1.0-rc.7`，仍需按 0.1.2-rc.1 重新验证，且供应链审查照做。**本文的立场**：把 bundle 视为**必须尽早启动、但不必阻塞 Go 移植**的并行工作项，而不是"整条路的唯一长杆"。

**归属与分发**
- **新建内网仓库**（如 `dingtalk-ai-lab/dsh-multica-runtime`）+ 自己的 Aone CI（typecheck / vitest / build / `pnpm pack` / 发布到 `registry.anpm.alibaba-inc.com` 私有 scope）**[V]**。
  - 不要放进 `multica-fc-hermes-runtime/dsh-runtime/`：那是镜像构建仓库，没有 Node/TS 工具链与测试运行器，而开发机与 Aone daemon 需要在不构建镜像的情况下拿到 bundle。那边只保留**版本 pin + SHA256**（和现在 `DSH_VERSION`/`DSH_NPM_TARBALL_SHA256` 一样的耦合度）。
  - 不要放进 `dt-fde-multica/packages/`：monorepo 的 `packages/*` 是 `views -> core + ui` 的前端共享包，一个 Node-only 的 Cordis 插件不适配那张依赖图与 turbo 管线，还会把 DSH peer 树拖进 monorepo lockfile。
- **镜像侧安装法（无需 pnpm）[V!]**：tarball 安装只把 bundle 放进 profile，`@deepseek-ai/*` peer **不会**装到 profile 里，而是通过每次启动时 heal 出来的 `$DSH_HOME/profiles/node_modules`（0.1.2 上 223 个符号链接）解析到 launcher 自己的那一份 —— 因此**镜像可以烘焙一份 profile 模板目录**（`package.json` + `pnpm-workspace.yaml` + `cordis.patch.yml` + `node_modules/@multica-ai/dsh-runtime`），任务期把模板拷进可写的每任务 `$DSH_HOME`，全程不需要 pnpm、不需要 registry。构建期用一次真实启动（`dsh --profile multica --help`）把 fallback heal 出来并断言 `profiles/node_modules/@deepseek-ai/dsh-tools` 存在。
- **开发机 / Aone daemon**：`dsh plugin --profile multica add @<scope>/dsh-multica-runtime@<ver>`（`dsh plugin` 转发 pnpm，因此 pnpm 必须在 PATH，registry 走 `~/.npmrc` 或 `npm_config_registry`）。**不要用 `link:` 目录安装**：被链接的 checkout 自带 rc.6 的 `node_modules`，Node 会优先从那里解析 bundle 的运行时 import，正是上游记录的 MUL-6186 双实例风险 **[V!]**。

---

## 5. DSH 插件动态组装与导入

### 5.1 上游 Multica 做了什么 / 没做什么

**做了**（`MUL-6350 1/4..4/4`、`MUL-6469`、`MUL-6485`、`MUL-6581/6582/6584`）：一套完整的 **Multica 插件系统**——`multica.plugin.json` manifest（scopes、含 secret 的 config schema、`contributes.surfaces/hooks/resources`）、`packages/plugin-sdk`、`server/internal/service/plugin*.go`、以 `transport: mcp` 把 hook 暴露成 agent 工具、`resources: skill`、托管不可变产物 + consent 屏 + 版本绑定。**flag `plugins_v1` 默认 false** **[V!]**。

**没做**：**任何与 DSH 插件相关的东西** **[V!]**。它的 manifest 是封闭世界（`ParseManifest` 用 `DisallowUnknownFields`，`validateContributions` 拒绝一切 `resources[].type != "skill"` 并强制 `entry == "skills/<key>/SKILL.md"`）；产物存 Postgres bytea，上传 zip ≤2 MiB、单文件 ≤1 MiB、解压总量 ≤4 MiB、≤512 条目，且**只接受 zip + manifest**，npm `.tgz` 在格式层就被拒（不是大小问题）**[V!]**。

**上游唯一对 DSH 组装有用的两件事**：
1. `runtime_profile.fixed_args` 被当作 **argv 前缀**（`agent.Config.LaunchPrefix`，`3b0842368`/#7046）。`dsh` 不在 `launchPrefixBlockedArgs` 里，所以 `fixed_args: ["--patch", "/etc/multica/dsh/site.json"]` 在上游**今天就能用** **[V!]**；但 `fixed_args: ["--profile","x"]` **无效**——`--profile` 是单值 option，后写的 `--profile multica` 覆盖前者；用 `--` 终止符虽能保住 profile x，却把 `--stdio` 降格成 app 参数，bundle 的 `parseMode` 要求恰好一个参数，握手起不来 **[V!]**。要选别的 profile 必须改 `dshLaunchArgs()` 或加 `MULTICA_DSH_PROFILE` 环境变量。
2. `claude_plugins.go` 的模式：daemon 读取**运行时自身的插件注册表**并把技能/MCP 汇入发现结果 —— 对 DSH 就是读 `$DSH_HOME/profiles/<name>/package.json` 的 `dsh.profile.bundles`。

⚠️ 注意：fork 当前把 `fixed_args` 拼进 `ExecOptions.ExtraArgs`（`daemon.go:6394-6397`），而初版 DSH backend **根本不读 ExtraArgs** **[V!]** —— 所以"`--patch` 前缀在 fork 也能用"需要先把 LaunchPrefix 那套移过来，或在 DSH 侧单独实现 argv planner（c 档）。

### 5.2 DSH 侧机制（这是设计的地基，全部实测）

| 机制 | 事实 |
| --- | --- |
| profile 目录 | `$DSH_HOME/profiles/<name>/{package.json, cordis.patch.yml, cordis.yml, pnpm-workspace.yaml}`；自定义名初始化为 `bundles: ["@deepseek-ai/dsh-base"]` + `patchReload: live`；`cordis.yml` 每次启动被重写 **[V!]** |
| `dsh plugin add` | 纯 pnpm 转发（`spawnSync("pnpm", …, {cwd: profileDir})`），成功后 `reconcilePlugins` 把每个声明了 `dsh.bundle.patch` 的依赖**按依赖顺序追加**到 `dsh.profile.bundles`；pnpm 不在 PATH 则 exit 127 **[V!]** |
| 组合顺序 | 每个 bundle 的 `cordis.patch.yml`（按 bundles 顺序）→ profile 的 `cordis.patch.yml` → `$DSH_HOME/cordis.patch.yml` → 每个 `--patch`（argv 顺序）→ `DSH_TELEMETRY_DISABLED` 生成的那条 **[V!]** |
| patch 语义 | `{id, ...overrides}` 中除 `id`/`insert`/`name` 外**每个 key 整体替换**（`config` 从不深合并）；`name` 是**断言**，不匹配则**整条 patch 被跳过**；带 `insert` 时该条目的其他 key 被静默忽略（insert 与 config 覆盖必须写成两条）；未知 id 只 warn **[V!]** |
| `--patch` 文件 | 按 YAML(JSON_SCHEMA + `!!js`) 解析，**JSON 可用、扩展名无关**；`{"__jsExpr": "process.env[...]"}` 在 entry 激活时求值 → **密钥可只走环境变量，不落 patch 文件** **[V!]**（fork 的 `mcp_secret_expression`（`multica-dsh:197-204`）已在用） |
| 行 `name` 解析 | **绝对文件路径可用**；**目录路径不行**（`ERR_UNSUPPORTED_DIR_IMPORT`，而且会**中止整个启动**，偏偏 `--dump-config` 仍 exit 0 → 不能拿 dump 当预检）；裸包名从 profile 目录单锚点解析（`dsh.profile.bundles` 才是双锚点：先 dsh 安装目录再 profile）**[V!]** |
| peer 解析 | catalog 插件的 peer **不会**装进 profile（`autoInstallPeers: false`），只能靠每次启动 heal 的 `<home>/profiles/node_modules`（483 个符号链接）解析到 launcher 那一份实例；共享 catalog 布局下，**catalog 自己那个 home 的 fallback 必须在镜像构建期 heal 好** **[V!]** |
| `allowBuilds` | 只对 `git+`/`github:` 规格有意义（pnpm 拦 `prepare` 脚本）；registry/tarball 安装用不到 —— **保持用不到的状态**，第三方插件就无法在 `dsh plugin add` 期间跑安装脚本 **[V!]** |
| `minimumReleaseAge` | **是传递且相对时间的**：设 10080（7 天）时，添加 `@openma/deepseek-harness-acp@0.4.27` 会因为**已装依赖**的 5 个传递包（cosmokit、schemastery、fast-uri、qs、zod）过新而**整份 lockfile 被拒**；设 1 则 2.1s 成功 **[V!]** → 只能用在导入/CI 路径，绝不能放进任务路径 |
| 冷启动成本 | 全新 `DSH_HOME`：插件 mount 耗时 0.80s；同 home 第二次 0.50s；纯组合（`--dump-config`）0.36s；home 仅 88KB **[V!]** → 现有"每任务临时 home"设计成本约 0.3s，相对 pnpm（暖 2.3s、冷则网络绑定）可忽略 |

**⚠️ 本节最重要的一条修正 [争议→refuted，双 lens 一致]**：plugin-assembly 报告原称"`--patch` 无法选择/添加 bundle，选择只能通过 profile manifest"。核验推翻了它：`--patch` 的 `insert` 行携带插件模块 `name` + 完整 `config`，**这正是一个 bundle patch 所做的全部**；实测用未列该插件的 stock `headless` profile，通过三种解析路径（绝对入口文件路径 / profile 的 `node_modules` 软链到 catalog / `$DSH_HOME/profiles/node_modules`）都成功真实挂载了 `dsh-mcp-lens` **[V!]**（claim 74）。fork 现在这套 `--profile headless` + JSON overlay **已经足以做插件集**，代价是两件构建期杂活，而不是设计上限：
- **没有自动发现**：镜像构建时必须把每个 catalog 包的 `cordis.patch.yml` 机械转录成 overlay 行（`!!js expr` → `{"__jsExpr": "expr"}`），且**必须转全**——例如只给 `mcp-lens` 一个 `config: {}` 会报 `invalid config: $.cachePath missing required value`。
- **没有自动依赖链接**：DSH 只为 manifest 里列出的 bundle 做外部包链接，`--patch` 加入的插件其依赖/peer 必须在镜像构建期 hoist 到它旁边（fork 的 MCP bridge 已经是这个待遇）。
- `--patch` 真正做不到的：安装/解析任何东西（包必须已可解析）、把 bundle 层排到 profile/home 层**之前**、让插件出现在 `dsh plugin --profile X list` 里。

⇒ **Phase 1 里"每任务写 `profiles/multica/package.json` + 软链 node_modules"是一个可选优化，不是必需**；把插件行直接写进已有的每任务 overlay 是更小的改动、更少的每任务状态。

### 5.3 选项矩阵

| 维度 | **A 镜像烘焙 catalog + 每任务 `--patch` 组装** | **B 任务启动时 `dsh plugin add`** | **C 服务端 "DSH profile" 实体挂在自定义 runtime profile 上** | **D 把 DSH 插件做成 Multica 插件包（托管 + consent）** |
| --- | --- | --- | --- | --- |
| 供应链安全 | **5**：任务期零安装；lockfile + tarball 完整性在构建期固定，`--ignore-scripts`；catalog root 只读 | 2：任务沙箱里跑 pnpm + 联网；每沙箱漂移 | 3：开发机从内网 registry 装，服务端无法证明 pnpm 实际解析了什么 | 4：不可变已授权版本 + digest，secret 密封在 PG |
| 冷启动 | **5**：组合 0.36s + heal 0.3s（今天已经在付） | 2：暖 2.3s，冷 = 下载数十 MB；FC run-once 沙箱没有持久 store | 4：每机器物化一次 | 3：每机器首个任务下载产物 |
| 离线 / 内网 | **5**：Aone CI 从 npmmirror 构建，运行期不需要 registry | 3：沙箱出网走 relay；anpm 落后 npmmirror ≥1 天 **[V!]** | 3 | 4 |
| 多副本正确性 | **5**：除 `agent.runtime_config` 外无状态 | 4 | 3：期望态在 PG、物化态在机器，需要 reconcile + 漂移上报 | 5 |
| UX | 4：按 runtime 展示目录，按 Agent 勾选与配置；导入 = 一次发布 | 4 | 3：贴合已有对话框，但是 per-runtime 而非 per-agent | 5：consent 屏、版本、密钥、升级流 |
| 合规成本（zod / 无 FK / 9xxx 幂等） | **5**：Phase 1 **零迁移** | 4：一张 allowlist 表 | 4：需要 `dsh` 进 `protocol_family`（9xxx） | 3：多张表 + 对象存储版本 |
| 对 hermes/opencode/pi/claude/codex 的影响面 | **5**：只碰 `multica-dsh`、一个 env key、provider=dsh 时读 `runtime_config` | 3：沙箱网络策略 + 镜像里加 pnpm | 3：runtime-profile handler / launch-prefix 移植影响所有 provider | 4 |
| 工作量 | 低–中 | 中–高 | 中 | **很高**（fork 完全没有这套系统） |

**结论**：**A 在除"不发版也能装任意包"外的每个维度都占优**，而它唯一的结构性限制（catalog 随镜像冻结）恰恰是它的安全属性。**B 留作本地 daemon 的后期逃生口**（Phase 2，配服务端 allowlist），**永远不给 FC/ASB 用**。**C 有用的那一半**（站点级 overlay `fixed_args: ["--patch", …]`）在 LaunchPrefix 移植落地后即免费获得。**D 的数据模型值得抄**（Phase 3 工作区导入），但存储必须换成 OSS。

### 5.4 推荐的分阶段设计

#### Phase 1 —— 镜像烘焙 catalog + 每 Agent 选择（云沙箱，**零迁移**）

**数据模型**
- **目录（谁存在、可解析）**：`server/pkg/dshcatalog/catalog.json`（`go:embed`）+ `catalog.go`（解析/校验/sha256）。条目形状：`{name, version, integrity(sha512 来自 pnpm-lock), license, source(repo URL + commit), summary, rows:[...], optional_rows:[...], config_schema:[...], secret_keys:[...], requires_capabilities:[...], review:{ticket,reviewer,date}}`。镜像 CI 从 `MULTICA_REF` 同步同一份 `catalog.json` + `catalog/package.json` + `pnpm-lock.yaml`，两边由构造保持一致，任务期由 adapter 校验 sha 兜底（不一致 → fail closed）。
- **选择（这个 Agent 开哪些、什么配置）**：`agent.runtime_config` JSONB 的 `dsh` 键 → `{"catalog_sha256": "...", "plugins": [{"name","enabled","config"}]}`。**已经原样进 claim payload，零迁移** **[V!]**。
- **密钥**：存 `agent.custom_env` 的生成键，`config` 里以 `{"$env": "MULTICA_DSH_PLUGIN_SECRET_x"}` 引用，adapter 翻成 `__jsExpr`。响应里只回 `configured_secrets: [key]`，**永不回显值**。
- **runtime/工作区默认集**：**先不做**。**[V! 重要修正]** 绝不能写 `agent_runtime.metadata` —— `UpsertAgentRuntime` 在每次 daemon 注册时**整份替换** metadata（`runtime.sql:66`），UI 写进去的键会在下次重连时静默丢失；对本地 daemon runtime 与 FDE 托管 runtime 是**必然丢失**（claim 29）。核验补充：也不一定要新表——fork 已有"给 `agent_runtime` 加可空列 + 专用 UPDATE 查询"的既有范式（migration 145 + `UpdateAgentRuntime*`）。

**API**（都挂在既有 router 组里，member 可读、admin 可写，参照 runtime-profiles 的 `router.go:2006-2027`）
- `GET /api/runtimes/{runtimeId}/dsh-plugins` → `{catalog_sha256, dsh_version, capability, plugins:[DshCatalogEntry]}`
- `GET /api/agents/{id}/dsh-plugins` → `{catalog_sha256, plugins:[AgentDshPlugin]}`
- `PUT /api/agents/{id}/dsh-plugins/enabled` → body `{runtime_id, name, enabled, config?, secrets?}`，**逐字复制 `SetAgentRuntimeSkillEnabled`**（`agent_runtime_skills.go:83-210`）：`GetAgentForUpdate` 行锁下的读-改-写、**锁前锁后各一次** runtime 不匹配 409、结尾的 `EventAgentStatus` 广播 **[V!]**
- （Phase 2）`POST /api/runtimes/{runtimeId}/dsh-plugins/discover` + `GET …/discover/{requestId}`：复用 local-skills 的异步任务壳（Redis/Tair store，多副本安全）
- 用**独立端点**而非 `PUT /api/agents/{id}` 带 `runtime_config` 的理由：插件集含**只写密钥**，通用更新路径要为 N 个动态键各配一个 mask sentinel + preserve hook（openclaw 那套 `agent.go:426-480` 不能泛化）；独立端点让密钥根本不出现在 `AgentResponse` 里——这和 `custom_env` 被单独挪到 `/api/agents/{id}/env` 是同一个理由 **[V]**。
- zod：`DshCatalogConfigFieldSchema` / `DshCatalogEntrySchema` / `DshCatalogSchema` / `AgentDshPluginSchema` / `AgentDshPluginsResponseSchema`，全部 `.loose()` + 默认值，客户端方法一律 `parseWithFallback` + `EMPTY_*` 常量，**每个新端点配一条 malformed-response 测试**（仓库硬规则）。⚠️ 不要照抄邻居：`listFCE2BTemplates`（`client.ts:2005-2007`）与 4 个 local-skills 方法**今天就在裸 cast，是既有欠债** **[V!]**。

**daemon / adapter**
- 新增 `server/internal/daemon/dsh_runtime_config.go`（~90 行，照抄 `openclaw_runtime_config.go:29-88`），JSON 畸形时 fail-soft。**注意**：云 DSH 沙箱里 daemon 的 provider 是 `opencode` 而非 `dsh` **[V!]**，所以 gate 要看 `dsh` 子对象或云 runtime capability，不能照抄 `provider == "openclaw"`。
- env 注入点是**承重的**：`isolateA2AChildEnv` 会把 `agentEnv` 里所有 `MULTICA_` 前缀键**置空**，且**跑两次**（`daemon.go:6272` 与 `:6344`，后者在 `layerCustomEnvAndHermesHome` 之后）**[V!]**。**[V! 修正]** 它只对 `task.A2AInvocation == true` 的任务生效（不是"所有云 DSH 任务"），并且**写在第二次 scrub 之后（`:6344` 之后）即可**，不必写进 `configureManagedA2AV2ProviderEnv`（那里还多一层 `A2AManagedRuntimeV2` 门禁，会漏掉非 A2A 的云任务和全部本地任务）。这是整套设计里**最可能"测试通过、生产消失"**的地方，必须有专门回归测试。
- adapter `scripts/multica-dsh`：读 `MULTICA_DSH_PLUGIN_SET` 与 `MULTICA_DSH_CATALOG_SHA256`，先校 sha（不符 → `InvocationError` fail closed），再在既有 `write_patch` 里为每个被选插件追加 `{id: <row>, config: <目录默认 deep-merge Agent 配置>}`（**因为 DSH 整块替换 config，deep-merge 必须由 adapter 做**）和为 `optional_rows` 追加 `{id, disabled: true}`；密钥字段改写成 `__jsExpr` + env 注入。既有四行（`agent-default-model`、`llm-pi-ai`、`session-persistence-jsonl`、托管 MCP）原样不动。
- capability：`dsh_plugin_catalog_v1`。**[争议→refuted]** gui-fork-ui 原称"除了伪 provider 或 schema v2 没有安全通道"，核验给出至少三条更安全的路 **[V!]**（claim 36）：①**新开一个 Diamond 数据 ID**（老副本根本不引用该常量，不会拒绝；runtime-provider 目录本身就是这么引入的，fetch/listen 失败非致命）；②复用**已按发布版携带 `capabilities_by_backend` 的 `fc_e2b_stable_release.manifest` JSONB**（老 binary 对未知 capability 字符串原样透传，且 DSH 已在该通道上，见 9065）；③把 alias 里的 `m(\d+)` **捕获**出来而不是硬编码 7（两行改动，最坏影响只是老副本看不到新的 m8 模板）。**推荐 ②**，其次 ①；**不要**在原 Diamond 文档上做 v2 版本升级（`DisallowUnknownFields` + `Version != 1` 会让老副本保留旧快照，滚动期新模板"没有 provider"）。

**UI**：见 §6.4。

#### Phase 2 —— 本地原生 backend + daemon 侧 profile 物化

- 前置：§3 的定向移植（`dsh.go`、`agents_probe.go`、`execenv`、`local_skills.go`、`SupportedTypes`、`RUNTIME_PROFILE_PROTOCOL_FAMILIES`、9128）。
- `dshLaunchArgs()` 改造：接受 profile 名（来自 `Config.LaunchPrefix` 或 `MULTICA_DSH_PROFILE`）+ 支持每任务 `--patch <tmpfile>`。**必须做统一 argv planner**，因为模型目录缓存 key 只有 `provider:path`（`models.go:399-403`）、`handleModelList` 只取 path、thinking 校验又独立走一次目录（`thinking.go:671`）——只改 Execute 不够 **[V]**。planner 契约：默认 `--profile multica <modeFlag>`；`fixed_args` 里最多一个 `--profile name`；拒绝 `--stdio`/`--probe`/`--list-models`/`--version`/`--`；缓存 key 要编码 provider + 规范化 path + **完整 prefix 数组（JSON 序列化，不能空格 join）** + 生效的 `DSH_HOME`。
- 新增 `server/internal/daemon/dsh_plugins.go`（照 `claude_plugins.go` 的模式）：注册时读 `$DSH_HOME/profiles/multica/package.json` 上报已装 bundle 与版本；任务期若与服务端期望集不符，用 `dsh plugin --profile multica add <name>@<version>` 物化（profile 的 `pnpm-workspace.yaml` 带 `minimumReleaseAge`、**无 `allowBuilds`**、`--ignore-scripts`、registry 来自 daemon 配置），并按 catalog 的 `integrity` 校验 lockfile；不符则 fail closed。漂移在 runtime 详情页可见（照 `failed_profiles` 的上报方式）。
- **服务端只持有期望态，机器上报的已装态永远只当数据，绝不当事实来源** —— 本地 daemon 天然是节点本地状态。
- 本地轨迹上传（可选，~80 行 Go，**无需服务端改动** **[V!]**）：`result` 帧后按 `MULTICA_DSH_SESSION_ROOT` 找 `session.jsonl` 上传。**[V! 修正]** 但不是"任何带 mat_ token 的任务"都行：A2A 来源任务根本不发 mat_ token；handler 还要求 `h.Storage` 已配置、`Content-Type: application/x-ndjson`、`X-DSH-Session-ID` 等于 ledger header 的 `id`、精确 `X-Content-SHA256`、体积 ≤32 MiB、以及严格 ledger schema（`version==0`、`delegationDepth==0`、无 `parentSession`/`origin`、事件 `seq` 从 0 连续）；且 PUT 必须发生在 `CompleteTask`/`FailTask` **之前**（那时 mat_ token 会被删）。

#### Phase 3 —— 工作区导入开源插件（不在镜像里的）

- **导入源**：见 §6.5——现实来源是 **npm 元数据（经 npmmirror）**，由 CC0 的 awesome 列表做种子，**不是**任何市场网站。
- **流程「allowlist / pin / review」**：`POST /dsh-plugins/packages {spec: "<name>@<精确版本>"}` → **服务端**（不是沙箱）向 `MULTICA_DSH_REGISTRY` 解析、下载 tarball、校验 registry `integrity`；若 `package.json.scripts` 含 `preinstall/install/postinstall`、缺 `dsh.bundle`、license 不在白名单、或 peer 范围排除镜像的 `DSH_VERSION`，一律拒绝；解析 `cordis.patch.yml` 成 `bundle_patch`/`rows`；tarball 落 **OSS**，PG 只存 `storage_key` + digest（仓库硬规则：大不可变对象走 OSS）；`review_status = pending` → admin 在 consent 屏（列出插入了哪些行、覆盖了哪些行、config key、peer、license）批准后才可选。
- **表**：**[争议→采纳修正]** gui-upstream-plugins-tab 建议"照抄上游 stem 与表名以便将来同步收敛"，核验双 lens 都指出这**行不通且更危险** **[V!]**（claim 43）：fork 停在迁移 272，下次同步会先送来**未应用的 Plugin-V1 链 285–326**，其中 `285_plugin_lifecycle_v1` 用**无 `IF NOT EXISTS` 的 `CREATE TABLE plugin_installation`**，排在 344 之前 → 对已建同名表的库**硬失败**；而 344 对 `plugin_installation` 是先 `DROP TABLE IF EXISTS`，意味着**静默销毁 fork 数据**。另外报告给的子集 {344,345,347,368,392–397} 根本跑不通：`392` 里有 `DELETE FROM plugin_invocation`，那张表来自被"推迟"的 362。⇒ **本文采纳：用 fork 自有表名 `dsh_plugin_package` / `dsh_plugin_package_version`，放 9000+ 段，`CREATE TABLE IF NOT EXISTS`，唯一索引单独文件 `CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS`，无外键**，代价是将来若真采用上游插件系统会并存两套 schema。
- **回流规则**：任何被工作区批准的插件，都成为下个 runtime 发布的 catalog 候选，让 Phase 1 的快路径保持常态、Phase 3 只是例外。
- 别忘了新表要进 `workspace_delete.sql` 与 `workspaceDeletionManifest`，否则 `TestWorkspaceDeletionManifestCoversPublicSchema` 会失败 **[V]**。

### 5.5 安全边界（跨阶段）

1. **consent 文案必须诚实**：上游那句"插件能做的绝不超过使用它的人"对 Cordis bundle **是假的**——它在 agent 进程内运行，拥有该进程的全部文件系统与网络权限 **[V!]**。用一个诚实的 `runtime:dsh` scope，中文写成"在 DeepSeek Harness 智能体进程内运行，拥有该进程的全部权限"。建议 `dsh_bundle` 的发布权限收到 owner。
2. **禁止任务期安装**：FC/ASB 路径永远不跑 pnpm；`allowBuilds` 保持缺席，第三方包就无法跑安装脚本。
3. **`minimumReleaseAge` 只放在导入/CI 路径**（它是传递+相对时间的，放任务路径会随机炸）。
4. **密钥**：只走 env + `__jsExpr`，永不落 patch 文件；注意 DSH 会把 `KEY|PASSWORD|SECRET|TOKEN` 与所有 `DSH_*` 从模型派生的 shell 里 scrub 掉，只有 `mat_` 开头的 `MULTICA_TOKEN` 被 bundle 重新放行 **[V!]** —— 所以 DEAP key 能到 LLM adapter 但到不了 agent 的 bash（这是好事）。A2A 子任务里，`MULTICA_` 前缀与 `_TOKEN/_SECRET/_API_KEY` 后缀的 `custom_env` 会被清空，插件密钥需要显式 restore。
5. **`DSH_*` 是 bootstrap-only**：不能通过 `.env` 注入，只能来自 daemon 进程环境；但 Agent 的 `custom_env` **目前可以设** `DSH_*`（不在 `isBlockedEnvKey` 里）**[V!]** → 若工作区管理员不完全可信，建议把 `DSH_` 加进阻断列表。
6. **权限模式**：bundle 会自动批准每个 `approval/request`，唯一真实防线是 `DSH_PERMISSION_MODE` 选中的 OS 沙箱；没有 bwrap/Landlock/seatbelt 时 DSH **fail closed**（`SANDBOX_UNAVAILABLE`），除非 `danger-full-access` —— **不要在共享机器上设它** **[V!]**。
7. **stdout 归属**：bundle 只往 stdout 写协议帧，但第三方插件可能 `console.log`。Go 侧对非 JSON 行是容忍的（计入 `invalidFrames` 后跳过），真正危险的是伪造 `v:1` 且无 `request_id` 的行 → 在 fork 副本里做 stdout 劫持（0.5 天）。

---

## 6. 插件导入与管理的 GUI

### 6.1 能否直接复用 DSH 自带 Web UI？——**不能**

四条独立的否决理由，全部实测 **[V!]**：

1. **它根本不是插件管理器**。Settings → Plugins 只有两个 tab：**只读**的「Plugin list」（`pluginInventory/list`，README 明写"cannot enable, disable, add, or remove plugins"）和「Plugin configuration」（只编辑 4 个已组合的 host 插件：`bash`、`agent-loop`、`subagent-model-selection`、`web-search-deepseek`）。**没有任何 route / Remote / JSON-RPC / ACP 方法能安装、删除、启停插件**；实测 `POST /api/loader/create` → 404，`POST /api/pluginInventory/add` → 404。`--profile sdk` 的 JSON-RPC 只有 3 个请求（`initialize`/`session/prompt`/`shutdown`），`--profile acp` 也没有插件管理。
2. **它是单租户全 RCE 控制台且只绑 loopback**。bind host schema 只允许 `127.0.0.1|0.0.0.0`，而启动时**显式拒绝 `0.0.0.0`**（"would expose remote code execution to the network"）。鉴权是一个进程级 32 字节启动 token 换成 30 天 `HttpOnly; SameSite=Strict` cookie（**没有 `Secure`**），绑定原始 `Host` 头；**没有用户、没有角色、没有登出、没有只读模式**。持有 cookie == 宿主机 bash + 写 settings/凭据。
3. **它硬编码 origin 根**：`API_PATH = "/api"`、`<base href="/">` 注入 index.html、token 交换只在 `GET /` 上接受、从不读 `X-Forwarded-*` → 路径前缀反代或跨站 iframe 都跑不了；只有独占一个同站主机名 + 重写 Host（或 `--trusted-host`）才可能。
4. **fork 自己的镜像文档已经禁止反代它**（`multica-fc-hermes-runtime/README.md:89`、`CLAUDE.md:163-169`），理由与 2/3 完全一致。

另外两条与之相关的事实：DSH **没有任何插件市场 / 目录 / 搜索发现功能**，UI 只查本地 Cordis Loader **[V!]**；`plugin-package-inventory-deepseek` 行默认开启，会让每个 **官方 DeepSeek adapter** 请求携带活跃包清单 `dsh_plugin_packages` —— fork 走 `deap` 自定义 provider 所以当前不触发，但**平台组合出来的 profile 应显式把它设成 `enabled: false`** **[V!]**。

**[争议→补充]** 核验指出一个值得知道的生态事实（不改结论）：社区包 `dshmarket@1.43.0`（MIT）**确实在 DSH web settings UI 里做出了浏览/搜索/一键安装的插件市场**（在 host plane 里 spawn `dsh plugin`），`dsh-extension-hub@0.2.19` 也在同一页面管理 skill/MCP **[V!]**。这证明"UI 内装插件"技术可行，但它建立在"cookie 持有者 == 宿主机全权"之上，与我们的多租户模型冲突 —— **依然不采用**。

**可复用的是**：CLI（`dsh plugin … add|remove|update|ls|why`、`--dump-config`、`--patch`）、profile/patch 组合模型、以及 inventory 的**信息设计**（按作用域分组：preset 组合在前、global 折叠在后、失败项浮顶、标注"preset 提供"）——把这些做成 Multica 原生页面背后的后端原语。许可不是障碍（223 个包里 222 个 MIT，唯一 BSD-3 是 `node-addon-landlock-run`）**[V!]**。

### 6.2 能否复用上游 Multica 的 Settings → Plugins？——**抄形状，不抄整套**

- 上游有完整的 admin 侧生命周期 GUI：`packages/views/settings/components/plugins-tab.tsx` **721 行**，背后是 `plugin_package` / `plugin_package_version` / `plugin_package_file` / `plugin_installation` / `plugin_secret`，流程为 publish → preview → **consent** → 绑定到不可变版本的 install → enable/disable → configure（secret 密封、只回 `configured_secrets` 名字） → upgrade（二次 consent） → uninstall（显式逐表删除，因为"仓库政策没有外键与级联"）**[V!]**。fork **一样都没有**（fork 历史里没有 plugin 文件，迁移里没有 plugin 表）**[V!]**。
- **能直接泛化到 DSH 的**：不可变版本 + digest + 已授权 manifest 快照；config schema + 密封 secret；`resources: skill`（DSH 本来就读 `.dsh/skills` 与 `$DSH_HOME/skills`）；`HostCapabilities()` 门禁（fork 里打开 `ResourceTypes["dsh_bundle"]`，上游侧则 422 拒绝——一个干净的前向兼容故事）。
- **不能泛化的**：封闭 scope 列表（对 Cordis bundle 无意义，改成单个诚实的 `runtime:dsh`）；`contributes.surfaces`（iframe + 独立 cookie-free origin + SDK + Action API）；`contributes.hooks`（HTTP/MCP、调度器、`plugin_invocation`、`remotemcp` broker）——方向恰好相反：hooks 是 Multica 去调插件的服务，DSH 插件是被加载进运行时。
- **依赖缺口**：fork 缺 `server/pkg/remotemcp/` 与 Go 模块 `github.com/tdewolff/parse/v2`（只被 `bundle.go` 的 surface 脚本解析器用到）；`secretbox`、`TxStarter`、feature-flag 包都已具备 **[V!]**。
- **成本对照 [I]**：完整移植 12–19 人日且对 DSH 目标复用率仅 ~30%；只移植生命周期切片 5–7 人日；切片 + `dsh_bundle` 资源类型合计 9–14 人日。**Phase 1 不需要它**（镜像烘焙的目录不需要 publish/consent/upgrade 语义），**Phase 3 才值得**。
- **[争议]** 关于"照抄上游迁移 stem 以便未来同步收敛"——见 §5.4 Phase 3：**已被推翻，改用 fork 自有表名**。

### 6.3 fork 自己已有的 GUI 原语（这才是 Phase 1 的地基）

**[V!] 结论：fork 已经具备这个功能需要的每一个 GUI/API 原语，不需要发明任何东西，也不需要新页面路由。** 四个 1:1 对应：

| 需求 | fork 里已经这么干的地方 |
| --- | --- |
| 对"运行时继承来的能力"做每 Agent 勾选 | `SkillsTab` + `agent.disabled_runtime_skills`（JSONB）+ `PUT /api/agents/{id}/runtime-skills/enabled` |
| 每 Agent 托管配置 + 运行时发现清单并排展示 | `McpConfigTab`（`agent.mcp_config`）+ `runtimeCapabilitiesOptions` 发现查询 |
| 每 Agent、每 provider 的结构化配置 + 掩码密钥 | `RuntimeConfigTab` + `agent.runtime_config`（openclaw）+ `maskGatewayToken`/`preserveMaskedGatewayToken` |
| 目录勾选写回 agent 列 | `AgentMcpTab`（Composio）+ `agent.composio_toolkit_allowlist` |
| 管理员视角"这个镜像带了什么"的只读卡 | `DiagnosticsCard` → runtime-detail 里的「Cloud sandbox image」块 |
| 从 runtime 往工作区异步导入产物（带进度与冲突处理） | `runtime-local-skill-import-panel.tsx` + Redis/Tair 支撑的 pending-job-over-heartbeat |

而且 `RuntimeDetail` 与 `AgentOverviewPane` 都已经在 web 与 desktop 共享挂载 —— **在里面加 section/tab 会同时生效，不需要任何平台层接线** **[V!]**。

### 6.4 推荐方案与要复制/扩展的具体文件

**归属划分（按可变性拆）**：**Runtime 侧拥有只读目录**（镜像/机器带了什么），**Agent 侧拥有选择**（开哪些、什么顺序、什么配置/密钥，存 `agent.runtime_config.dsh`）。理由：`--patch` overlay 只能为**已可解析**的代码插行，"有没有"是镜像构建的属性；而选择必须在任务期抵达 runner，`agent.runtime_config` 是唯一已经能到沙箱的通道；且一个 FC runtime 常同时服务多个 Agent（`ServingAgentsCard`），把选择放 runtime 会破坏隔离 **[V!]**。

**新增文件**
- `packages/views/agents/components/tabs/dsh-plugins-tab.tsx`（~280 行，结构上是 `mcp-config-tab.tsx` 的 fork）：两段——「该运行时可用」（目录逐行：名称、版本、license 徽标、一句话说明、`Switch`、有 `config_schema` 时内联展开表单，密钥是只写输入 + "已配置"徽标）与「已选择但不可用」（当前镜像不再提供的条目，用 `update-fc-e2b-runtime-template-dialog.tsx:231-236` 的 unavailable 处理方式）。空/离线/不支持等 6 种状态照抄 `mcp-config-tab.tsx:216-249`。
- `packages/views/agents/components/tabs/dsh-plugins-tab.test.tsx`（~200 行，harness 照抄 `mcp-config-tab.test.tsx:1-40`，**不得 mock `next/*` 或 `react-router-dom`**）。
- `packages/views/runtimes/components/dsh-plugin-catalog-section.tsx`（~140 行，挂在 `runtime-detail.tsx` 右栏，紧邻 `ASBRuntimeCredentialSection`）：目录条目 + 页脚 `catalog <sha7> · image <template alias>`；本地 runtime 则渲染发现结果 + 刷新按钮；管理员额外看到「导入插件」——本地 runtime 走真实安装任务，云 runtime 置灰并给出诚实理由"云沙箱镜像不可变，请走运行时发布流程"。
- `packages/core/agents/dsh-plugin-support.ts`（~25 行）+ `.test.ts`：`providerSupportsDshPlugins(p) => p === "dsh"`；`runtimeSupportsDshPlugins(provider, metadata)` 再查 `metadata.capabilities.includes("dsh_plugin_catalog_v1")`。
- `server/pkg/dshcatalog/{catalog.go,catalog.json,catalog_test.go}`；`server/internal/handler/dsh_plugins.go`（2 GET + 1 PUT）；`server/internal/daemon/dsh_runtime_config.go`。

**要改的既有文件（都是小 diff）**
- `packages/views/agents/components/agent-overview-pane.tsx` 五处：`DetailTab` union（`:57-76`）、`SecondaryTab["labelKey"]`（`:78-97`）、`CAPABILITY_TABS`（`:99-107`，插在 `mcp_config` 之后）、可见性过滤（`:258-268`）、内容 switch（`:548-641`）。**[V! 修正]** 注意 `provider === "openclaw"` 那条先例在 `SETTINGS_TABS` 过滤里（`:285-287`），不在 `CAPABILITY_TABS` 过滤里；若把 DSH tab 放能力区，应参照 `isASBRuntime(runtime)` / `agent.runtime_mode === "cloud"` 的写法。
- `packages/views/runtimes/components/runtime-detail.tsx`：一行挂载。
- `packages/core/api/{schemas.ts,client.ts}`、`packages/core/runtimes/cloud-runtime.ts` 的 `cloudRuntimeKeys` 加 `dshPlugins`。
- `server/cmd/server/router.go`：`/{runtimeId}` 组（`:2557-2597`）与 agents 组（`:2468-2481`）各加路由，写操作放 admin 组。
- `server/pkg/runtimeconfig/runtime_providers.go` + `server/internal/service/fc_e2b.go`：capability 常量与派生（按 §5.4 选 ②）。
- i18n 四语（`locales/*/agents.json` 的 `tabs.dsh_plugins` 等约 14 个 key、`locales/*/runtimes.json` 的 `detail.dsh_plugins.*` 约 6 个 key）。术语：**DSH / DeepSeek Harness 不翻译**（与 `display.ts:45` 一致），plugin → **插件**（fork 已有先例）、catalog → 目录、runtime → 运行时、agent → 智能体、image → 镜像；zh 只用 `_other`。`packages/views/locales/parity.test.ts` 会强制四语齐全 **[V!]**。

**不要做**：新路由、新 settings tab、desktop 单独接线、嵌 DSH 自带控制台、Phase 1 就移植上游 `plugins-tab.tsx`。

### 6.5 社区市场调研与首批建议导入的开源插件

**三个候选目录（全部实测抓取）[V!]**

| 来源 | 机器可读？ | 许可/条款 | 能否作为导入源 |
| --- | --- | --- | --- |
| `github.com/0xsline/awesome-deepseek-harness` | ❌ 只有 Markdown README（提到有 `CATALOG.md`），无 JSON/API/RSS | **CC0-1.0**，内容可自由复用；单一具名维护者；996 stars / 366 forks | ✅ **可作种子清单**（解析一次、人工策展）；条目是 git repo 而非 npm spec |
| `dsh-plugin.org`（"DSH Plugin Hub"，GitHub org `dshplugin`） | ❌ 只有 HTML + sitemap.xml | 自称与 DeepSeek 无关联；有 /terms（未读） | 仅用于**发现**；有价值的字段是 `dshTarget`（每插件的 DSH 版本目标），可惜只渲染在 HTML 里 |
| `deepseekharnessplugins.com` | ❌ | 自称与 DeepSeek 无关联 | 仅用于发现，信号最弱（大量第三方目录徽标、邮件订阅、交叉推广） |

**关于"数千个人工验证插件"的说法：不可信 [V!]**。①三个数字互相矛盾且都不可复现：dsh-plugin.org 说 8360 indexed / 5637 verified，deepseekharnessplugins.com 说 13912 indexed / 3828 human-curated，而同一维护者的 npm 包 `dsh-plugin@1.4.2` 描述里写 "4000+ human-curated"。②规模不合理：`@deepseek-ai/dsh` 2026-08-13 才上 registry，三周内"人工验证"5000–14000 个插件在吞吐上不成立；真正有具名维护者的 awesome 列表条目量级是 10²。③两个站点都不公布验证方法、审核人名单或审计轨迹，也都不提供可供第三方核查的目录接口。④唯一有具名维护者和真实许可的来源（awesome 列表）**根本没有做任何验证声明**。⇒ **把两个市场的 "verified" 徽标当作零保证的营销元数据**，只可用于发现候选名，绝不能作为信任输入。

**唯一机器可读且有契约的目录是 npm registry 本身**：`npm search --json dsh-plugin --registry https://registry.npmmirror.com` **可用**并返回正常 JSON；而 **`registry.anpm.alibaba-inc.com` 不实现 `/-/v1/search`（404）** **[V!]**。⇒ **发现走 npmmirror（或我们自己维护的目录），安装/镜像走 anpm** —— 平台不能问 anpm"有哪些 dsh 插件"。npm 元数据已经包含平台侧插件注册表需要的全部字段：name、精确版本、`dist.tarball` + integrity、`license`、发布时间、`peerDependencies`（DSH 兼容范围）、`scripts`（安装脚本风险）。

**首批建议进 catalog 的开源插件**

| 插件 | 版本 | 许可 / 体积 | 为什么先要它 | 注意 |
| --- | --- | --- | --- | --- |
| `dsh-mcp-lens` | `0.1.0-rc.9` | MIT，143 KB | 渐进式 MCP 网关；单条 `mcp-lens` 行、config 块声明完整；peer（cordis、dsh-subprocess、dsh-tools）都在盒内；registry tarball 不会跑 `prepare` **[V!]** | 它正好演示"config 整块替换"规则：启用者必须提供 `servers`/`allowTools`，adapter 要先与目录默认 deep-merge；`config: {}` 会直接报 `$.cachePath missing required value` |
| `dsh-model-router` | `0.6.2`（`@welsione/dsh-model-router` 亦同族） | MIT，91 KB（tarball 实测 ~412 KB） | 模型路由，与 DEAP 多模型场景天然契合；peer `dsh-plan-mode`/`dsh-settings` 在盒内；`dsh.client` 的 web UI 部分对 headless 无关 | 注意某些同族包 peer 指向 npm 上不存在的 `@deepseek-ai/dsh-type-meta`，`autoInstallPeers: true` 会直接装失败 **[V!]** |
| （观察位）`memsearch`（zilliztech）、`dsh-context` | — | — | 共享记忆 / 上下文与 token UI，都是 FDE 场景高频诉求 | `dsh-context` 发版极频（0.1.0 起 70+ 版本），必须锁定精确版本 |

**明确不要放进第一批 [V!]**：`@openma/deepseek-harness-acp`（peer 写 `@deepseek-ai/dsh: *`，且内嵌一份 46 MB 的 `vendor/dsh-runtime.tgz`，解包 50,298,433 字节 —— 报告里"56 MB"是安装目录 `du` 值）；`dsh-clawrouter`（依赖外部付费模型钱包 `@blockrun/llm`）。

---

## 7. 本地 daemon 落地 runbook

### 7.1 macOS 开发机（已在本机全程实测 **[V!]**）

```bash
# 0. 网络：anpm 只在内网
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy

# 1. 工具链（Node 22 LTS；pnpm >= 10 必须在 PATH，dsh plugin 会 shell out 给它）
nvm install 22.19.0 && nvm use 22.19.0
corepack enable && corepack prepare pnpm@10.28.2 --activate
node --version; pnpm --version

# 2. dsh CLI，锁版本，从内网镜像装，不跑安装脚本（node-pty 自带 darwin 预编译）
export DSH_HOME="$HOME/.dsh"
mkdir -p "$HOME/.multica-dsh" && cd "$HOME/.multica-dsh"
echo '{"name":"multica-dsh-cli","private":true}' > package.json
npm install @deepseek-ai/dsh@0.1.2-rc.1 --ignore-scripts --no-audit --no-fund \
  --registry https://registry.anpm.alibaba-inc.com     # 实测 523 包 / 223 个 @deepseek-ai / 22s
ln -sf "$HOME/.multica-dsh/node_modules/.bin/dsh" "$HOME/.local/bin/dsh"
dsh --version    # 0.1.2-rc.1

# 3. Multica bundle —— 用 tarball，绝不要 add 一个 checkout 目录（双实例风险）
DSH_TELEMETRY_DISABLED=1 dsh plugin --profile multica add /abs/path/<bundle>-<ver>.tgz
dsh plugin --profile multica list

# 4. 模型 provider
#    (a) DEAP：把 llm-pi-ai / agent-default-model 两行追加进
#        $DSH_HOME/profiles/multica/cordis.patch.yml（可选再加 `- id: llm-deepseek\n  disabled: true`），
#        然后在 DAEMON 的环境里导出：
export OPENAI_BASE_URL='https://<deap-gateway>/v1' OPENAI_API_KEY='<key>' OPENAI_MODEL='qwen3-coder-plus'
#    (b) DeepSeek 官方 key：export DEEPSEEK_API_KEY='<key>'（无需 patch）

# 5. 先验 DSH 侧（不调用模型）
DSH_TELEMETRY_DISABLED=1 dsh --profile multica --probe
DSH_TELEMETRY_DISABLED=1 dsh --profile multica --list-models       # 期望 deap/<model>(default) 或 deepseek-official/*
DSH_TELEMETRY_DISABLED=1 dsh --profile multica --dump-config | grep -n 'llm-pi-ai' -A8

# 6. daemon 环境 + 探测
export MULTICA_DSH_PATH="$HOME/.multica-dsh/node_modules/.bin/dsh"
export MULTICA_DSH_MODEL='deap/qwen3-coder-plus'          # 必须是 --list-models 给出的完整 provider/model
export DSH_TELEMETRY_DISABLED=1 DSH_TELEMETRY_MODE=DISABLED
multica daemon probe-runtimes                              # 期望 "dsh":1（必须用 FORK 二进制验收）
multica daemon restart && multica daemon logs
```

DEAP 的 patch 片段（实测 `--list-models` 输出 `deap/qwen3-coder-plus | DEAP | default`，用 `http://127.0.0.1:9/v1` 作 baseURL 证明**没有发生网络往返**、密钥不落盘）**[V!]**：

```yaml
- id: agent-default-model
  config: { provider: deap, model: !!js process.env.OPENAI_MODEL }
- id: llm-pi-ai
  config:
    providers:
      deap:
        displayName: DEAP
        apiKeyEnv: OPENAI_API_KEY
        api: openai-completions
        baseURL: !!js process.env.OPENAI_BASE_URL
        defaultInput: [text, image]
        headers: { X-Multica-Provider-Generation: !!js process.env.MULTICA_A2A_PROVIDER_GENERATION ?? 'local' }
        models: [{ id: !!js process.env.OPENAI_MODEL, name: !!js process.env.OPENAI_MODEL }]
```

**计价现成可用 [V!]**：`ReportTaskUsage` → `pricing.ResolveModel` 会剥掉 `provider/` 前缀，所以 `deap/qwen3-coder-plus` 能落到 Diamond 目录里的 `qwen3-coder-plus`；DEAP 已在 `dt-fde-multica-model-pricing.json` 的模型无需任何新增。唯一小瑕疵：静态表里锚定的 `^deepseek-chat$` 匹配不上 `deepseek-official/deepseek-chat`，真要用就加进 Diamond 或把正则放宽成 `(^|/)deepseek-chat$`。注意 `docs/runtime-config.md:89-103` 的 price-first 规则：先进定价文档，再进 `runtime.llm.models`。

### 7.2 Linux（自建 Linux 开发机 / 长驻 daemon 宿主）

```bash
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy
curl -fsSLo /tmp/node.tar.xz https://cdn.npmmirror.com/binaries/node/v22.19.0/node-v22.19.0-linux-x64.tar.xz
sudo tar -xJf /tmp/node.tar.xz -C /opt && sudo ln -sfn /opt/node-v22.19.0-linux-x64 /opt/node
export PATH=/opt/node/bin:$PATH
COREPACK_NPM_REGISTRY=https://registry.anpm.alibaba-inc.com corepack install --global pnpm@10.28.2 && corepack enable
# dsh + bundle 同 7.1 的 2-3 步（node-pty 与 landlock 启动器都有 linux-x64/arm64 预编译，anpm 已镜像 [V!]）
# 沙箱三选一：apt/yum install bubblewrap ；或依赖 Landlock（内核 >= 5.13 且 seccomp 放行 landlock_*）；
#            或在容器确实无法沙箱时 export DSH_PERMISSION_MODE=danger-full-access（FC 镜像就是这么做的）
# 以非登录服务方式跑 daemon，环境块同 7.1 第 6 步，DSH_HOME 放持久卷
```

### 7.3 Aone 托管

**结论：这是一个新的部署单元，不是配置开关** **[V!]**。`src/main.sh` 只起三个长驻进程（`bin/server`、`node apps/web/server.js`、health server）外加一次性 `bin/migrate up`；`APP-META/` 与 antx 配置都加不了第四个进程，`RUNTIME_CONFIG_KEYS` 里也没有 daemon/server-URL 相关键。

**[V! 修正]** 但容器比报告说的更接近就绪：daemon 二进制**已经在镜像里且在 PATH 上**（`Dockerfile:27` 构建 `./cmd/multica` → `server/bin/multica`，`:116` 拷入，`:84` 入 PATH），并且它有完全 headless 的 `daemon run-once`（靠 `MULTICA_SERVER_URL` / `MULTICA_RUNTIME_ID` / `MULTICA_DAEMON_TOKEN` 驱动）；base 镜像 `alios8-nodejs:2.0` 自带真实 node 与 npm（`Dockerfile:105` 的 `ln -sf $(command -v node)` 只是 Aone 路径约定），还带 git。**真正缺的是**：pnpm（`dsh plugin add` 要 shell out）、`dsh` CLI 本体、Multica bundle、daemon 凭据，以及 node 版本没有对着 DSH 的 `^22.19 || >=24` 底线校验过。

**两条路 [I]**：
- **（推荐）复用 runner 镜像**：`multica-fc-hermes-runtime` 已经在 `/opt/multica-dsh` 装好 dsh + 补丁 + node-pty，把它以 local 模式跑 `multica daemon start` 即可，省掉重建镜像。
- 或新建一个专用 daemon 镜像（Node/pnpm/dsh/bundle 烘焙进去 + 服务令牌），profile 目录与 `DSH_HOME` 放持久卷。

无论哪条，**都不能把 profile / `node_modules` / `settings.yaml` / `.credentials.yaml` / sessions 当作跨副本的权威状态** —— 它们是节点本地缓存，权威态必须在 PostgreSQL（仓库硬规则）。

### 7.4 今天挡路的 11 项（fork develop 75c0a055f）

| # | 缺口 | 证据 | 修复量 |
| --- | --- | --- | --- |
| G1 | 无原生 backend：`server/pkg/agent/dsh.go` 缺失、`SupportedTypes` 无 dsh（`agent.go:264-305`）、`models.go` 无 dsh 发现分支、`agents_probe.go` 无 `MULTICA_DSH_PATH` 探测 | **[V!]** | 移植 `4f10a944b`（+ b 档进程树）：`dsh.go` ~476 行 + 3 个测试文件 |
| G2 | `runtime_profile.protocol_family` CHECK 无 `dsh`（最新是 254） | **[V!]** | fork 自有幂等 `9128`（与 `SupportedTypes` 同提交） |
| G3 | ~~`applyProviderSessionContract` 清空所有 dsh 的 `PriorSessionID`~~ | **[争议→refuted]**：唯一生产调用已按 cloud 收敛，本地 resume 未受影响 **[V!]** | **不需要修**；补一条 `RuntimeMode:"local"` 保留 `PriorSessionID` 的回归测试即可 |
| G4 | `scripts/agent-cli-command-names.txt` 无 `dsh` | **[V]** | `4f10a944b` 自带该改动 **[V!]** |
| G5 | `execenv/context.go` 的 skills 分支与 `local_skills.go` 的 provider 根缺 dsh | **[V]** | 两个小 case（`{workDir}/.dsh/skills`、`$DSH_HOME/skills`） |
| G6 | `dshLaunchArgs()` 硬编码 `--profile multica --stdio`，无 `--patch` 钩子；自定义 runtime profile 选不了别的 profile | **[V!]** | c 档：统一 argv planner（§5.4 Phase 2） |
| G7 | bundle 的 `cordis.patch.yml` disable 了不存在的行 `telemetry-otel` | **[V!]** | fork 副本改成 `session-telemetry-otel`；daemon 同时导出 `DSH_TELEMETRY_DISABLED=1` |
| G8 | bundle 没有内网分发（上游 `private: true`，npm 404） | **[V!]** | §4.4：内网仓库 + anpm 私有 scope + tarball SHA 纪律 |
| G9 | 本地路径没有轨迹上传 | **[V]** | daemon 侧 PUT（~80 行 Go，**服务端零改动**，但要满足 §5.4 Phase 2 列的全部前置条件） |
| G10 | Aone 应用容器没有 daemon / pnpm | **[V!]**（含修正） | 新部署单元（§7.3） |
| G11 | 版本漂移：镜像 pin 0.1.1-rc.2、镜像源 latest 是 0.1.2-rc.1、GitHub 已有 0.1.3-alpha.1（破坏性 session API） | **[V!]** | 镜像与 daemon 统一 pin 一个版本；每次升版重跑 §4.1 的漂移检查（typecheck/test/probe/stdio） |

**默认测试的隔离要求**（CLAUDE.md 硬规则，实施时必须遵守 **[V]**）：默认测试绝不能解析或执行用户已安装的 Agent CLI；backend fixture 传 `t.TempDir()` 造的可执行文件路径或测试自建的不存在路径；`ProbeAgentCLIs` 测试要设 `PATH=fakeDir` 并 stub 登录 shell 解析、清缓存；`pinNonCodexAgentsToMissingPaths` 要加 `MULTICA_DSH_PATH`。真实 smoke 走 `-tags=agentintegration` + `MULTICA_RUN_REAL_AGENT_SMOKE=1` 单独授权：

```bash
(cd server && MULTICA_RUN_REAL_AGENT_SMOKE=1 MULTICA_DSH_PATH=/abs/dsh DEEPSEEK_API_KEY=… \
  go test -tags=agentintegration ./pkg/agent -run 'TestDshRealRuntimeSmoke' -count=1 -v)
```

---

## 8. 风险与未决问题

### 8.1 必须在实施前拍板的（决策项）

| # | 问题 | 现状 / 证据 | 建议 |
| --- | --- | --- | --- |
| R1 | **bundle 许可**：无 LICENSE、`UNLICENSED`、`private`，上游 LICENSE 不覆盖，且维护者的发布 PR 仍保留 UNLICENSED —— **发布到 npm 也不解除限制** **[V!]** | §4.4 | 三选一并立刻启动；在拿到明确授权前**不得** vendor 进内网仓库或烘焙进对外分发镜像 |
| R2 | **capability 下发通道** | **[争议→refuted]** 原报告称只有"伪 provider"或"schema v2"两条路且都不理想；核验给出至少三条更安全的路 **[V!]** | 首选复用 `fc_e2b_stable_release.manifest` 的 `capabilities_by_backend`；次选新开一个 Diamond 数据 ID；**不要**在现有 Diamond 文档上做 v2（滚动期老副本会保留旧快照，新模板"没有 provider"） |
| R3 | **`web_fetch` 默认打开**（0.1.2 起）——模型在沙箱里可访问公网 URL | **[V!]** | 明确网络姿态：要么 patch 里 `{"id":"tool-web","config":{"fetch":false,"searchTimeoutMs":60000}}`（整块替换，两个 key 都写），要么写进文档并接受 |
| R4 | **telemetry 默认值翻转**为 `FEEDBACK_ONLY` | **[V!]** | adapter/daemon **同时**设 `DSH_TELEMETRY_MODE=DISABLED` 与 `DSH_TELEMETRY_DISABLED=1`；fork 副本 bundle 修正行 id |
| R5 | **上传入口范围偏宽**：`dsh_trajectory.go:210-214` 只看 `runtime.Provider == "dsh"`，既不看 `runtime_mode` 也不看 `dsh_trajectory_v1` capability **[V!]** | 目前是**潜在**问题（fork 还注册不了 local dsh runtime） | 移植落地的同一批里收窄成 `service.CloudSandboxRuntimeProvider(runtime)=="dsh"`；原生 exporter 另设格式与授权契约 |
| R6 | **`9064_agent_task_dsh_trajectory` 的外键级联**：`task_id UUID PRIMARY KEY REFERENCES agent_task_queue(id) ON DELETE CASCADE`，违反 CLAUDE.md 两条（禁 `REFERENCES`、禁级联删除）**[V!]**。溯源：由 `0de291676`（xumo，2026-08-14）以 `9062` 引入，后改号为 `9064` 并在 `cmd/migrate/main.go:204` 留了 alias；此后无迁移删除该约束；该规则没有全局 lint | 既有欠债，与版本升级无关 | 在 DSH 这批工作里一并修：新增幂等 `9xxx`，`DROP CONSTRAINT IF EXISTS`，把清理逻辑改成应用层事务 |
| R7 | **`SupportedTypes` 与迁移必须同提交** | `agent_supported_types_test.go:36-53` 按数量+成员钉死 **[V!]**（与 port-plan §5 的"独立第二提交"矛盾） | 采纳"同提交"；不要为让测试过而改那条守卫 |
| R8 | **Phase 3 表命名** | **[争议→采纳修正]**：照抄上游 `plugin_*` stem 会在下次同步被 `285_plugin_lifecycle_v1`（无 `IF NOT EXISTS`）硬失败，而 344 对 `plugin_installation` 是先 `DROP TABLE IF EXISTS` → **静默销毁 fork 数据** **[V!]** | 用 fork 自有名 `dsh_plugin_*`，9000+ 段，`IF NOT EXISTS`，唯一索引单独文件 CONCURRENTLY |
| R9 | **是否要把 `DSH_` 加进 `isBlockedEnvKey`** | 目前 Agent 的 `custom_env` 可以设 `DSH_*` **[V!]**，能改沙箱模式、telemetry、DSH_HOME | 若工作区管理员不完全可信，建议加入阻断列表（同时保证 daemon 生成的 `MULTICA_DSH_SESSION_ROOT` 不可被覆盖） |

### 8.2 实施期最容易踩的（工程陷阱）

| # | 陷阱 | 证据 |
| --- | --- | --- |
| T1 | **A2A env scrub 跑两次**（`daemon.go:6272`、`:6344`），会把所有 `MULTICA_` 前缀键**置空**；插件集 env 写早了就会"测试通过、生产消失"。**[V! 修正]** 只影响 `task.A2AInvocation == true` 的任务；写在 `:6344` 之后即可，不必写进 `configureManagedA2AV2ProviderEnv`（那儿还有 `A2AManagedRuntimeV2` 门禁会漏掉非 A2A 云任务和全部本地任务） | claim 34 |
| T2 | `go test ./internal/handler` 在 Postgres 不可达时打印 `ok` 但**零测试执行**；必须在 `-v` 里确认 `--- PASS` | **[V]** |
| T3 | 全新数据库死在 `271_task_completion_canceled_status`（它 ALTER 的表由 `9025` 创建）；先手工跑 9025 | **[V]** |
| T4 | 本机 git 消息是中文，`grep CONFLICT` 会静默返回 0 | **[V]** |
| T5 | 本机 `multica 0.4.33` 是 **upstream** 构建的二进制（认识 zeroclaw/dsh），**不能用它来验收 fork 的探测**；必须 `make cli` | **[V!]** |
| T6 | `dsh plugin add` 一个 checkout 目录（`link:`）会引入 rc.6 的双实例（MUL-6186）；**只用 tarball** | **[V!]** |
| T7 | `--dump-config` **不能当预检**：patch 里写了目录路径的行会让真实启动整体中止（`ERR_UNSUPPORTED_DIR_IMPORT`），而 dump 仍 exit 0 | **[V!]** |
| T8 | patch 是**整块替换 config**，不是深合并；`name` 不匹配会**跳过整条 patch**；带 `insert` 时同条目其他 key 被忽略 | **[V!]** |
| T9 | `minimumReleaseAge` 是传递+相对时间的，7 天窗口会因为**已装依赖太新**而拒掉整份 lockfile | **[V!]** |
| T10 | lockfile 验收数字要按平台写：583 lock entries，**Linux 构建机 526 installed**（macOS 是 523） | **[V!]** |
| T11 | 三个 DSH 测试不在 Aone CI 白名单里；构建期的 SHA/版本/node-pty/bridge 冒烟是目前唯一自动门禁 | **[V!]** |
| T12 | anpm 落后 npmmirror 至少一天，且 **anpm 没有 `/-/v1/search`（404）** | **[V!]** |
| T13 | `agent_runtime.metadata` 在每次 daemon 注册时被整份替换 —— 任何写进去的 UI 状态都会丢 | **[V!]** |
| T14 | `client.ts` 里 `listFCE2BTemplates` 与 4 个 local-skills 方法**今天就在裸 cast**，不要照抄；新端点必须 `parseWithFallback` + malformed 测试 | **[V!]** |

### 8.3 未决（需要外部信息或后续观察）

1. **DSH 0.1.3 的落地时间与形态**：release notes 已宣布 session 格式 v2（不可变的相邻代迁移）、`SessionHandle`、`agentLoop.create()` 异步 + session 锁。核验补充：DSH master 已经**去掉了 `packChunks` 配置项**，并把 session 写成 `session.vN.jsonl[.zstd]` —— 这会同时打断**文件名 glob** 与服务端 `version == 0` 校验，且与 compression 设置无关 **[V!]**。→ 每次升版都要预算一次 validator/viewer/projector 修订；升级前先跑 bundle 的 `pnpm typecheck && pnpm test && dsh --profile multica --probe`。
2. **catalog ↔ 模板漂移**：钉在旧 candidate 模板上的 runtime 不会有后加的插件。靠"已选择但不可用"分区 + 启动期 fail-closed 校验把它变成可见状态（照 `daemon.go:1929-1948` 的 Pi-MCP 拒绝方式）。
3. **是否让云路径也切到 native stdio 协议**（用 bundle 取代 875 行 Python adapter）：收益明确（逐步 `text`/`thinking`/`usage`/`result{status,stop_reason,error.code}`、取消时能 flush 后上传轨迹、模型与 thinking 发现、本地与云一套协议、MCP 密钥走 stdin）；代价也明确（要先有本地 backend；镜像要烘焙 profile；轨迹上传要挪进 bundle 或 daemon；A2A 的 DWS token 转发与 MCP 就绪屏障要在 bundle 里重做；bundle 最后验证过的 DSH 还是 0.1.0-rc.6）**[V]**。bundle 报告倾向"只留一套集成"，**本文建议先不做**：等本地 backend 与 0.1.3 的破坏性变更落定后再评估，否则会在一个快速变动的协议上同时开两条战线。
4. **上游 issue #6936**（请求发布 `@multica-ai/dsh-runtime`）的进展，直接决定 R1 走哪条出口。
5. **`agent_supported_types_test.go` 之外，是否还有别的"代码-迁移锁步"守卫**：实施时应跑一次 `./internal/migrations` 与 `./pkg/agent` 全量确认。
6. **verdicts.json 中一条已过期的记录**：claim 84/86 断言"port-plan 报告尚未产出"，那是核验当时的快照；`reports/port-plan.md`（493 行）现已存在，本文已据其正文引用。

---

## 9. 分阶段实施计划

> 每个阶段末尾都有一条硬性动作：**提交前 Codex review**（`/codex:rescue` 或等效的第二实现/诊断通道），对该阶段的 diff 做独立审查后再提交。

### Phase 0 — 钉输入与解锁（0.5–1 周，两条线并行）

**交付物**
- `@deepseek-ai/dsh` 版本决议书：全线锁 `0.1.2-rc.1`，子包**显式 pin**（不用 `latest`）。
- bundle 出口决议（授权 / 自研 / fork MIT 包三选一）+ 内网仓库骨架 + Aone CI（typecheck / vitest / build / pack / 发布 anpm 私有 scope）。
- 一份"漂移检查清单"脚本化：`pnpm typecheck && pnpm test && dsh --profile multica --probe && dsh --profile multica --list-models && 一次 --stdio 跑通`。
- 确认当时空闲的 fork 迁移号并预留。

**验证命令**
```bash
npm view @deepseek-ai/dsh dist-tags --registry https://registry.anpm.alibaba-inc.com
dsh --version && DSH_TELEMETRY_DISABLED=1 dsh --profile multica --probe
cd <bundle-repo> && pnpm typecheck && pnpm test && pnpm build && pnpm pack
```

**边界**：不改 fork 仓库、不建镜像、不碰数据库。
**➜ 提交前 Codex review**：审 bundle 副本的 `cordis.patch.yml` 三处修正（`session-telemetry-otel`、`compression: none, packChunks: false`、`patchReload: startup`）与许可归属说明。

---

### Phase 1 — 云镜像升级到 0.1.2-rc.1（1 周，可与 Phase 2 并行）

**交付物**：§4.2 的必做 4 项 + 建议 5–8 项；三个 DSH 测试进 CI 白名单；一次 candidate 镜像 + canary。

**验证命令**
```bash
# 镜像仓库本地（这三个测试不在 CI 里）
python3 -m unittest tests/multica_dsh_test.py tests/dsh_install_contract_test.py tests/dsh_dws_identity_patch_test.py
# 摘要重导（必须跟随 302）
curl -fsSL https://registry.npmmirror.com/@deepseek-ai/dsh/-/dsh-0.1.2-rc.1.tgz | sha256sum
# lockfile 重生成后
cd dsh-runtime && npm ci --omit=dev --ignore-scripts   # Linux 构建机应报 526 个包
# 构建期离线守卫
DSH_HOME=$(mktemp -d) DSH_TELEMETRY_DISABLED=1 dsh --profile headless --help >/dev/null
# 流水线
python3 .agents/skills/fc-runtime-dev-loop/scripts/fc_runtime_dev.py doctor --profile pre-fde
python3 .agents/skills/fc-runtime-dev-loop/scripts/fc_runtime_dev.py cutover --runtime-ref <branch> \
  --runtime-commit <40> --multica-ref <40> --provider dsh --visibility private --profile pre-fde
```
**canary 四件套**：普通 DSH 任务（看 `dsh_trajectory_available` 与查看器）、A2A inbound 任务（走 `MULTICA_DEAP_DWS_TOKEN`）、带托管远程 MCP 的任务、故意失败的任务（验证新的错误提取不再吐推理）。

**边界**：manifest version 保持 7、provider/capability 标签不变 → 指纹不变 → **不改服务端、不改 Diamond**。
**➜ 提交前 Codex review**：重点审 stderr 错误提取的正则锚点与 `redact_dsh_error` 的交互、以及 `tool-web` 网络姿态决策。

---

### Phase 2 — 本地原生 DSH backend（b 档，1.5–2 周）

**交付物**：§3.7 Step 1–6 全部；4 个提交切分——
1. `4f10a944b` 的 DSH 增量 + fork 进程签名适配 + 显示名冲突解决 + **`SupportedTypes` 与 9128 同提交**；
2. 云/本地边界回归（session contract 表驱动测试、轨迹上传 gate 收窄、A2A inbound 拒绝 local dsh、云 run-once 仍映射 opencode）；
3. b 档 DSH 专用进程树 ownership（含 Windows helper 测试）；
4. 四语文档与安装说明（**改写上游的可安装性表述**）。

**验证命令**
```bash
cd server && gofmt -l ./pkg/agent ./internal/daemon && go vet ./pkg/agent/... ./internal/daemon/... && go build ./...
../scripts/go-test-with-agent-cli-guard.sh -- go test -count=1 ./pkg/agent -run 'Dsh|SupportedTypes|Thinking|ProcessGroup|Cancel' -race -v
../scripts/go-test-with-agent-cli-guard.sh -- go test -count=1 ./internal/daemon -run 'Dsh|DSH|RuntimeProfile|ModelList|AgentCLIGuard' -v
cd server && go test ./internal/handler -run 'DSH|Dsh|SessionContract|Trajectory|AgentA2A' -count=1 -v   # 看 --- PASS
cd server && go test ./internal/migrations -count=1 -v && go run ./cmd/migrate up
pnpm typecheck && pnpm test && make test && make check
make daemon && make cli MULTICA_ARGS="daemon probe-runtimes"     # 期望 "dsh":1
```
**真实验收**：两轮 session resume（第一轮埋 nonce、第二轮只给 `resume_session_id`）、中途 cancel 无孤儿、stdio MCP、`.dsh/skills`、**外加一次云 DSH 回归**。三组兼容矩阵（新 server+旧 daemon、新 server+新 daemon、新 server+现网云镜像）。

**边界**：不移植 dim/mcode/zeroclaw/codearts；不做全 provider 的 LaunchPrefix/进程树重构；不做自定义 DSH profile（c 档）；不动云 adapter 与轨迹格式。
**➜ 提交前 Codex review**：重点审 `dsh.go` 的取消/超时资源释放路径、`prepareDshTaskSessionRoot` 的插入位置（必须在 custom_env 之后）、9128 的 up/down 幂等性。

---

### Phase 3 — DSH 插件动态组装（Phase 1 设计，2–3 周）

**交付物**
- 镜像侧：`dsh-runtime/catalog/`（真实 profile：`package.json` 精确 pin + `pnpm-lock.yaml` + `pnpm-workspace.yaml`（**无 `allowBuilds`**，导入路径带 `minimumReleaseAge`）+ `cordis.patch.yml` + `catalog.json`）；Dockerfile 里 `pnpm fetch` + `--frozen-lockfile --ignore-scripts --offline` 安装、`--dump-config` 断言每个 catalog bundle 都被组合、**构建期真实启动一次把 `profiles/node_modules` fallback heal 出来并断言 `@deepseek-ai/dsh-tools` 存在**、目录只读 + label `com.multica.runtime.dsh.catalog.sha256`。
- adapter：读 `MULTICA_DSH_PLUGIN_SET` / `MULTICA_DSH_CATALOG_SHA256`，校 sha（不符 fail closed），`write_patch` 追加每插件的 `{id, config}`（先与目录默认 deep-merge）与 `{id, disabled:true}`，密钥走 `__jsExpr`。
- 服务端：`pkg/dshcatalog`（embed + 校验 + sha）、3 个 REST 端点（zod + `parseWithFallback` + malformed 测试）、capability 派生（按 R2 选定通道）、启动期"选择 ⊆ 目录"的 fail-closed 校验。
- daemon：`dsh_runtime_config.go` + env 注入（**写在第二次 A2A scrub 之后**）。
- 前端：`dsh-plugins-tab.tsx` + `agent-overview-pane.tsx` 五处小 diff + `dsh-plugin-catalog-section.tsx` + 一行挂载 + 四语 i18n。
- 首批 catalog：`dsh-mcp-lens@0.1.0-rc.9`、`dsh-model-router@0.6.2`。

**验证命令**
```bash
pnpm typecheck && pnpm test && pnpm lint
cd server && go test ./pkg/dshcatalog ./internal/handler -run 'DshPlugin|Catalog' -count=1 -v
cd server && go test ./internal/daemon -run 'DshRuntimeConfig|A2A' -count=1 -v      # 必含 "MULTICA_DSH_PLUGIN_SET 挺过两次 scrub" 用例
cd server && go test ./internal/service -run 'FCE2B|Capabilit' -count=1 -v
# 镜像侧
DSH_HOME=/opt/multica-dsh-home dsh --profile catalog --dump-config | grep -c '^# == '
```
**边界**：Phase 3 **零迁移**；不做 runtime/工作区默认集（要做也**不能**写 `agent_runtime.metadata`）；不做工作区导入；不暴露排序 UI（catalog 顺序即权威、确定且 CI 可验证）。
**➜ 提交前 Codex review**：重点审 env 注入点、sha 校验的 fail-closed 路径、config deep-merge 与"整块替换"语义的对齐、密钥不出现在任何响应/WS 广播里。

---

### Phase 3.5（可选）— c 档：自定义 DSH profile

统一 argv planner（`dsh_command.go` + 测试）、`Config.LaunchPrefix`（**只对 DSH 传**）、模型目录缓存 key 编码 prefix 与 `DSH_HOME`、thinking 校验走同一入口、`appendProfileRuntimes` 用真实 `fixed_args` 探测。**➜ 提交前 Codex review**。

### Phase 4（可选）— 工作区导入开源插件

按 §5.4 Phase 3：fork 自有表名、OSS 存 tarball、服务端解析 + 审核 + consent、回流镜像 catalog。**➜ 提交前 Codex review**。

---

## 10. 证据索引

### 10.1 报告与核验产物

| 内容 | 路径 |
| --- | --- |
| 共享 brief | `scratchpad/dsh-brief.md` |
| 移植计划（Codex，中文，493 行） | `scratchpad/reports/port-plan.md` |
| 云镜像与 0.1.2 升级（249 行） | `scratchpad/reports/cloud-image.md` |
| bundle 审计（294 行） | `scratchpad/reports/bundle.md` |
| 本地 daemon 落地（401 行） | `scratchpad/reports/local-daemon.md` |
| 插件组装（254 行） | `scratchpad/reports/plugin-assembly.md` |
| 合并策略（630 行） | `scratchpad/reports/merge-strategy.md` |
| DSH 自带 Web UI（304 行） | `scratchpad/reports/gui-dsh-web.md` |
| 上游 Plugins tab（320 行） | `scratchpad/reports/gui-upstream-plugins-tab.md` |
| fork GUI/API 触点（387 行） | `scratchpad/reports/gui-fork-ui.md` |
| 生态/市场调研（161 行） | `scratchpad/reports/gui-ecosystem.md` |
| 160 条对抗性核验 | `scratchpad/verdicts.json`（87 claims：77 confirmed / 5 disputed / 5 refuted） |
| 本次整理的争议摘录 | `scratchpad/verdict-contested.txt`、`scratchpad/verdict-nuance.txt`、`scratchpad/verdict-index.txt` |
| 移植用补丁与合并预览 | `scratchpad/dsh-port-base.patch`（`git show 4f10a944b`，1458 行/30 paths）、`dsh-port-{46b5d9e6d,3b0842368,e3ec3f8b5,…}.patch`、`dsh-port-daemon-merge-preview.txt` |
| 全量 sync 冲突原始输出 | `scratchpad/merge-tree-full.txt`、`conflicts-grouped.txt`、`conflict-hunks-raw.txt`、`upstream-new-stems.txt` |
| bundle 构建与组合实验 | `scratchpad/bundle-build-{typecheck,test}.log`、`dump-config.{multica,layered,layered2}.yml`、`run-stdio.sh`、`stdio.multica.stdout/stderr`、`multica-ai-dsh-runtime-0.1.0-private.1.tgz` |
| 本地 daemon 实验 home | `scratchpad/local-dsh-home/`（含 `deap-overlay.patch.yml`、`multica-dump-config.yml`、`headless-dump-config.yml`） |
| npm/镜像分析 | `scratchpad/npm-inspect/cloud-image/{analysis.txt,analysis2.txt,regen-lock.log,tarball-hash.sh,home-*/dump.yml}`、`npm-inspect/experiments{,2,3,4}.sh` + `experiments.log` |
| 上游插件系统抽取 | `scratchpad/upstream-plugin/`、`scratchpad/reports/plugin-v2-files.txt`、`reports/mt-<sha>.txt` |

### 10.2 关键代码坐标（fork，除非注明 upstream/IMG）

**服务端 · DSH 云路径**：`server/internal/service/fc_e2b.go:55,390,469-502,515-527,817-824,1224-1248,2700-2740`；`server/pkg/runtimeconfig/runtime_providers.go:22-30,39-63,104-106,123-152`；`server/internal/handler/dsh_trajectory.go:49-109,184-214,210-214,276-305`；`server/internal/handler/daemon.go:2980,2988,3037-3041,5113-5131`；`server/internal/handler/agent_a2a_config.go:1040-1066`；`server/internal/service/cloud_sandbox.go:73-76,186-192`；`server/cmd/server/router.go:1902-1913,2006-2027,2468-2481,2557-2597`。

**服务端 · 移植目标**：`server/pkg/agent/agent.go:242-261,264-305`；`proc_other.go:35,44`；`proc_windows.go:36-56`；`server/pkg/agent/models.go:190-196,399-403`；`thinking.go:671,807-850`；`server/internal/daemon/agents_probe.go:152-249`；`server/internal/daemon/daemon.go:2461-2545,5235,6272,6335-6361,6344-6348,6367-6377,6394-6397,7838-7862,8085-8105,8298-8312`；`server/internal/daemon/execenv/context.go:222,426+,482-489`；`runtime_config.go:173,216-219`；`local_skills.go:137-208`；`server/internal/metrics/labels.go`、`pricing.go:53-54,108-109`；`server/pkg/modelpricing/catalog.go:60-78`；`server/migrations/254_runtime_profile_add_reasonix.up.sql:3-27`；`9064_agent_task_dsh_trajectory.up.sql:2`；`server/internal/migrations/migrations_lint_test.go:15,75-109`；`server/cmd/migrate/main.go:67-73,133-142,202-204,481-509`；`server/internal/deploymentfence/fence.go:167-199,343-402`；`src/main.sh:322-326,417-436,472-483`。

**前端**：`packages/core/runtimes/cloud-runtime.ts:41-49,120-133,328-392,429-448`；`packages/core/agents/mcp-support.ts:8-41`；`packages/core/types/agent.ts:108-132,432-560`；`packages/core/api/schema.ts:41-55`；`client.ts:2005-2007,2768-2799`；`packages/views/agents/components/agent-overview-pane.tsx:57-118,248-302,548-641`；`tabs/{mcp-config,skills,runtime-config,agent-mcp}-tab.tsx`；`packages/views/runtimes/components/{runtime-detail.tsx:243-256,561-676, asb-runtime-credential-section.tsx, update-fc-e2b-runtime-template-dialog.tsx:231-236, runtime-profiles-dialog.tsx}`；`packages/views/common/task-trajectory/{dsh-trajectory-button.tsx:59,trajectory-model.ts:23,35-51}`；`packages/views/locales/parity.test.ts:55-75`。

**镜像（IMG = multica-fc-hermes-runtime）**：`Dockerfile:62,81-83,123-124,527-554,601-618,635,671`；`scripts/multica-dsh:29-30,54-149,197-205,207-311,324-382,385-462,511-687,730-871`；`scripts/multica-fc-hermes-runner:182-188,865-869,1090-1105`；`dsh-runtime/{patch-terminal-bash-dws-identity.py,multica-mcp-bridge.mjs,multica-mcp-bridge-smoke-test.mjs,package-lock.json}`；`scripts/runtime_template_name.py`；`scripts/runtime-smoke-test:30,73,180,187,191,269-272`；`.aoneci/{runtime-master-release.yaml:28-34,runtime-asb-release.yaml,runtime-a2a-providers-fc-candidate.yaml}`；`README.md:89,189,293,495`；`CLAUDE.md:58,163-169,289,330+`。

**upstream**：`server/pkg/agent/dsh.go:16-21,116-118,120-144,146-200,223-297,371-476`；`server/internal/daemon/agents_probe.go:225-230,309-330`；`daemon.go:7982-7988,9718-9740`；`local_skills.go:182-187`；`execenv/context.go:392-395`；`server/migrations/{313,344,362,366,367,382,392-397,441}_*.sql`；`server/pkg/plugincontract/{manifest.go:74-77,356-387,686-705, bundle.go:27-47,199-306, capabilities.go:28-58}`；`server/internal/service/{plugin.go:263-296,331-471,490-504,541-631,697-766, plugin_package.go:105-173,336-412}`；`packages/views/settings/components/plugins-tab.tsx`（721 行）；`packages/core/types/plugin.ts:57-81,103-155`。

**bundle（`dsh-multica-runtime` e29aae2）**：`package.json:80`（`UNLICENSED`）、`cordis.patch.yml:24`、`src/index.ts:38,61-67,85-87,89-134,136-153,155-176,205-268,277-305,317-320,335-349,357-358,422-428,465-468`、`src/protocol.ts:5,65-120,274-293`、`src/environment.ts:42-126`、`README.md:21-22,33-46`。

**DSH 0.1.2-rc.1（安装树内）**：`dsh/lib/{bin.js,plugin-F7ZVfRyo.js,profile-boot-*.js:121-147,186-211,264-288}`；`dsh-app-boot/lib/index.js:59-108,285-398,640-720,798-870,950-990,1067,1126-1170,1136-1144`；`dsh-base/cordis.patch.yml`；`dsh-headless/lib/{index.js:58-165,startup.js}`；`dsh-session/lib/*`（`SESSION_FORMAT_VERSION=0`）；`dsh-session-persistence-jsonl/lib/types/{index.d.ts:25,33,35,format.d.ts}`；`dsh-llm-pi-ai/lib/index.js:644,716-728,942-976,1646-1652,1967,2070-2184`；`dsh-llm-deepseek/lib/index.js:1823-1888`；`dsh-mcp-client/lib/{index.js:35-48,120,types/index.d.ts}`；`dsh-subprocess/lib/index.js:31-49`；`dsh-skill-filesystem/lib/index.js:155-177`；`dsh-sandbox-local/lib/index.js:165-174`；`dsh-bash-sandbox/lib/index.js:146,179`；`dsh-host-plugin-inventory/lib/index.js`；`dsh-client-connection/lib/index.js:12,196-420`；`dsh-host-webserver/lib/index.js:141`；`dsh-web-app/lib/{startup.js,index.js:91,125}`。

### 10.3 关键外部 URL / 命令

- npm：`@deepseek-ai/dsh` dist-tags `alpha=0.1.2-alpha.5 / latest=next=0.1.2-rc.1`；`0.1.2-rc.1` tarball sha256 `ca370668053ad6d0ac325e919ef5f65de53de00b7bad78008e6fb422dfce3530`（sha1 `fef2130433…`，15248 B / 10 files）；`0.1.3-alpha.1` npm **404**，仅 GitHub release（published_at 2026-09-04T11:34:32Z，无 asset）。
- 第三方：`dsh-profile-multica@0.1.0`（MIT，2026-08-18，`caizhihaoczh/dsh-profile-multica`）；`dsh-mcp-lens@0.1.0-rc.9`（MIT）；`dsh-model-router@0.6.2`（MIT）；`@openma/deepseek-harness-acp@0.4.27`（unpackedSize 50,298,433 B）；`dshmarket@1.43.0`、`dsh-extension-hub@0.2.19`、`dsh-plugin@1.4.2`（市场客户端）。
- 目录站点：`github.com/0xsline/awesome-deepseek-harness`（CC0-1.0，996★）、`dsh-plugin.org`（8360/5637）、`deepseekharnessplugins.com`（13912/3828）——后两者的"verified"无保证价值。
- 上游：`multica-ai/multica` PR **#6923**（DSH 支持）、**#6940**（文档）、**#7522/#7531**（进程树）、**#7052**（fixed_args 前缀）；bundle 仓库 issue **#6936**（请求发布）、PR **#1**（准备发布但保留 UNLICENSED）。
- registry：发现走 `https://registry.npmmirror.com`（`/-/v1/search` 可用）；安装走 `https://registry.anpm.alibaba-inc.com`（**`/-/v1/search` 404**）；访问任何内网主机前先 `unset HTTP_PROXY HTTPS_PROXY ALL_PROXY`。
- 部署：`a1 cd-pipeline run 66 --app 342160 --cr-id <id>`（预发，一分支一 CR）。

---

### 附：本文与原始报告的分歧清单（供交叉阅读时对照）

| 分歧点 | 报告原说法 | 本文采纳 | 依据 |
| --- | --- | --- | --- |
| 本地 resume 是否被 `applyProviderSessionContract` 破坏 | local-daemon §0/G3 说被破坏、需按 cloud 收敛 | **未被破坏**，不需要修，只补回归测试 | refuted claim 57；confirmed 58/76 |
| `--patch` 能否装载插件 | plugin-assembly §1.8/§6 说不能，必须改 profile manifest | **能**（insert 行 + 已可解析的代码）；per-task manifest 是可选优化 | refuted claim 74（双 lens） |
| capability 下发是否只有两条不理想的路 | gui-fork-ui §1.7 | **至少三条更安全的路**，推荐复用 stable release manifest 的 `capabilities_by_backend` | refuted claim 36（双 lens） |
| `daemon.go` 是否是最热冲突文件 / 排序约束 | gui-upstream-plugins-tab §6.4 | 不是；排序理由改为功能性依赖 | refuted claim 45 |
| Phase 3 表名 | gui-upstream-plugins-tab §4.1 建议照抄上游 stem | **用 fork 自有名 `dsh_plugin_*`** | disputed claim 43（双 lens） |
| 全量 sync 的迁移冲突 | merge-strategy §3.2 "为零" | **非零**：`issue_origin_type_check` 会丢 `agent_mcp` 并让 367 硬失败 | disputed claim 65 |
| bundle 是否是唯一关键路径 | merge-strategy §0/§6 | 是**必须尽早启动的并行项**，但 MIT 替代包/公开源码构建可在数小时内解锁 probe；Go 移植才是更长的杆 | disputed claim 64（external） |
| 迁移可否独立成第二个提交 | port-plan §5 | **不可**，须与 `SupportedTypes` 同提交 | disputed claim 59（external） |
| `agent-cli-command-names.txt` 需额外加 dsh | merge-strategy §9 Step 1 | `4f10a944b` 自带该改动 | claim 64（external） |
| lockfile 验收数字 | cloud-image §4.1 "523 installed" | **Linux 构建机 526** | claim 14（双 lens） |
| stderr 尾切片是否"替换"了真错误 | cloud-image §3(h) | 通常是**稀释**（真错误仍在结尾）；只有非 `error` 收尾时才被完全替换 | claim 7（双 lens） |
| DSH 三个测试是否完全无门禁 | cloud-image §1.7 | CI 白名单确实没有，但构建期有 SHA/版本/node-pty/bridge 冒烟 | claim 8（双 lens） |
