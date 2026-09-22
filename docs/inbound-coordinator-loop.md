# Coordinator 现行行为合同

policy_version: `2026-09-22.1`。装配版本：`42`。本文件描述此分支的实现合同；发布和行为验收状态以对应 Plan 与运行证据为准。

Coordinator 的交付物是每条请求的去向与有证据的协调状态。它识别人和请求、恢复指代、必要澄清、选择新建或续接，并通过有限动作承接问候、能力、记忆、进度与结果回报。产品机制、专业分析、检索查证、文件及发送等工作交执行器；任何动作的 reply 字段都不能用来抢答业务结论。快循环和执行器属于同一个员工，分别承担协调与执行。

优先级是不误执行、不漏请求、不串身份和场域、不虚报状态，然后才是自然表达、响应速度和上下文成本。历史翻车经验保存为行为义务与案例；旧补丁可以被明确替换，不能把相反的规则同时留成现行合同。

## 1. 维护入口与历史账本

修改 Coordinator 的提示词、工具、上下文、handler、assoc、scenememory、窗口、回执或 trace 前，先读本文件和 [规则目录](../server/internal/service/inboundcoord/policy/registry.json)。目录登记 `COORD.F01`–`COORD.F19` 的行为义务、模块、实现引用、对照案例和已撤回手段。

- [场景容量按委托人计 Plan](plans/2026-09-16-coordinator-scene-capacity-per-delegator.md) 记录正式 oa测试群 连发三条 VOC 诉求被「名额已满」挡住的证据、容量改按「场景×委托人」、在飞判定与等待上限；
- [目录/记忆伪造权限 Plan](plans/2026-09-14-coordinator-invented-access-limit.md) 记录审核用协调目录和场域记忆发明 MCP 权限、把建单降级成文案单再死锁，以及 Host 放行合法 start_work、把 different_deliverable 改写成 start_work 的合同；
- [同类批量变更被误拆 Plan](plans/2026-09-14-coordinator-same-kind-batch.md) 记录正式 oa测试群 金龙把两类负责人改配判成独立交付并 review_deadlock 的证据、合同收窄与 Host 修复；
- [历史预取与轻量问候免审 Plan](plans/2026-09-10-coordinator-history-prefetch.md) 记录冬翔→菲迪→须莫代问链路的触发证据、89 轮扫描统计、历史预取/免审/转告规则与验证状态；
- [DWS执行恢复Plan](plans/2026-09-10-dws-execution-recovery.md) 记录来源交付、单项执行边界与Host等待反馈；
- [通用语义修复 Plan](plans/2026-09-10-generic-proactive-semantics.md) 记录身份未知边界、词表撤回与跨场景验证；
- [decline边界Plan](plans/2026-09-10-coordinator-decline-boundary.md) 记录主模型、Host与审核配置来源一致性及验证；
- [Host预取Plan](plans/2026-09-09-coordinator-prefetch.md) 记录当前场域必需读取前置的收益假设、额外成本与实测；
- [有限动作与短合同 Plan](plans/2026-09-09-coordinator-closed-actions.md) 记录当前动作协议、迁移边界和验收；
- [上下文减重与终结审查 Plan](plans/2026-09-09-coordinator-scope-and-finish.md) 记录本次实现与验证状态；[原实施 Plan](plans/2026-09-07-coordinator-progressive-context.md) 保留此前分阶段证据。
- [103 条来源清单](plans/2026-09-07-coordinator-progressive-context-inventory.json) 保留原 trace 的 99 条 bullet、4 条开场原文及哈希；候选映射另列，不覆盖源证据。
- [最小对照合同](../server/internal/service/inboundcoord/policy/cases.json) 说明预期与禁止效果，未运行的仍为 `not_run`。
- [9 月 7 日 collect 事故](plans/2026-09-07-coordinator-collect-window.md) 保留原事故及红绿证据；其真实 IM 0/3、权限阻塞的旧结果不能因本次结构检查而变成通过。
- 历史 Plan 是当时的设计/复盘记录。遇到冲突按本现行合同和明确的 `superseded_by` 关系处理，不能从旧计划恢复“先等容量再判断”“排队就静默结单”等已撤回行为。

## 2. 每轮的行为义务

1. **认人和场景。** 可信入站消息决定说话人、对象和来源；评论的 Multica 作者只是执行账号。缺失的 uid 不补造，显示名不当稳定身份。数字员工接收名只来自绑定账号；缺失时标name_unavailable，配置标签只留在运维日志，不投影为会话身份或模型账号别名。按当前群参与规则逐句判断资格；主动群中的明确求助不只看@，不接管指向他人的工作，员工自己的出站不重新触发自身。
2. **听完整当前窗口。** 各句保持原文、作者、引用和时间；补充与纠正合并到对应交付物；尾部谢谢不取消工作。一句话也可能有多份请求。
3. **能恢复就不再问。** 用已提供的对象、引用或真实上一问理解“这个/好/行”。关键缺口确实未解决才问一个短问题。“给某人发消息”没有正文时不创建空工作，补齐后继续。
4. **区分沟通和执行。** 能力介绍用 describe_capabilities；要实际使用能力、回答产品机制或进行专业分析就进入执行。事项召回只能证明工作与进度，不能替代业务证据。问候、状态询问、催促或重复已接受请求都不授权第二次执行。明确的实质补充、变更或重试按原范围推进。
5. **同工作且有推进才续接。** 当前明确请求及原始引用定义工作对象，旧purpose只是匹配线索。同一交付物指用户或历史锚定的同一个产物实例，主题、人或任务标题相似不够。answer仅指用户回答真实待答问题；明确重发已完成原产物用retry/redelivery，保持原内容，不能变成重新研究。一张卡、同一个人、rank-1、忙、旧承诺都不充分。跨天的明确答复仍可续接，时间不是否决票；跨天问候不自动恢复旧任务。
6. **按事实说话。** 任务状态/数量问题回答当前明确目标与真实覆盖范围，不用无关清单代答或把部分列表当总量。准备、排队、提交、送达、对方回复和完成各有证据。未加载、失败、过期或截断不是空；“我去做”不是已做。记忆盘点、任务清单和聊天记录不能互相代答。
7. **整窗有去向。** 先形成完整计划，后受校验提交。已提交、待提交、失败和剩余输入保留，终结第一件不能漏后面的事；重试不能重放已成功效果。
8. **像同事一样表达。** 有用才说，接单简短、清单完整、失败缺口具体。不逐句“收到”，不汇报内部路由，不靠短语黑名单吞掉有效答案。人格/语气管表达，其中可见的显式限制可进一步收窄；配置不能扩大Host权限或覆盖岗位/用户当前授权。

## 3. 规则装配

正文只存于 `server/internal/service/inboundcoord/policy/*.md`；元数据在 `registry.json`，不另抄一份完整 prompt。`policy.go` 通过 `go:embed` 装配，`prompt.go` 负责事实投影。

| 模块 | 加载条件 | 作用 |
| --- | --- | --- |
| `core` | 所有轮次 | 可信身份、事实来源、约束、真实效果 |
| `voice` | 入站与 `task_finished` | 员工表达 |
| `inbound` | 入站 | 当前窗口、沟通/执行、澄清、工作规划 |
| `completion` | `task_finished` | 当前结果与目标会话的必要回报 |
| `finish_check` | 候选含非工作动作及 task_finished 的审查；群上下文同时披露channel/group | 核验有限协调动作或当前结果能否终结 |
| `finish_check_work` | 候选含 start_work/continue_work 的计划审查；群上下文同时披露channel/group | 核验未来工作能否启动 |
| `channel` / `web` | 实际入站 source | 身份缺失、场景边界、响应规则 |
| `group` | 实际群聊 | 响应资格与不打扰 |
| `window` | 当前有多条原文 | 合并补充、逐句来源、整窗覆盖 |
| `memory` | 已有本场景记忆快照 | 稳定知识、当前纠正、撤回与盘点 |
| `skills` | 已有技能快照 | 名称/能力介绍与实际执行区别 |
| `dialogue` | 已载入对话证据 | 上一问、短答、引用与原窗口水位 |
| `recall_match` | 本轮成功召回后（含首次模型调用前的Host预取） | 候选比较、工作进度、旧工作与续接 |

模块只按 Host 已知条件选择，不用词表裁决业务意图。主循环可一轮形成完整 actions；入站非工作动作和 task_finished 的结果动作提交前均进入独立终结审查。2026-09-10 曾评估让短问候的 `acknowledge(greeting|thanks)` 免审以省一次模型调用，因 Host 无法证明短消息里没有夹带请求（「你好，帮我查一下昨天的日志」）而撤回；审查成本应通过更小的审查模型或更短的审查输入降低，不通过跳过覆盖检查。带岗位约束的工作计划使用未来计划审查，不能要求尚未执行的任务先交付答案。内部归一化仍可使用 `ActionIssue` 选择工作审查，它不是对模型开放的旧 `finish(action=issue)`。

纯工作、纯非工作候选分别只载对应审查模块；混合候选在同一次审核装配core及两类审查模块，岗位约束仍只提供一次，不增模型调用或轮数。合法工作不能替同行非工作回复通过真实性与范围审查。不混载voice/inbound；群上下文同时披露channel/group，与主判断共享逐句参与资格及绑定接收身份。完成结果仍按完成合同审查，不开放新工作路由。审查不替模型执行或选目标。有效短合同替代协调层的完整SOP；缺失、过期或不可读短合同时，Host保留原完整岗位审查。旧Agent因此仍有全文审查成本，本轮不通过截断或硬失败门槛删除约束，也不宣称所有Agent整轮token已有固定上限。成功读取后重建system和manifest，模块与旧工具正文不重复累积；实际效果与当前限制不能因省token丢失。

接口：`buildSystemPrompt(turn)` / `policyManifest(turn)` 提供未披露召回的视图；`buildSystemPromptForStage(turn, recalled)` / `policyManifestForStage(turn, recalled)` 提供实际披露阶段。Host预取成功时，首次模型请求已经采用已召回阶段；预取失败仍保持未召回阶段。成功召回前不得开放依赖匹配规则的工作目标工具。

## 4. 事实投影与读取

用户输入只展示一次有序 `current_message`，每条有本窗口 `source_ref=u1...`。真实 `evidence_id`、作者 ID、timestamp、reply-to 单独保留；本地序号不能冒充平台消息 ID。`HistoryBefore` 是原窗口水位，重试不能使用 now 把后来问题倒灌成旧授权。

| 数据 | 可证明什么 | 不能证明什么 |
| --- | --- | --- |
| 可信入站及原文引用 | 谁说了什么、当前限制 | 评论账号就是委托人 |
| `agent_skills` | 已提供的安装能力目录 | 未展示即未安装、已有所有权限 |
| `scene_memory` 与 revision | 本场景已提交的稳定知识 | 合法 issue_id、当前未完成工作、待写已生效 |
| 有水位的历史 | 上一问、对象、短答的上下文 | 新消息替旧消息授权、外群事实属于本群 |
| 召回与任务证据 | 有限范围内的工作及状态 | 空 48h 结果等于全部历史无事、外部查询结果 |
| 当前运行送达上下文 | 哪个结果已覆盖哪个接收场域 | 同任务任意旧出站已送达新答案 |

读取区分 `loaded / empty / not_loaded / unavailable / stale`，裁剪另标 `truncated`。只有成功且覆盖声明范围的读取可称空。当前窗口原文只投影一次，保留 `source_ref=uN`、真实证据ID、作者、引用与原窗口水位；不能静默裁掉用户当前授权限制。

`agent.coordinator_contract` 是显式、可版本化的 JSONB：`version=1`、`scope`、`must_delegate[]`、`constraints[]`、`clarify_when[]`、`source_instructions_sha256`。包含字段名与hash的完整canonical JSON最多1600 Unicode code points；未知字段/版本、无效hash、超限对象拒绝，不截断或摘要。合同只收紧平台动作，不能新增工具或赋予业务抢答权限。

Host按当前instructions精确hash区分 `loaded / not_configured / stale / unavailable`。写入未带source hash的显式合同，由Host绑定当前原文；带hash的复制保留原版本关联，不把stale自动认证成loaded。只更新instructions会保留旧合同并变stale。更新省略合同字段表示保留，JSON null原子清空。API、CLI、复制、Builder、Git/DTA源和模板都保持字段；Git管理的合同与instructions一起禁止API热改。不自动给线上Agent编造短合同。

主循环只看有效合同或未迁移状态/hash/全文长度元数据，不能通过 `context_read(kind=job_policy)` 打开长SOP。`context_read(kind=history)`按需恢复本场景原窗口前的对话对象、延续关系、指代或上一问，最多3000字符并声明截断；它不是业务查询。所有托管读上下文总预算8000字符，只保留当前有效快照；最新修复反馈800字符。另独立保留最近一次被拒的精确提案JSON，最多6000字符，供下一轮按诊断修复具体字段；超限或非法JSON时明确标omitted，不静默截成残缺提案，也不累积历次候选。完整执行SOP仍由执行器持有；旧Agent的全文审查属于上段明确保留的迁移成本。

审查Reason始终保留具体缺陷诊断，引文不能替换Reason。constraint_quote字段必填：allow或无规则依据填空；规则驱动revise须提供最多200字符的逐字指令/所需固定话术，避免要求主模型猜不可见SOP。对非空 `constraint_quote` 原文，Host逐个来源验证它是当前限制、已加载岗位约束或实际可见persona/reply_tone的逐字子串，不跨字段拼接，再供主循环decline引用。真实引文只供修复，不构成额外授权。revise的非空摘录若伪改写、去Markdown或不匹配来源，复用现有一次、共用12秒截止的审核协议修正；仍失败则停止提交，不静默清空并缓存无依据revise。allow夹带无效附加引文仍丢弃并记录 `finish_check_boundary_quote_discarded=true`，不阻塞合法裁决；必填quote ref及verdict仍严格校验。该短摘录只证明这条限制，不是运行时生成的新合同或长SOP摘要。读取失败/缺失不得描述为无约束。

数字员工Tab的人格和语气通过 `GetAgentVoice` 读取；主prompt、Host引用校验与侧审核共用persona400/reply_tone200字符的同一投影，审核同时注明各字段是否截断。逐字出处不等于限制适用：配置仅可收窄，不覆盖岗位或当前授权，decline仍需独立审核；记忆、旧报告、旧工具结果及不可见尾部不新增边界来源。已启用技能通过 `ListEnabledAgentSkillCardMetadata` 提供名称与简介。网页、机器人及数字员工 Dispatch 都由 `FillVoice` 调用 `FillSkills`，延续预发已有的技能快照链路。快循环仍最多展示24条、1200字符，单条描述80字符并注明覆盖范围。元描述为空或仅Managed by标记时，只从最多4096字符前缀内完整frontmatter的已声明description补充能力简介；普通已有描述保持不变，前缀不完整不猜测。模型不加载正文/SOP，简介不等于权限或执行结果；明确请求技能对应工作时，仍按新建/续接计划进入沙箱，不能仅复述能力。

记忆读取保留预发的员工自述清洗：`prefetchSceneMemory` 同时使用智能体名称与绑定钉钉身份的 `AccountDisplayName`，避免数字员工自己的发言被当作人的稳定记忆。`scene_memory_status` 根据清洗后的实际快照区分 `loaded/empty`，不会把被清除的自述当作有效知识。

## 5. 工具与提交边界

有可信当前CID的正常入站，除主动会话中未@本员工的群消息外，Host在首次模型调用前执行一次无q、48h/3项 `assoc_recall`，读取timeout为2秒。复用现有归一化、8000字符内读快照及合法事项记录，不额外引入业务读取或权限。成功结果可直接满足本场景召回前置，模型无需重复同一机械读取；失败保留unavailable，不解锁工作前置，模型仍可按需重试。明确其他CID、更早范围、关键词、工作状态或历史缺口仍需对应读取，不能由当前预取代替。

主动会话中未@本员工的群消息先在同一循环判断相关性，必要时按需召回；历史事项不能先入上下文诱导接管。DM与明确@的预取保持既有行为。首次模型请求若已有成功预取，按已召回阶段开放合法目标的 `work_state`；没有成功召回时仅开放 `assoc_recall`、`context_read(history|coordination_state)` 和 `finish`。task_finished、无CID请求及进入主循环前的既有Host短路/持久化计划恢复均保持原路径。不再向模型提供 `issue_get`、`issue_comment_list`、`assoc_bind` 或 `issue_comment_add`。内部既有函数不代表对模型开放。

同一批预取还读取本场景的有界钉钉历史（`shouldPrefetchHistory` / `history_prefetch.go`）：数字员工与机器人入站、可信 CID 与 DWS 身份齐全、history 尚未加载、且不是主动会话中未@本员工的群消息时，Host 与 assoc 预取并行调用 `DWSHistory.Load`，上限 2.5 秒。成功或空结果按原 `context_read(kind=history)` 快照登记（r2），首个模型请求同时装配 `dialogue` 模块，因此「对方回答了员工代问的问题」在首轮就有证据和规则。超时保持 `not_loaded`，模型仍可按需读一次；其它失败标 `unavailable`，不当作空会话。同一轮再次 `context_read(kind=history)` 时，若快照已在且状态不是 not_loaded，Host 返回复用提示而不重读，避免预发 trace `cb7dacb19ec246c584971e148ea8c11f` 那样连读 6 次的空转；basis=answer 的历史前置反馈仍按原路径解决。Langfuse 根 metadata 记 `history_prefetch_status / history_prefetch_elapsed_ms`，SLS 事件为 `inbound_coordinator_history_prefetch`。触发证据：正式 trace `bf0bcb544bed486ab4fbb16454dabe67`（须莫答「6 点」时 history not_loaded，被判 clarify）与 `39427c330a2b4182a5b10fe44503355f`（答复从未转告委托人，回复泄漏「任务状态为 completed」）。

这次预取针对事项关联，不是Scene Memory刷新或提交。问候/能力介绍等非工作请求也可能增加一次有界关联读取，内部可包含多条数据库查询，不能宣称所有请求提速。Langfuse根metadata记录 `scene_prefetch_status / scene_prefetch_elapsed_ms`，对应Tool observation标 `origin=host_prefetch`；SLS事件为 `inbound_coordinator_scene_prefetch`、字段 `status / elapsed_ms`。其工具步骤不算LLM发起的工具调用；模型轮数、Host读取耗时与额外读次数分别报告。

`assoc_recall`先使用可信当前CID；用户明确给出其他合法openConversationId时按原ID读取。日志链接 `cid=数字` 不是会话ID。q只过滤明确范围，person_id只辅助排序。默认3项、最多5项，仅返回协调视图：精简原目标、意图、真实状态、等待对象、更新时间与可用状态引用；不传事件全文、原始评论、业务报告和执行结论。图关联/等待快照不冒充最新执行状态。读取保留scope、status_source、complete/truncated及unknown，默认48h范围不冒充全部历史；按明确旧请求可扩7d/30d。

work_state仍只读本Agent工作区的合法目标，总预算2000字符。顶层status/status_source是Issue流程状态；latest_execution只在显式work_state读取时查询该当前归属Agent/Issue最近创建的一次执行，含read_status、task_id/status、创建/开始/完成时间、status_source=agent_task_database及限定scope。read_status区分loaded/not_found/not_loaded/unavailable；不返回result/error/context。delivery_status保持not_loaded，completed只证明该次执行结束，不能推断事项已关闭、业务全部完成或消息已送达。assoc_recall/Host预取不额外批量查询执行记录。

同一次Coordinator运行中的work_state重复读取只复用本次运行新增且仍保留的成功快照：先通过当前scope/合法召回Issue检查，再核参数仅含同一issue_id（空白等价，额外字段不命中）及快照issue_id/read_ref/scope和issue_database来源，latest_execution不可为unavailable。命中时跳过下游读取和remember，返回原result/read_ref，不分配新rN；partial、unknown、not_loaded和complete/truncated原样，不把重复调用当刷新或展开摘要。失败/unavailable、跨运行/参数变化或预算淘汰后可按原权限重读；不共享到其他Turn/身份，不批量预取候选。

成功状态可用时以现有latestFeedback位提示snapshot_available及重复无法扩展摘要；已有review、history原问题或其他错误反馈优先保留。私有原参数仅用于同参判断，不进入模型。读取8000/反馈800预算和最大轮数不变，不新增LLM。减少下游读取与模型是否少走轮次是两项指标，实际延迟收益需独立回放/预发证据。 SLS/步骤及Langfuse以reason=work_state_snapshot_reused标记命中；它仍是一次模型工具请求，但不是新增后端读取或更新的证据。

用户问“刚才拆了几项/受理几项”时，按需context_read(kind=coordination_state)。以Host当前job为锚，限制同workspace/Agent/endpoint_namespace/source.platform/source.type/非空CID，严格只读created_at早于锚的最近3个窗口；不读当前及后来窗口，也不接受任意目标覆盖。返回scope=previous_3_jobs_same_host_endpoint_and_scene、status、records、complete=false/truncated及2000字符预算；每条仅job_id、首条问句<=120字符摘要/截断标记、时间/job_status、plan_present、nullable planned_work_count/confirmed_work_count及confirmation_source。计划计数仅来自合法window-plan-v1 Items；确认仅来自真实持久化计划回执、IssueResults或匹配Items.action_key的CompletedActionKeys，去重且不代表执行完成/外部送达。只计工作项，不是所有动作、澄清或消息数；不能拿旧关联事项数量代答本轮拆分。

无合法锚/reader为not_loaded，DB失败或锚不可见为unavailable，锚存在但没有前序记录为empty；未知计数保持null，不能写0。该读取归一成kind=coordination_state的rN快照并纳入8000读取总预算，可为状态回报取证；它不增加原问题history证据，不能满足basis=answer门槛。普通history读取仍独立。

report_status.state_refs仍只能引用Host本轮实际提供的rN状态证据；目标ID或关联卡片不是完成证明，不从任务评论重建业务结果。


工具契约先于失败提示：Host 每轮把本轮可校验的值写进 schema（`tool_contract.go`），而不是等模型调错再用 hint 纠正。`source_refs` 枚举当前窗口 `u1..uN`；`state_refs` 枚举本轮已有的 `rN`；`issue_id`（finish continue_work 与 work_state）枚举本轮实际召回的 Issue id；`memory_revision` 固定为当前 revision；`decline.constraint_quote` 枚举可见来源（persona、reply_tone、当前原文、已加载短合同）逐句拆出的候选，不含 Host 持有的完整岗位说明，且每个选项都能通过 `suppliedConstraintQuote`。本轮没有对应引用的动作不出现在 `kind` 里：没有可引用限制句就没有 decline，没有快照就没有 report_status，没有召回 id 就没有 continue_work。触发证据：正式 trace `342b8b1cfe8040a29f79e4a613a59ecf` 中 decline 因 constraint_quote 出处校验失败 35 次、`40526d2be3604629b705cf73d2a12a85` 中 work_state 用陈旧 id 连续失败。

重试预算（`retry_budget.go`）：同名同参数的读取失败 2 次后从工具列表撤回，第 3 次相同调用直接拒绝并提示改用已有证据或 finish；finish 提案因同一 Host 缺陷被拒 3 次、或审核连续 3 次返回同一 reason（相同提案命中审核缓存也计数），以 deferred 提前结束，Langfuse 根 metadata 记 `loop_stop_reason=repeated_invalid_plan|review_deadlock`、`loop_stop_round`、`withdrawn_tools`，SLS 事件 `inbound_coordinator_tool_withdrawn`。不同缺陷/不同 reason 的修复不受预算影响。触发证据 `5a21b47f8f774f9e914734277b7d6818`（同一 reason 30 次、152s）。

审核请求的分段（`finish_check.go`）：finish_check 请求固定为 system（按提案形态选出的策略模块）、Agent 配置段（岗位说明或短合同、技能目录、persona、reply_tone、配置范围说明）、本轮段（收信身份与账号、合同读取元数据、会话、@、历史状态与水位、场域记忆、read_evidence、交付保证、task_finished 字段）、提案段（窗口原文、candidate.actions、quote_options）四条消息。配置段只取决于 Agent，同一 Agent 连续两轮字节相同，模型侧前缀缓存可以命中 system 加配置段；之前所有字段放在一个 JSON 里按键名排序，`conversation_id`、`history_before` 排在 `job_policy` 之前，每轮前缀在几百 token 处就断掉（正式 trace `39427c330a2b4182a5b10fe44503355f` 的审核只命中共享的 core 模块 1152 token）。内容和字段一个不少，只是分段；审核缓存 key 仍取四段整体哈希。

确定性停止的兜底（`loop_stop_fallback.go`）：`rounds_exhausted`、`repeated_invalid_plan`、`review_deadlock` 三种停止只取决于窗口和规则，job worker 重投 6 次只会原样重演，所以 Host 不再返回 deferred：被 @ 或单聊的入站轮以固定文案回复「这条我没接住，麻烦再说一遍或者换个说法，我再看。」，未被 @ 的轮 silence；两者都不带任何工作项、不经 finish_check（不是模型提案），按 `window-plan-v1` 存 checkpoint（重投的 job 直接恢复该裁决，不再推理；checkpoint 存不下则仍 deferred），没有回复通道的入口对兜底返回 503 而不落沙箱，`Decision.Reason` 保留停止原因，Langfuse 根 metadata 记 `loop_stop_fallback=reply|silence`，SLS `inbound_coordinator_decided` 带 `loop_stop_reason`、`loop_stop_fallback`。模型/审核/存储错误（`coordinator_undecided`）和 `task_finished` 循环仍 deferred，由 worker 重试。触发证据 `342b8b1cfe8040a29f79e4a613a59ecf`：真人在群里 @ 后 6 次 deferred、48 次 generation，当天没有任何出站。

模型只能调用 `finish({actions:[...]})`。旧顶层 `action=reply|issue|silence`、`text`、`issue_id`和`items`不接受。入站动作如下；所有动作以 `source_refs`关联当前 `uN` 原文，回复内聚到动作，不存在通用回复动作。

| kind | 用途与必要输入 |
| --- | --- |
| `start_work` | 新工作：purpose、可选intent（缺省other）/context及短接单reply |
| `continue_work` | 同交付物实质推进：本轮召回issue_id、basis=answer/change/retry、purpose、可选intent（缺省other）/context及reply |
| `clarify` | 真正必要缺口：missing_fields从intent/recipient/message_body/scope/timing/authorization/work_target/source_material选择，reply只问具体缺口 |
| `report_status` | 已读工作进度：state_refs与忠实的reply，不重做结果 |
| `acknowledge` | ack_kind=greeting/thanks/correction/receipt；reply仅完成对应协调表达 |
| `describe_capabilities` | 根据已加载目录说明能力，不实际做业务分析 |
| `report_memory` | 引用当前memory_revision盘点已提交稳定记忆，不假称待写已成功 |
| `decline` | reason_code=scope/authorization/privacy；constraint_quote逐字引用适用当前限制、有效合同、可见persona/reply_tone限制或Host验证的边界摘录，reply仅解释该边界 |
| `ignore` | reason说明为何没有待答复或待执行请求；直接web请求不能用其结束 |

`task_finished`只允许 `report_result(result_ref,reply)` 或 `ignore(reason)`；当前result_ref来自Host，不可另造、拿旧结果替代或重新计算业务结论。结果回报同样接受独立审查，忠实回报执行器当前结果不属于入站抢答。

工作`intent`描述操作类型：ask/confirm/notify/lookup/wait/other，可省略或留空，Host缺省other；`basis`解释为何续接，是独立字段。仅answer/change/retry这三个basis词误放intent时可窄规范化为other，其他未知值仍拒绝；不会自动改变basis或授权范围。basis=answer必须对应用户正在回答的真实待答问题，并保留原问题证据门槛；用户询问状态不是answer，重发已完成原报告是retry/redelivery；新的未锚定样本/最新业务查询是新工作，不能凭同主题旧标题接成原产物的answer。工作项item.Content保留所选uN的原始引用JSON（来源及被引用作者、证据ID、正文/读取状态），避免旧事项purpose覆盖用户指定的日志/报告对象；引用仅是材料，不构成新授权，也不引入无关history/memory。

continue_work(answer/change/retry)还可在Item.Content追加本轮已经向路由/审核披露的最近普通history快照：须HistoryStatus=loaded、同一非空CID、同一原始HistoryBefore、水位/作用域匹配且含实际消息。仅复用已有有界投影，不重新查询或展开raw Turn.History；coordination_state不能充当这份history。最新普通history读取失败、不可用或范围不符时不复活更早快照。

交接说明与快照合计沿用3000字符预算，保留作者、证据ID、原时间、引用及truncated。若说明占用预算，按已有优先级裁去较旧消息并标截断；不能把缺失当完整。说明明确历史只帮助理解本次续接对象/限制，不新增授权、不恢复其他工作，助手自述不证明完成或送达。当前请求仍定义允许动作；new start_work不加该历史。此补丁不增加主prompt、工具schema、模型请求或业务读取，装配版本仍8。

history_handoff_test.go覆盖各续接basis、错误/过期scope与3000预算；主线程已报告本地受影响整包验证通过，运行日志由主线程归档。本次93次冻结回放先于此补丁且O组没有history调用，不能据此宣称模型/真实Executor交接已验证。


正确模型调用是`finish({actions:[...]})`，kind值是动作类型，source_refs是数组（单条也为["u1"]）。Host仅把已注册有限动作名误作tool的情况归一到同一finish候选，并在该恢复路径对合法JSON数组字符串的source_refs解码一层；之后仍按当前循环允许动作、完整结构、来源、目标、授权及审查校验。未注册业务工具、任意alias、再次编码或非数组文本不能靠这条恢复路径获得执行权限。

工作purpose最多240字符、可选context最多500；普通动作reply最多600，记忆/结果回报最多1800，总reply最多2400。字数预算不允许省略用户目标、边界或尚未处理的请求；超限必须在有限动作与真实覆盖范围内重新组织。

Host逐项校验kind专属字段、引用、目标、作者及整窗覆盖。一句话可含多份请求，多个动作可引用其同一source_ref；不能用尾部谢谢或某一项完成吞掉另一项。混合窗口可澄清一份请求并提交另一份明确工作，但同一缺口未解决的请求不能又澄清又提交。只有同一交付物的实质answer/change/retry才续接；问候、进度、催促及重复已接受请求都不授权重新执行。

执行器收到的IssueDescription按Host保留的Decision.Source生成交付要求：Web通过当前任务结果或已有会话回传，不额外寻找钉钉发信人/收件人；数字员工/机器人按可信事件和已有钉钉交付上下文回复，身份缺失不补造；未知来源只使用已有上下文，不推定渠道。原文明确授权的外发、代问或转达仍保留指定对象/渠道/范围，需发送时核验结果；Issue创建人/评论人不是默认委托人，接待文案不是已完成或送达证据。

执行交接的角色追溯按需进行。CoordinatorIssueFollowUp已有可信当前sender UID/openID、当前CID及与当前消息一致的origin定位，并实际提供ready reply hint时，按原回复策略直接使用该目标；Issue-comment触发本身不要求默认assoc找人。仍需读取当前Issue及相关最新comments确认授权。真实第三方代问/转达、角色冲突或目标缺失时继续追溯原始委托与必要assoc；已知目标不证明送达，也不授予无关外联/跨会话权限。详见[Dispatch执行合同](agent-dispatch-v2-execution-contract.md#coordinator-issue-follow-up-reply-targets)。Coordinator建出的独立Issue任务是纯Issue工作：claim时不再注入Diamond派发策略（common+surface或Agent的policy覆盖）与Router contextPrompt（两者excluded_reason=coordinator_issue），只保留钉钉会话事实、scene graph、回复格式与企业身份段；短循环已消费的派发不重复投喂给执行器。

单项执行描述优先保留purpose，并仅消除平台生成的默认context重复；独立上下文、scene_cid、原始发言/引用和有界history handoff原样保留。遇依赖故障记录已完成步骤、原始错误和阻塞，不把明确工作擅自扩展为凭证寻找/修改、登录或环境维修；实际操作始终以本次最新原始授权为准。

所有副作用沿正常域服务路径提交。start_work/continue_work的reply在对应工作真实入库/排队后才回传。受理文案简短自然地说明用户目标，默认不提沙箱等内部实现；必要命令名称和用户明确要求的技术细节照常保留，不改变action/basis/target_match。工作台出现文案或回调被接受不等于钉钉已送达，须由渠道回读/发送凭证确认。审查输入声明该Host保证，因此允许合法受理/排队回执，不要求候选生成时任务已执行；该保证不等于实际开始执行、完成或外部送达。容量按最多两项的批次处理，保留完整计划与每项outcome；部分成功恢复不得从头重放，未处理请求不能静默丢弃。已持久化的旧checkpoint保持既有幂等效果，不因升级强制失效。

数字员工的托管回复路由在入站事务中冻结本次触发消息的 `openMsgId`。普通协调回复、工作接单、结果回报和自然错误兜底都沿该路由调用 DWS 引用回复，不能退化成同会话里的普通新消息；群聊仍保留原有 @ 发信人。未启用托管response policy、由Router callback返回 `dwsDelivery` 的兼容路径，同样把可信 `sourceOpenMessageId` 用作 `ReplyToOpenMsgID`；它不能只用该ID解析收件人后再普通发送。合窗工作明确选择某个 `source_ref` 时，任务执行上下文继续以该动作的 `WindowEvidenceID` 为准。缺失消息ID不凭正文、昵称或历史猜测，旧版本已经冻结的不完整路由保持原效果。是否实际引用及送达须从钉钉回读的 `quotedMessage.messageId` 和发送回执确认。

终结审查只呈现唯一candidate.actions视图；Host为每项分配action_ref=aN，工作动作使用实际提交时的规范化purpose/context，不同时展示旧action/items/non_work_refs投影。review返回 `verdict=allow|revise`、从Host本轮 `quote_options.requests[{ref:qN,text}] / candidates[{ref:cN,text}]`选择的必填 `request_quote_ref / candidate_quote_ref`、最多160字符reason、未处理 `missing_source_refs[]`及必填 `work_checks[]`。每个start_work/continue_work恰好对应一个 `{action_ref, deliverables: single|multiple|none}`：single为一个独立交付物（可含相关步骤、同类批量变更，如同一类配置改多个对象），multiple为本动作purpose混入了不同种类产出，none为没有实际工作。产品查证与另起通知草稿是两个产出；同一通知内整理议程/校对、同一类负责人批量改配是一个产物的步骤。审查只按该动作自身purpose分类，不得因整窗或兄弟动作还有其他请求把已拆开的动作标成multiple；未覆盖请求用 missing_source_refs。非工作动作不填检查项，纯非工作必须为空数组。Host校验引用与恰好覆盖，allow携带multiple/none不放行；交付物语义仍由LLM判断，不能据此声称Host已确定理解用户意图。规则驱动revise须提供上述200字符constraint_quote，其余填空。Host只接受本轮选项中的引用ID，绑定其原始内容并继续记录 `RequestQuote / CandidateQuote`，不再让模型自由转录引文。完整window仍是语义全集，选中的短证据不能缩小请求范围。Host严格验证引用、verdict/missing_source_refs，只有满足上述work_checks一致性的allow才可提交；非空constraint_quote无效时按上述revise修正/allow丢弃规则处理，decline动作本身仍需真实适用边界。空原窗/无文字ignore的哨兵由Host选项提供，模型仍选择对应qN/cN；既不重新开放旧模型动作，也不因换行/转义重抄错误而丢失有效裁决。

岗位业务对象/产物与Coordinator自己的issue/task记录分开解释。对有资格响应的请求，用户已给岗位业务对象类别并让员工挑任意样本时，实例、人选和常规时间范围属于委派给Agent的选择，不是missing_fields。可查询事实、可见范围及身份/权限核验交Executor；只在确实必须用户决定的安全/授权/目标类别缺口时clarify，不让用户补齐可查询资料。取样仍是岗位数据范围内的有界检索，不能变成从关联事项名称中选一张卡；关联中没有该名字不能据此否认能力或制造澄清。仅当用户明确询问所做工作的执行/进度时才解释为任务元数据，并且必须回答被问的工作。技能目录已列出的岗位业务对象即使用「齐了么」「情况怎样」这类状态句式提问也仍是工作；标题里带同样词的关联事项不是该对象的数据，审查对这类 report_status 返回 revise。该原则依据现有岗位、能力及当前引用，不做平台固定词义映射，不要求先写短合同，也不新增数据访问或外发授权；执行器继续检查身份/权限。

必要澄清不能来自无关旧事项的干扰。岗位或能力证据已能解释“日志”等对象时，非工作审核应revise错误澄清，并在reason指出有证据支持的具体对象及能力路由，让主模型据此纠正；不需新增scope LLM或把完整SOP灌回主循环。该纠正只解释岗位语义，不新增权限，执行器仍核实际调用者身份/访问权。真正缺少对象依据时仍可澄清。

工作审查核计划能否授权启动，不要求未来检索已有答案；只起草不能改成发送。非工作审查核每个reply是否属于其kind：不能把未经查证的产品结论、专业分析或空接单承诺塞到acknowledge/能力说明/状态中。decline须有真实适用限制，不能编造缺口或拒绝正常工作。task_finished核当前result_ref及目标场景送达事实，不能把忠实结果回报当成需要新研究的业务问题。一般格式、口吻、状态灯及完整报告建议不用于拒绝合法短协调动作；岗位明确规定的固定拒绝话术须遵守，不算润色建议。

审查传输失败/超时不放行，也不自动派发猜测的任务。非法审核协议可在同一12秒截止内独立修复最多一次，例如work_checks误含clarify或revise引文不匹配；这不是业务动作重试，不扩大授权，修复后仍须完整校验，再次非法不能作为allow。work_checks只列start_work/continue_work；必要clarify已处理当前轮缺口，既不要求用户先补齐，也不进入工作检查数组。修复上下文保留未执行的最近提案与具体Reason，不能只剩一段限制原文让模型猜测哪个action出错；提案omitted时明确要求从完整当前窗口重新组织。缓存绑定实际上下文；新证据后不能复用旧裁决。固定词黑名单及一次hint后放行继续保持撤回状态。

## 6. 窗口、回执与任务完成

自然语言意图由LLM判断并审查。Host不再用ACK、停止回复、工具名称或诊断编号词表决定静默、拆窗或工作关联；自发事件、监听范围、去重与持久化状态继续按协议事实检查。

collect 只按入站来源和生命周期区分，普通提问与礼貌收尾可在同一窗口。collect 只合并正在输入的消息：4 秒静默，创建起最多 12 秒。封窗、已 claim、重试或挂起的窗口不再吸收新消息。同 scene 同时一个 Coordinator 窗口，沙箱执行仍受容量保护；容量不能阻止新窗口判断聊天。collect/park 不提前 sync-silence 完成，回执随真实处理关闭。

执行容量按「场景 × 委托人」计（`SceneDelegatorMaxInFlightMatters`，当前 2），归属取 assoc 的 `task_person` 边，人按 `assoc_person_alias` 双向归一（输入 → canonical person_key → 该人其它别名），所以同一人的 uid/staffId/openDingTalkId 算同一份预算，合窗内分组也用同一份闭包。续办他人事项成功后按当前委托人补建归属，否则准入算在当前发言人头上、执行却仍记在原委托人名下。一个人把自己的名额用满时只有他自己等待，同群其他人照常受理；合窗里每位发言人各自结算自己的新增事项。在飞只含 `queued/dispatched/running/waiting_local_directory`：`deferred` 与 `fire_at` 在未来的事项是排期或等外部输入，不占名额；非 running 的行超过在飞判定（2 小时）也不再占名额，running 由 daemon 心跳自证存活、长跑合法占用（真正卡死由 `cmd/server/runtime_sweeper.go` 负责失败）。没有任何 `task_person` 归属的在飞事项计入每个委托人，缺失身份不凭措辞或显示名归属；委托人身份不可信时退回按场景计数。单窗口一次最多起两项（`SceneWindowMaxItems`）不变。

一个窗口等待容量有上限（10 分钟，从 job 创建起算）。到期后该轮通过 worker context 的容量豁免直接执行并记 `inbound_coordinator_scene_capacity_waived`，不再重复 5 秒 park；等待说明仍只发一次，不追加第二条通知。重试前置检查按持久化计划里未完成项各自的委托人判断，只要还有人有名额就进入逐项准入，不让合窗首位发言人的满额挡住其他人。豁免只针对场景容量：同 Issue 已有未结束任务的 park 是重复执行保护，不因等待时长放行，事务内的 active-task 检查仍是最终保护。新 park 原因保留旧前缀、新二进制同时识别两种写法，滚动发布期间旧副本按旧的场景总量语义继续工作。

已判断并保存的工作仍有未提交项，因容量或同Issue busy停放时，Host可提供一次真实等待说明：计划已保存、相关新执行尚未开始；部分成功只描述剩余项，不冒充任务已入队/运行。说明用委托人能理解的话讲清已记下、尚未开始以及在等什么（容量说「你前面交代的事还在处理」，同事项busy说「同一件事上一轮还没结束」），不出现名额、槽位、队列等内部说法。真正开始执行时由该工作原本的接单回复回传，不另发一条「开始处理」。仅处理持有当前lease的job；主动会话及task_finished不新增该notice。每job的_coordinator_wait标记、本地message和具备冻结等待资格的响应outbox同事务、稳定键去重；不修改原计划/CompletedActionKeys，不消费原completion callback，也不使用终结coordinator消息类型。新job以Host字段_coordinator_wait_delivery v1冻结enabled、revision与发送input：要求response_enabled/inbound_coordinator开启、revision>=1，普通digital_employee/channel/message.created且DWS出站、非cancel/proactive/task_finished，可信DWS UID/org/CID及Host callback target齐全；单聊还需明确sender openID。入站查询在事务外最多2秒，失败冻结disabled。等待资格与legacy/managed最终结果归属独立，不能被公开wire提供。

停放发送仅用此快照，复核workspace/Agent/CID、当前Host target及完整发送身份；disabled/未知版本不回退。旧无快照managed job仍可用原冻结route，旧legacy无快照只本地说明，不因后续开关开启追补IM。collect比较等待资格enabled/revision/身份/目标，变化或旧无快照则拆窗；同群不同发言人可合并但保留原冻结recipient。普通发送入口拒绝调用者自带等待标记；缺资格仅本地反馈，不绕过发送范围。

response worker在发送前复核该Coordinator job仍pending/running、关联action一致且有未提交Items；job已终态或工作已全部提交则取消尚未发送的notice。该检查不撤回已送达消息，也不泛指任意Executor task终态。notice写入/路由失败整笔回滚；反馈最多使用park前2秒，不阻止现有park与后续恢复。等待反馈是Host状态效果，不新增LLM调用或动作，不放宽并发及完成判定。

`task_finished` 使用独立系统规则与当前结果事实，不带技能目录、长期记忆、群历史或新建路由；结果动作也先审查再交付。只读该工作与当前结果的送达证据。已在当前会话覆盖的同一结果无需二刷；仅问过联系人、另一任务出过消息或旧阶段已回报，不能压掉新的相关答案/失败。未知送达状态不能声称已告知，也不能仅因“等他回”或“已确认”而吞掉应说的事实。

故障与预算不足不扩大执行权限。不把错误当成功静默，也不在失败后自动派出含糊任务。已确认效果、未决请求和真实错误状态分别保留。

## 7. 每次修改的守则

1. 写明真实触发上下文、错误效果和期望行为，关联 `COORD.*` 义务和历史证据；区分口吻、语义、取数、工具与状态机变化。
2. 为规则选唯一主要归属：核心、条件模块、工具合同、Host、上下文投影或案例。不得默认在 prompt 末尾追加禁令；语义门槛也不能只藏进离线案例。
3. 合并、删除、迁移都记录源映射；替换旧手段写 `superseded_by` 和原因。无法解释的历史先登记未决，不为预算擅删。
4. 更新模块 `version`、注册预算、依赖、规则所有权、适用条件和副作用前置。检查每个披露路径、读取失败、重试水位及多义输入。
5. 同一变更同步修工具 schema、hint、返回提示、Host 守卫与本文，消除相反规则；禁止只改 system 就声称已修完整行为。
6. 提供同一句话只改一个上下文条件的最小正反例；实际副作用、应读取资料和一轮直达路径分别验收。自然语言样本去掉 `token=W2` 等探针仍须有效。
7. 检查分层报告：结构检查只证元数据闭合；预置模型返回的单测只证 Host 协议；真实模型回放证判断；任务记录和同 CID 回读证实际执行与投递。`BLOCKED/INCOMPLETE` 不能写成通过。
8. 临时事故保护注明误伤对照、范围、替代方式与移除条件。它不能默默固化成员工永久人格。
9. 每轮记录 `policy_version / assembly_version / prompt_hash / modules / active_rule_ids`，以及实际上下文状态、可用工具和效果。读取快照记录read_snapshot_count/runes/truncated/budget，被拒提案另记录repair_proposal_runes/repair_proposal_budget；6000提案、8000读取与800反馈是不同预算，不能相互冒充整轮上下文总量。回滚不得恢复已撤回的漏响应手段。

运行 `python3 scripts/check-coordinator-policy.py` 检查来源哈希、103 条映射、规则/案例/实现引用、模块依赖、预算和版本。它不锁提示词原句，也不认证模型行为。策略模块测试在 `inboundcoord/policy_test.go`；语义与真实链路验收按风险使用相应案例，不把全仓测试数量当成对话正确性的证明。

Scene Memory 的全文上限 1600 Unicode code points 包含标题和引用，合并目标为1200；超限返回实际长度及压缩提示。修复轮有独立50秒请求预算，仍受整个job原120秒截止约束，并为CAS提交预留5秒。相同被拒commit不重复消耗剩余轮次，保持dirty和旧水位等待既有退避。Langfuse工具accepted只证明草稿通过校验，根committed=true才证明数据库提交；cursor_at与planned_cursor_at分开，历史时间使用实际min/max，缺少claimed证据不能自动跳过。9月9日巡检修复仅完成Host协议/观测窄验证，真实模型收敛和真实投递未做E2E。

澄清与计划覆盖：必要意图、正文或目标缺失的请求使用clarify及对应missing_fields/source_refs，不得同时生成其工作动作。其他已明确请求照常提交。旧事项相同联系人不构成当前消息正文。该语义由COORD.F05/F06的inbound模块、有限finish schema及缺正文/已给正文对照共同维护。

## 8. 前次职责减重的历史证据

正式 trace `605e116e29704bf294f653bb4508a684` 的岗位块有 16,336 字符。用户要求重新回答 AI 听记机制，召回只有旧文件事项，Coordinator 却直接回答“只生成一份”且首次 finish 即通过，没有任务派发。前次修复把完整岗位从主循环初始输入移到Host-held按需读取和独立终结审查。当前有限动作方案进一步撤回主循环按需全文读取，并以显式短合同承载协调限制；该历史证据不能自动认证新协议。此 trace 与问候、能力说明、真实进度/记忆、缺正文澄清、末尾岗位限制、审查不可用构成最小对照。

相对本轮改动前 HEAD，普通钉钉主循环 system 从 7,737 降为 4,557 字符（-41.1%）；带记忆和技能为 9,441 → 5,926（-37.2%）；群聊加召回为 11,806 → 7,605（-35.6%）。完整岗位块另从主循环初始输入移出，审查仍读取全文，且带岗位的执行计划也会增加审查调用，因此不能据此宣称整次任务总 token 或延迟必然下降。

注册目录和 103 条来源映射只证明结构与继承；本次 Host 测试、冻结真实模型回放及部署证据由实施 Plan 回填，当前案例初始标记 `not_run`。用户本轮不要求 E2E，不把冻结回放或健康检查写成真实任务派发与投递已验收。

## 9. 有限动作实施的验收边界

当前变更关联[有限动作Plan](plans/2026-09-09-coordinator-closed-actions.md)。合同基础层验证覆盖未知字段/版本/超限、原文hash、过期复制、API省略/清除及字段透传；这些只证明协议与存储，不证明真实路由正确。模块、工具、Host引用和当前文档由policy结构检查关联；新动作、拒绝边界、状态/结果引用、混合窗口、原AI听记问题和有界读取需单独验证。

本轮不做业务E2E；健康检查、Host单测和冻结模型回放分别报告。未配置或过期短合同的Agent仍有完整岗位审查成本，尚不能宣称整轮上下文均已缩为1600字符；有效短合同也不免除有限动作的语义审查。

字段修复提示对准具体参数缺口。purpose允许技术名称、命令和字段作为用户要求处理的工作对象；Host只校验结构、长度和可信引用，不按词表拒绝。真实凭据泄漏与无关执行指令由语义审查结合上下文判断。

## 10. 主动处理会话

默认关闭的“主动处理会话所有新消息”位于“入站先判断”下方。开启时同事务开启入站判断；关闭入站判断同时关闭主动处理。Router 继续按原订阅范围投递，Multica 将 message.observed 转入持久 Coordinator 窗口，按消息 ID 去重，4 秒静默/12 秒封窗，最多 100 条一窗；无 Autopilot 新执行或额外 30 秒冷却。未被 @ 和纯确认词不能在主动模式下跳过语义判断。原 Autopilot 只收尾升级前的消息，消息去重覆盖两个入口；旧版本入库在切换后返回可重试错误。

自动化中的 `dingtalk_message` 触发方式是独立功能：`message.statistics` 仅收集元数据，按用户配置的固定窗口触发自动化，不进入 Coordinator，也不改变主动处理开关或窗口。账号自身发出的消息不计入统计。旧 `conversation.summary` 仅返回静默收据，不再创建任务。实现与验收契约见 [钉钉消息自动化方案](plans/2026-09-10-dingtalk-message-autopilot.md)。

同 Issue 的补充先保存评论与私有待处理上下文，再结束本轮事件消费。独立工作线程待当前任务终态后，将同身份、同会话的连续补充合并到一次执行，保留逐条来源和实际评论投递水位；独立的 DWS 身份在启动前重新解析，不沿用前一任务的临时 Token。任务完成跟进开关维持原义。完成回路只汇报结果及已接收补充的真实等待/执行状态，不新增路由。

结构测试、数据库并发测试与真实预发对话分别记录在本次实施记录中；未完成的验证不得记为通过。

### 群聊响应资格（COORD.F01）

主动订阅只保证消息进入判断，不意味着消息在问员工。主判断和入站终结审查共用 group/channel 规则：逐句按接收账号、原始 @ 对象、点名称呼、引用与对话判断是否应参与。绑定账号名称仅在当前 DWS UID/组织与持久化绑定一致时提供；不从发送人或召回任务推断员工身份。单条事件的 @ 信息在合窗前固化，空数组与未知分开保存，不把整窗 @ 合集铺到每条消息。

找别人的问候、交给别人处理的事情和无关闲聊静默，不发澄清、不代答、不建/续 Issue；找数字员工、明确邀请它协助、岗位内开放问题及已有工作真实补充仍由同一 Coordinator 决定回应或委派。窗口包含两类消息时分别处理，不能全回或全丢。问候与已接受请求的重复提醒不新增执行。此改造复用现有 finish_check，不增加分类模型请求、定时任务或业务规则开关；任务完成审查仍使用独立完成合同。

对未 @ 本员工的主动群消息，首轮先判断响应资格，历史 Issue 改为同一循环按需召回，避免同群旧事项诱导接管群友的工作。DM 与明确 @ 的预取保持原有行为；所有工作仍须先完成召回才能提交，不增加独立分类 LLM 请求。

对话对象依赖前文且已提供证据不足时，模型按需调用 `context_read(kind=history)`，由现有 DWS reader 查询当前会话原窗口前的消息，保留作者和时间；对象明确或已有充分证据时不重复读取。结合当前消息与历史回复的时间间隔、引用对象、话题连续性及中间其他人的交流判断是否在对员工说话。时间近仅支持延续，时间久削弱无锚定指代，但不否决明确引用或续问；不设置固定超时阈值。未读取、读取失败或空事项召回不等于没有对话，时间戳缺失保持未知。主判断和现有群聊终结审查共享该指引，Host 的读取权限、水位、预算和动作校验保持现行合同。

策略 `.5` 合入候选 `.3` 的指引，针对 2026-09-10 15:55 无 @ 追问、15:56 引用员工回复仍被忽略的现场证据补充以上工具与提示词指引。五组对照登记于 cases.json；模型回放与真实投递尚未验证，本地结构及单测结果不能作为线上修复验收。

群内名字优先从当前有效的 message 路由绑定获取，与执行身份授权独立。新绑定的 account/tenant 必须与可信入站身份一致；存量无 account-key 的绑定使用同一认证工作区/Agent 下的有效路由显示名，不据此新增身份权限。只有不存在消息绑定时才尝试精确匹配执行身份。

## 11. 回复恢复的当前证据边界

本次恢复登记针对正式`.4`的日志目标/进度被旧skills事项带偏，以及后续answer历史门槛、重复读取、有限动作误作工具和source_refs编码造成的协议循环。另有new-eb0e/d8b2真实trace简称对应的对象误澄清：岗位已指明成长日志，却被旧skill事项带偏；完整证据与fixture待主线程回填。最新真实回放还暴露将领域材料改问成工程任务记录，以及N_progress列旧skills；新增对象层次对照，五条真实消息候选仍待复跑。另有78项回归报告的intent/basis混淆、原报告重发误当answer、clarify被写入审核work_checks。精确日志/trace和测试输出尚待关联，新增病例统一为`not_run`；本文与结构检查不能代替运行证据。

Hi/你好等普通会话已有正常回复，是本次必须保留的对照，不能误诊为所有轻量沟通都需派发业务任务。策略`.6`保留回复恢复，新增事实读取使用装配8，区别于预发`.5`；合并后的代码、Host恢复与模型回放由主线程记录，未核实的修复不写成已发布或已送达。


当前执行授权以指定对象回归Plan的2026-09-10范围更新为准：部署前先fetch并核对最新远端/发布基线，复用现有预发run推进，随后继续指定预发对象E2E。前文“不做E2E”是对应历史阶段的边界，不覆盖这次明确的后续要求。

工作审查的每项work_checks另须返回target_match。start_work使用new_work；continue_work对照Host按真实读取投影的existing_work.original_goal，判断same_deliverable/different_deliverable/no_advancement/unknown。只有same_deliverable允许续接。Host校验明确结论与动作的一致性，不从关键词或reason推断语义；独立输出应改为start_work，目标证据缺失不能允许续接。投影不增加读取或额外模型调用。

审查将可信接收身份与当前窗口置于同一个最新输入中，避免长岗位背景隔开身份与原文；不新增身份别名。相同目标但无新增执行输入的提醒或「抽完了吗」用no_advancement拒绝重复执行；Host须把修复写成改用report_status，不能让模型只改continue_work的reply。引用本员工更早回复作背景不是新指令。仍需模型按当前原文判断。

主动群的现有审查还返回participation_checks：共享依据的source_refs可分组，但每条来源须恰好覆盖一次；模型独立判断对象依据、原文称呼或已读对话引用和ignore/coordinate/work去向。Host只验证出处及判定与动作的一致性，不用人名或意图词表分类。审查输出预算随窗口条数从768有界增加到3072，仍在原12秒截止内，不增加独立分类调用。

DWS 历史读取通过 `MULTICA_DWS_HISTORY_MCP_URL` 显式选择 MCP 环境，并在每次隔离配置目录中写入 `mcp_url`；预发配置使用预发地址。`im.message-list.v1` 的无时区显示时间按 DWS 约定的上海时区解析，未知格式保留原文。群聊对象未明且历史未加载时，忽略提案须先读取一次历史；明确引用当前文本中的其他收件人可直接判断。读取失败保持未知，不循环读取，也不凭空建立对话。CLI 标准错误仅记录稳定诊断字段。

### 跨组织历史读取续授

`MULTICA_DWS_HISTORY_CROSS_ORG_RENEW_AGENT_IDS` 显式列出已获账号所有者同意续授的 Agent UUID（逗号分隔，默认空）。仅当这些 Agent 的历史读取返回 `CrossOrgPermissionDenied` 时，Host 使用本次隔离身份执行 `dws chat data-auth cross-org --all --grant-type timed --ttl 7d --yes`，确认限时读取授权成功后重试原查询一次。授权失败、其他错误或再次拒绝均保留失败；不改变绑定身份、会话范围和历史截止时间。

因未读取历史而阻止静默的 Host 提示，在读取返回 loaded、empty 或 unavailable 后解除；empty/unavailable 仍是证据缺失，不证明没有对话，也不授权继续工作。语义审查提出的其他限制继续保留。

## 拒绝裁决的协议恢复

审核明确返回 `revise` 时不产生任何执行或发送效果；Host 丢弃其中的工作检查项，保留已经校验来源的拒绝原因，交回主循环修正候选。审核要求候选改成工作动作时，不能把它描述的未来动作当成本次已有动作引用，并因此让整窗提前退出。`allow` 仍逐项验证实际工作引用、覆盖、交付物和目标，错误引用不得放行。工具 schema 的 `work_checks.maxItems` 等于当前实际工作动作数，非工作候选为 0。该恢复不改岗位提示、Agent 定义或执行指令。

### 2026-09-15 接收身份与失败兜底修复

当前修复见 [Plan](plans/2026-09-15-coordinator-receiving-identity.md)。逐条可信 mention_relation 优先于可能过期的绑定显示名及岗位人设；显示名差异不能否定稳定ID匹配。Host不再按“帮我/查一下”等词覆盖独立审核通过的动作；先判断参与资格，再判断是否需要执行。来源、目标匹配及权限协议校验继续保留。新要求：发给员工的消息出现失败或无回复时须通过持久回执链路兜底，不能只在Multica界面记录；明确发给别人的群消息不借此获得执行授权。兜底使用固定自然短句，不暴露内部reason，不声称执行成功。运行验证与送达证据由Plan回填。


### 2026-09-15 连续对话与UID硬约束

[实施与验收](plans/2026-09-15-coordinator-contextual-replies.md)：逐条response_required来自可信单聊或接收UID匹配，主模型、schema与审核共用；缺失/冲突ID不从昵称补猜，人设/改名不能把已匹配的收件人解释为另一个人。合窗不把整窗@铺给每条，未被寻址的群消息仍可ignore。正常直接消息不得用ignore后静态收据代替回答，新增受限acknowledge(conversation)承接社交追问、连续对话与沟通反馈。它不能进行业务抢答，不能编造现实活动、失败原因或工作结果。真正异常保留已有持久化自然兜底。原稿重发的工作reply只能接单，不能提前贴正文再让执行器重复投递。


社交措辞整理是独立的conversation_reply阶段：最多12秒/2048输出token，只处理已经选中的acknowledge(conversation)。输入为当前来源、逐条UID回应事实、语气与最多6条/1800字历史；不带岗位SOP、人设姓名、旧卡、场域记忆或原草稿。未知历史作者不凭昵称补认，历史自述不能证明活动/执行。整理后的完整候选仍由原finish_check审核，审核修复轮不再次覆盖它；通过后SavePlan，再走既有发送路径。该阶段失败仍走现有异常处理，不能绕审核或直接执行。独立policy/hash及generation记录其成本与实际输入。

该受限社交阶段使用qwen3.8-max；路由及审核仍为qwen3.7-plus。真实同输入对比已记录事实边界和延迟，完整预发验收另记。沟通反馈本身是完整诉求，回复须完成本轮交流，不把抱怨反问成新需求；不解释系统记录可见性。

社交整理历史只保留稳定SenderID匹配当前发言人或接收UID的行，未知作者不按昵称补认、不传给整理阶段；原路由及审核保留完整历史。身份过滤后的partial/空不表示之前没有交流。

### 2026-09-16 社交整理的延迟处理

正式trace `c8ef6a1ecbd34dce8689bbc965c882aa`（单聊「Hi」）整轮8567毫秒，其中路由2118、社交整理3247、终结审查2563，预取631；三次模型调用串行占92.5%。整理阶段输入仅1304 token却耗时3247毫秒，成本来自思考模式而非上下文。按§3既定方向处理延迟：不跳过任何覆盖检查，只改并行度与单次请求成本。

1. **整理阶段取消思考模式。** 恢复`enable_thinking=false`与`tool_choice=required`（thinking+required的上游400不再适用），模型仍为qwen3.8-max，路由与审核不变。同fixture的历史对照：无思考约1389毫秒、低思考约3709–4743毫秒。**当前的事实边界（不编造活动/执行、不按昵称认人、不以亲历包装建议）是在低思考条件下冻结回放通过的，取消思考后必须用同一组正式输入复跑才算通过，本文不把旧条件的结论记作新条件的证据。**
2. **整理与路由并行推测。** 整理请求的全部内容是(turn, 选中的conversation动作)的纯函数，不含任何路由判断。Host在两次预取完成后、首个路由请求的同时，按“单条acknowledge(conversation)覆盖全部response_required来源”的假设先发一次整理，并记录该请求的`input_hash`。路由返回后按真实提案重建请求：**逐字节哈希相同才复用推测结果，不同一律丢弃并正常重发**。推测无副作用，不改提案、不读工作状态、不接触任何效果路径；推测失败不是本轮裁决，同一问题在正常路径上重问一次，只有那次答案作数。
3. **推测的成本被报告，不被隐藏。** `ack_kind`为greeting/thanks的回合和工作回合根本不进入整理，其推测必然作废；多条动作或不同来源集合的提案同样作废，该回合出现两次整理调用。Langfuse以`speculative=true`的`coordinator.conversation_reply.speculative` generation记录每次推测的真实输入与usage，根metadata记`conversation_reply_speculation=hit/miss/error/unused`，SLS事件为`inbound_coordinator_conversation_reply_speculation`。推测的observation由主协程开启、由推测协程结束，不并发写trace自身的metadata。
4. **验收分层。** 已完成：Host协议与并发单测（含-race连跑3次，四条推测用例在关闭推测后全部失败）、policy结构检查、Codex静态审阅（3项已修）、**预发实时E2E 8/8通过**（2026-09-16 19:00–19:07，证据见[E2E报告](reports/2026-09-16-coordinator-reply-latency-e2e.md)）。实测社交整轮从基线9042/9538毫秒降到4940–7847毫秒，推测渲染1152–2633毫秒且全部短于同轮路由，正常渲染一次未发生。**仍未完成：冻结真实模型回放（本机无凭据）**，因此无思考条件下6组正例与UID负例的硬边界没有证据，只有5条活体社交对照；实时8轮出现6次hit、3次unused、**0次miss**，不命中与推测失败路径仅有单测覆盖。

回放命令（需操作者提供私有fixture与凭据，不写入仓库）：

```bash
cd server && MULTICA_RUN_CONTEXTUAL_REPLAY=1 \
  MULTICA_CONTEXTUAL_REPLAY_FIXTURE=/tmp/coord-contextual-replay-fixture.json \
  MULTICA_CONTEXTUAL_REPLAY_REPORT=/tmp/coord-contextual-replay-report-no-thinking.json \
  MULTICA_LLM_API_KEY=... MULTICA_LLM_BASE_URL=... \
  go test ./internal/service/inboundcoord -run TestCoordinatorContextualReplay -count=1 -v
```

UID负例fixture `/tmp/coord-contextual-replay-fixture-other-uid.json` 与S5 fixture 需同样复跑；正例6组与负例的对照结论按§7.7分层报告，不合并成一句“通过”。

存在正式@元数据的普通数字员工群消息也进入参与引文校验。UID均为他人时，不能以@片段或其内部同名子串认成本员工；仅位置独立的自然称呼、开放邀请或已载对话仍可评估。解析@语法边界，不比较人名词表；缺少provider span的多词裸@保持保守，不靠空格推断另一个受话人。

UID与参与引文的一致性对allow/revise同样生效。审核把其他UID的正式@认成本员工时，整份审核结论为无效协议，先走现有一次、同12秒截止内的审核修复；不能将错误revise传回主循环，也不把它直接改成allow。

社交整理的reply_author明确数字员工与账号主人生活经历的区别；没有个人习惯资料时，日常话题给用户可选建议，不以自身亲身经历包装建议。

### 工作接单文字归属（2026-09-15.8）

start_work/continue_work的接单回复由Host生成，模型仅可选择闭合receipt_language；自由reply即使含完整稿件或格式错误也不进入接单文字。新计划与恢复计划仅规范化展示字段，不改action key、工作内容或效果账本，已冻结outbox保持原样。工作全部持久提交后才沿既有outbox发送；混合窗口保留非工作回复、同类工作收据只保留一次。社交整理使用同一聚合函数，原文重发等产物仍只由执行器交付。

### purpose 只复述被请求的结果（2026-09-15.9）

触发证据：璟琦(主用钉)单聊里用户原话只是「人脸识别打卡太慢……再不解决我天天给你反馈」，Coordinator 却把 purpose 写成「整理……VOC 体验问题，作为产品优化需求提交待审批」，reply 同步承诺了这条处理路径。窗口没有请求整理动作、没有指定 VOC 通道、也没有要求走审批，这些都是协调层替执行器做的方法判断，派工描述因此把「怎么做」当成了交付物。

现行合同：`purpose` 复述用户请求的交付物与对象原话，不设计方法，也不添加窗口未请求的处理、转交、评审或审批步骤；任何动作的 reply 同样不先替员工承诺某条处理路径。用户自己说明的方法照常保留，完整岗位工作流仍归执行器。工作审查（`finish_check_work`）对越界 purpose 返回 revise，在 reason 点名要去掉的步骤并要求回到被请求的结果；它只判断范围，不改写计划、不新增权限，也不因此放宽 single/target_match 校验。规则归属 COORD.F04，正反例见 `f04_requested_outcome_vs_invented_method`。

本轮只做提示词与审查层收窄：模块 `inbound` 升 29、`finish_check_work` 升 22，预算相应上调，无工具 schema、Host 守卫或状态机变化。结构检查通过。

预发同轮观察（2026-09-16 13:08/13:13，群 `cidVaO557dsSgYcgnvRNbwY4g==`）：「你去查一下到底什么原因」派出 WS-271 `冬翔委托：排查预发工作台登录转圈超时问题，定位具体原因`；与璟琦同形的纯抱怨「切工作区每次都要整个重新加载，慢…再不解决我天天跟你念叨」派出 WS-272 `冬翔委托：排查工作台切换工作区时全量重新加载导致的性能问题，定位具体原因`，都没有出现被发明的整理/提交/审批步骤。这是正样本观察，未对同一窗口跑修复前提示词做 A/B，模型回放仍缺，案例保持 `not_run`。证据见 `docs/reports/2026-09-16-dingtalk-trigger-quoted-reply-e2e.md`。

### 岗位业务对象的状态句式仍是工作（2026-09-15.10）

触发证据：正式群「客户交付-数字员工小群」（`cidG5GNL/T3DWwkDf9LxiEJ5w==`，2026-09-17）。云欢问「梳理一下昨天的日报搜集情况」（trace `2aab414d5323409db82de83d83a57a9a`）与「昨天所有人的日报都搜集齐了么？」（`df6a74eb963a4b2da87c956134f9201b`），两轮都用 `report_status` 引用 `r1`（Host 预取的 3 条关联事项）和 `r3`（`work_state`）代答，`finish_check` 均 allow，reason 为 `request is a status inquiry, not a new work execution; report_status with loaded state is appropriate.`。同一请求改写成「按照日报提交的技能要求检查……而不是看目前正在处理的问题」后，`943f462ff6f54dfd98d7b369269b31f1` 立即 `start_work`。三轮的证据、工具、权限与技能目录完全相同，只有措辞变化，属纯语义路由失败。

放大因素有两条，都不是缺数据：预取返回的三条事项标题分别为「将李分分、刘雪、赵璐璐加入日报名单，收录其日报并向三人回复收录完成」「……加入日报名单」「向孙潇页(笑曳)回报近期日报中质量较好的案例清单」，与被问对象字面高度重合；该群的 `dws_chat_history` 恒为 `CrossOrgPermissionDenied`，`history_status=unavailable`，关联事项因此是唯一场景信号。技能目录本身是完整的（`catalog_complete=true`，13 条），`work-report-operator` 的简介已含「查询日报情况」。

现行合同：技能目录已列出的岗位业务对象，即使用「齐了么」「情况怎样」这类状态句式提问，仍按工作路由；标题里带同样词的关联事项不是该对象的数据。`report_status` 只适用于对象是本员工自己已受理事项的提问，原「Asking whether existing work is done」因此收窄为「an accepted matter」，它继续管它本来要管的 `continue_work` 滥用。两个集合相交时（既是目录对象、又确是本员工已受理的那件事，例如「刚才交给你的日报整理完了吗」）以已受理为准，仍按 `report_status` 引用该事项已加载的状态；`finish_check` 只对主语是本员工从未受理过的目录对象的 `report_status` 返回 revise，不因目录命中否决正确的进度回答。规则归属 COORD.F04，正反例见 `f04_role_material_vs_coordination_record`（已回填上述三条 trace 作为证据，模型回放仍缺，保持 `not_run`）。

本轮只做提示词与审查层收窄：模块 `inbound` 升 30、`finish_check` 升 23，预算相应上调，无工具 schema、Host 守卫或状态机变化。主循环 system prompt 9494 字符，仍在 9500 门槛内；判别句只在 `inbound` 留一句，完整规则放在不计入该门槛的 `finish_check`。结构检查与 inboundcoord 全包用例通过。

## Coordinator model configuration

Diamond `dt-fde-multica-runtime.json` / `DEFAULT_GROUP` exposes `runtime.llm.coordinator_model`. Each decision snapshots the model once for the main loop, finish checks, logs and Langfuse; updates apply to the next decision. Missing/blank values retain `qwen3.7-plus` for existing documents during rollout. The configured target is `qwen3.8-max`. Requests keep `enable_thinking=false` and `reasoning_effort=none`; this setting does not change executor models or the global default. Local protocol tests do not certify real model behavior or deployment.


### 发起人选择处理方式（2026-09-21，已接线，预发验收中）

`inbound_coordinator_user_decision` 默认关闭；关闭 Coordinator 同时清除此设置。候选提案与执行 checkpoint 分开：`UserDecisionSnapshot` 保存冻结上下文、模型实际输入、召回任务、提案及 policy 版本，`proposeUserDecision` 在 `SavePlan` 之前返回等待。每次入站仅一次提问；续接最多三个真实任务，加新建与具体直接回复；卡片不暴露内部计划或默认勾选。

回调身份只读取可信 `operatorDTO.openDingTalkId`，新协议 `a2uiEvent.action.context` 与旧 `actionData.context` 在边界兼容。数据库事务锁住决策、去重事件并接收首次有效提交；拒绝事件不消耗机会。未确认发送成功不启动 24 小时计时；过期不自动选择。发送链路标识不构成 DWS 消息幂等保证，结果不明禁止盲目重发。

选择锁定动作方向与目标；只补充文字可解释为计划，歧义或矛盾不执行、不二次询问。用户选择、模型推荐与人工金标分别记录。结构检查与单元测试不表示内部群产品链路通过；服务端消费、派发恢复、OSS/导出与真实模型/群验收仍须完成。

候选发卡前的结构校验最多允许三次内部模型生成，校验失败反馈与原始候选全部留在快照中；耗尽后不发卡、不执行。该修正不增加用户询问次数，也不能绕过岗位或事实约束。

候选完成结构校验后，由独立 `review_choices` 模型调用仅核对直接回复的事实与标签语义；即使动作被标为 conversation，也不得承诺选回复后执行工作。审查不代选，不审新建／续接路线优劣，不把技能目录缺失当作执行器能力缺失；审查原始输出与修正次数保存在冻结快照，仍在最多三次发卡前生成预算内。

消费者重连后通过持久化待办刷新现存 waiting 卡片的组件，复用原卡与冻结候选，保留客户端已输入值；等待态不得显示已接收。终态卡片保留已接受选项与补充说明，处理成功与接收确认分开。回复计划的 JSON null IssueResults 视为空数组，不能阻断其它决策的终态汇总。

补充说明解释的结构／引用校验失败允许最多三次内部修正，保留全部原始解释输出并提供冻结召回 ID 校验反馈；不增加用户询问，不修复或掩盖用户意图矛盾，不允许切换明确选择的目标。耗尽后未执行。

用户提交后的 finish_check 仍校验结构、参与边界、目标与限制，但不执行旧自动路由中按审查理由关键词改判能力／开工方向的修正。显式选择由语义审查判断合法性，Host 不因 reason 中出现 tool、forbids 或 start_work 等词替换 verdict。R9 实测暴露误拒绝，修复验收单独记录。

卡片问题与接收／执行状态使用公开 Catalog 的 Markdown 组件，并在确认创建回执后通过持久化待办补充对应 surface/component 的 artifact 注解；此后每次更新保留注解；不能让 DWS 更新默认的空注解抹掉摘要。等待表单恢复仅更新问题绑定及组件，不重置 answers。数据集导出保留非凭证的 card_biz_id 供卡片关联，凭据、消费者身份和租约字段仍排除。等待态使用 CONFIRMING，避免 INPUTTING 覆盖会话列表摘要为“正在回复中”。R12 实卡更新后显示问题正文且原生提交成功；R13/R14 自动接线的原生卡片、回调和结果均通过。关闭设置仅影响新请求，已发出的卡片继续按冻结规则处理。

发卡回执重试只清理 sending/send_unknown 的发送租约；不得清理 resuming 的解析租约。显式预发启动验证通过隔离环境记录及回滚事务检查数据库过期、取消和卡片更新重试；MULTICA_USER_DECISION_VERIFY_ON_BOOT 默认关闭，正式环境拒绝开启。测试不新增 HTTP 调试入口，不冒充真实客户端失败注入。

大快照上传失败不得产生已冻结引用；读取失败、超限或摘要不一致不得返回部分上下文。`snapshot_test.go` 覆盖这些失败边界与原引用重试；R14 实测约80KB快照的原生提交和导出通过，不能替代真实OSS故障注入。

预发run3109340164（release dad5c06c）在两个实例上实际通过PostgreSQL过期／取消／重复回执租约、卡片更新持久重试与首次有效并发测试，发布allEnd=true；R14大快照与提交发布后回读未变化。测试夹具42P08失败及修正保留在验收记录中，未声明真实手机双端或OSS服务故障已验收。

用户决策监控每分钟读取 PostgreSQL 权威状态，服务启动留两分钟订阅恢复时间。等待超过五分钟且订阅当前未就绪、发送未知超过五分钟、已接受未恢复超过五分钟、卡片更新积压超过五分钟，分别生成工作台 attention 提醒。收件人为仍在工作区内的 Agent 所有者，不新增决策人配置、不转移卡片作答权；没有有效所有者时不猜测收件人。通知按环境／决策／问题／所有者确定唯一 ID，由 inbox_item 主键去重，多实例或重启不重复投递同一提醒；WebSocket 只是唤醒，权威通知已持久化。健康接口补充权威状态数量、最久等待、24 小时接受等待均值／回调原因分布及累计重复投递数，保持工作区、Agent、环境隔离。正常等待且订阅健康不报警。

数据集导出对嵌套 JSON 字符串执行同样的凭证与无关联系方式字段清理，仍以字符串返回该层，不修改权威输入；卡片 ID、作答者关联和选项 ID 保留。

全局模型配置以数据库版本为权威，Diamond 保留只读默认 Provider。每次 Decide 冻结主模型与跨 Provider 降级链；所有模型调用（含回复整理和 finish 审查）共用该快照。上游限流、网络、超时、5xx、认证/模型不可用及 provider_error 可尝试下一候选；通用请求校验错误、业务审查拒绝、取消和总预算耗尽不降级。每个候选最多一次，共享原总预算；成功切换后本轮后续调用沿用备用模型。失败重试不重新执行工具或提交计划。原 required→auto 参数重写已移除，原工具和 Host 审查保持。每次供应商尝试独立记录 provider、model、配置版本与结果。当前结构/单元测试及预发验收状态见 registry 与交付记录。

最终审查必须透传 UserDecisionSubmission 到 reviewTurn，实际装配 user_decision 模块。发起人的后续提交定义当前选择和补充要求；仅说明时可修改或取消原请求，不强制已被修改的原回复措辞。岗位／平台／身份边界仍生效，明确选项的方向与目标仍锁定，矛盾不执行。R18错误审查保留为反例。

Candidate semantic review receives the full frozen proposal: continuation labels must identify their actual recalled task, and cannot rename it from an unrelated nearby message. This check does not choose the handling direction for the initiator.

Submission interpretation receives the trusted latest submission after the frozen snapshot. Both protocol and semantic review repairs are bounded to three attempts with audits, locked direction and target, and no second human question. Verbatim review constraints may quote trusted supplementary text. Reply truthfulness and continuation-label grounding use separate model checks, so task identity checks cannot change the existing reply semantics.

Dataset provenance includes exact proposal and candidate-review system prompt hashes, the actual interpretation policy manifest, and the Host-stamped policy manifest for each final review attempt. This preserves the distinction when a waiting request resumes after a deployment. Explicit non-executable interpretation reasons are shown to the initiator with a bounded message; infrastructure and review-protocol failures retain the generic public message.

Submission interpretation uses only the registered user-decision policy, frozen evidence and typed plan schema. Automatic routing instructions are not assembled again at this stage; the full final-review and Host constraints still validate the resulting plan. Explicit cancellation is non-executable even when a cancellation acknowledgment could be written.

### 百炼 DeepSeek 工具 schema（2026-09-22）

百炼 `deepseek-v4.1-flash` 的 `required` 工具请求会拒绝 JSON Schema 的 `uniqueItems` 关键字，包括值为 false 的情形。`oneOf` 子分支只声明父级属性差量时还会漏掉分支必填字段。已以同一请求的最小对照及完整上下文复现；这不是通过改为 `auto` 解决的问题。

仅该官方百炼端点与模型的 Coordinator 请求在 modelregistry 出站边界转换工具 schema：移除不支持的关键字并保留“引用不可重复”的说明，将对象 `oneOf` 的父级约束完整展开到每个互斥分支。原始 schema、参数快照、required 工具选择和其它模型保持不变。Host 的 `parseValidatedWindowPlan` 仍拒绝重复 source_refs/state_refs，`validateFinishParticipationChecks` 仍拒绝重复参与判断；不会重放工具或绕过语义审核。原有 policy 与历史义务不变，仅协议表示调整。

验证：`TestDeepSeekSchemaPreservesRequiredChoiceAndBranchConstraints`、`TestReferenceUniquenessRemainsHostEnforced` 与现有参与判断重复引用对照。预发完整调用与结果验证单独记录，HTTP200本身不证明计划可提交。
