# Employee / Tag 开发、发布与真实验收工作流

2026-10-04 冬翔确认：接续开发要先明确工作流、环境和验收方法，对照 session 目标与实际代码交付推进；每次测试后独立观测 IM、SLS、Langfuse 和执行事实。本文件记录这套做法，不替代当前产品合同或赋予额外发布/外联权限。

## 1. 从目标与地面事实开始

读取根 `CLAUDE.md`、当前模块合同、session Plan、用户后续纠正和交接材料。用提交、源码与实时配置逐项判断 implemented / integrated / deployed / e2e_verified；历史计划和模型口述只作线索。

每个切片登记：用户结果、验收条件、当前代码 head、已合入等价实现、部署 run/release、验证状态、剩余阻断。`git cherry +` 需进一步检查源码或等价提交，不能直接当缺口；某类型或工具已定义也不等于生产链路已接线。

优先填当前 session 的产品缺口和真实反例。完整原设计的开放承诺单独保留，不把一个切片通过说成整个系统交付。

## 2. 开发与集成

主代理唯一负责集成分支、共享预发部署和租户 Apply。需要并行时，开发者在各自 worktree 和明确文件所有权内修改；验证者独占约定的钉钉场域，避免近期历史相互污染。主目录其他会话的 WIP 不 stash、不提交、不还原。

涉及状态机、并发、核心链路或跨模块先更新 Plan/spec，再动代码。架构选择先对照已固定来源和类似框架，写明借鉴谁、为什么及不照搬的边界，不重新展开已定方案评委流程。

定向验证真实风险：权限、事务、重放、取消/退出、效果归属、旧快照与混版。使用独立本地数据库/Redis，显式配置环境，禁止默认共享库、预发库和 ambient agent CLI。低价值实现镜像测试不新增；未覆盖风险不能由 skip 或其他 regex 的绿灯销掉。

按测试名归一化基线，注释、子测试和环境失败分别说明。已有且未受改动影响的通过用例不反复重跑。广泛 handler/service 回归按发布风险集中做，文档修改不触发部署。

集成前检查 diff、工作区/场域边界、来源冻结、迁移完整 stems、事务通知次序、SOURCE_MAP。保留其他会话变更，冲突语义解决，默认 rebase/fast-forward，避免无意义 merge commit。

## 3. 发布与生效

操作前读本仓 `aone-deploy` 和当前 a1 命令契约。确认真实目标分支、CR、应用及环境；不把旧 CR 或 runId 直接当本次目标。内网命令在进程内去除六个代理变量，凭据只在进程内读取，不打印、不入库。

代码按本次授权远端提交，本仓 Aone fork 用 `aone`；不能把 GitHub 的个人提交偏好混进本仓 Aone 命令。提交分支→触发 pipeline 66→观察精确 run→等待实际部署结果。流水线等待用有界通知/后台观察，不反复查未变状态；发布中仍向用户报告有意义的进展。

“可开始当版验收”需要三组证据：

1. release/source ancestry 包含交付代码；只看到分支 head 或 build_id=dev@unknown 不够。
2. 所有 live 副本有本次部署后的 backend `server starting`。记录各 pod 和启动时刻；fence 的历史 started_at 不能当本次启动日志。
3. 当前 fence normal，所有 live 副本有新 reader 所需的累积 marker。先 reader 后 producer；新来源不能暴露给不懂它的旧 consumer。

新 schema 随 Aone packaged migrator 执行，不手工迁移预发。模板修改之后必须针对约定租户 Apply，并 GET 回读 revision、runtime 和目标配置；不把模板已改当租户已生效。共享 runtime 变更需核对其他绑定，候选优先新建独立 runtime。运行时/Daemon 协议触及时按 `fc-runtime-dev-loop` 保留固定 commit/template 与真实 canary、滚动兼容证据。

流水线手动“预发验证”门在有效验收结论满足本次范围后再关闭。pipeline 66 不等于正式发布。回退先停新 producer、处理已受理工作、保留兼容 reader，再确认旧版本可读剩余数据；不能只把二进制降回去。

## 4. 验证环境与准入

每波写不含凭据的 manifest，固定以下事实；运行时读数优先于旧交接：

| 类别 | 必须登记 |
| --- | --- |
| 对象 | server origin、workspace_id、agent_id、tenant org、coordination mode、Tag revision、runtime/template |
| 部署 | source/release head、pipeline/run、各 live pod、累积 marker、各 pod 启动时间、窗口前后读数 |
| 场域 | 服务端目录返回的 scene_id、kind、外部 cid、每个 case 的独占场域和参与人 |
| 演员 | 真实组织/profile、身份读取、认证有效、发送方可见的 openDingTalkId；多组织同 uid 不可混用 |
| 能力 | 当前有效配置、探针/订阅状态、harness 版本、显式 capabilities、dry-run 的 runnable/partial/blocked 名单 |
| 干扰 | 常驻 routine、未终结 Task/邀请、测试窗口、独立库/Redis、第三真人/跨日等依赖 |

本仓真实 IM 使用线上 DWS 网关，后台对象仍是预发 Multica；二者环境独立。用进程私有 `DWS_CONFIG_DIR` 固定线上网关，每次发前断言，不切全局 `~/.dws`。演员以用户指定的真人和号池为准：@/单聊/采集按当前剧本要求使用真实身份，DEAP 入群介绍卡不算答复。缺身份或实际答复条件记 waiting_actor，不能偷偷换演员。

唯一场域键是 `scene_id`；cid 用于外部消息读取/发送，不可作内部场域键。观察他人群话不自动获得执行授权。隔离用例需要两个确切独立 cid/scene；续接用例保留原 scene，同一场域的多消息先后不能交错给其他案例。

临时暂停 routine 或修改测试配置前记实际业务状态，设置明确恢复点；结束、失败或中断都恢复并回读。MEMX-D1 的 group_t 和 M5 的 g_team 不串群。不得把其他案例写入计为本案例未消化人话。

## 5. 一条案例怎么运行

开始前写 case manifest：要证什么、来源/演员/场域、输入台词、准确期待、会抓住哪个错误实现、需要的观察面、截止/等待与清理。台词像真人，不塞实现术语；唯一探针用于对应来源，下一轮问题不含答案或探针。

执行顺序：

1. 固定当版环境与前置事实。能力/身份未就绪不开依赖测试。
2. DWS 发送，记录 UUID/source messageId、时间与演员；未知发送结果先回读，重试复用同一幂等键，不双发。
3. 独立回读真实目标 cid 的消息/文件。保留引用、发送者、时间、消息 id、complete/hasMore/failures；分页未覆盖窗口不能做否定结论。本仓历史 harness 避免 `--start`，使用已验证的分页方法。
4. 按精确 source/receipt/job/Task/Run/scene 关联 SLS、业务 API 和 LF，读完整 generation/tool 事实与终态；查已知 ID 优先于全项目最近列表。
5. 记忆/关系等本轮只证明写入，等待实际 flush/状态落稳后，下一轮独立询问并检验召回与使用；不能用当前 history 中的原话冒充长期记忆。
6. 记录结束环境；部署/重启穿过窗口时 invalid_env，不能算产品 fail/pass。证据缺失记 incomplete/blocked，零命中与无模型调用都需完整窗口证明。
7. 完成即写结论、原始证据路径、未证明项与清理结果，并更新进度板。失败缩成原反例→修复→部署→原场景复测，再补受影响回归。

## 6. 测完观测什么

| 观察面 | 必查内容 | 能证明与边界 |
| --- | --- | --- |
| 真实 IM/文件 | 目标会话、收件人、quote、文本/格式、时间、次数；文件下载与字节/hash | 用户实际看见什么。sendStatus、模型说“已发”及 outbox committed 均不能代替 |
| 业务 API / 获准只读状态 | Receipt→Job→EmployeeTask/Run→queue→outbox/action；等待/输入归属、幂等、取消/退出 proof | 接了哪条来源、推进是否正确。completed 不自动证明子进程退出；本机 PG 不替预发 |
| SLS / backend logs | 指定 agent/scene/job/receipt 时间窗与 pod；摄入、路由、门禁、完成/拒绝、通知 owner、错误、重复、启动窗口 | Host 确定性事实。完整模型输入需转相应 LF observation；单 pod log tail 不能冒充全副本窗口 |
| Employee LF | `employee_loop`，job_id/receipt_ids/scene_id；`employee_model` 的冻结 messages/tools、native tool/result、lease/replay、failure/outbox状态和截断标记 | 模型看到了什么、为何 reply/quiet/dispatch；实际输出和投递仍回读 IM |
| 后台 Task LF | `agent_task`，精确 task_id/runtime_id/autopilot_run_id；`llm.call.N` 输入输出、真实工具、结果/失败 | 沙箱确实执行了什么。不要套前台三次请求预算；根摘要不能代替 generation |
| Digest LF | `employee_scene_digest`，scene/run/page_hash/游标、call、accepted/rejected、replay | 哪个段落为何 flush、引文是否真人逐字来源。调用成功不等于事实接受或下一轮召回 |
| Coordinator 专项 | `inbound_coordinator_*` SLS 与对应 observation，assoc_recall/finish | 仅用于 Coordinator 路径；不能把它的旧 prompt 格式强套到 Employee |

Employee 本仓当前可定位的日志事件包括 `dws_event_optional_probe`、`dws_event_received`、`employee_scene_job_completed`、`employee_proactive_decided`、`employee_verbatim_recall`、`employee_scene_history_record_failed`、`employee_scene_digest_run`。具体字段从当前源码确认，不猜日志名。禁止拿 Router LLM trace 顶 Host 上下文；送达也不能由 Router 工具自述替代。

SLS 用 Normandy 当前命令，固定 project/logstore/预发标签、起止时间、size/offset 与截断/分页说明。超时可用 log tail 查看当前故障并留缺证，必要时有限补查；不要把查询失败写成“没有事件”。

LF 用本仓 `inspect-langfuse` / `inspect-langfuse-trace` 和 `scripts/query-langfuse.sh`，环境 `pre`。Employee 查询显式 `--name employee_loop`；当前 helper 单用 `--loop employee_loop --agent <uuid>` 仍会加默认 inbound_coordinator tag，产生假零命中。后台用 `--name agent_task`，digest 用 `--name employee_scene_digest`。已知 trace 直接取详情，不因列表时间窗被派生任务覆盖而判丢失。检查 `input_truncated/output_truncated`，压缩正文需可靠解码；不可读则记缺证。自建 LF 3.x 不走通用 CLI 的 v2 observations。

记忆时机按当前 Employee 合同：每条群话 upsert/标脏 0 模型，每 wake/Task 终态确定性账本 0 模型；模型 flush 仅空闲 ≥30 分钟或未消化人话 ≥12 条（去抖 60 秒、最长 10 分钟）。证明零模型调用需同场域完整窗口与原始状态/trace，不能只看最近列表没命中。

## 7. 签收、进度与长期复用

状态区分 pass / fail / invalid_env / waiting_actor / blocked / incomplete / known_limit。partial 或 vacuous 检查不计完整 pass；安全、权限、隔离等硬失败不能被平均质量分抵消。

模型质量由事实准确、指代、连续性、必要派发、自然交互和边界反例判断。LLM-as-judge 的结论关联证据；原始延迟/token 不能作为主要交付标准。保留失败证据，禁止通过清历史、换场域、换口径躲原反例。

交付材料包含本次代码/部署 manifest、测试名单与边界、逐例 IM/API/SLS/LF 证据、清理/恢复、开放承诺和下步。临时材料尽快转存持久证据目录，认证目录不入库、不分享；只读 token 不落正文。

当前 Employee 第五批续接详情见 [15 接续核对](plans/2026-10-03/employee-loop-backend-delivery/15-codex-resume-assessment.md) 与 [执行板](plans/2026-10-03/employee-loop-backend-delivery/11-execution-board.md)。具体账号、群、run、runtime、marker 以当波 manifest 的实时读取为准，不能从个人 skill 永久抄固定值。
