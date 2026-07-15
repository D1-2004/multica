# FDE 移动端开通

FDE 开通入口是 `/fde/start`。它使用独立的钉钉 OAuth 入口验证当前用户；后端 onboarding API 只接受带签名 `auth_method=dingtalk` 的用户 JWT，拒绝普通 PAT、其他登录方式 JWT、Agent task token 和 cloud PAT。随后在用户全程可见的页面中完成：

1. 读取当前用户拥有或可管理的工作空间；没有工作空间时要求输入名称，一个工作空间时直接使用，多个工作空间时要求选择。
2. 为目标工作空间创建或复用平台固定的 FC Agent Sandbox runtime。
3. 从 PostgreSQL 中最近一次校验成功的固定公开 Git 仓库快照创建或更新平台托管 Agent，并物化其 instructions、skills 和 skill files。
4. 启动钉钉机器人 device flow，等待安装成功后显示“创建完成，可以返回钉钉”。

重复请求不会为同一用户创建第二个工作空间，也不会在同一工作空间创建第二个平台托管 Agent。该入口要求用户是工作空间 owner 或 admin。

平台托管 Agent 的 `owner_id` 始终对齐发起开通的当前钉钉用户；重复进入并复用既有 Agent 时也会重新对齐。固定 FC runtime 的 `owner_id` 与 Agent owner 保持一致。现有 FC claim 链路使用 `runtime.owner_id` 写入 `task_token.user_id`，再把 `mat_` token 作为 `MULTICA_TOKEN` 注入沙箱，因此沙箱中的 Multica 调用仍以该用户身份执行，而不是平台服务身份。

## 配置

```dotenv
MULTICA_FDE_AGENT_REPOSITORY_URL=https://gitee.com/keeperqaq/fde-agent.git
MULTICA_FDE_AGENT_REPOSITORY_REF=master
MULTICA_FDE_AGENT_SYNC_INTERVAL=30m
MULTICA_FDE_AGENT_ROLLOUT_BATCH_SIZE=50
```

仓库可位于任意 Git 托管平台，但必须使用不含凭据的 HTTPS URL 并且公开可读；公开性由无交互 `git clone` 实际验证。仓库还需包含可由现有 `agentsource` compiler 校验的 `multica-agent.yaml`、instructions 和 skills。服务启动后立即尝试同步，之后默认每 30 分钟检查一次。多节点通过 PostgreSQL advisory lock 串行同步；成功快照保存在 PostgreSQL，远端暂时不可用时继续使用 last-known-good 快照。

还必须配置现有的钉钉 OAuth/机器人注册参数、FC E2B 参数和至少一个 FC 模型。最终运行镜像需要包含 `git`。

平台托管 Agent 的仓库内容更新由服务自动应用：空闲 Agent 立即分批更新，运行中 Agent 跳过，任务终态后再安全对齐。显示名称、描述、用户手工添加的 skills、runtime、模型和钉钉绑定不由源同步覆盖。

## `--allow-unbound` 边界

`multica dingtalk install begin --allow-unbound` 仍然保留，用于明确允许未绑定发送者使用机器人的独立安装场景。FDE 移动端开通固定以当前已认证用户完成绑定，始终使用 `allow_unbound=false`。
