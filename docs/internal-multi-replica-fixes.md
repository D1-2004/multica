# 预发多实例（2 pod）改造记录

> 冬翔（103262），2026-07-12 ~ 07-14。写给在同一个 develop 上并行开发的同学，避免重复劳动。
>
> **一句话**：预发跑 2 个 pod，但 Multica 上游本质上是按单副本设计的（Helm chart 默认 `replicas: 1`，上游 issue #3647 至今开着）。我这几天把踩到的多实例坑逐个补上了，本文列清楚**接入了哪些外部资源**、**改了哪些代码**、**还剩什么没做**。

## 我接入的外部资源

| 资源 | 名称 / ID | 用途 | 谁申请的 |
| --- | --- | --- | --- |
| Redis (Tair) | `dt-fde-multica-pre-redis` | 跨 pod 实时广播、daemon 唤醒、限流、各类缓存 | 须莫 |
| OSS Bucket | `dt-fde-multica-oss`（cn-zhangjiakou，私有） | 附件 + 头像共享存储 | 须莫申请，我接入 |

两者都走 **normandy 托管凭证**（CloudCenter 里做「应用授权」把 `dt-fde-multica` 绑到资源上），代码用 SDK 换 STS 临时凭证，**不落任何静态 AK/SK**。

OSS 的 bucket 策略明确只接受 `acs:AccessId` 为 `TMP.*`/`STS.*` 的调用者 —— **静态密钥会被直接拒绝**，所以这不是风格选择，是硬约束。

## 新增的运行时配置（预发 env-vars trait）

```
REDIS_AUTHZ_INSTANCE_ID / REDIS_AUTHZ_ENDPOINT   # 须莫加的
S3_BUCKET=dt-fde-multica-oss
S3_REGION=cn-zhangjiakou
AWS_ENDPOINT_URL=https://oss-cn-zhangjiakou-internal.aliyuncs.com   # 内网端点
S3_USE_PATH_STYLE=false
OSS_AUTHZ_BUCKET=dt-fde-multica-oss
ATTACHMENT_DOWNLOAD_MODE=proxy   # 内网端点浏览器不可达，必须后端转发
MULTICA_DINGTALK_SECRET_KEY      # 钉钉机器人/外部集成的总开关
MULTICA_LOG_TAIL_TOKEN           # 远程日志端点
```

**新增 env key 必须同时加进 `src/main.sh` 的 `RUNTIME_CONFIG_KEYS` 白名单**，否则配了也进不了进程。

## 提交清单（都在 develop 上）

### 多实例正确性

| 提交 | 问题 |
| --- | --- |
| `6f374e57` | **钉钉安装会话跨 pod 404**。会话存进程内存 map，扫码后轮询被负载均衡分到另一个 pod → "安装会话已失效"。改为落库（迁移 181）。 |
| `d30c8c41` | **nginx 不转发 WS 升级头**，daemon 唤醒长连（`/api/daemon/ws`）从来没握手成功过 → 任务只能靠 30 秒轮询发现。加了 WS 专用的精确匹配 location。（上游 issue #2500 至今开着） |
| `fa5dc1d1` | **跨 pod 唤醒丢失**。daemon 的 WS 钉在一个 pod，任务可能在另一个 pod 入队 → 约 50% 唤醒丢失。加了 Postgres LISTEN/NOTIFY 中继（**Redis 配上后自动走 Redis relay，PG 中继退居兜底**）。 |
| `5f1e2121` | **入站消息不发 WS 事件**。渠道引擎写 chat_message 走 service 层，继承不到 handler 的广播 → 钉钉发来的消息网页上必须刷新才可见；任务入队失败时永远不可见。Router 提交后广播。 |
| `b612c7fb` | **「思考中」指示器永不消失**（钉钉/飞书/Slack）。表情 ID 存进程内存，**添加**被租约钉在 pod A，**清除**由 daemon 的 HTTP POST 触发（随机落 pod）→ 约一半的运行清不掉。改为落库（迁移 182），`DELETE...RETURNING` 兼做跨副本抢占。 |
| `cd1dc5cd` | **FC/E2B 沙箱重复启动（烧钱）**。`sync.Map` 去重是进程内的，两个 pod 都会建沙箱，一个成为孤儿计费到超时。用 Postgres 咨询锁串行化 `(runtime, scope)` 的查-建。 |
| `2ecd6cc4` | **附件 + 头像跨 pod 404**。上传落各 pod 本地盘 → 下载打到另一个 pod 就 404；删除也只删单个 pod，另一个 pod 的文件永远留着且**免鉴权可下载**。接 OSS。 |

### 其他

| 提交 | 内容 |
| --- | --- |
| `c73973bf` | 钉钉通讯录搜索（花名搜索走 TOP 网关），直连模式复用登录凭证 |
| `d768e0ae` / `5279ab4b` | 远程日志端点 `/api/internal/logs/tail`（token 保护）+ Redis 端口校验 |
| `569681f4` | 同步上游 multica-ai/main（197 个提交） |
| `e4fac179` | pg_trgm 迁移加守卫（PolarDB 拒绝非高权限账号建扩展，会导致启动崩溃） |
| `b325bc0b` | 修复过时的 sweeper 竞态测试（其姊妹测试一直在假通过） |
| `22b0c8a1` | Aone 部署 runbook（`.agents/skills/aone-deploy/`）+ CLAUDE.md 硬规则 |

## 上游做对了的（别重复造轮子）

- **渠道长连（钉钉/飞书/Slack Stream）**：`ws_lease_token` CAS 租约，只有一个 pod 持连。入站**不该**"均衡"——单消费者才是对的。
- **入站去重**：DB 原子声明，跨 pod 安全。
- **出站回复/卡片**：事件总线是进程内的，而事件只在处理 daemon HTTP 的那个 pod 上发布 → 全集群恰好触发一次，**不会重复发消息**。
- **所有后台循环**（自动驾驶调度、清扫器、用量汇总）：条件 UPDATE / 咨询锁 / 唯一索引三重保护，**不会重复派发、重复计费**。

## 还没做的（谁有空谁捡）

| 问题 | 影响 |
| --- | --- |
| **DWS 登录会话**（`handler/dws_auth.go:66` 进程内存 map） | 和钉钉安装会话同一个 bug：轮询 404，登录界面卡死（后台其实已登录成功） |
| **飞书安装会话**（`lark/registration_service.go:128`） | 同上，钉钉那个已修、飞书没修 |
| **渠道租约续约吞掉 DB 错误** | DB 抖动 >90 秒，两个 pod 会同时持连（去重能兜底，但"单连接"不变式破了） |
| **租约过期时间用应用时钟算、和 DB 时钟比** | NTP 偏差大会导致两个 pod 反复互抢租约 |
| **`taskInProgress` 指标只增不减** | claim 和 complete 落在不同 pod → 指标持续上涨，迟早误报警 |

## 写新功能时请自问的四个问题

这几天所有的坑都是同一个模式——**看起来对、注释也自信，但跨 pod 就塌了**（上游甚至专门写注释论证「思考中」指示器是多实例安全的，结论对但论证错了）：

1. 这个状态存在**进程内存**里吗？（→ 跨 pod 就废）
2. 这个文件写在**本地磁盘**上吗？（→ 跨 pod 就废）
3. 这是个**长连接**吗？（→ 钉在单 pod，重部会断，要有租约）
4. 缺了依赖会**报错还是静默降级**？（→ 后者最危险：一半功能悄悄失效，没人发现）
