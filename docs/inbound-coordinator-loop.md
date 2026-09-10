# Coordinator 现行行为合同

policy_version: `2026-09-10.4`。装配版本：`15`。本文件描述此分支的实现合同；发布和行为验收状态以对应 Plan 与运行证据为准。

Coordinator 的交付物是每条请求的去向与有证据的协调状态。它识别人和请求、恢复指代、必要澄清、选择新建或续接，并通过有限动作承接问候、能力、记忆、进度与结果回报。产品机制、专业分析、检索查证、文件及发送等工作交执行器；任何动作的 reply 字段都不能用来抢答业务结论。快循环和执行器属于同一个员工，分别承担协调与执行。

优先级是不误执行、不漏请求、不串身份和场域、不虚报状态，然后才是自然表达、响应速度和上下文成本。历史翻车经验保存为行为义务与案例；旧补丁可以被明确替换，不能把相反的规则同时留成现行合同。

## 1. 维护入口与历史账本

修改 Coordinator 的提示词、工具、上下文、handler、assoc、scenememory、窗口、回执或 trace 前，先读本文件和 [规则目录](../server/internal/service/inboundcoord/policy/registry.json)。目录登记 `COORD.F01`–`COORD.F19` 的行为义务、模块、实现引用、对照案例和已撤回手段。

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

模块只按 Host 已知条件选择，不用词表裁决业务意图。主循环可一轮形成完整 actions；入站非工作动作和 task_finished 的结果动作提交前均进入独立终结审查。带岗位约束的工作计划使用未来计划审查，不能要求尚未执行的任务先交付答案。内部归一化仍可使用 `ActionIssue` 选择工作审查，它不是对模型开放的旧 `finish(action=issue)`。

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

主循环只看有效合同或未迁移状态/hash/全文长度元数据，不能通过 `context_read(kind=job_policy)` 打开长SOP。`context_read(kind=history)`仅恢复本场景原窗口前的指代、对象或上一问，最多3000字符并声明截断；它不是业务查询。所有托管读上下文总预算8000字符，只保留当前有效快照；最新修复反馈800字符。另独立保留最近一次被拒的精确提案JSON，最多6000字符，供下一轮按诊断修复具体字段；超限或非法JSON时明确标omitted，不静默截成残缺提案，也不累积历次候选。完整执行SOP仍由执行器持有；旧Agent的全文审查属于上段明确保留的迁移成本。

审查Reason始终保留具体缺陷诊断，引文不能替换Reason。constraint_quote字段必填：allow或无规则依据填空；规则驱动revise须提供最多200字符的逐字指令/所需固定话术，避免要求主模型猜不可见SOP。对非空 `constraint_quote` 原文，Host逐个来源验证它是当前限制、已加载岗位约束或实际可见persona/reply_tone的逐字子串，不跨字段拼接，再供主循环decline引用。真实引文只供修复，不构成额外授权。revise的非空摘录若伪改写、去Markdown或不匹配来源，复用现有一次、共用12秒截止的审核协议修正；仍失败则停止提交，不静默清空并缓存无依据revise。allow夹带无效附加引文仍丢弃并记录 `finish_check_boundary_quote_discarded=true`，不阻塞合法裁决；必填quote ref及verdict仍严格校验。该短摘录只证明这条限制，不是运行时生成的新合同或长SOP摘要。读取失败/缺失不得描述为无约束。

数字员工Tab的人格和语气通过 `GetAgentVoice` 读取；主prompt、Host引用校验与侧审核共用persona400/reply_tone200字符的同一投影，审核同时注明各字段是否截断。逐字出处不等于限制适用：配置仅可收窄，不覆盖岗位或当前授权，decline仍需独立审核；记忆、旧报告、旧工具结果及不可见尾部不新增边界来源。已启用技能通过 `ListEnabledAgentSkillCardMetadata` 提供名称与简介。网页、机器人及数字员工 Dispatch 都由 `FillVoice` 调用 `FillSkills`，延续预发已有的技能快照链路。快循环仍最多展示24条、1200字符，单条描述80字符并注明覆盖范围。元描述为空或仅Managed by标记时，只从最多4096字符前缀内完整frontmatter的已声明description补充能力简介；普通已有描述保持不变，前缀不完整不猜测。模型不加载正文/SOP，简介不等于权限或执行结果；明确请求技能对应工作时，仍按新建/续接计划进入沙箱，不能仅复述能力。

记忆读取保留预发的员工自述清洗：`prefetchSceneMemory` 同时使用智能体名称与绑定钉钉身份的 `AccountDisplayName`，避免数字员工自己的发言被当作人的稳定记忆。`scene_memory_status` 根据清洗后的实际快照区分 `loaded/empty`，不会把被清除的自述当作有效知识。

## 5. 工具与提交边界

有可信当前CID的正常入站，除主动会话中未@本员工的群消息外，Host在首次模型调用前执行一次无q、48h/3项 `assoc_recall`，读取timeout为2秒。复用现有归一化、8000字符内读快照及合法事项记录，不额外引入业务读取或权限。成功结果可直接满足本场景召回前置，模型无需重复同一机械读取；失败保留unavailable，不解锁工作前置，模型仍可按需重试。明确其他CID、更早范围、关键词、工作状态或历史缺口仍需对应读取，不能由当前预取代替。

主动会话中未@本员工的群消息先在同一循环判断相关性，必要时按需召回；历史事项不能先入上下文诱导接管。DM与明确@的预取保持既有行为。首次模型请求若已有成功预取，按已召回阶段开放合法目标的 `work_state`；没有成功召回时仅开放 `assoc_recall`、`context_read(history|coordination_state)` 和 `finish`。task_finished、无CID请求及进入主循环前的既有Host短路/持久化计划恢复均保持原路径。不再向模型提供 `issue_get`、`issue_comment_list`、`assoc_bind` 或 `issue_comment_add`。内部既有函数不代表对模型开放。

这次预取针对事项关联，不是Scene Memory刷新或提交。问候/能力介绍等非工作请求也可能增加一次有界关联读取，内部可包含多条数据库查询，不能宣称所有请求提速。Langfuse根metadata记录 `scene_prefetch_status / scene_prefetch_elapsed_ms`，对应Tool observation标 `origin=host_prefetch`；SLS事件为 `inbound_coordinator_scene_prefetch`、字段 `status / elapsed_ms`。其工具步骤不算LLM发起的工具调用；模型轮数、Host读取耗时与额外读次数分别报告。

`assoc_recall`先使用可信当前CID；用户明确给出其他合法openConversationId时按原ID读取。日志链接 `cid=数字` 不是会话ID。q只过滤明确范围，person_id只辅助排序。默认3项、最多5项，仅返回协调视图：精简原目标、意图、真实状态、等待对象、更新时间与可用状态引用；不传事件全文、原始评论、业务报告和执行结论。图关联/等待快照不冒充最新执行状态。读取保留scope、status_source、complete/truncated及unknown，默认48h范围不冒充全部历史；按明确旧请求可扩7d/30d。

work_state仍只读本Agent工作区的合法目标，总预算2000字符。顶层status/status_source是Issue流程状态；latest_execution只在显式work_state读取时查询该当前归属Agent/Issue最近创建的一次执行，含read_status、task_id/status、创建/开始/完成时间、status_source=agent_task_database及限定scope。read_status区分loaded/not_found/not_loaded/unavailable；不返回result/error/context。delivery_status保持not_loaded，completed只证明该次执行结束，不能推断事项已关闭、业务全部完成或消息已送达。assoc_recall/Host预取不额外批量查询执行记录。

同一次Coordinator运行中的work_state重复读取只复用本次运行新增且仍保留的成功快照：先通过当前scope/合法召回Issue检查，再核参数仅含同一issue_id（空白等价，额外字段不命中）及快照issue_id/read_ref/scope和issue_database来源，latest_execution不可为unavailable。命中时跳过下游读取和remember，返回原result/read_ref，不分配新rN；partial、unknown、not_loaded和complete/truncated原样，不把重复调用当刷新或展开摘要。失败/unavailable、跨运行/参数变化或预算淘汰后可按原权限重读；不共享到其他Turn/身份，不批量预取候选。

成功状态可用时以现有latestFeedback位提示snapshot_available及重复无法扩展摘要；已有review、history原问题或其他错误反馈优先保留。私有原参数仅用于同参判断，不进入模型。读取8000/反馈800预算和最大轮数不变，不新增LLM。减少下游读取与模型是否少走轮次是两项指标，实际延迟收益需独立回放/预发证据。 SLS/步骤及Langfuse以reason=work_state_snapshot_reused标记命中；它仍是一次模型工具请求，但不是新增后端读取或更新的证据。

用户问“刚才拆了几项/受理几项”时，按需context_read(kind=coordination_state)。以Host当前job为锚，限制同workspace/Agent/endpoint_namespace/source.platform/source.type/非空CID，严格只读created_at早于锚的最近3个窗口；不读当前及后来窗口，也不接受任意目标覆盖。返回scope=previous_3_jobs_same_host_endpoint_and_scene、status、records、complete=false/truncated及2000字符预算；每条仅job_id、首条问句<=120字符摘要/截断标记、时间/job_status、plan_present、nullable planned_work_count/confirmed_work_count及confirmation_source。计划计数仅来自合法window-plan-v1 Items；确认仅来自真实持久化计划回执、IssueResults或匹配Items.action_key的CompletedActionKeys，去重且不代表执行完成/外部送达。只计工作项，不是所有动作、澄清或消息数；不能拿旧关联事项数量代答本轮拆分。

无合法锚/reader为not_loaded，DB失败或锚不可见为unavailable，锚存在但没有前序记录为empty；未知计数保持null，不能写0。该读取归一成kind=coordination_state的rN快照并纳入8000读取总预算，可为状态回报取证；它不增加原问题history证据，不能满足basis=answer门槛。普通history读取仍独立。

report_status.state_refs仍只能引用Host本轮实际提供的rN状态证据；目标ID或关联卡片不是完成证明，不从任务评论重建业务结果。


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

执行交接的角色追溯按需进行。CoordinatorIssueFollowUp已有可信当前sender UID/openID、当前CID及与当前消息一致的origin定位，并实际提供ready reply hint时，按原回复策略直接使用该目标；Issue-comment触发本身不要求默认assoc找人。仍需读取当前Issue及相关最新comments确认授权。真实第三方代问/转达、角色冲突或目标缺失时继续追溯原始委托与必要assoc；已知目标不证明送达，也不授予无关外联/跨会话权限。详见[Dispatch执行合同](agent-dispatch-v2-execution-contract.md#coordinator-issue-follow-up-reply-targets)。

单项执行描述优先保留purpose，并仅消除平台生成的默认context重复；独立上下文、scene_cid、原始发言/引用和有界history handoff原样保留。遇依赖故障记录已完成步骤、原始错误和阻塞，不把明确工作擅自扩展为凭证寻找/修改、登录或环境维修；实际操作始终以本次最新原始授权为准。

所有副作用沿正常域服务路径提交。start_work/continue_work的reply在对应工作真实入库/排队后才回传。受理文案简短自然地说明用户目标，默认不提沙箱等内部实现；必要命令名称和用户明确要求的技术细节照常保留，不改变action/basis/target_match。工作台出现文案或回调被接受不等于钉钉已送达，须由渠道回读/发送凭证确认。审查输入声明该Host保证，因此允许合法受理/排队回执，不要求候选生成时任务已执行；该保证不等于实际开始执行、完成或外部送达。容量按最多两项的批次处理，保留完整计划与每项outcome；部分成功恢复不得从头重放，未处理请求不能静默丢弃。已持久化的旧checkpoint保持既有幂等效果，不因升级强制失效。

终结审查只呈现唯一candidate.actions视图；Host为每项分配action_ref=aN，工作动作使用实际提交时的规范化purpose/context，不同时展示旧action/items/non_work_refs投影。review返回 `verdict=allow|revise`、从Host本轮 `quote_options.requests[{ref:qN,text}] / candidates[{ref:cN,text}]`选择的必填 `request_quote_ref / candidate_quote_ref`、最多160字符reason、未处理 `missing_source_refs[]`及必填 `work_checks[]`。每个start_work/continue_work恰好对应一个 `{action_ref, deliverables: single|multiple|none}`：single为一个独立交付物（可含相关步骤/修正），multiple为合并了无关交付物，none为没有实际工作。产品查证与另起通知草稿是两个产出；同一通知内整理议程/校对是一个产物的步骤。非工作动作不填检查项，纯非工作必须为空数组。Host校验引用与恰好覆盖，allow携带multiple/none不放行；交付物语义仍由LLM判断，不能据此声称Host已确定理解用户意图。规则驱动revise须提供上述200字符constraint_quote，其余填空。Host只接受本轮选项中的引用ID，绑定其原始内容并继续记录 `RequestQuote / CandidateQuote`，不再让模型自由转录引文。完整window仍是语义全集，选中的短证据不能缩小请求范围。Host严格验证引用、verdict/missing_source_refs，只有满足上述work_checks一致性的allow才可提交；非空constraint_quote无效时按上述revise修正/allow丢弃规则处理，decline动作本身仍需真实适用边界。空原窗/无文字ignore的哨兵由Host选项提供，模型仍选择对应qN/cN；既不重新开放旧模型动作，也不因换行/转义重抄错误而丢失有效裁决。

岗位业务对象/产物与Coordinator自己的issue/task记录分开解释。对有资格响应的请求，用户已给岗位业务对象类别并让员工挑任意样本时，实例、人选和常规时间范围属于委派给Agent的选择，不是missing_fields。可查询事实、可见范围及身份/权限核验交Executor；只在确实必须用户决定的安全/授权/目标类别缺口时clarify，不让用户补齐可查询资料。取样仍是岗位数据范围内的有界检索，不能变成从关联事项名称中选一张卡；关联中没有该名字不能据此否认能力或制造澄清。仅当用户明确询问所做工作的执行/进度时才解释为任务元数据，并且必须回答被问的工作。该原则依据现有岗位、能力及当前引用，不做平台固定词义映射，不要求先写短合同，也不新增数据访问或外发授权；执行器继续检查身份/权限。

必要澄清不能来自无关旧事项的干扰。岗位或能力证据已能解释“日志”等对象时，非工作审核应revise错误澄清，并在reason指出有证据支持的具体对象及能力路由，让主模型据此纠正；不需新增scope LLM或把完整SOP灌回主循环。该纠正只解释岗位语义，不新增权限，执行器仍核实际调用者身份/访问权。真正缺少对象依据时仍可澄清。

工作审查核计划能否授权启动，不要求未来检索已有答案；只起草不能改成发送。非工作审查核每个reply是否属于其kind：不能把未经查证的产品结论、专业分析或空接单承诺塞到acknowledge/能力说明/状态中。decline须有真实适用限制，不能编造缺口或拒绝正常工作。task_finished核当前result_ref及目标场景送达事实，不能把忠实结果回报当成需要新研究的业务问题。一般格式、口吻、状态灯及完整报告建议不用于拒绝合法短协调动作；岗位明确规定的固定拒绝话术须遵守，不算润色建议。

审查传输失败/超时不放行，也不自动派发猜测的任务。非法审核协议可在同一12秒截止内独立修复最多一次，例如work_checks误含clarify或revise引文不匹配；这不是业务动作重试，不扩大授权，修复后仍须完整校验，再次非法不能作为allow。work_checks只列start_work/continue_work；必要clarify已处理当前轮缺口，既不要求用户先补齐，也不进入工作检查数组。修复上下文保留未执行的最近提案与具体Reason，不能只剩一段限制原文让模型猜测哪个action出错；提案omitted时明确要求从完整当前窗口重新组织。缓存绑定实际上下文；新证据后不能复用旧裁决。固定词黑名单及一次hint后放行继续保持撤回状态。

## 6. 窗口、回执与任务完成

自然语言意图由LLM判断并审查。Host不再用ACK、停止回复、工具名称或诊断编号词表决定静默、拆窗或工作关联；自发事件、监听范围、去重与持久化状态继续按协议事实检查。

collect 只按入站来源和生命周期区分，普通提问与礼貌收尾可在同一窗口。collect 只合并正在输入的消息：4 秒静默，创建起最多 12 秒。封窗、已 claim、重试或挂起的窗口不再吸收新消息。同 scene 同时一个 Coordinator 窗口，沙箱执行仍受两槽保护；容量不能阻止新窗口判断聊天。collect/park 不提前 sync-silence 完成，回执随真实处理关闭。

已判断并保存的工作仍有未提交项，因容量或同Issue busy停放时，Host可提供一次真实等待说明：计划已保存、相关新执行尚未开始；部分成功只描述剩余项，不冒充任务已入队/运行。仅处理持有当前lease的job；主动会话及task_finished不新增该notice。每job的_coordinator_wait标记、本地message和具备冻结等待资格的响应outbox同事务、稳定键去重；不修改原计划/CompletedActionKeys，不消费原completion callback，也不使用终结coordinator消息类型。新job以Host字段_coordinator_wait_delivery v1冻结enabled、revision与发送input：要求response_enabled/inbound_coordinator开启、revision>=1，普通digital_employee/channel/message.created且DWS出站、非cancel/proactive/task_finished，可信DWS UID/org/CID及Host callback target齐全；单聊还需明确sender openID。入站查询在事务外最多2秒，失败冻结disabled。等待资格与legacy/managed最终结果归属独立，不能被公开wire提供。

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

同 Issue 的补充先保存评论与私有待处理上下文，再结束本轮事件消费。独立工作线程待当前任务终态后，将同身份、同会话的连续补充合并到一次执行，保留逐条来源和实际评论投递水位；独立的 DWS 身份在启动前重新解析，不沿用前一任务的临时 Token。任务完成跟进开关维持原义。完成回路只汇报结果及已接收补充的真实等待/执行状态，不新增路由。

结构测试、数据库并发测试与真实预发对话分别记录在本次实施记录中；未完成的验证不得记为通过。

### 群聊响应资格（COORD.F01）

主动订阅只保证消息进入判断，不意味着消息在问员工。主判断和入站终结审查共用 group/channel 规则：逐句按接收账号、原始 @ 对象、点名称呼、引用与对话判断是否应参与。绑定账号名称仅在当前 DWS UID/组织与持久化绑定一致时提供；不从发送人或召回任务推断员工身份。单条事件的 @ 信息在合窗前固化，空数组与未知分开保存，不把整窗 @ 合集铺到每条消息。

找别人的问候、交给别人处理的事情和无关闲聊静默，不发澄清、不代答、不建/续 Issue；找数字员工、明确邀请它协助、岗位内开放问题及已有工作真实补充仍由同一 Coordinator 决定回应或委派。窗口包含两类消息时分别处理，不能全回或全丢。问候与已接受请求的重复提醒不新增执行。此改造复用现有 finish_check，不增加分类模型请求、定时任务或业务规则开关；任务完成审查仍使用独立完成合同。

对未 @ 本员工的主动群消息，首轮先判断响应资格，历史 Issue 改为同一循环按需召回，避免同群旧事项诱导接管群友的工作。DM 与明确 @ 的预取保持原有行为；所有工作仍须先完成召回才能提交，不增加独立分类 LLM 请求。

群内名字优先从当前有效的 message 路由绑定获取，与执行身份授权独立。新绑定的 account/tenant 必须与可信入站身份一致；存量无 account-key 的绑定使用同一认证工作区/Agent 下的有效路由显示名，不据此新增身份权限。只有不存在消息绑定时才尝试精确匹配执行身份。

## 11. 回复恢复的当前证据边界

本次恢复登记针对正式`.4`的日志目标/进度被旧skills事项带偏，以及后续answer历史门槛、重复读取、有限动作误作工具和source_refs编码造成的协议循环。另有new-eb0e/d8b2真实trace简称对应的对象误澄清：岗位已指明成长日志，却被旧skill事项带偏；完整证据与fixture待主线程回填。最新真实回放还暴露将领域材料改问成工程任务记录，以及N_progress列旧skills；新增对象层次对照，五条真实消息候选仍待复跑。另有78项回归报告的intent/basis混淆、原报告重发误当answer、clarify被写入审核work_checks。精确日志/trace和测试输出尚待关联，新增病例统一为`not_run`；本文与结构检查不能代替运行证据。

Hi/你好等普通会话已有正常回复，是本次必须保留的对照，不能误诊为所有轻量沟通都需派发业务任务。策略`.6`保留回复恢复，新增事实读取使用装配8，区别于预发`.5`；合并后的代码、Host恢复与模型回放由主线程记录，未核实的修复不写成已发布或已送达。


当前执行授权以指定对象回归Plan的2026-09-10范围更新为准：部署前先fetch并核对最新远端/发布基线，复用现有预发run推进，随后继续指定预发对象E2E。前文“不做E2E”是对应历史阶段的边界，不覆盖这次明确的后续要求。

工作审查的每项work_checks另须返回target_match。start_work使用new_work；continue_work对照Host按真实读取投影的existing_work.original_goal，判断same_deliverable/different_deliverable/no_advancement/unknown。只有same_deliverable允许续接。Host校验明确结论与动作的一致性，不从关键词或reason推断语义；独立输出应改为start_work，目标证据缺失不能允许续接。投影不增加读取或额外模型调用。

审查将可信接收身份与当前窗口置于同一个最新输入中，避免长岗位背景隔开身份与原文；不新增身份别名。相同目标但无新增执行输入的提醒用no_advancement拒绝重复执行，仍需模型按当前原文判断。

主动群的现有审查还返回participation_checks：共享依据的source_refs可分组，但每条来源须恰好覆盖一次；模型独立判断对象依据、原文称呼或已读对话引用和ignore/coordinate/work去向。Host只验证出处及判定与动作的一致性，不用人名或意图词表分类。审查输出预算随窗口条数从768有界增加到3072，仍在原12秒截止内，不增加独立分类调用。
