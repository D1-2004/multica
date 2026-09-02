# FC Runtime / Daemon 兼容闭环 Review

> 状态：已完成
> 日期：2026-09-03
> dt-fde-multica 分支：`codex/fc-runtime-dev-loop`
> Runtime 分支：`codex/fc-runtime-dev-loop-20260903@8079938ac998ff481714a91c3b1e790de6f1796e`

## 结论

`fc-runtime-dev-loop` 已从“构建 Template 并修改 Runtime 配置”升级为“不可变来源 → 候选镜像 → Runtime 全字段回读 → FC 真任务 → 本地持久 Daemon 真任务 → 停止与清理”的可审计闭环。

真实验证得到两个必须分开报告的结论：

- FC 路径通过：新版 Runtime provenance 构建、candidate Runtime 切换和真实 Multica task 均成功。
- 本地 Daemon 功能路径通过，但相对已安装 0.4.33 的二进制兼容门禁未通过：候选少 `--workspaces-root`，并少 `zeroclaw` provider。不能把一次本地任务成功表述为“本地设备完全兼容”。

## FC 沙箱和本地 Daemon 的关系

两者共享 `handleTask/runTask` 任务执行核心，但生命周期不同：

```text
FC cloud Runtime
  -> 服务端创建/复用 E2B sandbox
  -> 每任务注入 mdt_ token + 精确 task/runtime ID
  -> root runner 降权到 uid 1000
  -> multica daemon run-once
  -> 精确认领、执行、complete、进程退出

本地 Runtime
  -> 真实设备长期运行 multica daemon start
  -> PAT 可见 Workspace 发现
  -> provider 探测和 runtime_mode=local 注册
  -> WebSocket/HTTP 心跳与 batch claim
  -> 共享 handleTask/runTask
  -> 更新、reload、GC、token renewal、stop/offline/清理
```

FC warm sandbox 复用的是文件系统和 provider proxy，不是上一次 Daemon 进程。FC 成功不能覆盖本地注册、设备 identity、心跳、滚动版本偏差和停机语义；本地成功也不能覆盖 E2B Template、runner、sandbox token 与 `run-once`。

## Review 发现与修复

### 完成语义

- 修复：create/switch 回读后不再返回笼统 `complete=true`，而是 `runtime_configured=true, complete=false, required_next_gate=multica_task_canary`。
- 修复：Runtime 回读覆盖 mode、backend、channel、provider、status、visibility、Template ID/ready，不再只看 provider/template。
- 修复：switch 保存 `previous_template_id`，canary 失败时有确定回滚目标。

### 构建来源与分支流水线

- Runtime branch、Runtime commit 和内嵌 Multica commit 分别校验；两个 commit 都必须是 40 位不可变 SHA。
- Aone run、结构化 step marker 和调用参数三方一致；缺 marker、重复冲突或来源不一致都阻断 mutation。
- Runtime 镜像内 CLI 的 `main.commit` 改为真实 `MULTICA_REF`，manifest 与 step log 同时输出 `runtime_commit`、`multica_commit` 和 provider fingerprint。
- 只允许 `.aoneci/*candidate*` 分支专用流水线；拒绝 master/formal/ASB 路径。

### API、Token 与重试

- Bearer Token 不进参数；远程 HTTP、URL userinfo/path/query/fragment 和 redirect 全部拒绝。
- profile 与环境中的 server/workspace 冲突时 fail closed；调用 a1 时剥离 `MULTICA_*` 并设置超时。
- create 用精确唯一名称作为 operation key：先读、唯一匹配复用、响应丢失后对账、多个同名或字段不匹配立即停止。响应丢失且 0 匹配时只能反复 `--reconcile-only`（无 POST），直到行出现或服务端权威证明原请求未提交。
- cutover 在耗费 CI 前先验证 switch 目标确实为可变更的 candidate FC Runtime。

### 本地设备兼容

- 新增 `daemon_compat.py compare`：比较正式/候选 provider 与 `daemon start` 公共 flag，breaking removal 默认失败。
- 新增 `verify-live`：验证精确 Daemon ID/版本、每个 PAT 可见 Workspace 都完成注册、provider 集合精确匹配、local/online、WS connected 和 heartbeat ACK。
- 新增 `verify-task`：按 Runtime/可选 task ID 验证 completed、时间/workdir 和 nonce；默认 exact，只有已知 provider 包装输出时显式使用 contains。
- 新增 `verify-stopped`：验证进程 stopped、Runtime 无 online，严格清理后可要求 ledger 全部不存在。
- 文档固定安全边界：profile.workspace_id 不限制 Daemon 注册；默认 daemon ID 跨 profile 共享；legacy IDs 可能迁移旧行；只测 Codex 时必须限制全部 provider 发现路径；清理只按 ledger 精确 ID，409 停止，禁止 cascade。

## 真实验证证据

### Runtime provenance 与 FC canary

- Aone pipeline：`295064`
- Run：`68463287`，`SUCCESS`
- Runtime commit：`8079938ac998ff481714a91c3b1e790de6f1796e`
- 内嵌 Multica commit：`2a46c86eef665eaecfbd59fa2e49762574cac079`
- Template：`hwctr5pd6ueefjcvc41y`
- display alias：`multica-m7-va2eb67817f146ef4-r1-807993`
- candidate Runtime：`f069b307-2bc1-4ab5-92ff-7fdd5bcf2114`
- 切换前 Template：`gzjr36hynucvy721p9qk`
- 切换后完整回读：cloud / hermes / aliyun_fc / candidate / online / private / ready，全字段匹配新 Template。
- FC task：`a6285476-e704-4ffb-9fcf-b22d53c8b6d5`，`completed`，Runtime 精确匹配，nonce `FC_PROVENANCE_CANARY_OK_68463287` 已验证。Hermes 将 nonce 包在完成摘要中，因此以显式 `marker_mode=contains` 通过；默认 exact 仍保持失败门禁。
- 临时 Issue 已 DELETE（204）；临时 Agent 已 archive。Archive 保留 Runtime binding，作为平台审计残留如实报告；未伪造 unbind。

### 真实 Mac 持久 Daemon

- 候选 binary：`dev-fc-runtime-daemon-compat`，commit `003d8242ffc8112a8ef9b504c9cfa492923b3e4c`。
- 隔离 profile、全新 `daemon_id`、临时 workspace root、`--no-auto-update --no-auto-reload`。
- PAT 实际可见两个 Workspace；Daemon 注册 8 条 local Runtime，全部 online，WS 连接与 heartbeat ACK 成功。
- 本地 Codex task：`f0f7b9cc-175d-4e02-8e39-a02f5a2ce258`，16 秒完成，精确输出 `LOCAL_DAEMON_COMPAT_OK_003d8242`，complete callback 已确认。
- 停止后 8 条 Runtime 全部 offline；临时 Issue、Runtime、profile 和 PAT 副本已清除，Agent 留作 archived audit record；默认 Daemon 未启动、未触碰。
- 功能结论：`local_daemon_verified=true`。
- 表面兼容结论：`compatible=false`，因为相对已安装 Multica 0.4.33 缺少 `--workspaces-root` 和 `zeroclaw`。临时 binary 目录已移动到废纸篓，可恢复。

## 验证

- Skill Python tests：22/22 通过。
- Go Daemon/CLI tests：`go test -count=1 ./internal/daemon ./cmd/multica` 通过。
- Runtime repository provenance tests：11/11 通过。
- Skill frontmatter/结构：`quick_validate.py` 通过。
- 隔离的新旧 Skill response eval：新版 15/15（100%），旧版快照 6/15（40%）。每题每配置仅 1 次，因此用于验证覆盖差异，不作为统计稳定性证明。
- 静态 review：`.agents/skills/fc-runtime-dev-loop-workspace/fc-runtime-dev-loop-review.html`；打包产物：`fc-runtime-dev-loop.skill`。

## 未放行项

- 本地设备 rollout 仍被 binary-surface gate 阻断。要宣告“正确兼容”，必须决定并修复/明确批准 `--workspaces-root` 与 `zeroclaw` 两项回退，再用修复后的 commit 重跑 compare + live + task + stop。
- 若未来修改服务端 `/api/daemon/*`、DaemonAuth、register/heartbeat/claim/complete wire contract，必须先部署精确服务端 commit 到预发，再跑：旧正式 Daemon + 新服务端、候选 Daemon + 新服务端、候选 FC + 新服务端。三项都过后才可写 `rolling_compatibility_verified=true`。
