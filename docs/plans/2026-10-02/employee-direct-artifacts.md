# Direct 文件产物实施计划

**Goal:** Direct 无需 Issue/Chat，也能上传、持久引用、私有读取并清理真实文件产物。

**Architecture:** 复用 attachment、现有 Storage、CLI task_id 上传和附件下载接口。新增 employee_task_artifact 记录可信 queue/run/task/场域来源、上传状态与幂等身份；对象通过现有服务器 secretbox 主密钥加密，认证 API 代理解密。只在对象上传与数据库 ready 状态均完成后返回稳定 ArtifactRef。

**Tech Stack:** Go、pgx/PostgreSQL、现有 Storage/secretbox、真实 PostgreSQL 与本地 fake 对象存储测试。

执行方式：本代理 TDD；Root 调度独立复审。不提交或推送。已验收隐私和记忆模块保持冻结，调用其既有授权函数。

## 已确认边界

- 采用既有附件表加 Direct 来源绑定；不创建新的对象存储或独立文件平台，不强制挂 Issue。
- 使用 InternalConnectorSecretBox/contextCredentialBox 的现有主密钥管理。密文包含来源和摘要；数据库不保存明文 per-object key。未配置密钥时拒绝上传。
- 上传只接受真实 mat task token，且对应正在执行的同一 queue/run/task。模型提供的 Issue/Chat/tenant/source 不授信。
- 普通 JWT/PAT 的 Agent manager 或可信 originator、精确 mat 执行主体，按既有 Direct ACL 读取。机器凭据不继承关联用户权限；持久来源始终校验。
- Direct URL 始终指向认证附件接口，不返回裸存储 URL、公开 CDN 明文或绕过授权的签名能力。
- 同 queue、相同文件名及内容 SHA 去重。pending/deleting/失败不是已完成产物。
- Direct 与 attachment 的绑定不可被旧 Issue/Chat 自动链接流程改域；相关 SQL 只排除新 Direct 绑定。
- 硬删除 Task 当前没有 API，不新增 API。workspace 删除前显式登记对象清理，失败重试与墓碑由最小后台 reconcile 完成。
- Notice 仅引用已 ready 的真实附件；引用或入队不等于渠道原生文件已送达。无可信 workspace 用户映射的钉钉 requester 不因此获得下载权。

## 文件与接口

- 新增 `server/internal/handler/employee_task_artifact*.go`：来源解析、保留上传 intent、ready 原子提交、授权读取、加密代理、清理与测试。
- 窄改 `server/internal/handler/file.go`：Direct 上传与附件读取接缝，旧附件保持现有行为。
- 新增 `server/migrations/9660`–`9669` 中所需表及独立并发索引；无外键。
- 窄改 `server/pkg/db/queries/attachment.sql`，使用可复现 narrow sqlc 配置生成完整受影响查询块。
- task_domain 负责 router、workspace 删除及后台 worker 接线，避免共享文件冲突。
- Host 查询：`ListEmployeeTaskArtifacts(ctx, scope, taskID, runID)` 仅返回当前可信来源下 ready 的引用。

## 实施顺序

- [x] 写真实 queue/run/task + 两个人类主体 + fake Storage 测试，观察现有 Direct 上传拒绝与权限边界 RED。
- [x] 创建来源/intent 表及索引，先覆盖上传、重放、并发和失败不返回完整引用。
- [x] 使用现有主密钥加密范围绑定的对象；ready 与 attachment 写入同一事务。
- [x] 接入 metadata/content/download/list 私有 ACL 和完整性验证，覆盖跨人、跨 tenant、跨 workspace、public CDN 绕过和密文篡改。
- [x] 阻止旧附件自动绑定改变 Direct 所属域，生成相关 SQL 并记录生成命令。
- [x] 实现 deleting/重试/墓碑清理；覆盖 workspace 删除与上传竞争、失败对象回收和迟到对象重新出现。
- [x] task_domain / direct_review 接完成 notice 的真实 ArtifactRef 查询与后台清理。
- [ ] 跑新范围 race/vet、旧附件/Direct 权限回归及迁移约束检查，交 Root 独立复审。

## 验证环境与事实边界

使用本机 127.0.0.1:55462 的独立 `employee_artifact_test` 数据库；不调用真实对象存储账户。真实外部渠道文件送达、生产密钥装配和生产对象存储验收不在本地测试成功声明中。

## SQL 生成复现

全量 sqlc 当前受既有 `agent.sql:102` ambiguous id 阻塞。本增量没有手写
生成 SQL；用 sqlc v1.31.1 只生成完整 attachment 查询文件，再复制对应产物。
在 `server/` 目录执行：

```sh
artifact_gen_dir=$(mktemp -d)
ln -s "$PWD/migrations" "$artifact_gen_dir/migrations"
ln -s "$PWD/pkg/db/queries/attachment.sql" "$artifact_gen_dir/attachment.sql"
cat > "$artifact_gen_dir/sqlc.yaml" <<'YAML'
version: "2"
sql:
  - engine: "postgresql"
    queries: "attachment.sql"
    schema: "migrations"
    gen:
      go:
        package: "db"
        out: "generated"
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_empty_slices: true
YAML
sqlc generate -f "$artifact_gen_dir/sqlc.yaml" > "$artifact_gen_dir/generate.log" 2>&1
cp "$artifact_gen_dir/generated/attachment.sql.go" pkg/db/generated/attachment.sql.go
```

本次实际命令用 `/private/tmp/employee-loop-tools/sqlc`，配置及日志位于
`/private/tmp/employee-artifact-sqlc/`；成功退出码为 0，日志为空。
生成产物与落地文件 SHA-256 均为
`23826f6b2d962fff62db1232211cfb888b6b80b90141c47e7a16c807b4e31fff`。

## 当前验证记录

14 个 Artifact 主测试及其权限子例已经在独立数据库通过 `-race`。
同轮选跑的既有 upload/get/download/chat attachment、Direct access 回归通过。
`go vet ./internal/handler` 与迁移方向/范围检查通过；全量迁移编号唯一性存在
既有重复编号，本增量使用的 9660–9664 无冲突。独立复审和真实外部环境验收
仍分别记录，不能由本地 fake Storage 成功推导线上交付。


## 最终本地复核补充

- 上传按已认证 Task 的真实来源选择存储路径，再处理可选表单关联；缺少 Direct task_id 的不完整请求不会写入对象或附件。主线程补了正式 PG 回归并观察红绿。
- Direct 对象 PUT 使用最长 60 秒的请求 context；存储实现必须遵守取消。清理账本在 1 分钟、1 小时、24 小时尾随检查后回收，不能据此声称任意超长、失约的迟到 PUT 都会被无限回收。
- 两个并行附件会话被自动内容检查中止，没有计为审查通过。主线程补齐上述普通上传约束后，另一独立审查在普通上传、事务和文件生命周期范围内通过；真实 PG/本地 Storage fixture、正常 Issue/Chat 回归、race/vet 通过。
- 真实 OSS/FC、密钥运营以及外部渠道文件投递尚未验收；鉴权下载 URL 不等于钉钉原生文件送达。
