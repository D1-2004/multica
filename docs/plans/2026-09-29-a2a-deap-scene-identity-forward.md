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
  - `GET /a2a/operator`：非运营者只得 `{"operator": false}`；运营者得到身份、预发侧转发开关与登记状态、
    生产侧当前转发目标。
  - `PUT|DELETE /a2a/operator/dws-identity`：`{uid, org_id, display_name?, organization_name?, deap_agent_uuid?}`。
  - `PUT /a2a/operator/prod-forward`：`{accept}`，只在预发（登记方）存在。
- 每次写入打审计日志（不含密钥）。前端：A2A Tab 底部的「运营配置」卡片，仅 `operator=true` 时渲染。

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

## 3. 员工身份：与「集成 → 钉钉身份」是同一份（2026-09-29 review 修改）

- 身份存在 `agent_dingtalk_identity`，即集成页扫码绑定写的同一行。运营卡片绑定时直接写这一行
  （并清掉未完成的扫码尝试），清除时删掉这一行；集成页的绑定/解绑同样反映到运营卡片。
- `agent_a2a_operator_config.a2a_identity_enabled` 决定 A2A 能否用这份身份：运营卡片绑定时置 true，
  清除时置 false。只在集成页扫码绑定的智能体，A2A 仍不带身份，避免现有智能体的 A2A 调用方突然
  获得员工身份。之后在集成页改绑，A2A 用的也随之变成新身份。
- 准入：A2A 请求没有 `X-DWS-Token`、没有外部 ContextToken，且上述开关为真并有身份时，任务上下文
  写非敏感标记 `a2a_operator_dws_identity=true`。
- 启动：FC launcher 见标记后重新读取（撤销即时生效），调用 Agent Identity `CreateContext(uid, orgId)`，
  runner 现有 `context_token` 模式完成兑换与核验；ASB 不签发。
- claim 对带标记的 FC 任务下发 `a2a_runner_identity=true`；daemon 据此在 A2A 子进程恢复
  runner 的任务级 `DWS_CONFIG_DIR/GH_CONFIG_DIR`。已知缺口：A2A Pi 子进程的 HOME 是隔离目录，
  dws 的登录态跟 HOME 走（`HOME=/home/user dws auth status` 可见 Tagggg，子进程默认 HOME 看不到），
  需另行处理后随镜像生效。
- 优先级：DEAP `X-DWS-Token` > 外部 ContextToken > 运营开启的身份。

## 4. 生产→预发转发：预发勾选，生产自动转（2026-09-29 review 修改）

- 角色由配置决定：
  - 登记方（预发）：`MULTICA_A2A_FORWARD_REGISTRY_URLS`（要登记到的生产 origin）+ 共享密钥；
    运营卡片显示「接收生产转发」开关，默认开启。
  - 登记处（生产）：`MULTICA_A2A_FORWARD_ALLOWED_ORIGINS`（接受的预发 origin）+ 共享密钥 +
    `MULTICA_A2A_PUSH_SECRET_KEY`；运营卡片只读显示当前转发目标，不显示开关。
  - 共享密钥 `MULTICA_A2A_FORWARD_REGISTRATION_SECRET`（≥32 字符，两边相同）。
- 预发智能体在「绑定身份 + 身份允许 A2A + 开关开启 + 本智能体 A2A 已开启且未归档」时，对每个登记处
  发 `POST /api/internal/a2a/forward-registrations`，登记 `{uid, org_id, rpc_url, target_client_id, token, 智能体名}`。
  - 签名：HMAC-SHA256(时间戳 + "\n" + body)，时间戳为 Unix 毫秒、±5 分钟。生产按签名时间排序：
    签得更早的请求（重放或乱序重试）返回 409 且不生效。撤回必须带上所撤登记的 Key 指纹
    `token_sha256` 和同一 `rpc_url`，且签名晚于该登记。撤回与拒收都只把登记置为墓碑
    （`token_encrypted` 置空），保留签名时间，所以旧登记被重放后也不会复活。
  - 专用 client（`agent_a2a_forward_registrant`）：每个登记处、每名员工各一个。同一员工重新登记只轮换
    Key，client 不变，已转发任务仍可读取和取消；换成另一名员工时，吊销旧 client 连同其任务，
    另建新 client。生产侧按「目标 URL + 目标 client」认领来源 client（9317 的绑定表），轮换后认领不变。
  - 每个专用 client 建好后、发出第一把 Key 之前，先写一条不可改的归属记录
    （`agent_a2a_forward_client`：client → 员工，换员工时标记 `retired_at`）。
  - 顺序：新 client 及其员工先落库（落库失败就吊销新 Key、不发登记），再发登记，所以生产一拿到新 Key，
    预发就认。登记处接受后才吊销旧 Key；4xx（写入前就拒绝）只吊销没用上的新 Key；5xx、超时、断连
    视为「未确认」，两把都保留，下次同步再登记一次并清掉其余 Key。
  - 身份变化、关闭开关、清除身份时：先吊销本地 Key（预发立即拒绝），再向登记处撤回。
  - 触发：运营卡片绑定身份和开启开关（强制重新登记）、清除身份、A2A 端点上线（不强制；已绑定身份的
    智能体上线后自动登记）。端点下线不撤回：已转发任务照常读取和取消，新回合由端点自己拒绝。
  - 同一智能体的同步跨副本串行：`pg_try_advisory_xact_lock` 轮询（等待时不占连接池），拿到锁后
    重新读取状态。
  - 每个登记处的结果（登记时间、是否对应当前身份、错误）分别记录并显示；卡片提供「重新登记」。
- 失效即关闭：预发收到专用 client 的请求时，按归属记录核对：client 已退役，或所属员工与当前身份
  （开关、A2A 身份开关、`agent_dingtalk_identity`）不一致（例如在集成页改绑或解绑）时返回 401 并带
  `X-Multica-A2A-Forward-Key-Rejected: stale`；已吊销的 Key 返回 401 并带 `...: revoked`；读状态失败返回
  503。生产只在收到带该头的 401 时，把这条登记置为墓碑（只针对这把 Key），并重新解析一次：
  若恰逢轮换已有新登记，就改用新 Key 转发；否则在本地处理这次调用（带头的 401 说明目标什么都没执行）。
  其他 401 原样透传，不影响登记。因此撤回丢失、集成页改绑，都不会让 X 的流量以 Y 的身份在预发执行。
- 生产把登记存在 `a2a_forward_registration`（每个身份一条，Key 用 push secretbox 加密，只存 SHA-256
  指纹用于撤回和拒收）。
- 信任边界：预发运营者可以把生产上任一「已由运营开启 A2A 身份」的智能体的流量转到预发。两边的
  运营者名单是同一个人（冬翔），共享密钥只存在于两个部署的环境变量里；这是调试工具可接受的取舍。
- 生产 `HandleAgentA2ARPC` 在本地凭证校验通过后决定：
  - 本智能体未开启 A2A 身份、没有登记、登记指向本智能体自己、origin 已被移出白名单或读取失败 → 本地处理；
  - 请求带 `X-Multica-A2A-Forwarded` → 508（防环，也防伪造该头强制在生产执行）；
  - 方法不在该 client 的 scope 内，或端点下线/智能体已归档且是新建回合 → 本地 SDK 原生拒绝；
    已转发任务的读取和取消照常转发；
  - 认领转发目标：一个预发专用 client 终生只服务第一个来源 client（`a2a_forward_token_binding`，迁移 9317，
    键为目标 URL + 目标 client 的摘要）；同一智能体的第二个 client、或绑了同一身份的另一个智能体，
    都留在本地执行，不共享预发上的任务；
  - 其余情况换 `Authorization` 为登记 Key 转发，保留 DEAP 头，流式立即刷新，并发上限 64。
- 目标侧的限流和并发由预发专用 client 承担，这是调试工具可接受的取舍。
- Agent Card 仍由生产提供，DEAP 配置无需改动。只在生产部署并配置后对 DEAP 流量生效；
  预发把自己也列为登记处（`MULTICA_A2A_FORWARD_REGISTRY_URLS` 含预发 origin）即可自测。
- 迁移：9316 建表，9317 建认领表，9318 只做加法：运营表加 `a2a_identity_enabled`、
  `accept_prod_forward`、`updated_by`，新建 `agent_a2a_forward_registrant`、`agent_a2a_forward_client`（预发）和
  `a2a_forward_registration`（生产，含墓碑）。9316 的身份列和手动转发列停用但保留，滚动发布时旧版本
  副本仍能读取；等没有旧二进制后再在后续版本删除。预发旧列里的测试数据不迁移，按新入口重新绑定。
  均可重放，down 只删 9318 新增的表和列。

## 5. 验证

- 单测：信封合并/冲突/截断、Card 扩展、运营鉴权（非白名单 403、非 owner 403）、转发头处理与防环、
  claim chat_type、身份标记与 launcher 分支。
- 预发：直连 A2A 模拟 DEAP 单聊/群 @/群不 @/话题引用，核对 source_payload、Langfuse 中模型输入与
  Audience；运营卡片绑定身份后集成页同步可见；预发两个智能体绑同一身份，一个开「接收生产转发」自动
  登记，另一个收到的 A2A 请求转到前者。
- 生产发布（须莫分身代劳，需冬翔授权）后：Tagggg 在 DEAP 重新保存以刷新 Card，DEAP → 生产 → 预发实测。
