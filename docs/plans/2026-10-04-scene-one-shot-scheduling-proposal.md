# 场域一次性定时任务方案

日期：2026-10-04。状态：已实现并完成定向本地验证，尚未部署或完成真实 IM 验收。

## 结论与问题证据

一次性是调度资源的时间规则，不能用周期 Cron 加“执行后自行删除”模拟。建议扩展场域现有定时入口，支持 `once` 与 `cron`；用户直接说“15 分钟后提醒我”即可创建独立安排，不需要预先有例行任务、模板或 Issue。产品统一叫“定时任务”，分别展示“一次性”和“重复”。

读取冬翔账号线上 IM，Qwen-DWS 会话 `cidMF9FOe1ACHthSFNyLtfy5uHGOC8F8fjtAQpiGLe8r1I=`。相关后端 Langfuse environment 实际是 `pre`，不能由 DWS 环境推断服务器部署环境。时间均为北京时间：

| 时间 | 请求/结果 | 真实执行证据 |
| --- | --- | --- |
| 14:17:37 | “5分钟后 提醒我。” | task trace `85be76e7879a42c997fcb13fc067e69b` 读 `scene_routine_list` 得空列表；bash `sleep 255`，14:22:20 返回；IM 14:22:24 有提醒 |
| 14:17:52 | “你有哪些定时任务” | 回复没有定时任务。说明沙箱正在等的提醒没有可管理的持久资源 |
| 14:18:06 | “15分钟后提醒我” | trace `ff75a48e8db043f48bcfaa7c897123ff` 读空列表；创建待办 `57659443655`，截止 14:33，写 customTime 提醒成功 |
| 14:19:27 | 宣称已设置，解释改为待办提醒 | 待办提醒写入回执不是原单聊定时送达证据，提醒规则本身不能回读 |

不是统一“没有回复”：第一例 IM 送达，但等待依赖活着的沙箱；第二例改变交付入口。后台任务完成也不等于未来提醒已经送达。读取最近 250 条 IM 有截断，以上仅对已定位消息和两条精确 trace 下结论。SLS 指定预发 cid/时间窗口查询返回 0 条，未取得 Coordinator 裁决证据，不补推断。

## 对标依据与适用边界

### Claude Tag

官方 [Set up routines](https://claude.com/docs/claude-tag/users/proactivity) 和 [Academy 课程](https://academy.claude.com/courses/introduction-to-claude-tag/put-recurring-work-on-a-schedule) 描述自然语言建立周期工作、场域连接器与权限、原场域/线程交付、列表及停用。私聊安排归个人账号，频道安排归频道。公开资料未说明一次性调度内部实现，不能把 Claude Code 的 CronCreate 当成 Claude Tag 的实现证据。

采用：聊天就是创建与管理入口；保留场域归属；结果可继续追问。暂不采用其“创建者离开频道后继续运行”的权限语义：本方案先使用现有平台授权，不自动把私人安排升级为频道共同授权。

### gawkbot

本机源码 `/Users/yuanzhan/github/gawkbot`，核对 commit `71e82a1809565281cbd0bf8185d3c125b715d934`：

- `internal/team/broker_scheduler.go:186`：到期由 `DueAt/NextRun` 判断；`done/canceled` 不再触发，完成会清除到期字段。
- `internal/team/scheduler_lifecycle.go:398` 起：创建请求有 `DueAt/NextRun`，创建时可用 DueAt 初始化 NextRun；但普通创建入口仍要求周期字段或 legacy workflow，不能声称直接传 due_at 就能建普通一次性任务。
- `internal/team/scheduler.go:660` 起：周期 routine 完成后必须回到 `scheduled`；`done` 是终结一次性任务的语义。bot routine 的 nextRoutineRun 有周期 fallback，不能直接照搬当 once 执行器。

采用：定时、执行记录、生命周期由 broker/平台管理，而不是模型靠 sleep；一次性终结与周期重新调度明确分开。多副本数据库事务和外部消息投递仍需按本仓合同实现。

### 调查时旧 checkout 的复用边界

当前 commit `2a520dea5ce616c05861aec0e72192c7c09fb596` 无轨迹中的 `scene_routine_*` 实现，也缺 `docs/development-delivery.md`、`docs/employee-delivery-workflow.md`。实施前必须定位实际预发版本和该 MCP 服务源码，再写精确文件/表迁移清单，不能把当前 checkout 当运行版本。

已核对可复用合同：

- `server/internal/handler/autopilot.go` 的 schedule 创建需要 cron；`server/internal/scheduler/jobs_autopilot.go` 的周期 planner 不适合直接套一次性补发策略。
- `server/internal/dshschedule/README.md`、`schedule.go` 已有 once 到期不丢弃、occurrence 身份、事务内新任务 admission、取消 tombstone 与新执行凭证的合同。它绑定 DSH Session/native schedule，原生适配与上线验收未完成；本次实际 provider 为 `pi`。应复用调度与 admission 思路及可抽出的下层机制，不强行让 PI 伪装 DSH，也不另建平行任务执行系统。

## 建议接口与数据模型

以下是建议合同，不是现有 API：扩展现有 `scene_routine_create/list/update/delete` 入口；需要更名时保留兼容别名。新建规范参数为 discriminated union：

```json
{
  "name": "提醒冬翔",
  "schedule": {"kind": "once", "run_at": "2026-10-04T14:33:06.107+08:00"},
  "action": {"kind": "reminder", "text": "提醒时间到了。"},
  "idempotency_key": "derived-from-source-message-and-action"
}
```

周期形态是 `schedule={kind:cron,expression:...,timezone:Asia/Shanghai}`。旧 cron-only 调用在服务端映射成 cron。once 不接收 cron；时间必须带 offset，服务端存 UTC，展示用用户时区。相对时间按入站消息的 occurredAt 加时长解析，不能每次工具重试重新从 now 开始算。上例保留请求时间秒数，不能默默舍入成 14:33:00。

服务端从认证场域及来源证据派生 workspace、employee、conversation/thread、发起人、来源消息；不信任模型自报的归属或身份。保留原句、解析后的时间和 action。响应在事务提交后返回 id、准确时间、交付位置、状态、取消能力。相同幂等键相同内容返回原资源；不同内容冲突。用户再次独立发送相同文字可以是新的安排，不能仅按内容去重。

只说“提醒我”可用中性的“提醒时间到了”，不编造“之前交代的事情”。纯提醒是确定性的消息 action；“到点查数据并汇总”是 `agent_task` action，走既有数字员工执行链。两者共享时间规则、权限和 occurrence 记录。

## 调度、执行与投递合同

1. 短创建任务提交资源后结束，释放沙箱。平台按数据库时间扫描 `pending AND run_at <= now()`；once 不套周期 Cron 的五分钟丢弃窗口。
2. 锁定资源与 occurrence，按 `(schedule_id, revision, planned_at)` 唯一 admission；事务内写执行记录及既有 task/outbox，并把 once 标成 `admitted`、清空 next_due。多副本只能产生一个逻辑 occurrence。事务失败不消费资源。
3. after commit 唤醒既有持久队列。任务使用新凭证；不保存创建时 task token，不要求旧沙箱或旧 Session lease 仍活着。场域、来源和交付目标保留，必要上下文重新装配；计划内容作为存储的触发材料，不能伪装新的系统指令。
4. occurrence 执行/投递可重试，但不重新产生下一次计划。纯提醒写既有消息 outbox；agent_task 经既有任务结果投递链。只有取得实际发送回执才标 `delivered/completed`，LLM 生成文本不能当投递完成。
5. `pending -> admitted -> completed/failed`；取消 pending 原子变 `cancelled`。已 admitted 时取消对应执行/outbox，按既有取消边界返回是否来得及；已经发送则返回“已发送”，不能承诺撤回。改期使用 revision 与行锁，过期编辑返回冲突；已 admission 需明确取消旧执行再另建，不能静默重新激活。
6. 保存终结与取消记录，避免迟到的创建重试复活。对外可以不在活动列表显示，但留历史与关联 run。清理按隐私/保留规则；不以模型自己删资源实现 once。

外部 IM 不承诺严格 exactly-once：内部唯一 admission 可保证，发送超时却已送达时需依赖渠道幂等键或可核验回读；无此能力标 `delivery_unknown` 并对账，不能无条件重发或宣称“绝不重复”。

逾期默认补发一次并标明原计划时间；可显式设 expires_at，超限返回 expired 并留记录。提交前目标时间已过去则如实说明并按明确的即时执行/逾期策略处理，不偷偷排到明年。权限、成员资格、连接器开关在触发时重查；不满足时 blocked，恢复后须重新 admission。创建者离开个人私聊授权范围则停止个人安排。

## 产品与实施顺序

用户说“你有哪些定时任务”，应列当前场域的 pending 与 admitted 一次性安排及周期任务，而非只返回例行配置。配置界面沿用现有入口，提供“一次性 / 重复”选项；已完成安排进入历史。取消、改期使用自然语言定位到真实 id，多候选再问。普通提醒只有创建确认与到点消息，不加“开始执行例行任务”的噪音。

实施顺序：先定位实际场域 MCP 源码并更新接口 spec；补 schedule union 与持久字段/occurrence admission；接原有任务和 outbox；更新工具 schema、能力提示与列表/管理页面。DSH native adapter 再通过适配器共享合同，不作为 PI 一次性提醒的前置门槛。数据库变更不加 FK/级联；新增唯一或扫描索引各用独立 CONCURRENTLY migration。

真实验收以预发 Qwen-DWS 私聊为目标，明确当前 manifest/runtime，不改已有业务任务。至少覆盖：5/15 分钟提醒准确创建且只在原单聊送达；原创建任务与沙箱结束后仍送达；服务重启/多副本/响应丢失；到期取消竞争；改期；权限撤销；延迟恢复补发；IM 投递未知状态；列表能查待触发与执行中安排；周期任务行为不退化。IM、API/DB、task/outbox、SLS、Langfuse 分面记录，不把任一层成功替代端到端完成。LLM-as-judge 重点评审是否偷换成待办、错误周期化、时间漂移、越权跨场域、无回执宣称成功。

最初调查阶段只产出方案；用户随后授权实现，当前实施与验收状态见文末。尚未部署或提交。精确 trace 与 IM 原始查询保留在本机 `/tmp/qwen-*-trace.json`、`/tmp/qwen-messages.json`（临时文件，不是长期证据归档）；此文记录复查 ID 和关键事实。后续实施须归档脱敏验收证据并回填结果。

## 实施合同（用户于本会话授权实现）

- 工作基线已定位：本工作区从旧 develop detached HEAD 切到本机当前 `feat/tag-multitenant` 的独立分支 `codex/scene-once-schedule-20261004`，保留原方案文件。该基线有场域 MCP 与 Employee routine 事务链；不导入其他 session 的未提交工作。
- 实际接口沿用 `trigger`，新增 `kind=once, run_at=RFC3339`；同一个 autopilot trigger ledger，不新增平行调度器。仅支持 Employee-mode 场域创建，一次性消费与已存在 occurrence/Task/Run/outbox admission 同事务。
- 原场域、原来源消息、请求人、相关已编译工作材料冻结到最小 source snapshot；到点新任务加载当前场域授权和配置，不继承旧 task token/私人凭证。原材料是有来源的数据，不升级成新的系统指令。
- 验收：创建即返回可查询资源、原沙箱结束后仍触发、逾期补发、多副本不多建、改期/取消竞争不执行旧计划、原单聊与上下文语义不丢、旧 cron/webhook 不退化。
- 本轮范围：实现与定向本地/数据库验证；不自动部署到共享预发、不发送真实业务消息。真实 IM E2E 状态独立记录。
- 并行所有权：调度/SQL；场域来源与packet；UI/schema；主代理负责handler/MCP/合同与整合。每个里程碑回填，不把编译成功当真实交付。

## 实施结果与验证

实现基线 `582cba1f73de253f7fc5fc1edcd6318f1328ed31`，分支 `codex/scene-once-schedule-20261004`。原实现已固定本地提交769d406a8f；本轮改号与合同补件另给固定SHA，最终净差分不含旧9977/9980/9981迁移文件。没有修改共享预发或业务消息。

- 已实现：once/run_at 的场域 MCP/API、配置界面、响应schema，原有routine/autopilot scheduler/occurrence/Task/Run/outbox链复用。独立一次性不要求先有周期任务。绝对时间保持秒及微秒精度；相对时间schema与skill要求以原消息occurredAt计算。
- 原source snapshot按workspace/agent/tenant/scene验证后持久化，数据库保护不可变；保存原请求时间、文本、请求人、queue/task/run引用、已编译工作材料。仅允许名单字段，材料文本经过现有redact规则；没有复制token/MCP配置，也不恢复个人层权限。触发时重查当前scene、来源任务绑定与权限。
- once admission与消费同事务；取消/改期与受理使用同一行锁；已消费不能恢复或手动再跑；取消tombstone在重复取消后仍保留，原创建响应重放不能复活。改期不改变原创建幂等身份。
- 仅强类型 `ErrOnceAdmissionPending`（mode/reader暂不可用）在三次短重试耗尽后，每15分钟给同一未受理计划增加一次冷探测；attempt审计单调递增，已消费、有run/receipt、停用/改期均不能重开。普通错误与cron重试规则不变。
- 一次性不发送开始通知，完成结果通过现有原场域outbox直接送出；失败或取消结果仍有明确文本。内部admission幂等不等于外部IM严格exactly-once，未冒称渠道保证。
- 滚动版本能力标记升为 `[employee-loop:13]`，新创建/受理需全部在线副本具备reader，防止旧副本忽略once或source合同。

本地专用PostgreSQL `multica_once_20261004` 已执行全部迁移（包括9977、9980、9981），未使用预发数据库。定向验证通过：

1. scheduler/service/handler受影响测试：并发首次受理、事务回滚、提交后崩溃重放、旧改期计划拒绝、到期后与改期后creation replay、重复取消tombstone、已消费操作拒绝；暂不可用三次耗尽后冷却恢复。
2. `TestEmployeeOnceSourceSurvivesCompletedOriginalRun`：真实PG中原task/run完成后，Capture→immutable source→新Direct admission→receipt/packet保留原消息、时间、请求人、历史/上游材料和原queue/task/run引用；无dispatch/person/凭证继承。Host/provider为fixture，不是线上IM E2E。
3. `TestOnceSceneOutboxKeepsOriginalDestinationAndSingleMessage`：实际PG/outbox记录保持原conversation/org/recipient与最终文本，无开始消息，重复终结写同一outbox action。尚未调用真实IM provider。
4. core schema 8项、views一次性/时间/场域UI 11项通过；core typecheck通过。Go vet受影响包、git diff --check、autopilot专项sqlc生成校验通过。Coordinator policy checker仅结构校验通过（PASS_STRUCTURAL_ONLY）。

基线已有且本次未修改的检查问题：views typecheck因 `agents/components/agent-detail-inspector.labels.test.tsx:54` 的RuntimeDevice fixture字段缺失失败；迁移lint发现基线9000+重复编号（新9977/9980/9981无重复）；全仓sqlc diff在既有agent.sql:102/runtime.sql:440歧义列失败。专项 `scripts/generate-autopilot-sqlc.py --check` 通过，未把全仓sqlc或全项目测试写成通过。

证据日志本机 `/tmp/once-final-backend.log`、`/tmp/once-final-handler.log`、`/tmp/once-outbox-test.log`、`/tmp/once-autopilot-sqlc.log`、`/tmp/once-vet.log`，长期复验入口为已提交到工作区的测试源码。

遗留验收：部署到全部支持13的副本后，用Qwen-DWS真实单聊复测5/15分钟、源任务结束后的提醒、取消/改期、服务重启与真实投递回执；实测到点排队延迟不等于承诺精确秒送达。本轮完成代码与本地链路，部署与真实IM效果独立签收。

## 用户补充：评测集、短延迟与统一发布协调

用户要求实现后增加评测集，协调「发布和验收」与评测维护方共同讨论；短期先3–5分钟简单case，考虑整体scheduler负载；<1分钟允许原沙箱内运行，但不是强制必须优化成双路径。

已发送功能与本地验证交接、source分支/工作目录、reader/迁移碰撞提示至「发布和验收」，并向「SPEC+EVALS」请求权威目录映射。对方当前审阅明确归属 `spec-collaboration → cron-office → G15`。贡献草稿6条已写 [机器定义](../evals/scene-one-shot-candidate.json) 与 [runbook](../evals/scene-one-shot-runbook.md)，结构检查通过；canonical整合/真实IM仍未完成。

新增负载边界记录：等待持久化不占sandbox；现有scheduler逐scope、无独立全局scan/dispatch预算，agent claim限额与FC429退避不等于全局压力签收。首波低负载先验；隔离第二波检查同刻到期的排队/容量/其他job进展，不在共享预发灌压。45秒可采用持久once，短等待替代仅单一owner/有界<60秒时允许。具体lateness与执行资源预算需由发布负责人在该波manifest冻结，不能事后调整。

## 统一发布补件：迁移编号及首波时间门

「发布和验收」已接收候选并纳统一队列，要求源侧解决未部署编号碰撞。已核对共享repo完整历史路径、HEAD、employee/progress-release、feat/evaluation-hub、codex/tag-human-integration-precheck-20261004、feat/employee-intent-stream-feedback及各worktree的10060–10069占用，未发现已有文件。将三对迁移重命名为：

- `10060_autopilot_once`（原9977）
- `10061_scene_routine_source`（原9980）
- `10062_scene_routine_source_immutable`（原9981）

源码/source-map同步更新；测试和migrator hook无旧编号引用，这三条不建索引，因此不新增concurrent-index hook；尚未部署的旧stem不加生产别名，保留旧编号只作为本文历史证据。未触及已部署10020–10023。源reader13仍由统一发布负责人语义纳入累积reader20/21，不能降级。

首波6稳定ID不变，case条目剥离execution/status，执行元数据移到独立executionPlan；正式纳入canonical由统一分支维护。已修正“无周期资源”为`trigger.kind=once且无重复cron规则`，旧周期case保留。

负责人确认低负载3/5分钟：due→admission≤45秒、due→消息≤120秒分别核验，非业务SLA；原occurredAt、受理、run_at、due、admission/claim、最终DWS投递完整保存。45秒另定预算；5/20同刻到期单独隔离第二波。场域避开dm_director/group_e2e与其他执行者，待精确版本/live证明后才跑；跨发布保留checkpoint只复验受影响例。

### 10000+迁移顺序修复

改号校验实际发现Files与专项sqlc按lexical排序，10060出现在9520前，fresh schema分析会在context_scope_routine创建前ALTER。实施范围补充为：实际migrator Files按数字前缀升/降序，同前缀保留完整文件名字典序；full stem识别、ledger及旧部署记录不变。专项autopilot sqlc采用相同数字前缀顺序。增加直接覆盖4位/5位版本跨界及反向rollback顺序的测试，并验证旧<10000序列顺序未改变。此问题直接影响本次新编号，不扩展到重写其他专项生成器；统一release已有修复时语义复用。

改号补件验证结果：新建专用本地PostgreSQL `multica_once_renumber_20261004`，实际packaged migrator从空库初始化到10060/10061/10062成功；已有本地库重放也成功。`TestMigrationOrderCrossesFiveDigitBoundary` 与 `TestMigrationOrderPreservesExistingFourDigitSequence` 通过，up跨4/5位数字与down逆序、原<10000序列均覆盖。新版数据库上handler/service/scheduler一次性定向回归、migrator alias校验、Go vet migrations、autopilot专项sqlc --check、候选6case结构与diff --check通过。未执行真实IM或共享部署。

完整交付由统一发布负责人采用base582c到最终source SHA的净差分或等价语义集成；不能在A2UI已有旧编号的目标里单取769d406a8f后提前执行migration。其他全schema专项生成器若仍用filename lexical排序，应在统一工具链按相同numeric版本合同对齐；本次未借机修改不相关生成器或修复既有SQL歧义。
