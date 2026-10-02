# 线上入口与预发处理

线上入口用 `/forward/{target}` 选择已配置的环境，例如：

```text
https://fde-workbench.dingtalk.com/forward/pre/dingtalk/configure?link=...
```

配置页、JS/CSS/字体、钉钉登录、链接兑换、配置读写和连接器授权返回都保留线上域名。第三方授权过程中仍会正常进入第三方站点。数据库、工作区、智能体、租户和场域归属保持在目标环境。

## 边界与扩展

`server/internal/forwarding` 是统一基础层：

- `policy.go` 定义目标名、规范路径和配置页面的 method/path 白名单。
- `gateway.go` 负责目标解析、请求代理、会话 Cookie 隔离、Location 重写、并发限制、防环和脱敏日志。
- `registration.go` 提供 OAuth 与 A2A 共用的 HMAC 注册协议（timestamp + 换行 + 原始 body）。现有线上签名格式保持兼容。
- `assets.go` 负责本部署构建资源的 namespace，与链接签发开关独立。

入口 middleware 在本地鉴权之前选择环境，目标服务仍执行原有身份、CSRF、租户、链接和场域授权。路由本身不授予权限。线上根路径 Cookie 和 Authorization 不进入预发；`mf_pre_multica_auth` / `mf_pre_multica_csrf` 只在该目标的代理请求中还原为原名称。前端 API、OAuth/pending 存储也按目标隔离。

新业务应增加明确的 policy 和对应测试，复用目标映射、签名与传输基础设施。不要默认放行整个 `/api/`。A2A 仍保留专用 credential 替换、身份注册和流式响应规则；sandbox relay 的任务令牌和调度协议不变。两者不是浏览器会话，不能改用 H5 Cookie 策略。

登录限流复用 `RATE_LIMIT_TRUSTED_PROXIES` 配置的可信代理链解析客户端地址，再用注册共享密钥签名 target、method、完整 URI、IP 和时间戳。目标仅将一分钟窗口内验证通过的地址用于限流，不将它当作用户身份；重放仍命中同一 IP 桶。启用入口前必须核对可信代理 CIDR，不能信任浏览器自行填写的 XFF。

目标必须来自部署配置，只接受 HTTPS origin；请求中的任意 URL 不能成为 upstream。目标不可用返回 502/503，未登记目标和路径返回 404，转发环路返回 508。已选中的预发请求不回落到线上执行。

## 配置与构建

线上运行时配置：

```sh
MULTICA_FORWARD_TARGETS='{"pre":"https://pre-fde-workbench.dingtalk.com"}'
MULTICA_A2A_FORWARD_REGISTRATION_SECRET='<两端相同且至少32字符的现有注册密钥>'
REDIS_URL='<线上共享Redis地址>'
```

预发运行时配置：

```sh
MULTICA_FORWARD_PUBLIC_BASE_URL='https://fde-workbench.dingtalk.com/forward/pre'
MULTICA_A2A_FORWARD_REGISTRATION_SECRET='<同一注册密钥>'
```

`MULTICA_APP_URL` / `FRONTEND_ORIGIN` 继续配置预发自身 origin。不能把它们全部改成线上，也不需要共享数据库或登录 JWT 密钥。

预发 Web **构建参数**必须设置：

```sh
MULTICA_FORWARD_ASSET_PREFIX=/forward/pre
```

本地构建可使用 `MULTICA_FORWARD_ASSET_PREFIX=/forward/pre pnpm --filter @multica/web build`。Aone Docker 构建需要在流水线的 Docker build arguments 中显式透传这个参数；仅在运行时 env-vars 中设置无效。现有 `build.sh` 不执行 Docker，不能把参数加在那里。等价 Docker 调用（在包含应用 tgz 的既有构建上下文中）：

```sh
docker build --build-arg APP_NAME=dt-fde-multica \
  --build-arg MULTICA_FORWARD_ASSET_PREFIX=/forward/pre \
  -f Dockerfile .
```

Dockerfile 将前缀同时用于 Next 构建，并生成 `forward-asset-prefix` 文件复制到最终镜像；`src/main.sh` 从该文件读取不可漂移的构建值给 Go。独立本地 Go+Next 验证需给 Go 进程设置同一 `MULTICA_FORWARD_ASSET_PREFIX`，这是本地启动配置，不会改变已构建资源。

Next 的 assetPrefix 会写入 JS/CSS/字体资源引用，不能只在运行时修改。**线上主应用构建保持前缀为空，预发使用 `/forward/pre` 的专用构建**。该预发产物不能直接晋级为线上主应用镜像；线上需重新构建为无前缀版本。公共链接的 target 与构建前缀不一致时服务启动失败，避免发布可打开但资源错位的页面。

## OAuth 往返

1. 代理配置页通过线上前缀调用 `/auth/fde/dingtalk`，用户和会话均由预发创建。钉钉登录应用须允许 `https://线上域名/forward/pre/dingtalk/configure` 作为回调地址。
2. 连接器授权 start 的绝对 `return_to` 指向上述页面。预发记录自己的 state、PKCE 与浏览器 nonce，并向线上注册 `{state_sha256, home_origin, forward_target, workspace_id, agent_id, connector_id, scope_type, expires_at_ms}`。
3. 线上验证共享密钥、时间戳、TTL 和 `forward_target -> home_origin` 的精确映射，用 Redis 保存登记。目标未部署、映射不匹配或注册失败时 start 明确失败。
4. 供应商仍回到固定线上 `/api/connectors/oauth/callback`，GitHub 为 `/api/github/authorize`。线上原子消费登记，用同一个 gateway 代理回调，只转发与该 state 对应的浏览器 nonce Cookie。
5. 预发再次校验自己的 state/PKCE/nonce，保存凭证，再返回线上前缀页面。固定 callback 与配置页 Cookie 不混用；回调 URL 重放失败。

工作区 GitHub 或预注册应用若使用 `callback_mode=self`，公共页面发起授权会在创建 state 前返回 409 `public_callback_required`；需要先在应用控制台登记线上固定回调并改为 `production_forward`，不能覆盖原登记地址。

直接从预发原生页面发起、`return_to` 仍为预发的已有 OAuth 流程维持原 302 跳转语义。非 `mcpc.` GitHub 安装回调保持原行为。

## 启用、关闭与验证

先部署线上入口和目标映射，再部署带匹配资源前缀的预发构建，最后启用预发 public base。配置好钉钉登录回调和原有连接器固定回调后，再让预发签发链接。线上只需发布一次这层入口；已开放的场景页面、资源和处理逻辑之后可随预发迭代。

关闭公共入口配置：清空预发 `MULTICA_FORWARD_PUBLIC_BASE_URL`，之后签发恢复为预发原生链接。构建 marker 保持不变，预发原生页面的资源仍可加载。但旧代理页面的新连接器 OAuth `return_to` 和线上 URL 的 JSAPI 签名也会停止被认可；这不是仅关闭签发的开关。已经创建 state 的在途 OAuth 可在保留线上映射时完成。需要平滑排空时，应先停止分发新的配置链接，并保留 public base 与目标映射直到当前操作结束，再关闭。立即删除映射是硬关闭，已有链接会返回 404、回调会被拒绝。不要把已发出的预发链接改为在线上处理。

`/forward/`、目标 `/dingtalk/configure` 与三个固定 OAuth callback 的 nginx access/error 日志关闭；Go 审计记录 target、surface、method、status，不记录 query、code、state、Cookie 或 Authorization。上游接入层如另有请求日志，也应按同一规则脱敏。

验证证据与限制见 [实施记录](plans/2026-10-02/unified-env-forwarding.md)。本次不包含真实部署、真实第三方账号授权或线上流量切换。
