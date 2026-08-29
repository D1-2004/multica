# Agent 静态网站托管协议

## 1. 能力边界

`sitehosting` 是独立资源模块，用于托管 Agent 生成的静态网站。它不复用 `attachment` 表、`/api/upload-file`、附件下载或 HTML preview。

- 支持单 HTML，以及包含 HTML、CSS、JavaScript、图片、字体和 JSON 等文件的 ZIP。
- 默认入口是 `index.html`，可启用 `spa_fallback`。
- Site 是 `public-unlisted`：知道 URL 的任何人都能读取，Workspace、Task 和 `publicSiteId` 都不是安全凭证。
- Workspace 只用于租户和权限隔离；Task Token 只用于创建/更新时的权威身份和审计。
- Site、revision、upload 表及公开 URL 都不保存或体现 `task_id`。
- 一个 Site 有多个 revision；只有全部文件上传成功后，数据库事务才原子切换 `active_revision_id`。失败 revision 不影响旧站点。

公开地址固定为：

```text
${MULTICA_SITE_PUBLIC_URL}/sites/<publicSiteId>/
```

`publicSiteId` 是独立生成的随机不透明标识，不包含 Workspace、Agent、Task、Site UUID 或 revision UUID。服务端绝不从 `Host`、`X-Forwarded-Host` 等请求头构造公开 URL。

## 2. MCP 工具

### `prepare_static_site_deploy`

仅接受 `mat_` Task Token。Workspace、Agent 和 Task 身份由鉴权中间件权威注入；参数不能携带 `workspace_id`、`agent_id` 或 `task_id`。

输入：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `expected_sha256` | 是 | ZIP 原始字节的 64 位十六进制 SHA-256 |
| `content_length` | 是 | ZIP 原始字节数 |
| `site_id` | 否 | 更新已有 Site；不传则新建 |
| `entrypoint` | 否 | 默认 `index.html` |
| `spa_fallback` | 否 | 默认 `false` |

输出包括 `upload_path`、`upload_url`、`upload_method=PUT`、`upload_token_header=X-Multica-Site-Upload-Token`、短期单次 `upload_token`、`expires_at`、`archive=zip`、限制和候选 `site_url`。`upload_token` 只出现在工具结果中，不进入 URL。

- 沙箱内不能访问公网 `upload_url` 时，使用当前 task 已注入的 `MULTICA_SERVER_URL` 拼接 `upload_path`。`Authorization` 继续携带 `mat_` Task Token，`upload_token_header` 指定的专用头携带原始 `mhs_` capability。
- 能直接访问公网 `upload_url` 时，可继续使用 `Authorization: Bearer <upload_token>`，此时不要再发送专用 capability 头。

### `get_static_site_deploy`

仅接受 `mat_` Task Token。输入 `site_id`，服务端按 token 固定的 Workspace 和 Agent 校验所有权，再返回 Site 和最新 revision 状态。

## 3. 上传与发布

调用方对上传地址发送原始 ZIP。沙箱经本地 relay 上传时：

```http
PUT /api/sitehosting/uploads/<uploadId>
Authorization: Bearer mat_<current-task-token>
X-Multica-Site-Upload-Token: mhs_<single-use-capability>
Content-Type: application/zip
Content-Length: <exact bytes>

<raw zip bytes>
```

其中路径来自 `upload_path`，origin 来自当前 task 的 `MULTICA_SERVER_URL`。relay 只在无 query 的精确 `PUT /api/sitehosting/uploads/<uuid>` 上游请求中保留 `X-Multica-Site-Upload-Token`；其他方法、路径和目标都会剥离该头。

公网直连兼容形式为：

```http
PUT <upload_url>
Authorization: Bearer mhs_<single-use-capability>
Content-Type: application/zip
Content-Length: <exact bytes>

<raw zip bytes>
```

服务端拒绝多个或逗号拼接的 capability 头，也拒绝同时在专用头和 `Authorization` 中携带 `mhs_`，避免代理合并或双值歧义。

不得使用 `multipart/form-data`，不得把 ZIP 或 base64 放进 MCP JSON。MCP 请求体原有 1 MiB 限制保持不变。

上传接口使用独立 capability 鉴权。数据库只保存 token 的 SHA-256 hash；token 默认 10 分钟过期且首次 claim 后立即失效。服务端不把 token 写入 query、日志或错误信息。

HTTP body 经 `MaxBytesReader` / `LimitReader`、`io.TeeReader` 和 `io.Copy` 流式写入临时文件，同时计算 SHA-256；生产路径不使用 `io.ReadAll`。ZIP 通过 `archive/zip` 的 `ReaderAt` 读取，每个条目再以流式 reader 写入：

```text
hosted-sites/<siteId>/revisions/<revisionId>/<path>
```

默认限制：

| 项目 | 限制 |
| --- | ---: |
| ZIP | 50 MiB |
| 解压总量 | 200 MiB |
| 单文件 | 50 MiB |
| 文件数 | 2000 |
| upload token | 10 分钟、单次 |

ZIP 校验拒绝绝对路径、`..`、反斜杠、百分号编码绕过、软链接、非普通文件、重复规范化路径、大小写或 Unicode 归一化冲突、`.env`、`.git`、私钥/凭据文件、缺少入口文件以及超过上述限制的归档。

## 4. 公开读取与安全

`GET` / `HEAD /sites/<publicSiteId>/...` 只读取 active revision。服务端不提供目录列表；根路径读取 entrypoint，开启 `spa_fallback` 后，仅无扩展名的缺失路径回退到 entrypoint。

响应使用清单中的 MIME、`Content-Disposition: inline`、ETag 和短时 revalidation cache。所有响应设置：

- `Content-Security-Policy`：禁止 object、base、frame、form 和网络连接；资源默认只能同源加载。
- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: no-referrer`
- 严格 `Permissions-Policy`
- `X-Frame-Options: DENY`
- `Cross-Origin-Resource-Policy: same-origin`

`MULTICA_SITE_PUBLIC_URL` 缺失或不是合法 HTTPS URL（本地 loopback HTTP 例外）时，托管能力显式不可用，不采用 Host fallback。

当前 Aone 各环境可暂时把 `MULTICA_SITE_PUBLIC_URL` 设置为对应 `MULTICA_PUBLIC_URL` 以做测试。但同源托管的 JavaScript 与已登录 Multica 应用共享 origin，响应头只能降低风险，不能形成完整隔离。正式对外开放前必须申请并迁移到不携带 Multica 登录 Cookie、与主应用隔离的独立域名；迁移只需更新配置，不改变公开路径和数据库模型。

## 变更历史

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-08-29 | 增加 `upload_path`、`upload_token_header` 和沙箱 relay 专用 capability 头协议；保留公网直连 Bearer 兼容。 | 沙箱 relay 必须用 `mat_` 验证 task，请求上游又需 `mhs_` 上传能力，单个 `Authorization` 无法同时表达两种凭据；专用头将路由身份与一次性上传能力分离，并限制凭据只进入精确上传路由。 |
| 2026-08-29 | 新增独立 Site/revision/upload 模型、Task Token MCP prepare/get、流式 ZIP capability 上传、OSS 发布和 public-unlisted 读取协议。 | Agent 需要发布多文件静态产物，同时必须与附件语义、Task 持久映射和主应用可信内容边界隔离。 |
