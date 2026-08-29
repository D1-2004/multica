# 静态网站托管协议

## 1. 能力边界

`sitehosting` 是独立资源模块，用于托管调用方生成的静态网站。它不复用 `attachment` 表、`/api/upload-file`、附件下载或 HTML preview，也不以 Agent、Task 或 Workspace 作为 Site 所有权模型。

- 支持单 HTML，以及包含 HTML、CSS、JavaScript、图片、字体和 JSON 等文件的 ZIP。
- 默认入口是 `index.html`，可启用 `spa_fallback`。
- Site 是 `public-unlisted`：知道 URL 的任何人都能读取，Workspace、Task 和 `publicSiteId` 都不是安全凭证。
- Site 归属于 API Token 或 Task Token 鉴权得到的用户；调用参数不能自行指定所有者。
- `mul_` API Token 可供 Codex、OpenCode 等通用 MCP Client 使用；沙箱内的 `mat_` Task Token 只映射到其绑定用户，Agent、Task 和 Workspace 不写入新 Site，也不参与 Site 授权。
- Site、revision、upload 表及公开 URL 都不保存或体现 `task_id`。
- 一个 Site 有多个 revision；只有全部文件上传成功后，数据库事务才原子切换 `active_revision_id`。失败 revision 不影响旧站点。

已发布旧版本创建的 Site 会在迁移时从原 Agent/Workspace 关系回填 `owner_user_id`；旧列仅为滚动升级兼容而暂时保留为可空字段，新版本不再写入，也不作为新 Site 的所有权依据。

公开地址固定为：

```text
${MULTICA_SITE_PUBLIC_URL}/sites/<publicSiteId>/
```

`publicSiteId` 是独立生成的随机不透明标识，不包含 Workspace、Agent、Task、Site UUID 或 revision UUID。服务端绝不从 `Host`、`X-Forwarded-Host` 等请求头构造公开 URL。

## 2. MCP 工具

### `prepare_static_site_deploy`

接受 `mul_` API Token 和 `mat_` Task Token。Site 所有者由鉴权中间件注入的用户身份确定；参数不能携带 `owner_user_id`、`workspace_id`、`agent_id` 或 `task_id`。

输入：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `expected_sha256` | 是 | ZIP 原始字节的 64 位十六进制 SHA-256 |
| `content_length` | 是 | ZIP 原始字节数 |
| `site_id` | 否 | 更新已有 Site；不传则新建 |
| `entrypoint` | 否 | 默认 `index.html` |
| `spa_fallback` | 否 | 默认 `false` |

输出包括 `upload_path`、`upload_url`、`upload_method=PUT`、`upload_token_header=X-Multica-Site-Upload-Token`、短期单次 `upload_token`、`expires_at`、`archive=zip`、限制和候选 `site_url`。`upload_token` 只出现在工具结果中，不进入 URL。

工具定义发布完整 `outputSchema`，覆盖上传标识、上传地址与方法、单次 capability、过期时间、候选站点地址和归档限制；支持结构化结果的 MCP Client 可以在调用前直接取得这份返回契约。

- 沙箱内不能访问公网 `upload_url` 时，使用当前 task 已注入的 `MULTICA_SERVER_URL` 拼接 `upload_path`。`Authorization` 继续携带 `mat_` Task Token，`upload_token_header` 指定的专用头携带原始 `mhs_` capability。
- 能直接访问公网 `upload_url` 时，可继续使用 `Authorization: Bearer <upload_token>`，此时不要再发送专用 capability 头。

### `get_static_site_deploy`

接受 `mul_` API Token 和 `mat_` Task Token。输入 `site_id`，服务端按 token 对应的用户校验所有权，再返回 Site 和最新 revision 状态。

工具定义发布完整 `outputSchema`。`active_revision_id` 和 `latest_error` 在没有对应值时可省略，其余 Site、最新 revision、时间和公开地址字段为必需输出。

## 3. 账号管理接口

Multica 设置页在“工作区”分组中提供“网站”页签，但 Site 所有权仍然归属于当前鉴权用户，不与当前 Workspace 建立关联。用户切换 Workspace 时看到的是同一份账号级列表。

管理接口使用 Multica 登录会话并要求 Human Actor：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/sitehosting/sites` | 按更新时间倒序返回当前用户仍处于 active 状态的 Site，以及最新 revision 状态和 `site_url` |
| `DELETE` | `/api/sitehosting/sites/<siteId>` | 将当前用户拥有的 Site 软删除；成功返回 `204 No Content` |

删除成功后，Site 会立即从列表中消失，公开读取接口也不再解析该 Site，因此原 `site_url` 立即失效。数据库记录和对象存储文件暂时保留，用于审计与恢复；接口不会暴露其他用户的 Site 是否存在，越权或不存在统一返回 `404`。

## 4. 上传与发布

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

## 5. 公开读取与安全

`GET` / `HEAD /sites/<publicSiteId>/...` 只读取 active revision。服务端不提供目录列表；根路径读取 entrypoint，开启 `spa_fallback` 后，仅无扩展名的缺失路径回退到 entrypoint。

响应使用清单中的 MIME、`Content-Disposition: inline`、ETag 和短时 revalidation cache。所有响应设置：

- `Content-Security-Policy`：禁止 object、base、frame 和 form；资源默认只能同源加载。`connect-src` 始终允许 `'self'` 和 `https://connector.dingtalk.com`，并可由 Diamond `web.site_connect_src` 追加 HTTPS origin。
- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: no-referrer`
- 严格 `Permissions-Policy`
- `X-Frame-Options: DENY`
- `Cross-Origin-Resource-Policy: same-origin`

`MULTICA_SITE_PUBLIC_URL` 缺失或不是合法 HTTPS URL（本地 loopback HTTP 例外）时，托管能力显式不可用，不采用 Host fallback。

当前 Aone 各环境可暂时把 `MULTICA_SITE_PUBLIC_URL` 设置为对应 `MULTICA_PUBLIC_URL` 以做测试。但同源托管的 JavaScript 与已登录 Multica 应用共享 origin，响应头只能降低风险，不能形成完整隔离。正式对外开放前必须申请并迁移到不携带 Multica 登录 Cookie、与主应用隔离的独立域名；迁移只需更新配置，不改变公开路径和数据库模型。

## 6. 托管页面 Fetch Proxy Runtime

服务端读取 HTML 时会在首个业务 `<script>` 之前注入同源 Runtime：

```html
<script src="/api/sitehosting/runtime/fetch-proxy.js" data-public-site-id="<publicSiteId>"></script>
```

该注入不改变上传、MCP 工具或站点部署协议。Runtime 在业务脚本执行前保存原生 `window.fetch`，业务代码仍调用标准 `window.fetch(targetUrl, init)`。页面通过全局数组声明需要代理的精确 HTTPS URL；数组可在 Runtime 加载后、首次请求前设置：

```js
window.__MULTICA_FETCH_PROXY_ALLOWLIST__ = [
  "https://connector.dingtalk.com/webhook/flow/<flowId>",
];
```

Runtime 对 URL 做标准解析后执行完整 URL 精确匹配，scheme、host、port、path 和 query 都属于匹配内容；不接受带凭据或 fragment 的声明。同源 URL 和未命中的 URL 完全交给原生 `fetch`。命中后只向以下同源接口发送一次请求，代理网络错误或接口错误都会直接 reject，不会回退并重试原始 URL，避免重复 POST：

```http
POST /api/sitehosting/sites/<publicSiteId>/fetch-proxy
Content-Type: application/json

{
  "version": 1,
  "url": "https://connector.dingtalk.com/webhook/flow/<flowId>",
  "method": "POST",
  "headers": [["content-type", "application/json"]],
  "body_base64": "eyJleGFtcGxlIjp0cnVlfQ=="
}
```

代理成功时，接口直接返回上游 status、body 和允许透传的响应头，并增加 `X-Multica-Fetch-Proxy-Result: upstream`。Runtime 仅在存在该标记时重建标准 `Response`；Multica 自身产生的 JSON 错误没有该标记，因此不会被误当作上游响应。

客户端声明只决定 Runtime 是否拦截，服务端不信任该白名单。服务端独立执行以下边界：

- 目标必须为 HTTPS，origin 必须是默认 `https://connector.dingtalk.com` 或 Diamond `web.site_connect_src` 追加的精确 origin。
- 解析目标全部 DNS 地址并固定本次连接地址；任一结果属于 loopback、私网、链路本地、metadata、文档保留地址或其他 reserved 网段时整体拒绝，防止 DNS rebinding。
- 只允许 `GET`、`HEAD`、`POST`、`PUT`、`PATCH`、`DELETE`；请求体上限 1 MiB，响应体上限 2 MiB，上游总超时 10 秒，禁止重定向。
- 请求仅保留 `Accept`、`Accept-Language`、`Content-Type`；不转发 Cookie、Authorization、Host、Origin、Referer、代理标识和 hop-by-hop 头。响应不转发 Set-Cookie、认证挑战、hop-by-hop 头及内部结果标记。
- 错误与日志只暴露固定错误码，不记录完整目标 URL，避免 webhook 路径泄露。

## 变更历史

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-08-30 | 托管 HTML 自动注入精确 URL Fetch Proxy Runtime，并新增受 origin、DNS/IP、方法、大小、超时、重定向和敏感头约束的同源通用代理协议。 | 让业务继续使用标准 `window.fetch` 调用不支持浏览器 CORS 的 HTTPS webhook，同时由平台防止重复 POST、SSRF、DNS rebinding 与凭据泄露。 |
| 2026-08-30 | 托管站点 CSP 的 `connect-src` 默认增加 `'self'`，并保留 connector 域名与 Diamond HTTPS origin 追加能力。 | 反馈提交改用 Multica 同源代理后，相对路径请求必须由 CSP 明确允许；同时保留既有 connector 默认值，避免 Diamond 配置移除安全基线。 |
| 2026-08-30 | 托管站点 CSP 默认允许 `https://connector.dingtalk.com`，并支持 Diamond `web.site_connect_src` 追加 HTTPS origin。 | 允许反馈站点直接 POST 钉钉 AI 表格 webhook，同时只放宽 `connect-src`，保留其他 CSP 安全边界。 |
| 2026-08-30 | 新增用户级 Site 列表与软删除管理接口，并在工作区设置中增加“网站”页签，支持打开、复制分享链接和确认删除。 | 托管能力此前只能通过 MCP 查询单个 Site，用户缺少统一可见、可分享和可撤销公网访问的管理入口；页签位置沿用工作区设置外壳，但不改变账号级所有权。 |
| 2026-08-29 | Site 所有权从 Workspace + Agent 调整为鉴权用户，并允许现有 `mul_` API Token 调用 prepare/get；endpoint 和 token 体系不变。 | Site Hosting 是独立资源能力，外部 Codex、OpenCode 等 MCP Client 应能以同一用户身份创建和更新网站，不应依赖某个 Multica Agent 或 Task。 |
| 2026-08-29 | 为 Site Hosting 的 prepare/get MCP 工具补充正式 `outputSchema`。 | 现有 Chat 与 Agent 查询工具已对稳定结构化结果发布输出契约；Site Hosting 同样返回稳定 `structuredContent`，补充 schema 可避免通用 MCP Client 猜测字段。 |
| 2026-08-29 | 增加 `upload_path`、`upload_token_header` 和沙箱 relay 专用 capability 头协议；保留公网直连 Bearer 兼容。 | 沙箱 relay 必须用 `mat_` 验证 task，请求上游又需 `mhs_` 上传能力，单个 `Authorization` 无法同时表达两种凭据；专用头将路由身份与一次性上传能力分离，并限制凭据只进入精确上传路由。 |
| 2026-08-29 | 新增独立 Site/revision/upload 模型、Task Token MCP prepare/get、流式 ZIP capability 上传、OSS 发布和 public-unlisted 读取协议。 | Agent 需要发布多文件静态产物，同时必须与附件语义、Task 持久映射和主应用可信内容边界隔离。 |
