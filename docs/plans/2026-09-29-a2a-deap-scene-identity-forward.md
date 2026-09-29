# DEAP A2A：场域标准化、员工身份绑定与生产→预发转发

日期：2026-09-29。负责人：Claude（本机）。触发：PRI-70 实测（Tagggg → QwenTag）后冬翔要求：
只有冬翔能手动配置员工身份；把 DEAP 入站数据（含场域）标准化成 Coordinator 入站信息；
生产域名可把指定 Agent 的 A2A 请求转发到预发，便于在预发调试。

## 0. 事实基线（已核实）

- DEAP 每次 A2A 请求带两类数据：请求级私有 `params.metadata.context.attributes`
  （userInfo、sessionInfo.openConversationId、invocationInfo.dialogType/isGroupProactiveResponse、
  agentInfo.agentCode）；以及 Card 声明后才有的标准扩展
  `message.metadata["https://api-deap.dingtalk.com/a2a/extensions/dingtalk-event/v1"]`
  （eventType、conversation{type, openConversationId, thread}、sender、senderOpenDingTalkId、
  quotedMessage、resources）。规范：DEAP `albert-agent-runtime/docs/specs/2026-08-24-dingtalk-a2a-event-extension-v1.md`。
- DEAP Skill Center 缓存 Agent Card 的扩展声明；Card 变更后员工需在 DEAP 重新保存/发布才生效。
- 请求头 `X-DingTalk-User-Id/Corp-Id` 是发送人；数字员工自身的 uid/orgId 不在请求里。
- 沙箱里的 daemon 与 prompt 构造随 FC 镜像发布（`MULTICA_REF`），不随服务端部署。
  服务端能直接影响的是 claim 字段（`chat_type` 等）与 `chat_message.source_payload`，
  现有 daemon 会把 source payload 原样交给模型（`server/internal/daemon/prompt.go:646-653`）。
- FC 上 A2A 子进程只能通过请求级 `MULTICA_DEAP_DWS_TOKEN` 拿到身份；ContextToken 由 runner
  兑换后，daemon 的 A2A 分支只在 claim 带 token 时恢复 `DWS_CONFIG_DIR`，而 FC claim 故意不带 token
  （`handler/agent.go:1130-1136`，`daemon/daemon.go:8280`）。

## 1. 运营入口（仅冬翔）

- 配置 `MULTICA_A2A_OPERATOR_EMAILS`（逗号分隔、大小写不敏感，未配置即无人可用）。
  冬翔：`mmmWiSp4Uy8iSZwcGaTvhqfgiEiE@dingtalk.com`。
- 接口挂在现有 `/api/agents/{id}/a2a` 下，先过 `requireAgentA2AManager`（人类 + owner/admin），
  再过运营邮箱白名单：
  - `GET /a2a/operator`：非运营者只得 `{"operator": false}`；运营者得到当前配置（不回显 token）。
  - `PUT|DELETE /a2a/operator/dws-identity`：`{uid, org_id, deap_agent_uuid?}`，uid/org_id 为十进制。
  - `PUT|DELETE /a2a/operator/forward`：`{rpc_url, token}`，token 为目标环境的 `mca2a_` Key，
    用 `MULTICA_A2A_PUSH_SECRET_KEY` 的 secretbox 加密存储。
- 表 `agent_a2a_operator_config`（迁移 9316，无外键，仅主键 agent_id）。每次写入打审计日志（不含密钥）。
- 前端：A2A Tab 底部的「运营配置」卡片，仅 `operator=true` 时渲染。

## 2. 场域标准化（服务端即可生效）

- Card 声明 `dingtalk-event/v1`，`required:false`。
- 纯函数把 DEAP 两处数据合并为与 Router `DispatchCommand` 字段名对齐的入站信封
  `multica.dingtalk_inbound.v1`：source / event / conversation(+thread) / sender / message
  (+referencedMessage、attachments) / receiver / deap(sessionId, runId)。
  取长补短：扩展优先（标准、带 thread/引用/开放 ID），私有 context 兜底（发送人 staffId、corpId、
  userType、proactive 标记、员工 agentUuid、DEAP session/run）。eventType 与 conversation.type
  不一致时丢弃扩展。临时下载 URL 不入信封，列入 `redacted_fields`。
  receiver 带上运营绑定的 uid/orgId，模型因此知道「我是谁」。
- 信封写进本轮 `chat_message.source_payload`；claim 在 A2A 会话无渠道绑定时按当前消息信封
  设置 `chat_type=group|p2p`，模型看到 `Audience: group room`。
- 不进入 Coordinator 决策循环（需要 Router 回调、场景互斥与窗口，会与 Router 同 cid 竞争）。
  信封字段与 DispatchCommand 对齐，为后续接入留口。

## 3. 员工身份（服务端先行，子进程生效依赖镜像）

- 准入：A2A 请求没有 `X-DWS-Token`、没有外部 ContextToken，且该 Agent 有运营绑定时，
  任务上下文写非敏感标记 `a2a_operator_dws_identity=true`。
- 启动：FC/ASB launcher 见标记后重新读取绑定（撤销即时生效），调用 Agent Identity
  `CreateContext(uid, orgId)` 把 ContextToken 交给 runner；runner 现有 `context_token`
  模式完成兑换与核验。
- claim 对带标记的 FC 任务下发 `a2a_runner_identity=true`；daemon 据此在 A2A 子进程恢复
  runner 的任务级 `DWS_CONFIG_DIR/GH_CONFIG_DIR`。这部分随下次 FC 镜像生效。
- 优先级：DEAP `X-DWS-Token` > 外部 ContextToken > 运营绑定。

## 4. 生产→预发转发

- 配置 `MULTICA_A2A_FORWARD_ALLOWED_ORIGINS`（HTTPS origin 列表，未配置即关闭）。
- 运营者给 Agent 配置目标 `rpc_url`（必须是允许 origin 下的 `/api/a2a/agents/{id}/v1`）、目标 Key，
  以及**唯一的来源 client**（`source_client_id`，Agent 只有一个启用 client 时可省略）。目标侧把所有转发请求
  看成它自己的同一个 client，只转发一个来源 client 才不会让不同调用方互看任务。
- `HandleAgentA2ARPC` 在本地凭证校验通过、请求体读完后决定：
  - 该 client 没绑定转发、读取失败或 origin 已被移出白名单 → 本地处理（配置清空即关闭）；
  - 请求带 `X-Multica-A2A-Forwarded` 而本 Agent 自己也在转发 → 508，既不本地执行也不再转发
    （防环，也防调用方伪造该头强制在生产执行）；
  - JSON-RPC 方法不在该 client 的 scope 内、或端点已下线且是新建回合的方法 → 本地 SDK 给出原生拒绝；
    端点下线后已转发任务的读取和取消照常转发；
  - 其余情况换 `Authorization` 为目标 Key，保留 DEAP 头（X-DingTalk-*、A2A-*、X-DWS-Token、trace、Skill Center 签名），
    去掉 Cookie、X-Forwarded-*、X-User-* 等，流式立即刷新，并发上限 64。
- 目标侧的限流和并发由目标 client 自己承担，来源 client 的限流不再生效；这是调试工具可接受的取舍。
- 一把目标 Key 终生只服务第一个来源 client（`a2a_forward_token_binding`，迁移 9317，只存 SHA-256）：
  换来源 client、清除后重建、换到别的 Agent 都会 409，要为新 client 在目标 Agent 另生成 Key。
  认领与转发配置在同一事务提交。9316 在预发单独存在过约 10 分钟，期间只有本次验证建过一条转发，
  已清除并在 9317 上线后重新认领；生产两条迁移同批上线，不存在未认领的旧配置，因此不做回填
  （Key 加密存储，SQL 无法回填）。
- 已归档的来源 Agent 与下线端点同样处理：不再转发新建回合，只转发已转发任务的读取和取消。
- Agent Card 仍由本环境提供，DEAP 配置无需改动。
- 只在生产部署后对 DEAP 流量生效；预发上用「预发 Agent A → 预发 Agent B」自转发验证。
- 身份绑定只对阿里云 FC 生效：只有 FC 的 claim 会向 daemon 证明 runner 已兑换身份，ASB 上不签发。

## 5. 验证

- 单测：信封合并/冲突/截断、Card 扩展、运营鉴权（非白名单 403、非 owner 403）、转发头处理与防环、
  claim chat_type、身份标记与 launcher 分支。
- 预发：直连 A2A 模拟 DEAP 单聊/群 @/群不 @/话题引用，核对 source_payload、Langfuse 中模型输入与
  Audience；运营接口绑定后看 runner `dws_auth_succeeded`；预发自转发链路。
- 生产发布（须莫分身代劳，需冬翔授权）后：Tagggg 在 DEAP 重新保存以刷新 Card，DEAP → 生产 → 预发实测。
