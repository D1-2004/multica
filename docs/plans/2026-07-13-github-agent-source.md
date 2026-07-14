# 从 GitHub 仓库创建智能体实施计划

> 工作流：grill-and-plan
> 状态：已完成
> 创建日期：2026-07-13
> 计划 ID：20260713-github-agent-source
> 最后更新时间：2026-07-13 14:23
> 当前分支：codex/github-agent-source
> 目标执行分支：codex/github-agent-source
> 基线 Commit：22b0c8a1125c9bc662b1aa57540c99ec3249f880
> 原始工作区：/Users/fanqi/test/code/dt-fde-multica
> Worktree 路径：不使用
> Worktree 来源：不使用
> 交付状态：已本地提交（与实现同一提交）
> 收尾状态：已完成；未 push、未部署、未创建 PR
> 当前里程碑：里程碑十一——完整验证与收尾（已完成）

## 背景与现状证据

- Multica 当前把智能体的可执行配置拆在 `agent`、`skill`、`skill_file` 和 `agent_skill` 中；创建接口要求一个明确的 `runtime_id`，所以一个智能体实例仍绑定一个运行时。
- task 被守护进程 claim 时，服务端读取当时活动的 `agent.instructions` 和已绑定 skill bundle；已经 claim 的 task 不会因为之后的配置更新而热切换，尚未 claim 的 task 会读到更新后的活动配置。
- 创建工作室当前已有空白、模板和 AI 辅助三种入口，GitHub 创建适合作为第四种入口，并在最终提交前复用现有运行时、模型和访问权限配置面板。
- 仓库已有 GitHub App installation、App JWT、setup 回调和 webhook 基础，但当前 App 文档只要求 Pull requests 与 Metadata 只读权限；尚未实现 installation access token、可访问仓库列表和私有仓库 Contents 读取。
- 现有 GitHub skill 导入走匿名 GitHub API 或部署级 `GITHUB_TOKEN`，不能作为工作区私有仓库的授权边界；新功能必须使用工作区绑定的 GitHub App installation。
- 现有 skill 导入已经提供文本/二进制识别、路径校验以及单文件、文件数和 bundle 大小限制，可提取成共用的 GitHub bundle 校验能力，但不能让工作区全局 token 或任意 URL 绕过 installation 授权。
- 当前工作区干净，`develop` 与 `origin/develop` 同步，计划基线为 `22b0c8a1`。

## 目标

1. 在创建智能体时支持从工作区已连接 GitHub App 可访问的仓库创建。
2. 使用稳定、严格的 `multica-agent.yaml` 协议解析一个仓库中的智能体名称、描述、通用 instructions、skills 及其文本 supporting files/scripts。
3. 让仓库定义保持与具体运行时无关；用户在 Multica 中选择一个运行时，Multica 沿用现有 provider 适配层把通用 instructions/skills 物化为 `CLAUDE.md`、`AGENTS.md` 和各 provider skill 目录。
4. 保存 GitHub 来源、ref 和最后一次成功同步的 commit SHA，同时继续用现有 `agent`/skill 表承载当前活动快照。
5. 提供显式手动同步：只有新仓库版本完整获取、解析、校验并在数据库事务中更新成功后，才切换活动快照；失败继续使用旧快照。
6. 为将来“智能体通过分支/PR 迭代自己的定义仓库”保留稳定来源和版本边界，但本次不授予 GitHub 写权限。

## 非目标

- 不把智能体定义仓库作为普通 task 的工作目录，也不改变项目业务仓库 checkout 流程。
- 不处理 task 产物、报告或其他文件回写 GitHub/钉钉。
- 不实现 push webhook 自动同步、定时同步、自动合并或智能体自我修改仓库。
- 不实现一个智能体实例跨多个 `runtime_id` 动态调度、运行时池或负载均衡；同一来源可重复创建多个绑定不同运行时的智能体实例。
- 不实现 `agent_revision` 历史表、task 入队时 revision 固定、版本差异页面或一键回滚；首版版本边界仍是 task claim 时读取活动快照。
- 不支持 provider-specific instructions overlay、多个智能体共用一个仓库、子目录 manifest、Git submodule、Git LFS、symlink、二进制 skill 资产或任意根目录脚本自动执行。
- 不实现从 Multica 编辑并反向写回 GitHub；仓库管理字段保持单向 GitHub → Multica。
- 不部署、不修改 Aone 预发配置、不 push、不创建 PR。

## 已确认需求

- GitHub 保存的是 Multica 智能体的配置源码，不是运行位置。
- 智能体继续运行在所选 Multica 运行时/沙箱内。
- 仓库可以包含 instructions、skills、工具脚本和参考文件。
- 一个 GitHub 定义应可被多个不同 provider 的运行时复用，具体 `runtime_id` 由 Multica 创建流程选择。
- 仓库发生变化时不实时影响执行；Multica 使用最后一次成功同步的活动快照。
- 已运行或已 claim 的 task 不热切换；同步后的新 claim 使用新活动快照。
- 架构需保留将来由智能体通过受控 GitHub 分支/PR 修改自己定义仓库的扩展能力。
- 计划确认后从当前基线创建独立功能分支再开始实现。

## 执行假设

- 首版强制仓库根目录存在 `multica-agent.yaml`；这项约定是未来自我迭代、严格校验和多运行时兼容的机器协议，不提供“猜测 AGENT.md/CLAUDE.md”的隐式 fallback。
- 一个仓库首版只定义一个智能体；`spec.instructions` 指向一份 provider-neutral 文本文件，推荐命名 `AGENT.md`。
- `spec.compatibility.providers` 是可选 allow-list；缺省表示支持所有 Multica 已知 provider，填写后所选运行时 provider 必须在列表内。
- 仓库字段拥有 `name`、`description`、`instructions` 和来源管理的 skills；Multica 字段拥有 `runtime_id`、model、thinking level、访问权限、并发、env、MCP 与第三方授权。
- GitHub 来源创建和私有仓库预览仅允许工作区 `owner/admin`，避免把 GitHub App 覆盖的私有仓库名称和内容暴露给所有普通成员；后续若需要开放给 member，必须另做仓库级授权设计。
- 首版只使用 GitHub App installation token，不接受用户 PAT、部署级 `GITHUB_TOKEN` 或任意未经 installation 验证的仓库 URL。
- 手动同步只由智能体 owner 或工作区 `owner/admin` 触发；GitHub 来源断开后，最后一次活动快照仍可执行，但同步入口显示断开且拒绝更新。
- 来源管理的 skill 为该来源独占，不允许被其他智能体手工绑定或通过普通 skill 编辑接口修改；普通手工 skill 仍可附加到 GitHub 来源智能体，且同步不会删除它们。
- 本次把 GitHub 能力实现为隔离的适配层，不引入通用 `AgentSpec`、`agent_revision`，也不重构空白、模板或 AI Builder 创建路径；只抽取维持既有校验与原子创建所需的窄 helper。

## 关键设计决定

### 1. GitHub 是源码，数据库活动记录是可执行快照

- 派发路径永远读取数据库，不在每次 task claim 时访问 GitHub。
- 首次创建和手动同步都先在事务外固定 ref 对应的 commit SHA、下载并完整校验 bundle，再在短事务内原子落库。
- GitHub 不可用、限流、权限撤销、manifest 错误或 skill 冲突都不能破坏上一个成功快照。
- 同步成功后更新 `synced_commit_sha`；运行中/已 claim task 保持旧 payload，尚未 claim task 和新 task 使用新活动快照。

### 2. Manifest v1 协议

首版协议固定为：

```yaml
apiVersion: multica.ai/v1alpha1
kind: Agent

metadata:
  name: code-reviewer
  description: Review code and identify correctness and security risks

spec:
  instructions: AGENT.md
  skills:
    - path: skills/code-review
    - path: skills/security-check
  compatibility:
    providers:
      - claude
      - codex
```

- 使用 `yaml.v3` strict decode（unknown field 报错），校验固定的 `apiVersion`/`kind`、名称和描述长度、相对路径、重复路径与 provider token。
- `instructions` 文件必须存在且为 UTF-8 文本。
- 每个 skill 目录必须包含唯一 `SKILL.md`；目录内其他安全文本文件作为 `skill_file`，脚本不会在导入阶段执行。
- 限制：最多 20 个 skills、单文件 1 MiB、单 skill 200 个 supporting files/8 MiB、整个智能体 bundle 32 MiB；二进制文件跳过并返回 warning，submodule/symlink/LFS pointer 拒绝。
- 首版不读取根目录 `CLAUDE.md`/`AGENTS.md` 作为 provider overlay；Multica 现有 daemon 适配层仍负责生成 provider 原生运行配置。

### 3. 来源与来源 skill 的数据模型

新增 fork-owned、双向、可重放的迁移（当前序列使用 `169_github_agent_source`）：

```text
agent_source
- id UUID PK
- agent_id UUID UNIQUE FK agent ON DELETE CASCADE
- source_type TEXT CHECK github
- github_installation_id UUID NULL FK github_installation ON DELETE SET NULL
- repo_owner TEXT
- repo_name TEXT
- ref TEXT
- manifest_path TEXT DEFAULT multica-agent.yaml
- synced_commit_sha TEXT
- sync_status TEXT CHECK ready/failed/disconnected
- last_sync_error TEXT NULL
- last_sync_attempt_at TIMESTAMPTZ NULL
- last_synced_at TIMESTAMPTZ
- created_by UUID NULL FK user ON DELETE SET NULL
- created_at / updated_at

agent_source_skill
- agent_source_id UUID FK agent_source ON DELETE CASCADE
- skill_id UUID UNIQUE FK skill ON DELETE CASCADE
- source_path TEXT
- PRIMARY KEY (agent_source_id, source_path)
```

- `agent_source` 只记录来源和同步账本，不复制 runtime、model、权限或 secrets。
- `agent_source_skill` 明确哪些 skill 受仓库管理，避免同步误删用户手工附加的 skill，并支持按 source path 幂等更新/删除。
- 来源 skill 仍物化到现有 `skill`/`skill_file`/`agent_skill`，因此无需改变 daemon claim 和 provider skill materialization。
- 普通 skill API 和选择器必须识别来源管理状态：禁止普通更新/删除/跨智能体绑定，UI 显示只读来源标签。
- workspace 内既有 `UNIQUE(workspace_id, name)` 继续生效；来源 skill/智能体名称冲突时预览给出 blocker，创建或同步失败，不静默改名、不覆盖手工对象。

### 4. GitHub App 授权边界

- 抽出 GitHub App client：用现有 App JWT 调用 installation access-token endpoint，token 只驻留服务端内存且不写日志/数据库/响应。
- 通过目标 workspace 的 `github_installation` 内部 UUID 解析 numeric installation id；仓库列表、ref 解析、tree/blob 下载都使用该 installation token。
- repository full name 必须来自该 installation 可访问仓库列表或由 GitHub API 对 token 验证成功，不能仅信任客户端 URL。
- GitHub App 需要新增 Repository Contents: Read-only；现有 Pull requests/Metadata 权限保持只读。本次不申请 Contents write。
- `GITHUB_APP_ID` 和 `GITHUB_APP_PRIVATE_KEY` 对现有 PR integration 仍可选，但 GitHub 智能体来源能力在缺失时必须明确报告 unavailable，不能退化到匿名 API 或全局 token。
- 所有 GitHub HTTP 调用设置 context timeout、分页上限、响应体上限和安全的 User-Agent；鉴权 header 只能发往配置的 GitHub API base，测试覆盖不向 raw/第三方 host 泄露 token。

### 5. API 与事务边界

新增工作区级 API（owner/admin）：

- `GET /api/workspaces/{id}/github/repositories`：分页列出 installation 可访问仓库，返回内部 installation binding id、full name、private、default branch；不返回 numeric installation id/token。
- `POST /api/workspaces/{id}/github/agent-preview`：输入 installation binding、repo、ref，解析 ref 为 SHA，返回 manifest 预览、skill 摘要、兼容性与 warnings/blockers，不写数据库。
- `POST /api/workspaces/{id}/github/agents`：输入相同来源坐标、preview SHA 和 Multica 管理字段；服务端按固定 SHA 重新读取/校验，不信任客户端回传 instructions/skill 内容，事务化创建 agent、source、skills、files、mappings 和访问目标。

新增智能体级 API：

- `GET /api/agents/{id}/source`：按现有 agent 可见性返回去敏后的来源、SHA 和同步状态。
- `POST /api/agents/{id}/source/sync`：agent owner 或 workspace owner/admin 手动同步；同步只更新仓库拥有字段和来源 skills，不改 Multica 拥有字段。

创建逻辑需要抽取窄的内部事务 helper，让普通创建与 GitHub 创建共用 runtime/permission/model/skill 约束，避免复制一份会漂移的 `CreateAgent` 校验；不改变现有 `/api/agents` 请求兼容性。

预览和创建之间用 `resolved_sha` 防 ref 漂移：创建请求携带预览 SHA，服务端固定读取该 SHA；若 ref 头已经前进，仍创建预览过的版本并在响应提示有更新，而不是悄悄换内容。

### 6. 同步并发与一致性

- 网络读取不持有数据库事务或行锁。
- 开始读取时记录 expected `synced_commit_sha`；下载并校验候选 SHA 后开启事务并 `SELECT ... FOR UPDATE` 来源行。
- 若当前 SHA 已等于候选 SHA则幂等成功；若当前 SHA 不再等于 expected SHA，返回 conflict/要求刷新，阻止较慢的旧同步覆盖较新的同步。
- 事务内按 `source_path` upsert 来源 skills/files，删除仓库已移除的来源 skills，保留普通手工 skill，再原子更新 agent 仓库字段和 `synced_commit_sha`。
- 任何 SQL、名称冲突或校验失败回滚整个候选版本，并在独立、最小失败状态更新中记录 attempt/error；活动数据和旧 SHA 不变。

### 7. UI 与字段所有权

- 创建工作室增加 GitHub 模式卡片：连接状态 → 仓库/ref 选择 → 预览 → 运行时/model/访问权限配置 → 创建。
- 没有 installation、缺 App credentials/Contents 权限或当前用户不是 owner/admin 时显示明确引导，不展示不可用的空 picker。
- 预览显示 commit 短 SHA、名称、描述、instructions、skills、兼容 provider、跳过文件 warnings 和 blocker。
- agent 详情页增加来源卡片：仓库链接、ref、当前 SHA、同步时间/状态、“检查并同步”操作。
- GitHub 管理的 name/description/instructions/source skills 在 UI 只读并标注来源；runtime/model/access/env/MCP 等继续可编辑。
- 后端同样拒绝通过普通 UpdateAgent/Skill API 改写来源管理字段，不能只依赖 UI。
- 为 `en`、`zh-Hans`、`ja`、`ko` locale 增加一致文案，遵循仓库术语规范。

## 被排除的方案

1. **每次派发直接读取 GitHub**：GitHub 故障会阻断执行、无法复现、会把私有 token 带入热路径，排除。
2. **创建时复制后丢弃来源**：无法同步、自我迭代、审计或判断当前 SHA，排除。
3. **把来源塞进 `agent.runtime_config`**：该字段属于 provider runtime 配置，混入 GitHub 授权和同步状态会破坏边界且难以查询/加 FK，排除。
4. **使用部署级 `GITHUB_TOKEN` 或用户 PAT**：不能表达 workspace installation 授权，私有仓库隔离错误，排除。
5. **没有 manifest 时猜测文件**：多 provider、多个 skill 和未来自我修改都会产生歧义，首版不提供 fallback。
6. **先调用普通 CreateAgent，再逐个导入 skill**：会暴露部分创建状态且客户端可篡改预览内容，排除。
7. **push 后立即自动热更新**：错误提交会直接改变执行，且本次没有 revision/evaluation gate，排除。
8. **首版加入 `agent_revision`**：长期正确但会同时扩大 task enqueue、claim、历史与回滚模型；本次用活动快照 + source SHA 保留后续演进路径。

## 复杂度与执行路由

- 规划复杂度：P3
- 执行复杂度：E1
- 判断依据：涉及新数据模型和迁移、GitHub 私有仓库安全边界、公共 API、跨 Go/SQL/TypeScript/UI 的原子创建与同步，以及失败恢复；各部分共享同一来源/快照语义，拆给多个 agent 容易造成字段和事务漂移。
- 主执行者：主 agent 在确认后的功能分支连续实现。
- Subagent 数量与职责：0；不启动 subagent。
- Review 安排：E1 主 agent 自检；完成前使用 `verify-before-finish`，不启动独立 reviewer。
- Worktree：不需要；当前工作区干净，用户明确要求确认后拉功能分支，工作强耦合且无需与另一分支并行。
- Worktree 来源与清理责任：不适用。
- 分支策略：确认后从基线 `22b0c8a1` 在当前工作区创建 `codex/github-agent-source`；创建前再次核对 `develop`、基线和工作区状态，不自动 rebase/pull。
- Commit 策略：由主 agent结合里程碑和最终状态自主判断，采用 Conventional Commit；不为计划文件单独提交。

## 文件与职责

预计范围，执行中按现有模块组织调整文件名，但不改变职责：

- `server/migrations/169_github_agent_source.{up,down}.sql`：来源及来源 skill 映射，双向且可重放。
- `server/pkg/db/queries/agent_source.sql`、`skill.sql`、`agent.sql`：来源读取/锁定/CAS、来源 skill 管理和共享创建 helper 所需 query；随后运行 sqlc。
- `server/internal/githubapp/` 或现有 GitHub handler 的可复用内部包：App JWT、installation token、repo/ref/tree/blob client，避免 handler 和 skill importer 相互依赖。
- `server/internal/agentsource/`：manifest 类型/strict parser、路径与大小校验、GitHub bundle compiler、同步 diff 与候选快照。
- `server/internal/handler/github_agent_source.go`：仓库列表、预览、创建、读取来源、手动同步和权限边界。
- `server/internal/handler/agent.go`、`skill.go`：提取共享创建事务能力，执行来源字段/来源 skill 写保护。
- `server/cmd/server/router.go`：注册 workspace/agent 路由及 owner/admin/member 边界。
- `packages/core/types/{agent,github}.ts`、`api/client.ts`、`api/schemas.ts`、`github/queries.ts`：请求/响应类型、Zod 兼容解析、API client、Query keys/options。
- `packages/views/agents/components/agent-creation-studio.tsx` 及拆出的 GitHub chooser/preview 组件：第四种创建模式和配置确认。
- `packages/views/agents/components/agent-detail-page.tsx` 或现有详情 tab：来源状态与手动同步。
- `packages/views/skills/`：来源管理标记、只读状态、通用 picker 过滤。
- `packages/views/locales/{en,zh-Hans,ja,ko}/agents.json` 与必要的 skills/settings 文案：多语言 UI。
- `.env.example`、`apps/docs/content/docs/github-integration*.mdx`、`environment-variables*.mdx`：Contents 权限、App credentials 和功能开关/可用性说明。
- 邻近 `*_test.go`、`*.test.ts(x)`：解析、授权、事务、并发、API 漂移和 UI 测试。

## 实施步骤

- [x] 里程碑一：确认计划并建立执行环境。核对计划、Git 状态、基线和未提交 diff；把状态更新为“已确认”，创建并切换到 `codex/github-agent-source`，验证后更新为“执行中”。
- [x] 里程碑二：落地数据模型与 sqlc。添加可重放的 up/down migration、`agent_source`/`agent_source_skill` query、来源行锁/CAS 和来源 skill ownership query，运行 sqlc 与 migration lint/tests。
- [x] 里程碑三：实现 GitHub App 只读 client。覆盖 installation token、仓库分页、ref→commit SHA、commit→tree SHA、tree/blob 读取、timeouts/body limits/token redaction，并用 `httptest` 验证缓存、401 重试、分页、限流和跨 origin 不泄漏 token。
- [x] 里程碑四：实现 manifest 与 bundle compiler。strict YAML、provider 兼容、路径/对象类型/UTF-8/容量限制、重复 skill name、skill 解析和稳定排序/hash，测试覆盖关键失败边界。
- [x] 里程碑五：实现预览与原子创建 API。按已确认的旁路边界复用现有校验 helper，不重构普通创建；创建 endpoint 固定 preview SHA 重新编译，事务内创建 agent/source/source skills/访问目标。
- [x] 里程碑六：实现来源读取、字段写保护与手动同步。加入 expected-SHA CAS、幂等 no-op、来源 skill 增删改、普通 skill 保留、断开 installation、失败保留旧快照以及既有创建/模板/本地导入入口的 ownership 防绕过。
- [x] 里程碑七：接入 core API 与兼容解析。新增类型/query/client/Zod schemas 和 malformed-response fallback 测试，旧后端 404 时现有详情保持可用。
- [x] 里程碑八：实现创建工作室 GitHub 模式。完成安装状态、管理员边界、仓库/ref 选择、预览、blocker/warning、运行时兼容校验和提交；补 provider allow-list 交互逻辑测试。
- [x] 里程碑九：实现详情来源卡片和来源 skill 只读体验。展示 repo/ref/SHA/同步时间/状态/错误，保留 Multica 管理字段编辑，并在同步后通过实时事件失效 agent/source/skill cache。
- [x] 里程碑十：更新配置与文档。说明 GitHub App Contents: Read-only、App ID/private key 对本能力的要求、manifest 示例、活动快照与手动同步语义；同步四语种产品文案。
- [x] 里程碑十一：完整验证与收尾。使用 `verify-before-finish` 对照验收标准，完成定向 Go 测试、core/views 全量测试和类型检查、格式/JSON/diff 检查；记录全仓 Go runtime 测试与 docs typecheck 的环境限制后本地提交。

## 执行记录

| 里程碑 | 状态 | 关联 Commit（可选） | 实际验证命令 | 结果与证据 |
|---|---|---|---|---|
| 计划确认与环境准备 | 已完成 |  | `git status --short --branch`; `git rev-parse --abbrev-ref HEAD`; `git rev-parse HEAD` | 已切换到 `codex/github-agent-source`，HEAD 保持基线 `22b0c8a1125c9bc662b1aa57540c99ec3249f880`，只有计划文件未跟踪 |
| 数据模型、GitHub client 与 compiler | 已完成 | 同最终提交 | sqlc generate；`go test ./internal/githubapp ./internal/agentsource ./internal/migrations` | 迁移 lint、generated queries、token/ref/tree/blob client 与 strict compiler 均通过 |
| 创建、同步与写保护 | 已完成 | 同最终提交 | `go test ./internal/handler ./cmd/server` | handler 与 router 编译/测试通过；普通创建、模板复用、本地覆盖导入均加入来源 ownership 防绕过 |
| Core 与 Views | 已完成 | 同最终提交 | `pnpm --filter @multica/core test`; `pnpm --filter @multica/views test`; 两包 `typecheck` | core 81 files/844 tests；views 185 files/1860 tests；两包 TypeScript 检查通过 |
| 配置、文档与多语言 | 已完成 | 同最终提交 | `jq empty ...agents.json`; `git diff --check` | 四语种 JSON 与 diff 格式检查通过；docs typecheck 因本轮过滤安装未包含 docs node_modules 而未执行 |

## 验证策略

| 改动或验收项 | 风险 | 验证方式 | 是否测试先行 |
|---|---|---|---|
| up/down migration 与 FK 删除语义 | 高 | migration lint、空库 up、down、再次 up；sqlc 编译；删除 installation 后 source 保留快照 | 是 |
| installation token 与私有仓库隔离 | 高 | `httptest` 断言 JWT/token exchange、workspace installation guard、分页、401/403/429、header host 限制 | 是 |
| manifest strict parser 与路径安全 | 高 | 表驱动测试覆盖 unknown fields、`..`/绝对路径、重复 skill、symlink/submodule/LFS、容量边界、provider allow-list | 是 |
| 预览 SHA 与创建 ref 漂移 | 高 | 模拟 preview A、ref 前进 B，断言创建仍固定 A 并提示更新 | 是 |
| 原子创建 | 高 | 注入 skill/file/target 写失败，断言 agent/source/skill 均不残留 | 是 |
| 同步 CAS 与旧快照保护 | 高 | 并发候选 B/C、失败回滚、同 SHA no-op、删除来源 skill、保留普通 skill | 是 |
| 来源字段和 skill 写保护 | 高 | handler 权限测试与 UI disabled/filtered 测试；普通 API 不能绕过 | 是 |
| 多运行时适配 | 中 | manifest provider allow-list 测试；选择 Claude/Codex/Kimi 等 runtime 时仅校验 provider，不复制 provider-specific 文件 | 是 |
| API response 漂移 | 中 | Zod malformed/缺字段 fallback 测试 | 是 |
| 创建与同步 UI | 中 | Vitest/Testing Library 覆盖无 installation、loading、preview blocker、创建成功、同步成功/失败 | 是 |
| 全仓兼容 | 高 | `gofmt`、`make sqlc`、`make test`、`pnpm typecheck`、`pnpm test`、`make check` | 否，最终整合验证 |

## 风险与回滚

- **GitHub App 权限升级**：现有 App 没有 Contents 权限时新入口不可用，但原 PR integration 必须继续可用。UI/API 返回明确 capability 状态；不做匿名 fallback。
- **私有仓库泄露**：首版 repo list/preview/create 限 owner/admin，响应不含 token/numeric installation id，日志不含文件正文或鉴权 header。
- **来源 skill 名称冲突**：预览 blocker + 数据库唯一约束双层保护；不覆盖、不自动 rename。
- **同步部分成功**：候选下载在事务外，活动切换在事务内；任何失败回滚并保留旧 SHA/旧 bundle。
- **并发同步倒退**：expected-SHA + 行锁 CAS 阻止慢请求覆盖新版本。
- **断开 GitHub installation**：FK `SET NULL`，活动快照继续执行；同步显示 disconnected。重新连接不自动猜测绑定，首版需要重新选择/恢复来源绑定的明确操作，如执行中发现缺少安全恢复接口则停在失败状态而不自动串 installation。
- **迁移回滚**：应用回滚到旧版本后新表为纯附加，不影响旧读取路径；需要数据库 down 时先确认无需保留来源账本，再删除映射/来源表。down 会丢失来源和同步信息，但不会删除现有 agent/skill 活动快照。
- **功能代码回滚**：旧服务忽略新表，现有普通创建和执行继续工作；来源智能体退化成普通已物化智能体，仍能运行最后快照。

## 发布边界

- Push：本地 commit 后告知“已提交，准备 push”；用户明确同意后直接执行。
- PR / 合并 / 部署 / 发布：未授权，除非用户另行明确确认。
- 新能力以服务端成功构造只读 GitHub App client 为 capability gate：缺少 App ID/private key 时 API 明确返回 unavailable，前端显示错误，不回退匿名 API 或部署级 token；未另增功能开关。
- Aone 预发部署和真实数据库验证不在本计划授权内；若后续要求部署，使用 `aone-deploy` skill，并在部署前单独确认。

## 计划变更记录

| 日期 | 变更 | 原因 | 是否重新确认 |
|---|---|---|---|
| 2026-07-13 | 初版计划 | 将已讨论的 GitHub 配置源码、活动快照、多运行时适配与未来自我迭代边界落档 | 是，等待首次确认 |
| 2026-07-13 | 用户确认计划，进入执行环境建立阶段 | 用户明确要求拉分支并开始执行 | 否 |
| 2026-07-13 | 明确采用隔离的 GitHub 适配层，不新增通用 AgentSpec/revision 架构 | Multica 不会长期作为通用 Agent 管理工具，避免无收益的全面重构 | 否，用户确认继续原计划 |
| 2026-07-13 | GitHub 创建保持独立事务 handler，仅复用现有权限、runtime、访问目标等窄 helper | 落实已确认的“当前可接受旁路”，避免为单一来源重构普通创建/模板/AI Builder | 否，属于已确认架构边界的实现细化 |
| 2026-07-13 | 最终验证以变更相关 Go 包和 core/views 全量测试为交付门禁 | 全仓 runtime 子进程测试在本机并发运行时出现既有 5 秒 timeout；docs 依赖未在过滤安装范围内 | 否，已记录未验证项与复现证据 |

## 最终验证结果

| 验收项 | 验证命令或检查 | 结果 | 证据摘要 |
|---|---|---|---|
| GitHub App client 与安全边界 | `GOCACHE=/private/tmp/multica-go-build go test ./internal/githubapp` | 通过 | 覆盖 installation token 缓存/401 刷新、分页、429、slash ref、commit→tree SHA 与跨 origin redirect |
| Manifest/bundle compiler | `GOCACHE=/private/tmp/multica-go-build go test ./internal/agentsource` | 通过 | strict YAML、路径、symlink/LFS、binary warning、重复 skill name、manifest-aware hash |
| Handler/router/migration | `GOCACHE=/private/tmp/multica-go-build go test ./internal/handler ./cmd/server ./internal/migrations` | 通过 | 新 API、配置装配、路由与 migration lint 编译通过 |
| Core schemas/realtime | `pnpm --filter @multica/core test`；`pnpm --filter @multica/core typecheck` | 通过 | 81 test files / 844 tests；Zod fallback 与 source cache invalidation 覆盖 |
| Views | `pnpm --filter @multica/views test`；`pnpm --filter @multica/views typecheck` | 通过 | 185 test files / 1860 tests；创建模式、详情与既有 UI 回归通过 |
| 格式与本地静态检查 | `git diff --check`；`jq empty` 四语种 locale；changed Go files `gofmt -l` | 通过 | 无 whitespace error，locale JSON 有效，变更 Go 文件已格式化 |
| 全仓 Go 测试 | `HOME=/private/tmp/multica-home GOCACHE=/private/tmp/multica-go-build go test ./pkg/agent`；先前 `go test ./...` | 环境性未通过 | 未改动的多 runtime 子进程测试在并发下集中触发 5 秒 timeout；抽取 `TestCursorExecuteStopsAfterTerminalResult` 单跑 1.62 秒通过；本次相关 Go 包全部通过 |
| Docs typecheck | `pnpm --filter @multica/docs typecheck` | 未执行完成 | 本轮只安装 core/views/ui 依赖，`apps/docs/node_modules` 缺失，命令以 `fumadocs-mdx: command not found` 退出；MDX 改动为纯文案且已人工检查 |

### 最终工作区

- 原始工作区与分支：`/Users/fanqi/test/code/dt-fde-multica` / `develop`（基线 `22b0c8a1`）
- 最终工作区与分支：`/Users/fanqi/test/code/dt-fde-multica` / `codex/github-agent-source`
- 交付状态：实现与计划同一 local commit 交付；未 push
- Worktree 收尾：不适用
- 当前未提交改动：无
- 未执行的验证：未做真实 GitHub App/private repository 联调、真实数据库 migration up/down、Aone 部署；docs typecheck 因依赖未安装未完成

## 遗留风险

- 首版没有 immutable `agent_revision` 与 `agent_task_queue.agent_revision_id`，所以已入队但未 claim 的 task 会使用同步后的新活动快照；未来做自动同步/自我迭代前应补 revision 固定、评测 gate 和回滚。
- 首版只声明 provider 兼容，不探测运行时镜像中 `git`、`rg`、Node 等命令能力；skill 脚本是否可执行仍由仓库作者和所选 runtime 镜像共同保证。
- GitHub App 将来若要让智能体创建分支/PR，需要单独提升 Contents/Pull requests 写权限并引入最小权限工具、审批与评测策略，本计划刻意不提前申请。

## 系统边界与职责

| 组件 | 负责 | 不负责 |
|---|---|---|
| GitHub 仓库 | 版本化 manifest、instructions、skills 和文本脚本 | 运行 task、保存 Multica secrets/权限/runtime_id |
| GitHub App client | 工作区 installation 鉴权和只读仓库内容获取 | 把 token 暴露给浏览器/守护进程或写 GitHub |
| Agent source compiler | 把固定 commit 编译为可校验候选 bundle | 直接写数据库或执行脚本 |
| Agent source service/handler | 权限、预览、原子创建、同步 CAS、失败状态 | task 执行和 provider 文件布局 |
| `agent`/skill 活动表 | 当前可执行快照 | Git 历史和候选版本历史 |
| `agent_source` | 来源、ref、活动 SHA、同步状态 | runtime/model/access/env/MCP |
| daemon/execenv | 把活动 snapshot 适配到当前 provider 并执行 | 访问 GitHub 同步智能体源码 |

## 关键数据流与状态归属

```text
GitHub installation
  → installation token（仅服务端内存）
  → repo/ref 解析为 immutable SHA
  → manifest + tree/blob 下载
  → strict compiler 生成候选 snapshot
  → preview（只读）
  → create/sync 短事务
      ├─ agent 仓库字段
      ├─ source-managed skills/files
      ├─ agent_source_skill ownership
      └─ agent_source.synced_commit_sha
  → task claim 读取数据库活动 snapshot
  → daemon 按 provider 物化并在沙箱执行
```

- GitHub ref/head 属于外部可变状态。
- `synced_commit_sha` 是 Multica 当前活动源码版本的权威指针。
- `agent`/skill 内容是执行权威；只有成功同步事务能改变它们。
- runtime/model/access/env 等 Multica 管理字段不从 manifest 覆盖。

## 接口与兼容性

- 新 endpoint 与新响应全部使用独立类型，不向现有 `CreateAgentRequest` 强塞 GitHub token/source bundle。
- 现有普通创建、模板创建、AI Builder、移动端和旧 desktop 客户端行为不变。
- 前端所有新网络响应通过 Zod `parseWithFallback`；新 agent source 字段为独立查询或 optional，旧后端返回 404/unavailable 时隐藏来源 UI。
- server enum switch 提供 default/error 分支；manifest provider token 使用现有 runtime provider 集合校验，未知 provider 不静默接受。

## 数据迁移与幂等

- migration 只新增表/索引/FK，不重写现有 agent/skill 数据，不需要 backfill。
- 按 Aone fork 规则使用当前最新号之后的 fork-owned migration，up 使用 `IF NOT EXISTS`/可重复安全语句，down 与 up 对称；不依赖扩展或特殊 opclass。
- sqlc 生成物与 query 同一提交交付。
- 同一 source path 的 skill 通过复合主键幂等；同一 agent 仅允许一个 source；同一候选 SHA 同步为 no-op。

## 失败模式与恢复

| 失败 | 用户可见结果 | 活动快照 | 恢复 |
|---|---|---|---|
| App credentials 缺失 | 功能不可用提示 | 不变 | 运维配置后重试 |
| Contents 权限不足/installation 断开 | 403/disconnected | 不变 | 更新 App 权限或重新绑定 |
| GitHub timeout/rate limit | 同步失败与可重试错误 | 不变 | 稍后手动重试 |
| manifest/路径/容量错误 | preview blocker 或 sync failed | 不变 | 修复仓库后重试 |
| 名称冲突 | 明确冲突对象 | 不变 | 修改仓库名称或清理冲突 |
| SQL/事务失败 | 创建无残留；同步回滚 | 创建无；同步保持旧版 | 修复服务后重试 |
| 并发 stale sync | 409 conflict | 保持较新版本 | 刷新后重试 |

## 并发与一致性

- preview 无写入，可并发。
- 创建使用固定 SHA，GitHub ref 前进不会改变已确认内容。
- 同步候选获取与 DB 事务分离，避免长事务；最终用 source 行锁 + expected SHA CAS 序列化活动切换。
- task claim 与同步事务按数据库提交时序看到完整旧快照或完整新快照，不允许半套 skills；已经组装的 claim payload 不回写。

## 可观测性

- 结构化日志记录 workspace/agent/source/repo/ref/SHA、阶段、耗时、文件/skill 数和错误类别；不记录 token、文件正文、env 或私有配置。
- API 响应区分 `unavailable`、`forbidden`、`not_found`、`invalid_manifest`、`bundle_limit`、`name_conflict`、`stale_sync`，UI 提供可行动提示。
- `agent_source` 保存最后 attempt、成功时间和去敏错误；详情页可直接诊断当前活动 SHA 与失败原因。
- 若现有 metrics 结构适合，增加 preview/create/sync result counter 和 GitHub fetch latency；否则首版以结构化日志与 DB 状态为验收，不为指标引入独立框架。

## 发布策略与功能开关

- 代码与 additive migration 可先部署，GitHub 智能体入口由 capability/flag 保护。
- 启用顺序：配置 App ID/private key → GitHub App 增加 Contents read-only 并让 installation 接受新权限 → 启用 capability/flag → 用测试仓库完成 preview/create/sync smoke。
- 未满足权限时不影响既有 GitHub PR webhook 功能。
- 本任务只交付代码与文档，不执行上述生产/Aone 配置步骤。

## 回滚步骤与触发条件

- 触发：发现私有仓库越权、token 泄露、同步覆盖错误或普通创建/执行回归时立即关闭 capability/flag。
- 关闭后：禁止新 preview/create/sync，已有来源智能体继续使用最后活动快照执行。
- 应用回滚：部署旧版本即可忽略 additive source 表；确认业务稳定后再决定是否保留表用于审计。
- 数据库 down：仅在明确放弃来源账本后执行；down 删除 mapping/source，不删除已有 agent/skill 快照。任何线上 down/部署都需要另行授权。
