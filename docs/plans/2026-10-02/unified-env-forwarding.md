# 统一线上入口转发层

> 执行方式：subagent-driven-development；按职责隔离修改，逐项验证。

**目标：** 预发签发的场景配置链接使用线上域名；页面、资源、登录、配置 API 和连接器 OAuth 往返始终通过线上入口访问，数据仍属于预发。

**已确认：** 用户要求地址栏始终保留线上域名。OAuth 授权期间正常进入第三方授权站点，返回 Multica 时必须回线上域名。

**架构：** Go `internal/forwarding` 提供固定目标映射、路由策略、代理、Cookie 隔离、跳转重写和结构化日志。显式命名空间 `/forward/{target}` 承载浏览器链路；目标仅来自部署配置。现有 OAuth state 注册决定固定回调属于哪个环境。A2A 身份授权及 sandbox relay 令牌校验仍由原业务负责。

## 约束与决策

- 线上配置 `MULTICA_FORWARD_TARGETS`，JSON 对象将环境名映射到 HTTPS origin。禁止从 query/header 接受任意 upstream URL。
- 预发配置 `MULTICA_FORWARD_PUBLIC_BASE_URL=https://线上域名/forward/pre`。此配置仅影响对外配置链接和相应 OAuth 返回路径，不改变数据库、工作区、Agent、scene_id 或任务归属。
- 所有 `/forward/` 请求由入口交给 Go；只放行明确注册的页面、资源、登录与场景配置路由。未知目标、非法路径、目标故障直接报错，不在当前环境重试写请求。
- 页面和 JS/CSS 同属目标部署。使用 Next.js 官方 multi-zone `assetPrefix` 能力区分资源，不替换 HTML/JS 文本。配置页以 namespace 构造 API 和登录往返地址；生产主应用会话不传给预发。
- 路由不授予业务权限。目标仍验证自己的登录、链接兑换、租户与场景授权。
- Cookie 使用目标专属名称并限定路径，转发只恢复本目标 Cookie。连接器 state 的浏览器绑定 Cookie 仍位于供应商已注册的固定回调路径，名称按 state hash 唯一。
- OAuth 注册增加透明代理模式；线上验证签名、TTL 和目标后代理回调。旧的直接访问预发发起的连接保持原有 cookie 和跳转语义。
- 统一传输负责去除身份伪造头和 hop-by-hop 头、防环、超时和流式传输。日志不记录 query、code、state、Cookie、Authorization 或链接 token。
- 本次接入场景配置与 OAuth，并抽取可复用基础能力。其他业务按 policy 接入；不将整个工作台、管理接口或调度协议无条件开放给浏览器代理。

## 实施清单

- [x] Go 转发基础层：先用 httptest 写目标选择、路由白名单、Cookie 隔离、重定向、防环及故障测试，运行失败后实现。`go test ./internal/forwarding -count=1`。
- [x] 服务端接入：Config、router、入口 nginx 与运行时变量；链接签发读取 public base；OAuth 注册及回调复用代理。覆盖直连与代理两种 OAuth 往返，以及第三方/无签名注册拒绝。
- [x] 前端接入：目标静态资源 namespace、页面入口、API base、独立存储与 CSRF cookie、登录及链接清理保留 prefix。运行相关 Vitest、typecheck，并验证真实页面加载。
- [x] 回归：现有 OAuth/A2A/场景链接测试、router guards、Go vet、Coordinator policy 检查；记录环境限制和失败，不把未运行的集成测试计作通过。
- [x] 文档：配置示例、请求链路、扩展 policy 方法、上线顺序（线上先部署入口，再启用预发链接）、关闭和回滚行为。

## 验收

1. 预发生成线上配置链接，打开、刷新、兑换、保存及授权返回都保留线上域名和正确 target。
2. 两个标签页分别打开线上与预发，登录、Cookie、页面资源与写入目标互不覆盖。
3. 未登记目标、越权路径、伪造 forwarding header、转发环路被拒绝；上游 5xx/网络错误不触发线上业务。
4. OAuth state 和浏览器绑定校验保持有效，固定 callback 被透明代理；普通 GitHub 安装回调保持原行为。
5. 线上页面与预发页面可以独立升级，静态资源不会混用。

参考：Next.js [Multi-zones](https://nextjs.org/docs/app/guides/multi-zones)、[assetPrefix](https://nextjs.org/docs/app/api-reference/config/next-config-js/assetPrefix)。

## 验证记录（2026-10-02）

- Go 1.26.1：`internal/forwarding`、`internal/routerguard` race tests；`go vet ./internal/forwarding ./internal/handler ./cmd/server`；`go build ./cmd/server` 均通过。
- handler 专项回归 23 项通过，实际连接独立本地 PostgreSQL 17：OAuth state/签名、透明回调、完整 DCR code exchange 与凭证入库、原生预发旧回调、A2A 转发与 Coordinator 链接签发。OAuth provider 与 Redis 登记使用测试 fixture；目标回调 handler 通过真实 TLS HTTP 调用。没有真实外部账号操作。
- 测试入口默认在数据库不可达时 exit 0，故早期默认命令的结果不计作执行证据；纯单元验证使用 `MULTICA_HANDLER_UNIT_TESTS_ONLY=1`，最终数据库验证指定独立测试 DSN。
- 新库迁移存在基线顺序问题：`271_task_completion_canceled_status` 依赖 `9025_task_completion_outbox`。仅在临时测试库先执行既有 9025，再继续迁移；未修改仓库迁移或已有环境。
- 最终复验 Web 定向 73 项、Core API 119 项通过（前端实现阶段更宽选集分别为 83 / 133 项）；Web typecheck 与改动文件 ESLint 通过。Web 全量 272 项中 270 通过，2 项未修改 views 的字号/对比度静态检查失败，没有将其报告为全量绿色。
- Google Fonts 网络请求超时，常规 Web production build 未完成；临时离线字体 fixture 下 production build 完成。实际 Go gateway + Next standalone + Chromium 验证 46 个请求全走 `/forward/pre/`，页面完成 hydration，零 JS/HTTP 错误，线上 token 不变，目标只收到预发 Cookie/CSRF。该浏览器检查的业务 API 用 fixture，不等同于线上端到端验收。
- Coordinator policy 检查 `PASS_STRUCTURAL_ONLY`，不代表模型语义或线上送达认证；未做模型回放或预发真实送达。
- 独立规格审查与代码质量审查已完成；修复了关闭链接配置破坏资源、目标入口记录敏感 query、失效回调恢复链接泄漏预发 origin 三项问题。
- 未部署、未改线上/预发运行配置、未调用真实第三方 OAuth。上线还需按部署文档配置 Docker build argument 与钉钉回调地址。
