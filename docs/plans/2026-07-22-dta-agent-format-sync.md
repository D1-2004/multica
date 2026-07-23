# Multica GitHub Agent 导入对齐 DTA Project 格式实施计划

> 工作流：grill-and-plan
> 状态：已完成
> 创建日期：2026-07-22
> 计划 ID：20260722-dta-github-agent-project-import
> 最后更新时间：2026-07-23 16:44 +0800
> 当前分支：`codex/dta-agent-format-sync`
> 目标执行分支：`codex/dta-agent-format-sync`
> 基线 Commit：`origin/develop@eb8b12677055cc7a7dd2c2680a0c9a8d28149e95`
> 原始工作区：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`
> Worktree 路径：不使用
> Worktree 来源：不使用
> 交付状态：本地已提交（见 Git 历史）
> 收尾状态：不适用
> 当前里程碑：实现与范围内验证完成

## 背景与现状证据

- Multica 的 GitHub Agent 导入由 `server/internal/handler/github_agent_source.go` 处理，以 preview 得到的不可变 commit SHA 调用 `agentsource.Compile()`，再把 Agent Definition 和来源 Skill 物化到现有 `agent`、`skill`、`skill_file`、`agent_source`、`agent_source_skill` 表。
- 当前 GitHub 导入要求仓库根目录存在 `multica-agent.yaml`；线上共享库在 2026-07-23 的只读检查中没有 `source_type=github` 的记录，因此本次不承担旧 GitHub source 数据兼容或迁移。
- FDE 快速初始化是另一条正在运行的 managed source 链路：`managedagent.Service` 使用 `CompileFS()` 编译 `multica-agent.yaml`，服务启动后立即同步、之后默认每 30 分钟同步，并通过 PostgreSQL LKG snapshot 和 rollout 更新现有 managed Agent。
- 预发和正式环境当前共享同一个 PostgreSQL；只读检查中存在 8 条 `source_type=managed_git` 记录。该链路不能被本次 GitHub 导入格式切换影响。
- 当前 `agentsource.Compile()` 与 `CompileFS()` 汇合到同一 YAML 编译器。要保持 managed 链路不变，本次不能直接替换共享 parser，而应拆分“来源格式解析”入口，复用底层文件安全检查和 Skill tree 编译能力。
- DTA 当前以仓库根目录 `dingtalk-agent.json` 表示 Agent Project。当前合同为 `$schema=dingtalk-agent/project@1`，必填 `name`、`dtaVersion`、`agent`、`workspaces`；`agent.definition` 和 `agent.skills` 必填，`agent.displayName`、`agentPlatform`、`multicaEndpoint` 可选，`workspaces` 允许为空对象。
- `codex/dta-agent-format-sync` 已存在，但仍停在旧基线 `e7110a9c`；刷新远端后确认它落后当前 `origin/develop` 17 个提交、没有自身业务提交。计划确认后应先将该分支安全快进到上述新基线，再开始实现。

## 目标

让 Multica 的“从 GitHub 导入 Agent”预览、创建和手动同步直接识别 DTA `dingtalk-agent.json`，不再要求该 GitHub 仓库额外维护 `multica-agent.yaml`。

本次建立一个版本化的 DTA Project 适配层：解析 `project@1` 中与 Agent/Skill 编译有关的稳定语义，忽略 DTA 开发环境和平台连接字段，再复用现有安全门禁把源码编译成 Multica 当前可物化的 Agent Definition 和 Skill 集合。

## 非目标

- 不修改 `managedagent.Service`、`CompileFS()`、FDE onboarding、默认 Gitee 仓库、同步周期、LKG snapshot 或 rollout。
- 不把 FDE managed 模板从 `multica-agent.yaml` 迁移到 `dingtalk-agent.json`。
- 不新增或修改数据库 migration，不改 `managed_agent_source_snapshot.bundle`，不批量更新 `agent_source.manifest_path`。
- 不修改 DTA CLI、DTA Project Schema 或 DTA 仓库。
- 不把 `workspaces`、provider、model、storage、authority、`agentPlatform` 或 `multicaEndpoint` 写入 Multica Agent/runtime/source。
- 不注入 DTA `deploy` 阶段额外装配的 `dingtalk-agent-boot-multica`。
- 不为 GitHub 导入保留旧 `multica-agent.yaml` fallback；线上当前无 GitHub source 存量，本产品入口直接切换为 DTA Project。
- 不新增普通 Workspace 默认 Agent，不扩展知识库、Memory、机器人安装或 Agent 自我修改流程。

## 已确认需求

### 1. DTA 原始 Project 格式

Multica 接受的最小合法输入如下：

```json
{
  "$schema": "dingtalk-agent/project@1",
  "name": "fde-development-manager",
  "dtaVersion": "^0.1.5",
  "agent": {
    "displayName": "FDE Development Manager",
    "definition": "AGENT.md",
    "skills": [
      "dingtalk-basic-behavior",
      "multica-development-manager"
    ]
  },
  "workspaces": {}
}
```

当前 DTA 合同还允许可选的 `agentPlatform`、`multicaEndpoint`，以及包含 provider、storage、authority 的完整 `workspaces`。这些字段属于 DTA 开发/连接环境，不进入 Multica 编译结果。

### 2. 协议版本演进

- `$schema=dingtalk-agent/project@1` 是版本判别字段；`project@1` 已有核心字段的语义一旦发布就保持不变。
- 在 `project@1` 中新增可选、可忽略字段属于兼容演进。Multica 不因不消费的未知字段而拒绝整个 Project。
- 删除必填字段、字段改名、字段类型变化、Skill 定位规则变化或已有字段语义变化属于不兼容演进，DTA 应发布 `project@2`。
- Multica 只显式支持已实现的版本；遇到 `project@2` 等未知版本时明确返回 unsupported schema，不用 `project@1` parser 猜测。
- 将来支持新版本时新增 `project@2 -> normalized Agent/Skill` adapter，保留 `project@1` parser，不原地改写旧版本语义。

### 3. Multica 稳定消费的核心语义

| DTA Project 字段 | Multica 用途 | 稳定要求 |
| --- | --- | --- |
| `$schema` | 选择版本化 parser | 必须是明确支持的版本 |
| `name` | Project 机器名及 Agent 名称 fallback | `project@1` 内保持稳定机器名语义 |
| `agent.displayName` | 新建 Agent 的默认展示名称 | 可选；存在时优先于 `name` |
| `agent.definition` | 读取 Agent instructions | 仓库内安全相对路径、UTF-8 文本、满足大小限制 |
| `agent.skills[]` | 声明来源 Skill 身份集合 | 非空、去重、显式包含 Basic |
| `dingtalk-basic-behavior` | Basic Skill 源目录 | `project@1` 固定读取 `.agents/skills/dingtalk-basic-behavior/` |
| 其他 Skill 名 | 岗位 Skill 源目录 | `project@1` 固定读取 `skills/<name>/` |
| 每个 `SKILL.md` | Skill name/description/content | 唯一存在；frontmatter name 与声明名一致 |

### 4. 可以变化或被忽略的内容

| 字段或内容 | 处理方式 |
| --- | --- |
| `dtaVersion` | 要求非空并保留用于诊断；Multica 不拿服务端版本做 DTA CLI semver 判定 |
| `workspaces` | 校验为对象后忽略；允许 `{}`，不进入 Agent/runtime/source |
| `agentPlatform`、`multicaEndpoint` | 允许存在但忽略，不得覆盖 Multica 服务端目标 |
| workspace provider/model/storage/authority | 忽略，不得提供远端 ID、身份、凭据或权限 |
| DTA 新增的非核心可选字段 | 忽略；只有 Multica 实际消费时才补对应校验和映射 |
| `agent.displayName` 的值 | 新建时使用；后续 source sync 默认不自动重命名已有 Agent |
| Definition 内容和路径 | 可以随 commit 变化，sync 时重新编译 instructions |
| `agent.skills` 集合及 Skill files | 可以随 commit 增删改，沿用现有来源 Skill 同步语义 |

## 执行假设

- DTA 在同一个 `$schema` major 内遵守兼容演进；若 `project@1` 原地改变核心语义，Multica 应通过跨仓 fixture 发现并阻塞升级，而不是静默猜测。
- GitHub 导入仍以 repository/ref/immutable SHA 标识源码版本；Project `name` 不是 source ownership 或同步身份。
- 现有 `agentsource.Skill`、文件限制、排序和安全检查可继续作为两种来源格式的共享底层能力，但 DTA GitHub 编译不复用 YAML Manifest parser。
- GitHub source sync 仍只更新 instructions 和来源 Skill，不用 Git 内容覆盖用户在 Multica 中管理的其他字段。

## 关键设计决定

### 1. 拆分来源 parser，保留 managed YAML 链路

- 保留现有 `ManifestPath = "multica-agent.yaml"`、YAML `ParseManifest()` 和 `CompileFS()` 行为，保证 FDE managed source 无代码、配置和数据库变化。
- 新增 `DTAProjectPath = "dingtalk-agent.json"` 及 GitHub tree 专用 DTA 编译入口，例如 `CompileDTAProject()`；具体命名在实现时遵循现有包约定。
- GitHub preview/create/sync 切到 DTA 入口；managed `cloneAndCompile()` 继续调用旧 `CompileFS()`。
- 两条入口只共享安全路径校验、Git object 校验、文本/大小限制、Skill tree 编译和排序工具，不共享来源 Manifest 结构。

### 2. 只严格校验消费字段，不复制整个 DTA strict schema

- 必须严格验证 `$schema`、`name`、`dtaVersion`、`agent.definition`、`agent.skills` 及其类型、必填性和安全约束。
- `agent.skills` 必须非空、名称合法且去重，并显式包含 `dingtalk-basic-behavior`。
- `workspaces` 必须存在且为对象，但内部字段由 Multica 忽略；`agentPlatform`、`multicaEndpoint` 和其他非核心可选字段不参与编译。
- 不使用全局 `DisallowUnknownFields` 拒绝 DTA 新增的非核心字段；核心字段拼写错误仍会因真正的必填字段缺失或类型错误而失败。
- 未支持的 `$schema` 明确失败，不做 schema fallback、文件猜测或自动降级。

### 3. DTA 字段映射

- Agent 默认名称：`agent.displayName ?? name`。
- Agent description：DTA 当前没有对应字段，新建时为空；不从 Definition 标题或正文猜测。
- Instructions：读取 `agent.definition` 指向的完整文本。
- Basic Skill：读取 `.agents/skills/dingtalk-basic-behavior/`。
- 其他 Skill：读取 `skills/<name>/`。
- `dtaVersion`、`workspaces`、`agentPlatform`、`multicaEndpoint` 不影响 runtime provider 或权限。
- 删除旧 YAML `spec.compatibility.providers` 只发生在 GitHub DTA 导入路径；managed YAML provider compatibility 保持原样。

### 4. 不做数据库迁移

- `agent_source.manifest_path` 的数据库默认值和现有 managed 记录保持 `multica-agent.yaml`。
- 新建 GitHub source 时由 handler 显式保存 `dingtalk-agent.json`；API preview/source detail 返回实际路径。
- 不修改 `managed_agent_source_snapshot`、`agent_source` 约束、sqlc schema 或已有 8 条 managed source。
- 前端和文档仅把“从 GitHub 导入”的入口说明改成 DTA Project；不把 managed source 的显示合同全局替换。

### 5. 保持现有 GitHub source 治理

- preview 固定 immutable SHA；create 使用 preview 的 SHA，不因 branch 前进而漂移。
- sync 沿用行锁/CAS、失败记录和旧活动 Agent/Skill 不被部分覆盖的原子性。
- GitHub App installation、Workspace ownership、repo/ref/SHA、手动同步和断开状态保持不变。

## 被排除的方案

- **直接用 DTA JSON parser 替换 `agentsource` 共享 YAML parser**：会让线上 FDE managed source 在下一次自动同步时失败。
- **同时迁移 managed 模板**：该链路在线上持续使用且预发/正式共享数据库，不属于当前无人使用的 GitHub 导入范围。
- **修改数据库默认值或批量迁移 `manifest_path`**：当前无 GitHub source 存量，功能不需要，反而扩大共享数据库风险。
- **完整复制 DTA strict schema**：DTA 仍在快速演进，非核心可选字段会让 Multica 产生不必要的版本耦合。
- **对未知 `$schema` 静默按 `project@1` 解析**：会把不兼容变化误编译为可执行 Agent。
- **保留 GitHub YAML 双读 fallback**：当前无存量使用者，产品入口直接收敛到 DTA Project，避免长期双格式。

## 复杂度与执行路由

- 规划复杂度：P2
- 执行复杂度：E1
- 判断依据：改动跨 Go compiler、GitHub handler、API 类型和前端文案，但范围已收敛到无人使用的 GitHub 导入路径；数据库和线上 managed source 明确不动。
- 主执行者：主 Agent 在计划确认后连续实现。
- Subagent 数量与职责：0；parser、handler 和 fixture 共享同一格式语义，单一上下文更能避免合同漂移。
- Review 安排：主 Agent 自检；完成前使用 `verify-before-finish` 对照验收矩阵，不启动独立 reviewer。
- Worktree：不需要；复用现有目标分支，执行前安全快进到确认后的 `origin/develop` 基线。
- Worktree 来源与清理责任：不适用。
- 分支策略：计划确认后从当前工作区切换到既有 `codex/dta-agent-format-sync`，确认无分支占用和工作区冲突后执行 `git merge --ff-only origin/develop`；不重建、不改写历史。
- Commit 策略：由主 Agent 根据完成状态和工作区事实自主判断；不自动 push。

## 文件与职责

- `server/internal/agentsource/`
  - 保留 YAML/FS managed compiler。
  - 增加 DTA `project@1` 类型、核心字段校验、GitHub tree 编译入口和共享 Skill tree helper。
  - 增加合法/非法 DTA fixture、未知非核心字段、未知 schema 和路径安全测试。
- `server/internal/handler/github_agent_source.go`
  - preview/create/sync 改用 DTA 编译入口。
  - 显式保存并返回 `dingtalk-agent.json` manifest path。
  - 保持 immutable SHA、权限、事务和同步失败语义。
- `server/internal/handler/*github_agent_source*_test.go`
  - 更新 GitHub source fixture 与预览、创建、同步、来源 Skill 增删改测试。
  - 增加 branch 漂移、失败不破坏旧活动数据的回归。
- `server/internal/managedagent/`
  - 原则上不修改业务实现。
  - 只运行现有定向测试证明 `CompileFS()` 和 managed YAML 行为未回归；若为编译器内部重构必须调整测试，行为断言不得变化。
- `packages/core/`、`packages/views/`
  - 更新 GitHub import 的 API schema/fallback、manifest path 展示和四语种文案。
  - 不把 managed source 文案或其他 YAML 入口误改为 DTA。
- `apps/docs/`
  - 更新 GitHub Agent 导入文档和最小 DTA Project 示例。
- `server/migrations/`、`server/pkg/db/`
  - 不修改。

## 实施步骤

- [x] 里程碑一：建立 DTA `project@1` 核心合同测试
  - 为最小 `{workspaces:{}}` 和包含完整 workspace/platform 字段的 Project 建立合法 fixture。
  - 覆盖 unsupported schema、缺字段、错误类型、重复/缺 Basic Skill、非法名称和路径。
  - 覆盖新增未知非核心可选字段不影响编译。
- [x] 里程碑二：拆分 DTA GitHub compiler 与 managed YAML compiler
  - 保留 `CompileFS()` 和 YAML Manifest 行为。
  - 新增 GitHub tree DTA 入口，复用安全读取、Skill tree、大小限制和排序能力。
  - 验证 Basic 与岗位 Skill 的固定路径、frontmatter name 和唯一 `SKILL.md`。
- [x] 里程碑三：切换 GitHub preview/create/sync
  - 返回 DTA Project 名称、Definition、Skill 摘要及 `dingtalk-agent.json` 路径。
  - 保持 immutable SHA、Workspace/installation 权限、事务原子性和同步状态。
  - 回归来源 Skill 新增、更新、删除、普通 Skill 保留和失败不破坏旧活动数据。
- [x] 里程碑四：更新 API 消费端、四语种文案和 GitHub 导入文档
  - 更新 core Zod schema/fallback 和 source detail。
  - 只修改 GitHub import 产品表述，明确 Project 核心字段及 `workspaces` 不进入 runtime。
- [x] 里程碑五：执行非回归与完成验证
  - 定向运行 agentsource、GitHub handler、managedagent Go 测试。
  - 运行受影响的 core/views tests 和 typecheck。
  - 证明 migrations/sqlc、managed snapshot/config、FDE onboarding 和默认 Gitee 仓库没有改动。

## 执行记录

| 里程碑 | 状态 | 关联 Commit（可选） | 实际验证命令 | 结果与证据 |
| --- | --- | --- | --- | --- |
| 计划确认与环境准备 | 已完成 |  | `git fetch --prune origin`; `git merge --ff-only origin/develop` | 目标分支已从 `e7110a9c` 快进到 `eb8b1267`，工作区仅有计划文件 |
| DTA `project@1` 合同测试 | 已完成 |  | `go test ./internal/agentsource -count=1` | 覆盖稳定核心、未知扩展字段、未知 schema、固定 Skill 路径、frontmatter name 和无 YAML fallback |
| 编译入口拆分 | 已完成 |  | `go test ./internal/agentsource ./internal/managedagent -count=1` | GitHub 新增 `CompileDTAProject()`；managed 继续使用 `CompileFS()` 和 `ManifestPath` |
| GitHub 导入链路切换 | 已完成 |  | `go test ./internal/handler -count=1`; handler 接线静态检查 | preview/create 与 sync 均调用 DTA 入口；新 source 显式保存 `dingtalk-agent.json` |
| 前端和文档 | 已完成 |  | core/views typecheck；目标 Vitest；四份 locale `jq empty` | API fallback、四语种入口文案和四份 GitHub 导入文档已更新 |
| 完成验证 | 已完成 |  | `git diff --check`; 零变更目录检查 | migrations、sqlc、managedagent、FDE onboarding 均无 diff |

## 验证策略

| 改动或验收项 | 风险 | 验证方式 | 是否测试先行 |
| --- | --- | --- | --- |
| DTA 核心字段解析 | schema 漂移或错误输入被接受 | table-driven parser tests + 合法/非法 fixtures | 是 |
| 非核心字段兼容 | DTA 新增字段导致 Multica 无谓失败 | `agentPlatform`、`multicaEndpoint`、完整 `workspaces` 和额外可选字段 fixture | 是 |
| 未知 schema | `project@2` 被错误当作 `project@1` | unsupported schema 精确错误断言 | 是 |
| Definition/Skill 路径 | 路径穿越、symlink、submodule、LFS 或目录装错 | 复用并扩展现有安全测试 | 是 |
| Basic/岗位 Skill 定位 | 错读 Host exposure 或错误目录 | 两类固定路径正向测试；缺失和 name 不匹配失败 | 是 |
| GitHub SHA 原子性 | preview/create 漂移或部分同步 | handler 集成测试：SHA A preview、branch 到 B、create 仍用 A；sync 才到 B | 是 |
| managed YAML 非回归 | 新编译器误伤线上 FDE source | `go test ./internal/agentsource ./internal/managedagent`，保留旧 `CompileFS()` fixture | 否，现有回归 |
| 数据库零变更 | 共享库受到不必要 migration 影响 | `git diff -- server/migrations server/pkg/db` 应为空 | 否 |
| 前端 API/文案 | UI 仍提示旧 YAML 或 schema fallback 错误 | 相关 core/views tests + `pnpm typecheck` | 视现有测试补充 |

建议的最终验证命令以实际模块依赖为准，至少包括：

```bash
cd server
go test ./internal/agentsource ./internal/managedagent ./internal/handler
cd ..
pnpm test --filter @multica/core --filter @multica/views
pnpm typecheck
git diff --check
```

## 风险与回滚

- **误伤 managed source**：若重构后 `CompileFS()` 行为变化，线上 8 个 managed Agent 会受自动同步影响。门禁是保持独立入口、现有 managed 测试通过，并确认 `server/internal/managedagent/` 业务 diff 为空或仅为无行为变化的接线。
- **DTA 在 `project@1` 内原地改变核心语义**：通过版本化 fixture 和明确 supported schema 阻塞；不能依赖宽松 JSON 解码掩盖核心字段变化。
- **宽松未知字段隐藏拼写错误**：只宽松处理非核心字段；`$schema`、`name`、`dtaVersion`、`agent`、`definition`、`skills`、`workspaces` 的缺失或错误类型必须失败。
- **Skill 来源路径改变身份**：`source_path` 继续作为 GitHub 来源 Skill 映射身份；Basic 和岗位 Skill 路径由 `project@1` adapter 固定并测试。
- **前端全局替换误导 managed source**：文案修改限定 GitHub import surface；source detail 使用实际 `manifest_path`。
- **回滚**：本次没有 migration、配置或 managed repo 变更；回滚应用代码即可恢复 GitHub YAML 导入，不涉及数据库 down、snapshot 恢复或线上 managed Agent 回滚。

## 发布边界

- 当前计划已获用户确认并完成本地实现。
- Push：本地 commit 后告知“已提交，准备 push”；用户明确同意后直接执行。
- PR / 合并 / 部署 / 发布：未授权，除非用户另行明确确认。
- 不修改 Aone 环境变量，不触发预发或正式流水线，不访问或写入 GitHub/Gitee source。

## 计划变更记录

| 日期 | 变更 | 原因 | 是否重新确认 |
| --- | --- | --- | --- |
| 2026-07-22 | 初版同时迁移 GitHub import 与 FDE managed source，并包含数据库默认值迁移 | 希望两条入口统一使用 DTA Project | 是 |
| 2026-07-23 | 范围收敛为只切换无人使用的 GitHub import；managed YAML、FDE onboarding、共享数据库和远端同步全部保持不变 | 预发/正式共享数据库，managed source 在线持续同步；用户明确本次先不处理初始化模板 | 是，已确认 |
| 2026-07-23 | 将稳定边界从“完整 DTA strict schema”调整为“版本化核心字段 adapter”；兼容新增非核心字段，不兼容变化升级 `$schema` | DTA 仍在开发，Multica 只应依赖稳定的 Agent/Skill 语义 | 是，已确认 |

## 最终验证结果

| 验收项 | 验证命令或检查 | 结果 | 证据摘要 |
| --- | --- | --- | --- |
| GitHub import 接受 DTA `project@1` | `go test ./internal/agentsource ./internal/handler -count=1` | 通过 | 合法 Project 编译出 Definition、Basic 与岗位 Skill；DTA 校验错误返回 422 |
| GitHub import 不再要求 YAML | `TestCompileDTAProjectDoesNotFallBackToLegacyManifest`; handler 接线检查 | 通过 | 仅有旧 YAML 时明确缺少 `dingtalk-agent.json`；preview/create/sync 全部调用 DTA 入口 |
| managed YAML/CompileFS 无回归 | `go test ./internal/agentsource ./internal/managedagent -count=1` | 通过 | `CompileFS()`、managed service 和旧 `ManifestPath` 保持原实现 |
| 数据库与 migration 无变更 | `git diff --name-only -- server/migrations server/pkg/db/generated` | 通过 | 无输出；没有 migration、sqlc 或数据更新 |
| 前端和文档只更新 GitHub import | core/views typecheck、19 个目标 Vitest、locale JSON 校验、范围 diff | 通过 | core 4 tests、views 15 tests 通过；只修改 GitHub import API fallback、文案和文档 |
| 全量 Go 套件 | `go test ./...` | 范围外失败 | `server/pkg/agent` 的 Codex app-server 72ms semantic inactivity 时序测试失败；本次未修改该包，相关目标包均独立通过 |

### 最终工作区

- 原始工作区与分支：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`，`codex/dta-agent-format-sync`
- 最终工作区与分支：`/Users/fanqi/test/code/ding-fde-agent/dt-fde-multica`，`codex/dta-agent-format-sync`
- 交付状态：本地已提交；未 push、未创建 PR、未部署
- Worktree 收尾：不适用
- 当前未提交改动：无
- 未执行的验证：未连接真实 GitHub installation 做端到端导入；未发布到预发或正式环境

## 遗留风险

- DTA `project@1` 的核心语义目前由跨仓代码与文档共同维护，尚无自动发布的跨语言 conformance package；本计划用固定 fixture 降低漂移风险，但未来仍应考虑由 DTA 发布可复用 JSON Schema/fixture 版本。
- GitHub import 当前无人使用，因此不提供旧 YAML fallback；若实现前发现已有 GitHub source 数据或外部调用方，应退回“待确认”重新评估兼容范围。
- 全量 Go 套件当前受 `server/pkg/agent` 的 Codex app-server 超短超时测试影响；该范围外失败不阻塞本次 GitHub import 交付，但合并前应由对应模块修复或稳定该测试。
