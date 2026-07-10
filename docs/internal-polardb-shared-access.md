# PolarDB PostgreSQL 共享实例接入说明

本文用于其他内部应用接入 `dt-fde-multica` 当前使用的同一套 PolarDB PostgreSQL 实例。文档不包含任何数据库密码、应用密钥或管理员凭证。

## 连接目标

- 数据库类型：PolarDB PostgreSQL 17
- 连接地址：`dt-fde-multica.rwlb.zhangbei.rds.aliyuncs.com`
- 端口：`5432`
- 网络路径：应用安全外联，经公网连接地址访问
- TLS：连接串使用 `sslmode=require`

这里共享的是 **PolarDB 实例**，不是 Multica 的业务库和账号。新应用不要复用 `multica_pre` 或 `multica_app`，应单独创建数据库和登录账号，避免迁移脚本、表名、权限和连接数互相影响。

## 最短接入流程

1. 数据库管理员为新应用创建独立数据库和账号。
2. 在新应用自己的 Aone 应用分组下申请安全外联，只申请 TCP `dt-fde-multica.rwlb.zhangbei.rds.aliyuncs.com:5432`。
3. 在 PolarDB 白名单中加入该应用经安全外联后的真实出口公网 IP，而不是机器的 `10.*`、`11.*`、`33.*` 或 `192.168.*` 私网 IP。
4. 在 Aone 预发配置中把连接串保存为 secret 配置项，不要写进代码仓库、构建参数或普通配置项。
5. 从每个预发实例分别验证 DNS、TCP、数据库认证和实际出口 IP。
6. 验证完成后删除临时的 `0.0.0.0/0` 白名单，只保留确认过的 `/32` 出口 IP。

## 1. 创建独立数据库和账号

以下 SQL 由 PolarDB 管理员账号执行。把占位符替换为新应用自己的名称，并使用随机生成的强密码。

```sql
CREATE ROLE <app_user> LOGIN PASSWORD '<strong-random-password>';
CREATE DATABASE <app_db>
  OWNER <app_user>
  ENCODING 'UTF8'
  TEMPLATE template0;

REVOKE ALL ON DATABASE <app_db> FROM PUBLIC;
GRANT CONNECT, TEMPORARY ON DATABASE <app_db> TO <app_user>;
```

连接到新库后收紧默认 schema 权限：

```sql
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO <app_user>;
```

扩展不是实例全局生效，而是按数据库安装。只有应用确实需要时，才由管理员在新库中执行，例如：

```sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;
```

不要授予 `SUPERUSER`、`CREATEDB` 或其他无关权限。

## 2. 申请应用安全外联

安全外联审批与 Aone 应用及应用分组绑定，`dt-fde-multica` 已有的审批不能直接给另一个应用复用。新应用需要单独提交：

- 申请类型：应用安全外联
- 应用：同事自己的 Aone 应用
- 环境/分组：需要访问数据库的预发分组；生产环境后续单独申请
- 协议：TCP
- 目标域名：`dt-fde-multica.rwlb.zhangbei.rds.aliyuncs.com`
- 目标端口：`5432`
- 用途：连接共享 PolarDB PostgreSQL 实例中的独立业务库

数据库连接不需要申请 HTTP 或 UDP。

## 3. 配置 PolarDB 白名单

机器 IP 通常不是数据库看到的来源 IP。安全外联会经过统一出口，PolarDB 白名单应填写数据库实际看到的出口公网 IP。

当前若仍临时开放 `0.0.0.0/0`，可在新应用首次连通后，从每个预发实例执行：

```sql
SELECT inet_client_addr();
```

收集所有不同的返回值，以 `/32` 形式加入 PolarDB 白名单，例如 `203.0.113.10/32`。两个实例都要验证，避免发布或故障切换到另一出口后连接失败。确认新白名单生效后，立即删除 `0.0.0.0/0`。

## 4. 配置应用连接串

连接串格式：

```text
postgresql://<app_user>:<url-encoded-password>@dt-fde-multica.rwlb.zhangbei.rds.aliyuncs.com:5432/<app_db>?sslmode=require&connect_timeout=10&application_name=<app_name>
```

注意：

- 密码中的 `@`、`:`、`/`、`?`、`#`、`%` 等字符必须做 URL 编码。
- 在 Aone 预发配置中保存为 secret，例如配置键 `DATABASE_URL`。
- 不要把完整连接串放进 Git、日志、工单正文、截图或群消息。
- 每台应用实例先把连接池上限设为 `5` 到 `10`，再根据实例最大连接数和实际负载调整。所有应用、所有实例的连接池总和应留出管理和迁移连接余量。

## 5. 分层验证

在每个预发实例上分别执行。

### DNS

```bash
getent hosts dt-fde-multica.rwlb.zhangbei.rds.aliyuncs.com
```

### TCP

```bash
nc -vz -w 5 dt-fde-multica.rwlb.zhangbei.rds.aliyuncs.com 5432
```

### PostgreSQL

以下命令会交互式询问密码，避免密码进入 shell 历史：

```bash
psql "host=dt-fde-multica.rwlb.zhangbei.rds.aliyuncs.com port=5432 dbname=<app_db> user=<app_user> sslmode=require connect_timeout=10"
```

登录后执行：

```sql
SELECT current_database(), current_user, inet_client_addr(), now();
```

验收标准：

- DNS 能解析连接地址。
- TCP `5432` 可建立连接。
- 登录的是新应用自己的数据库和账号。
- `inet_client_addr()` 返回值已进入 PolarDB `/32` 白名单。
- 应用启动日志没有持续出现 `timeout`、`password authentication failed`、`no pg_hba.conf entry` 或权限错误。

## 常见错误定位

| 现象 | 优先检查 |
| --- | --- |
| `dial timeout` | 安全外联是否覆盖当前应用分组、目标是否为域名加 TCP 5432、PolarDB 白名单是否为真实出口 IP |
| `password authentication failed` | 用户名、密码和密码 URL 编码；不要继续扩大白名单 |
| `database does not exist` | 是否已创建新应用独立数据库，连接串库名是否正确 |
| `permission denied` | 数据库 owner、schema owner 和迁移所需扩展；不要授予超级用户作为解决办法 |
| 一个实例正常、另一个失败 | 两个实例是否走不同出口 IP，是否都已加入 `/32` 白名单 |
| 连接数耗尽 | 每实例连接池上限、应用实例数及其他共享应用的连接池总和 |

## 交付时应提供的信息

数据库管理员只需通过安全渠道分别提供：

- 数据库名
- 应用用户名
- 一次性或受管数据库密码
- 已登记的出口公网 IP 列表

连接地址和端口可引用本文。密码必须通过密钥管理或受控私聊交付，不能补写到本文中。
