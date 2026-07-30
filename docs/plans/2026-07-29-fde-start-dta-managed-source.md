# FDE `/fde/start` 初始化 Agent 升级为 DTA Project 格式实施计划

> 工作流：grill-and-plan
> 状态：执行中
> 创建日期：2026-07-29
> 计划 ID：20260729-fde-start-dta-managed-source
> 最后更新时间：2026-07-29
> 当前分支：`codex/fde-start-dta-managed-source`
> 目标执行分支：`codex/fde-start-dta-managed-source`
> 基线 Commit：`origin/develop@01d9db0fb`
> 原始工作区：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`
> Worktree 路径：不使用；两个目标仓库当前工作区均干净
> Worktree 来源：不适用
> 交付状态：用户已确认数据库与 Skill origin 完整迁移，并授权提交到预发；Multica 建功能分支，初始化模板直接在 master 修改
> 收尾状态：本地实现与聚焦验证完成；正在提交预发，正式环境未授权
> 当前里程碑：预发提交与环境验收

## 一句话结论

保留 `/fde/start` 的平台自动同步能力，但彻底退出 `managed_git` 格式：FDE Development Manager 仓库升级为标准 DTA `dingtalk-agent.json + agent/` 布局，数据库 `agent_source.source_type` 迁移为现有 `github` 类型，Skill `config.origin` 迁移为 DTA 已支持的 `github_agent_source` wire contract；不修改 DTA。

## 背景与根因证据

- `/fde/start` 由 `server/internal/handler/fde_onboarding.go` 调用 `managedagent.Service.Provision()`；`managedagent.Service.cloneAndCompile()` 当前仍调用 `agentsource.CompileFS()` 读取旧 `multica-agent.yaml`。
- 当前初始化仓库是公开 Gitee Git 仓库 `https://gitee.com/keeperqaq/fde-agent.git`；本地对应仓库为 `/Users/fanqi/test/code/ding-fde-agent/fde_-agent`，远端 `git@gitee.com:keeperqaq/fde_-agent.git`。只读远端校验显示两个仓库 URL 当前指向同一 commit `0a2f8dfb87358500885df2ae3ddcf7422a15859c`。
- 初始化 Agent 的 Skill 当前由 Multica 写入 `config.origin.type=managed_git`，并带 `source_key/repository/ref/commit_sha/path`；来源受管 Skill 的手工解除或删除返回 409 是所有权约束，不是待绕过的故障。
- DTA 的 `inspectMulticaWorkspace()` 会先遍历和解析整个 Workspace Skill catalog，再按目标 Agent/Source 匹配 Skill。
- DTA 当前 `multicaSkillIdentity()` 只接受无 origin 的 direct Skill 和 `github_agent_source`，把合法 `managed_git` 与真正未知 origin 一起报为 `skills.origin-unsupported`。因此一个无关的旧 managed Skill 会在目标筛选前阻断其他 GitHub Agent 的 inspection、dry-run 和 apply。
- 本次不扩展 DTA 的 origin 类型。Multica 应在 Skill catalog 边界输出 DTA 已发布的 `github_agent_source` 字段结构。
- 数据库当前通过 `source_type=managed_git` 区分平台初始化来源，并用 `managed_source_key` 关联 LKG snapshot。迁移后统一使用 `source_type=github`，平台自动同步模式只由 `managed_source_key IS NOT NULL` 判别。
- 当前 GitHub source response 和失败状态把“没有 GitHub installation”直接等同于 disconnected；迁移后必须先区分 `managed_source_key`，否则平台初始化来源会被误判为断开。

## 目标

1. 新的 `/fde/start` 初始化 Agent 只消费 DTA Project 格式，不再依赖旧 YAML manifest。
2. FDE Development Manager 仓库成为可被 DTA 和 Multica 同时审计的单一源码布局。
3. 新建和存量初始化 Agent 的 Skill 对外 origin 均收敛为 DTA 支持的 `github_agent_source`。
4. 已归档 Agent 遗留在 catalog 中的旧 `managed_git` Skill 也通过正常 rollout 更新 origin，不再阻塞无关 GitHub Agent 的 DTA Workspace inspection。
5. 数据库中不再产生或保留 `agent_source.source_type=managed_git`；存量记录通过正式 migration 转为 `source_type=github`。
6. 已存在的初始化 Agent 可通过正常平台 source rollout 升级，不手工删 Skill、不直接执行临时生产 SQL。

## 非目标

- 不把 `/fde/start` 改成 GitHub App 安装或用户 GitHub Source 导入。
- 不要求 GitHub installation。平台初始化来源同样使用 `source_type=github`，但以 `managed_source_key=fde-agent` 表示平台自动同步模式；普通用户导入仍以 `github_installation_id` 表示 GitHub App 模式。
- 不删除 `managed_agent_source_snapshot`、`managed_source_key` 或现有 LKG/rollout 能力；本次删除的是 `managed_git` 来源格式，不是平台自动同步能力。
- 不绕过 source-managed Skill 的 409 约束，不手工解除、删除或重建线上 Skill。
- 不执行临时生产 SQL；数据库变化只通过可审计、可回滚的正式 migration 完成。
- 不为新格式保留长期 YAML 双读 fallback；切换前通过仓库和编译器门禁保证新格式可用。
- 本轮仅提交并部署预发；不发布正式环境。
- 不修改 `/Users/fanqi/test/code/dingtalk-agent` 的代码、测试、分支或未提交改动。

## 已确认的格式与所有权决定

### 1. 仓库格式

初始化仓库使用以下核心结构：

```text
dingtalk-agent.json
agent/
  AGENTS.md
  skills/
    dta-basic-behavior/
      SKILL.md
      references/...
    multica-development-manager/
      SKILL.md
      scripts/...
agent.bindings.json
AGENTS.md
tests/
```

`dingtalk-agent.json` 使用 `$schema=dingtalk-agent/project@1`，声明：

- `name=fde-development-manager`
- `agent.definition=agent/AGENTS.md`
- `agent.skillsRoot=agent/skills`
- `agent.skills=[dta-basic-behavior,multica-development-manager]`
- `workspaces={}`

这里选择 DTA `host=none`：仓库由 Multica 平台消费，远端 runtime、身份和权限由 `/fde/start` 服务端配置决定，不应伪造一个本地 Host。预期 DTA 的本地 Host 连接状态可以是 partial，但 Project 格式、Skill 合同和 Multica 编译必须通过。

### 2. 来源所有权

- Multica 内部继续由 `managedagent.Service` 创建、同步和 rollout 初始化 Agent，不要求用户提供 GitHub installation。
- `agent_source.source_type` 统一保存 `github`：
  - `managed_source_key IS NULL`：普通 GitHub App Source。
  - `managed_source_key IS NOT NULL`：平台自动同步的初始化 Git Source，`github_installation_id` 必须为空。
- Skill catalog 对外统一输出 DTA 已支持的：
  - `type=github_agent_source`
  - `repository=keeperqaq/fde-agent`
  - `ref=master`
  - `commit_sha=<40 位不可变 SHA>`
  - `path=agent/skills/<logical-name>`
- 新 Skill 创建和存量 Skill rollout 使用同一个 origin builder，避免两套字段语义。
- source-managed 的删除/解绑约束继续由 Multica 的来源关系和服务端 handler 保证，不依赖 `config.origin.type=managed_git` 字符串。

### 3. 存量升级

- 新建 managed Agent 的 `agent_source.manifest_path` 保存 `dingtalk-agent.json`。
- migration 先把所有 `managed_source_key IS NOT NULL AND source_type='managed_git'` 的存量行转换为 `source_type='github'`，再收紧数据库约束为只允许 `github`。
- 正常平台 rollout 成功后，在同一事务内把存量 source 的 `synced_commit_sha`、`manifest_path` 以及来源 Skill config 更新为新版本。
- rollout 继续通过 `agent_source_skill` 映射更新原 Skill，保持现有 Agent ID、Skill ID 和 source-managed 约束；不以“删除旧 Skill 再创建”作为迁移方案。
- archived Agent 仍需纳入该来源 rollout，确保其遗留 catalog Skill 不再保留 `managed_git` origin。

## 关键设计决定

### 1. 为本地 Git 仓库增加 DTA FS 编译入口

- 保留 GitHub Source 使用的 `CompileDTAProject()` 和底层安全校验。
- 在 `server/internal/agentsource` 增加面向 `fs.FS` 的 DTA Project 编译入口，复用同一 `ParseDTAProject()`、Definition/Skill 路径校验、大小限制、frontmatter 校验、排序与稳定 hash。
- `managedagent.Service.cloneAndCompile()` 从旧 `CompileFS()` 切换到 DTA FS 编译入口。
- 不让 DTA 路径回退到 `multica-agent.yaml`；缺少或非法 `dingtalk-agent.json` 时 snapshot 更新失败并继续保留上一份 LKG bundle。

### 2. 保持服务端 runtime 权威

- DTA Project 的 `workspaces`、provider、storage、authority、model 和 endpoint 不进入 Multica Agent/runtime/source。
- DTA Project 编译后的 compatibility providers 为空；`providerAllowed()` 对空列表保持“未声明限制”的现有语义，固定 Hermes/FC runtime 仍由 `/fde/start` 服务端决定。
- Agent display name、instructions、Skill 集合由 DTA Project 编译；已有 Agent 不因仓库 display name 变化被隐式改名，沿用当前 rollout 语义。

### 3. Multica 输出 DTA 已支持的 origin wire contract

- 修改 `createManagedSkill()` 使用的 origin builder，把旧 `managed_git/source_key/repository URL` 结构替换为 `github_agent_source/repository owner-name/ref/commit_sha/path`。
- `repository` 使用 DTA 当前要求的规范 `owner/name`，不输出 Gitee URL；实际 clone URL 仍只保存在 managed source 配置和 snapshot 中。
- `path` 使用新 DTA Project 中的真实 `agent/skills/<logical-name>` 路径。
- rollout 必须更新已有 Skill 的 config，即使 Skill 正文未变化；否则归档 Agent 的旧 origin 仍会继续污染 Workspace catalog。
- DTA 代码和 fail-closed 策略保持不变，以现有 inspection 作为消费者验收。

### 4. 数据库来源类型统一为 `github`

- 新增 forward migration：
  1. 删除旧 `source_mode/source_type` check。
  2. 将 `managed_source_key IS NOT NULL` 的 `managed_git` 行更新为 `github`。
  3. `source_type` check 收紧为只允许 `github`。
  4. source mode check 改为两种互斥身份：普通 Source 使用 installation；平台 Source 使用 managed key 且 installation 为空。
  5. rollout index 的条件从 `source_type='managed_git'` 改为 `managed_source_key IS NOT NULL`。
- down migration 可依靠 `managed_source_key` 精确识别平台来源并恢复为 `managed_git`，不猜测记录身份。
- `CreateManagedAgentSource`、rollout list、owner update、reconcile 和 lock guard 全部改为按 `managed_source_key` 判别，不再引用 `managed_git`。
- `agentSourceToResponse()` 只有普通 GitHub App Source 缺 installation 时才返回 disconnected；平台 Source 保留 snapshot/rollout 的真实状态。
- GitHub 手动 sync endpoint 遇到平台 Source 时继续拒绝或路由到平台 reconcile，不因 `source_type=github` 错进 installation 流程。

## 复杂度与执行路由

- 规划复杂度：P2
- 执行复杂度：E1
- 判断依据：改动跨两个独立仓库，涉及正式数据库 migration、来源模式判别、存量 rollout 元数据和对外 Skill origin wire contract；不含临时生产 SQL或 DTA 修改。
- 主执行者：主 Agent 在计划确认后连续实现。
- Subagent 数量与职责：0；两个仓库共享同一来源身份合同，保持单一上下文可减少格式漂移。
- Review 安排：实现后先做本地自检，再使用 `verify-before-finish` 对照验收矩阵；如需独立代码审查另行发起。

## 分支与工作区策略

### Multica

- 仓库：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`
- 基线：`origin/develop@1a0ce3836524eedf4e8789f6ab8057626487c073`
- 分支：`codex/fde-start-dta-managed-source`
- 当前工作区干净；计划确认后从 fresh `origin/develop` 创建分支。

### 初始化 Agent 模板

- 仓库：`/Users/fanqi/test/code/ding-fde-agent/fde_-agent`
- 基线：`origin/master@0a2f8dfb87358500885df2ae3ddcf7422a15859c`
- 分支：直接使用 `master`
- 当前工作区干净；按用户明确要求直接在 fresh `master` 修改，不新建功能分支。

## 文件与职责

### `/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`

- `server/internal/agentsource/`
  - 增加 DTA FS 编译入口和合法/非法本地 DTA fixture。
  - 复用 GitHub DTA parser 与底层 bundle 编译，不复制第二套格式规则。
- `server/internal/managedagent/service.go`
  - managed clone/snapshot 切换为 DTA FS compiler。
  - 新 source 保存 `agentsource.DTAProjectPath`。
  - 新建和 rollout 统一输出 DTA 支持的 `github_agent_source` origin。
  - rollout 成功时同步更新存量 manifest path，并覆盖旧 Skill origin。
- `server/pkg/db/queries/agent_source.sql` 与生成代码
  - 新建平台 Source 时写入 `source_type=github`。
  - 所有平台同步查询改按 `managed_source_key` 判别。
  - 扩展正常 sync success query 的 `manifest_path` 参数；运行 `make sqlc`。
- `server/migrations/<next>_unify_agent_source_type.*.sql`
  - 正式迁移存量 `managed_git -> github`，更新 check 和 rollout index。
  - down 依靠 `managed_source_key` 精确恢复。
- `server/internal/managedagent/*_test.go`
  - 覆盖新 DTA snapshot、LKG 保留、存量及 archived Agent rollout、origin config 与 manifest path。
- `server/internal/handler/fde_onboarding_test.go`
  - 更新 `/fde/start` 集成 fixture，断言新 Agent 的 Definition、两个 Skill、DTA manifest path 和 `source_type=github`。
- `server/internal/handler/github_agent_source*.go`
  - 使用 `managed_source_key` 区分平台自动同步与 GitHub App installation 模式。
  - 防止平台 Source 被误报 disconnected 或进入普通 GitHub 手动 sync。
- `docs/fde-mobile-onboarding.md`、`.env.example`
  - 更新 managed 仓库必须是 DTA Project 的运行合同和发布顺序。

### `/Users/fanqi/test/code/ding-fde-agent/fde_-agent`

- 用 `dta agent enhance --host none` 的 plan/apply 生成标准骨架。
- 将现有 `AGENT.md` 语义迁入 `agent/AGENTS.md`，不退化现有职责、授权和完成判定。
- 将岗位 Skill 与脚本迁到 `agent/skills/multica-development-manager/`。
- 引入当前完整 `dta-basic-behavior`，不手抄裁剪版。
- 更新 `README.md` 与 `tests/test_repository_contract.py`，验证唯一发布源、DTA manifest、Skill frontmatter 和无明显秘密。
- 新格式验证通过后删除旧 `multica-agent.yaml`、根 `AGENT.md` 和根 `skills/` 发布树，避免双源漂移。

## 实施步骤

- [x] 里程碑一：建立执行工作区
  - Multica fetch 远端并从记录的 fresh `origin/develop` 创建功能分支。
  - 初始化模板 fetch 远端并确认本地 `master` 与 `origin/master` 一致后直接修改。
- [x] 里程碑二：先建立失败回归
  - Multica：本地 managed repo 只有 `dingtalk-agent.json` 时复现当前编译失败。
  - Multica：新建数据库 source 和 Skill config 仍输出 `managed_git`，以及 archived Agent rollout 未清理旧格式的回归。
  - Multica：把 source type 改成 `github` 后，现有 response/sync 分支会把无 installation 的平台 Source 误判 disconnected。
  - 初始化仓库：测试先声明新 DTA 单一源码布局。
- [x] 里程碑三：迁移初始化 Agent 仓库
  - 重新生成并审阅 DTA enhance plan。
  - apply 后人工合并现有 Agent Definition 和岗位 Skill 内容。
  - bootstrap、audit 并更新仓库合同测试。
- [x] 里程碑四：切换 `/fde/start` 编译、数据库 source type、origin 和 rollout 元数据
  - 增加 DTA FS compiler。
  - 新增 `managed_git -> github` migration，查询改按 `managed_source_key` 判别。
  - origin builder 改为严格的 `github_agent_source` wire contract。
  - 更新 snapshot/provision/rollout、source response/手动 sync 分流及 sqlc。
  - 更新 handler、managedagent、agentsource 测试与文档。
- [ ] 里程碑五：跨仓验收
  - 用真实初始化仓库验证 DTA audit。
  - 用等价 fixture 验证 Multica DTA FS compiler 和 `/fde/start` 物化结果。
  - 使用现有 DTA CLI 对 Multica 测试输出做消费者侧 inspection，证明 catalog 中不再出现 `managed_git`。
  - 确认无 DTA diff、migration 可 up/down、无临时生产写入。

## 本地执行记录

| 验证 | 结果 |
| --- | --- |
| 初始化模板仓库分支 | 直接在 `master@0a2f8dfb87358500885df2ae3ddcf7422a15859c` 修改，未创建功能分支 |
| 模板仓库合同测试 | `python3 -m unittest discover -s tests -v`：14/14 通过 |
| DTA Project bootstrap | Definition `status=ready`，Basic 与岗位 Skill 均加载，`missing=[]` |
| DTA Agent audit | `host=none` 下为预期的 `partial`；Project/Definition/Skill/Storage/Authority 合同通过，缺少的是本地 Host 注入和 load probe 证据 |
| Multica 聚焦测试 | `go test -count=1 ./internal/agentsource ./internal/managedagent ./internal/handler ./internal/migrations` 通过 |
| Multica 静态检查 | `go vet ./internal/agentsource ./internal/managedagent ./internal/handler` 通过 |
| migration 实际 up/down | 在临时 PostgreSQL 数据库中验证：up 将 keyed `managed_git` 转为 `github`，down 仅将 keyed 行恢复；临时数据库已删除 |
| 全量 Go 测试 | 已执行；任务相关包通过，`server/pkg/agent` 的既有负载敏感时序测试在不同轮次出现超时/临时文件 flake，单测重跑可通过，因此不把全量 suite 记为全绿 |
| 工作区卫生 | 两仓 `git diff --check` 通过；未修改 DTA 仓，未写生产数据库 |

里程碑五尚未完成的部分是：在真实 Multica Workspace 上执行 source rollout/readback，并用未修改的 DTA 对真实 catalog 做 inspection/dry-run。它们属于部署后的环境验收，本轮没有部署授权。

## 验收矩阵

| 验收项 | 风险 | 验证方式 | 测试先行 |
| --- | --- | --- | --- |
| 合法 DTA managed repo 编译 | `/fde/start` 仍依赖旧 YAML | `go test -count=1 ./internal/agentsource ./internal/managedagent` | 是 |
| 非法新 repo 保留 LKG | 自动同步把好 snapshot 覆盖坏 | managed snapshot failure/rollout tests | 是 |
| 新 `/fde/start` 物化 | Agent 缺 Definition/Basic/岗位 Skill | handler 集成测试 + source/skill readback | 是 |
| DB source type migration | 存量记录残留 `managed_git` 或约束不一致 | migration up/down fixture，断言模式互斥和存量行转换 | 是 |
| 平台 Source 状态 | `github + no installation` 被误报 disconnected | response/sync handler tests | 是 |
| 存量平台 rollout | manifest path 或 Skill mapping 漂移 | rollout 事务测试，断言 ID/ownership 不变 | 是 |
| origin 格式 | DTA 仍看到 `managed_git` | managedagent config 精确断言：`github_agent_source` 五字段 | 是 |
| archived 存量升级 | 遗留旧 Skill 继续阻断 DTA | archived Agent rollout 测试，断言原 Skill ID 的 config 被覆盖 | 是 |
| DTA 消费兼容 | 输出字段名/path/SHA 不符合现有 parser | 使用现有 DTA inspection fixture/CLI 做只读验收，不改 DTA | 否 |
| 模板单一源码 | 新旧文件并存产生漂移 | Python repository contract + `dta agent audit` | 是 |
| 无遗留枚举 | 运行时代码继续依赖 `managed_git` | 除 down migration/历史文档外定向 `rg managed_git` 无命中 | 否 |
| DTA 零修改 | 修错仓库或改变消费者合同 | `git -C /Users/fanqi/test/code/dingtalk-agent diff` 与实施前基线一致 | 否 |

建议最终验证命令：

```bash
# 初始化 Agent 仓库
dta bootstrap --bindings agent.bindings.json --json
dta agent audit --bindings agent.bindings.json \
  --require-skill multica-development-manager --json
python3 -m unittest discover -s tests -v

# Multica
cd server
go test -count=1 ./internal/agentsource ./internal/managedagent ./internal/handler
go vet ./internal/agentsource ./internal/managedagent ./internal/handler
cd ..
make sqlc
git diff --check

```

## 风险与回滚

- **模板仓库先发、Multica 旧代码仍在线**：旧 compiler 会因缺少 YAML 同步失败，但 LKG snapshot 应继续服务；发布顺序必须先让兼容新格式的 Multica 版本到位，再更新模板仓库默认分支。
- **Multica 先发、模板仍是旧格式**：新 compiler 不保留 YAML fallback，会让新 snapshot 失败。预发发布时应先用带新模板 fixture 的应用验证，并把应用与模板发布作为一个受控窗口；若需要跨版本滚动兼容，必须在实施前重新评估短期双读，不在代码中悄悄加入。
- **多副本滚动期间格式不一致**：旧、新副本共享 snapshot；bundle schema 本身不变，但 clone compiler 能力不同。发布前必须明确顺序并验证旧副本不会用失败结果覆盖 LKG。
- **migration 先执行而旧副本仍按 `source_type=managed_git` 查询**：旧副本会暂时停止平台 rollout，并可能无法按旧约束写入新的平台 Source。当前代码是目标最终态，不应直接作为一次普通多副本滚动发布。正式发布前必须选择并验证 expand/contract 两阶段兼容发布，或经明确批准采用停流量的原子切换；不能直接赌单副本切换。
- **把平台托管 Skill 标成 `github_agent_source` 后被 DTA 视为同一 Source**：这是本次要求的消费者合同；必须确保 repository/ref/path/SHA 与模板仓库实际来源一致，不能伪造其他仓库身份。
- **只更新新建、不更新存量**：归档 Agent 的旧 `managed_git` 仍会阻断 Workspace。门禁是 archived rollout fixture 和原 Skill ID config 覆盖断言。
- **删除/解绑保护意外依赖 origin type**：实现前追踪 handler 的 source-managed 判断，测试 409 约束仍由来源关系生效。
- **回滚**：应用代码回滚前先执行兼容的 down migration，把 `managed_source_key IS NOT NULL` 的来源恢复为 `managed_git`；模板仓库再回滚到包含旧 YAML 的 commit。Skill origin config 通过下一次正常 rollout 恢复，不执行临时生产 SQL。

## 发布顺序建议

1. 本地完成两个仓库验证。
2. 用户已授权分别 push Multica 功能分支和初始化模板 `master`，并提交预发。
3. 先发布可编译 DTA managed repo 的 Multica 版本到预发，使用候选模板 commit 做只读/隔离验证。
4. 再把初始化模板默认分支切到 DTA 格式。
5. 触发 managed sync，验证 snapshot ready、存量 rollout 和新 `/fde/start`。
6. 最后用未修改的 DTA 验证同 Workspace inspection/dry-run 不再出现 `skills.origin-unsupported`。
7. 正式环境发布必须另行确认。

## 计划变更记录

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-07-29 | 初始计划，覆盖 Multica、初始化模板、DTA 三仓 | `/fde/start` 旧格式与 DTA managed origin 兼容问题属于同一来源合同，必须一起收敛 |
| 2026-07-29 | 按用户纠正移除 DTA 代码改动，收敛为 Multica 与初始化模板两仓 | 消费者 DTA 不应为生产者私有的 `managed_git` 扩展兼容；Multica 应输出 DTA 已支持的 `github_agent_source` 格式 |
| 2026-07-29 | 按用户纠正将数据库内部来源类型也从 `managed_git` 迁移为 `github` | 初始化来源需完整对齐现有 GitHub/DTA Source 格式，平台自动同步模式改由 `managed_source_key` 表达 |
| 2026-07-29 | 初始化模板仓库不建功能分支，直接在 `master` 修改 | 用户明确指定；commit 和 push 仍作为后续独立授权边界 |
| 2026-07-29 | 用户授权提交预发；Multica 基线更新到 `origin/develop@01d9db0fb` | 远端新增无关 runtime 修复，已 fast-forward 合入；正式环境仍未授权 |
