# Git 仓库与配置导入

GitRepo 管理 Multica 服务端读取 Agent、skill 配置仓库所需的地址、身份和版本。GitHub 与 Alibaba Code 使用同一业务入口；用户输入仓库地址，服务端识别平台并匹配当前工作区连接。

## 模块边界

- `server/internal/gitrepo`：地址规范化、身份选择、GitHub App / Code PAT 认证、分支和 tag、固定 commit 的 tree/blob 读取。`Remote` 不向调用方暴露凭据。
- `server/internal/agentsource`：通过 `gitrepo.Reader` 解析配置目录，使用内置 JSON Schema 校验 manifest，生成 bundle 与文件快照。ZIP 解压和 Git 读取共享解析及写入流程。
- `server/internal/handler/agent_package_create.go`：ZIP 与 Git 预览统一确认创建。
- `server/internal/handler/git_agent_source.go`、`agent_source_preview.go`、`agent_source_sync.go`、`agent_publication.go`：创建、diff、确认发布、发布历史和历史节点回滚。
- `server/internal/handler/git_skill.go`：独立 skill 与模板 skill 引用读取。skills.sh 链接解析到 Git 仓库后走相同读取模块。读取失败、截断树、过大文件、符号链接、子模块不作为完整 skill 写入。
- `packages/core/git-repo`：API 查询和 mutation；共享创建页面、Git 身份设置页面、发布页面同时用于 Web/Desktop。

这不改变 Agent 执行时的 GitHub 身份、账号认证或沙箱网络。Code 访问只发生在 Multica 服务端，token 不发送给 Agent、CLI 或沙箱。

## 身份与权限

`git_connection` 是工作区 Git 身份的唯一存储。GitHub App 安装的授权回调、重用安装、仓库权限更新、卸载 webhook 都读写该表；旧 `github_installation` 表通过一次性迁移移除，没有双写和旧 API 别名。

GitHub 使用 App JWT 换取安装 token；Code 使用人类工作区 owner/admin 绑定的 PAT。绑定时调用 Code 当前用户及目标仓库 API 验证身份和读取权限，然后用 AES-256-GCM 加密保存。重新绑定同一 Code 身份应更新其现有连接，不能借更新 token 更换账号。

连接授权给当前工作区，成员在其业务权限范围内使用。指定的连接必须属于当前工作区且匹配地址平台。自动匹配只有一个身份时使用它；多个匹配身份时要求选择连接。没有 GitHub 连接时允许匿名读取公开仓库；已经选择或自动匹配的连接失败时直接报错，不降级为匿名访问。删除连接保留 Agent 配置、发布历史和来源 ID，并标记来源断开。

服务端需配置 `MULTICA_GIT_REPO_SECRET_KEY`：base64 编码的 32 字节密钥，所有副本一致，重启不能更换。没有配置时 Code 身份绑定和解密不可用，GitHub 功能仍可用。密钥通过安全配置传入；不得放入仓库、manifest、日志或聊天。

Code PAT 应仅授予所需仓库及读取权限；不需要仓库写权限或 token 管理权限。PAT 过期、撤销、仓库授权移除后，下一次读取或确认发布会失败。API 返回账号标签和连接 ID，不返回 token、密文或 GitHub 安装 token。

## 地址和 API

支持 GitHub HTTPS / SSH 仓库地址及 Alibaba Code 的 `code.alibaba-inc.com`、`gitlab.alibaba-inc.com`、`code.aone.alibaba-inc.com`、`code-sc.aone.alibaba-inc.com` 地址。Code 统一记录为 `https://code.alibaba-inc.com/<namespace>/<repo>`，支持嵌套 namespace。不接受其他主机、明文 HTTP、端口、URL 凭据、query、fragment、路径穿越。

Agent manifest 固定在仓库根目录的 `agent.json`，使用仓库根 URL，单独选择 ref。独立 skill 可使用 `ref` / `path` 或 tree/blob 链接；目录必须明确定位一个 `SKILL.md`，斜杠分支按实际 refs 解析，歧义报错。

| API | 输入 / 返回 |
| --- | --- |
| `GET /api/workspaces/{id}/git/connections` | 当前工作区身份公开信息及 token 绑定是否可用 |
| `GET /api/workspaces/{id}/git/repository?repository=URL` | 规范化地址、自动识别的平台、匹配的工作区身份；skill 的 skills.sh 地址也会先解析到 Git 仓库 |
| `POST /api/workspaces/{id}/git/connections` | `repository_url`、`token`，可选 `connection_id` 更新同一身份 |
| `DELETE /api/workspaces/{id}/git/connections/{connectionId}` | 断开工作区身份 |
| `GET /api/workspaces/{id}/git/refs?repository=URL&connection_id=UUID` | 默认分支、分支及 tag；连接 ID 可省略，由服务端匹配 |
| `POST /api/workspaces/{id}/git/agent-preview` | `repository` 为 URL，可选 `connection_id`、`ref`；返回固定 SHA 的 `preview_id` |
| `POST /api/workspaces/{id}/agent-packages` | 仅通过 `preview_id` 确认创建；不再接受直接 SHA 创建 |
| `POST /api/skills/import` | 原 `url` / `on_conflict`，Git 来源可选 `connection_id`、`ref`、`path` |

GitHub App 自身的 OAuth、安装和 PR 集成 API 仍属于 GitHub 适配器；它们不承担 Agent 包业务。旧 `/github/agent-preview`、`/github/branches`、`/github/repositories`、`/github/agents` 路由已移除。

来源响应改为 `source_type: "git"`、`connection_id`、`connected` 和 `repository_url`。Git 发布继续使用 `/api/agents/{id}/source/preview` 与 `/source/sync`，只接受仓库版本；ZIP 来源继续上传 ZIP。

## 发布一致性

分支使用 `refs/heads/<name>`，tag 使用 `refs/tags/<name>`，也可输入 commit SHA。先解析完整 SHA，再读取所有文件；预览时把文件、解析结果、调用者、目标 Agent 和当前配置状态存入 PostgreSQL。确认时重新检查仓库权限和配置状态，应用保存的快照，禁止分支漂移或并发编辑绕过 diff。

发布历史保存 Git 地址、ref、commit、作者、时间、原始文件和发布后配置；回滚读取历史快照并产生新的发布记录。相同预览重复确认具有幂等性。skill 写入继续以 scope + skill ID 确定身份，并保持 Agent 专属关系；不会因 GitRepo 改造变成每次重新创建。

## Code 协议证据与验收

Code API 契约核对自[官方 API 文档](https://pre-code.alibaba-inc.com/doc.html)：项目使用 v3，分支分页使用 v3 的 `{amount,list}`，tag 分页使用 v4 的 `{amount,list}`，tree/blob 使用 v4。PAT 认证遵循[Code PAT 文档](https://aliyuque.antfin.com/alicode/docs/bcuxmsz48ls3e7ov)，只放在 `PRIVATE-TOKEN` 请求头；不跟随重定向，不读取响应提供的下载 URL。读取有超时、分页与内容大小限制。

自动测试使用模拟 HTTP 服务及隔离 PostgreSQL，覆盖 GitHub、Code 创建/发布/历史/回滚、预览固定 SHA、撤权拒绝写入、跨工作区/平台身份拒绝、分支分页和文件读取失败。真实 Code PAT 与预发到内网 Code 的网络连通性需要在预发单独验收。配置包所需的 Git LFS 内容暂不支持，须直接提交文件内容。

当前验证：Go 编译、GitRepo/配置包/托管 Agent 测试、相关 HTTP 与数据库流程、前端类型检查和定向测试、lint、sqlc 生成一致性均通过。全局迁移编号检查仍报告基线中两份 `9093` 的重号；本次 `9261`–`9266` 无重号，up/down 和 GitHub 身份保留已在隔离数据库验证。

## 升级与回滚

迁移 `9261`–`9266` 保留已有连接 UUID，把 Agent 来源与历史中的 GitHub 字段转换成 Git 通用字段，并更新 skill 来源元数据。部分字段被删除，因此不能让旧服务副本继续处理业务流量；应在维护窗口停旧实例、执行迁移，再同步启动新服务和前端。不能用“前后兼容”来替代此次切换。

数据库 down 在尚无 Code 连接、Code 来源及 Code skill 时可把连接表改回旧名，并恢复旧字段。ID 和工作区安装唯一索引在 down 时保留给恢复后的表使用。已有 Code 内容时 down 会拒绝丢弃这些数据，应优先发布修复版本；不要为回滚删用户配置。GitHub App 凭据仍使用既有部署配置。

## 变更记录

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-09-15 | 抽出 GitRepo；GitHub 与 Code 统一连接、地址和配置读取；删除旧 GitHub Agent 路径及独立 skill 下载分支 | 让用户只提供仓库地址，同时消除多套凭据与发布逻辑 |

- 2026-09-15 发布准备：本次迁移编号顺延至 9261–9266，避开预发集成分支已存在的迁移；这六份迁移尚未发布，无需旧编号兼容。
