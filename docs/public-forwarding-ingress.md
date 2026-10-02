# 独立公共转发入口

本分支 `codex/public-forwarding-ingress` 基于正式发布基线 `aa4676f2248a68557587f1f94e8cd836f518c2af`，仅提取公共 HTTP 转发入口。它不包含多租户、EmployeeLoop、Coordinator 提示词、配置页前端或数据库迁移的功能变更。

先作为独立变更单进入预发流水线 66 验证。正式环境流水线 67 需后续单独发布，本次不执行；Coordinator 的公共链接签发也保持关闭。

## 包含的能力

- `/forward/{target}` 的固定 HTTPS 目标映射与 HTTP 方法/路径白名单；未配置目标时返回 404。
- 目标专属会话 Cookie、CSRF 转发、敏感身份头清理、重定向改写、防环、并发限制和流式代理。
- OAuth state 注册的 `forward_target` 扩展：共享密钥验签、Redis 原子消费，只转发该 state 的 nonce Cookie。既有预发 302 回调保持兼容。
- 原客户端地址签名绑定目标、方法、URI、IP 和一分钟时间窗口，目标验证后仅用于限流，不作为业务身份。
- nginx 对公共入口、配置页及 OAuth callback 隐去敏感查询日志。
- 保留目标构建的资源 namespace 处理协议，便于同一基础层与预发应用版本配合；此入口分支不改变线上主站资源前缀。

## 后续正式启用

入口环境需要配置 `MULTICA_FORWARD_TARGETS`，例如：

```sh
MULTICA_FORWARD_TARGETS='{"pre":"https://pre-fde-workbench.dingtalk.com"}'
```

同时保留至少 32 字符的 `MULTICA_A2A_FORWARD_REGISTRATION_SECRET`，与目标环境一致。OAuth 登记使用入口环境的共享 Redis。入口的 `RATE_LIMIT_TRUSTED_PROXIES` 必须符合实际可信代理链，不能信任客户端自带的 XFF。

线上主应用不设置 `MULTICA_FORWARD_PUBLIC_BASE_URL` 或资源前缀。目标预发应用需先使用包含前端适配的版本构建，配置 `/forward/pre` 资源前缀，再设置：

```sh
MULTICA_FORWARD_PUBLIC_BASE_URL='https://fde-workbench.dingtalk.com/forward/pre'
```

钉钉登录应用允许公共配置页回调后，Coordinator 与执行器共用的签发器才能返回可用的线上链接。仅在预发部署入口代码不等于正式域名已提供该入口。

## 验证与回滚

本地验证转发白名单、Cookie 隔离、签名拒绝、独立客户端限流桶、真实 TLS 回调代理、旧回调兼容性、Go vet/build。预发验证构建和部署记录、健康、原生配置页及静态资源、鉴权、OAuth 无效 state 拒绝、默认关闭及伪造转发签名拒绝。

删除目标映射会立即停止该目标的新入口请求及透明回调；这会使在途链接不可用。平滑关闭时应先停止分发新链接，等待在途操作完成后再移除映射。未知目标或故障不得回落到当前环境执行写操作。

本次未启用目标映射或公共链接签发，不改变线上流量。预发流水线会将本独立变更单与已有变更单整合；正式发布时只选择此入口变更单，避免携带其他未上线功能。
