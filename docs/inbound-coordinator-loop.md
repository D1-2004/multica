# Coordinator 现行行为合同

policy_version: `2026-09-09.4`。装配版本：`5`。本文件描述此分支的实现合同；发布和行为验收状态以对应 Plan 与运行证据为准。

Coordinator 的交付物是每条请求的去向与有证据的协调状态。它识别人和请求、恢复指代、必要澄清、选择新建或续接，并通过有限动作承接问候、能力、记忆、进度与结果回报。产品机制、专业分析、检索查证、文件及发送等工作交执行器；任何动作的 reply 字段都不能用来抢答业务结论。快循环和执行器属于同一个员工，分别承担协调与执行。

优先级是不误执行、不漏请求、不串身份和场域、不虚报状态，然后才是自然表达、响应速度和上下文成本。历史翻车经验保存为行为义务与案例；旧补丁可以被明确替换，不能把相反的规则同时留成现行合同。

## 1. 维护入口与历史账本

修改 Coordinator 的提示词、工具、上下文、handler、assoc、scenememory、窗口、回执或 trace 前，先读本文件和 [规则目录](../server/internal/service/inboundcoord/policy/registry.json)。目录登记 `COORD.F01`–`COORD.F19` 的行为义务、模块、实现引用、对照案例和已撤回手段。

- [Host预取Plan](plans/2026-09-09-coordinator-prefetch.md) 记录当前场域必需读取前置的收益假设、额外成本与实测；
- [有限动作与短合同 Plan](plans/2026-09-09-coordinator-closed-actions.md) 记录当前动作协议、迁移边界和验收；
- [上下文减重与终结审查 Plan](plans/2026-09-09-coordinator-scope-and-finish.md) 记录本次实现与验证状态；[原实施 Plan](plans/2026-09-07-coordinator-progressive-context.md) 保留此前分阶段证据。
- [103 条来源清单](plans/2026-09-07-coordinator-progressive-context-inventory.json) 保留原 trace 的 99 条 bullet、4 条开场原文及哈希；候选映射另列，不覆盖源证据。
- [最小对照合同](../server/internal/service/inboundcoord/policy/cases.json) 说明预期与禁止效果，未运行的仍为 `not_run`。
- [9 月 7 日 collect 事故](plans/2026-09-07-coordinator-collect-window.md) 保留原事故及红绿证据；其真实 IM 0/3、权限阻塞的旧结果不能因本次结构检查而变成通过。
- 历史 Plan 是当时的设计/复盘记录。遇到冲突按本现行合同和明确的 `superseded_by` 关系处理，不能从旧计划恢复“先等容量再判断”“排队就静默结单”等已撤回行为。

## 2. 每轮的行为义务

1. **认人和场景。** 可信入站消息决定说话人、对象和来源；评论的 Multica 作者只是执行账号。缺失的 uid 不补造，显示名不当稳定身份。未被叫到不接管群友工作，员工自己的出站不重新触发自身。
2. **听完整当前窗口。** 各句保持原文、作者、引用和时间；补充与纠正合并到对应交付物；尾部谢谢不取消工作。一句话也可能有多份请求。
3. **能恢复就不再问。** 用已提供的对象、引用或真实上一问理解“这个/好/行”。关键缺口确实未解决才问一个短问题。“给某人发消息”没有正文时不创建空工作，补齐后继续。
4. **区分沟通和执行。** 能力介绍用 describe_capabilities；要实际使用能力、回答产品机制或进行专业分析就进入执行。事项召回只能证明工作与进度，不能替代业务证据。问候、状态询问、催促或重复已接受请求都不授权第二次执行。明确的实质补充、变更或重试按原范围推进。
5. **同工作且有推进才续接。** 一张卡、同一个人、rank-1、忙、旧承诺都不充分。跨天的明确答复仍可续接，时间不是否决票；跨天问候不自动恢复旧任务。
6. **按事实说话。** 准备、排队、提交、送达、对方回复和完成各有证据。未加载、失败、过期或截断不是空；“我去做”不是已做。记忆盘点、任务清单和聊天记录不能互相代答。
7. **整窗有去向。** 先形成完整计划，后受校验提交。已提交、待提交、失败和剩余输入保留，终结第一件不能漏后面的事；重试不能重放已成功效果。
8. **像同事一样表达。** 有用才说，接单简短、清单完整、失败缺口具体。不逐句“收到”，不汇报内部路由，不靠短语黑名单吞掉有效答案。人格管表达；岗位规则可收紧行为，不能扩大 Host 权限或覆盖用户当前限制。

## 3. 规则装配

正文只存于 `server/internal/service/inboundcoord/policy/*.md`；元数据在 `registry.json`，不另抄一份完整 prompt。`policy.go` 通过 `go:embed` 装配，`prompt.go` 负责事实投影。

| 模块 | 加载条件 | 作用 |
| --- | --- | --- |
| `core` | 所有轮次 | 可信身份、事实来源、约束、真实效果 |
| `voice` | 入站与 `task_finished` | 员工表达 |
| `inbound` | 入站 | 当前窗口、沟通/执行、澄清、工作规划 |
| `completion` | `task_finished` | 当前结果与目标会话的必要回报 |
| `finish_check` | 非工作动作及 task_finished 的审查，仅与 `core` 装配 | 核验有限协调动作或当前结果能否终结 |
| `finish_check_work` | 含 start_work/continue_work 的计划审查，仅与 `core` 装配 | 核验未来工作及同行协调动作能否启动 |
| `channel` / `web` | 实际入站 source | 身份缺失、场景边界、响应规则 |
| `group` | 实际群聊 | 响应资格与不打扰 |
| `window` | 当前有多条原文 | 合并补充、逐句来源、整窗覆盖 |
| `memory` | 已有本场景记忆快照 | 稳定知识、当前纠正、撤回与盘点 |
| `skills` | 已有技能快照 | 名称/能力介绍与实际执行区别 |
| `dialogue` | 已载入对话证据 | 上一问、短答、引用与原窗口水位 |
| `recall_match` | 本轮成功召回后（含首次模型调用前的Host预取） | 候选比较、工作进度、旧工作与续接 |

模块只按 Host 已知条件选择，不用词表裁决业务意图。主循环可一轮形成完整 actions；入站非工作动作和 task_finished 的结果动作提交前均进入独立终结审查。带岗位约束的工作计划使用未来计划审查，不能要求尚未执行的任务先交付答案。内部归一化仍可使用 `ActionIssue` 选择工作审查，它不是对模型开放的旧 `finish(action=issue)`。

审查按候选是否含工作严格二选一，不混载 voice/inbound，不替模型执行或选目标。有效短合同替代协调层的完整SOP；缺失、过期或不可读短合同时，Host保留原完整岗位审查。旧Agent因此仍有全文审查成本，本轮不通过截断或硬失败门槛删除约束，也不宣称所有Agent整轮token已有固定上限。成功读取后重建system和manifest，模块与旧工具正文不重复累积；实际效果与当前限制不能因省token丢失。

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

审查Reason始终保留具体缺陷诊断；可选边界摘录只能补充，不能替换Reason。若一条实际边界导致revise，可返回最多200字符的 `constraint_quote` 原文，Host验证它是当前限制或已加载岗位约束的逐字子串，再供主循环decline引用。allow附带真实边界旁证也可正常通过，但不把该旁证当作修复指令。无效可选摘录被丢弃并记录 `finish_check_boundary_quote_discarded=true`，不会替代对本轮必填quote ref和verdict的严格校验。该短摘录只证明这条限制，不是运行时生成的新合同或长SOP摘要。读取失败/缺失不得描述为无约束。

数字员工 Tab 的人格和语气通过 `GetAgentVoice` 读取；已启用技能通过 `ListEnabledAgentSkillCardMetadata` 提供名称与简介。网页、机器人及数字员工 Dispatch 都由 `FillVoice` 调用 `FillSkills`，延续预发已有的技能快照链路。快循环最多展示 24 条、1200 字的快照并注明覆盖范围，不读取或执行完整 `SKILL.md`；明确请求技能对应工作时，仍按新建/续接计划进入沙箱，不能仅复述能力。

记忆读取保留预发的员工自述清洗：`prefetchSceneMemory` 同时使用智能体名称与绑定钉钉身份的 `AccountDisplayName`，避免数字员工自己的发言被当作人的稳定记忆。`scene_memory_status` 根据清洗后的实际快照区分 `loaded/empty`，不会把被清除的自述当作有效知识。

## 5. 工具与提交边界

正常入站且有可信当前CID时，Host在首次模型调用前执行一次无q、48h/3项 `assoc_recall`，读取timeout为2秒。复用现有归一化、8000字符内读快照及合法事项记录，不额外引入业务读取或权限。成功结果可直接满足本场景召回前置，模型无需重复同一机械读取；失败保留unavailable，不解锁工作前置，模型仍可按需重试。明确其他CID、更早范围、关键词、工作状态或历史缺口仍需对应读取，不能由当前预取代替。

首次模型请求若已有成功预取，按已召回阶段开放合法目标的 `work_state`；没有成功召回时仅开放 `assoc_recall`、`context_read(history)` 和 `finish`。task_finished、无CID请求及进入主循环前的既有Host短路/持久化计划恢复均保持原路径。不再向模型提供 `issue_get`、`issue_comment_list`、`assoc_bind` 或 `issue_comment_add`。内部既有函数不代表对模型开放。

这次预取针对事项关联，不是Scene Memory刷新或提交。问候/能力介绍等非工作请求也可能增加一次有界关联读取，内部可包含多条数据库查询，不能宣称所有请求提速。Langfuse根metadata记录 `scene_prefetch_status / scene_prefetch_elapsed_ms`，对应Tool observation标 `origin=host_prefetch`；SLS事件为 `inbound_coordinator_scene_prefetch`、字段 `status / elapsed_ms`。其工具步骤不算LLM发起的工具调用；模型轮数、Host读取耗时与额外读次数分别报告。

`assoc_recall`先使用可信当前CID；用户明确给出其他合法openConversationId时按原ID读取。日志链接 `cid=数字` 不是会话ID。q只过滤明确范围，person_id只辅助排序。默认3项、最多5项，仅返回协调视图：精简原目标、意图、真实状态、等待对象、更新时间与可用状态引用；不传事件全文、原始评论、业务报告和执行结论。图关联/等待快照不冒充最新执行状态。读取保留scope、status_source、complete/truncated及unknown，默认48h范围不冒充全部历史；按明确旧请求可扩7d/30d。

`work_state`只返回本Agent工作区内本轮召回目标的有界状态，最多2000字符。`report_status.state_refs`必须引用Host本轮实际提供的状态证据；目标ID或图关联本身不能证明完成。主循环不从任务评论重建名单、分数或产品答案。

模型只能调用 `finish({actions:[...]})`。旧顶层 `action=reply|issue|silence`、`text`、`issue_id`和`items`不接受。入站动作如下；所有动作以 `source_refs`关联当前 `uN` 原文，回复内聚到动作，不存在通用回复动作。

| kind | 用途与必要输入 |
| --- | --- |
| `start_work` | 新工作：purpose、intent、可选context及短接单reply |
| `continue_work` | 同交付物实质推进：本轮召回issue_id、basis=answer/change/retry、purpose、intent、可选context及reply |
| `clarify` | 真正必要缺口：missing_fields从intent/recipient/message_body/scope/timing/authorization/work_target/source_material选择，reply只问具体缺口 |
| `report_status` | 已读工作进度：state_refs与忠实的reply，不重做结果 |
| `acknowledge` | ack_kind=greeting/thanks/correction/receipt；reply仅完成对应协调表达 |
| `describe_capabilities` | 根据已加载目录说明能力，不实际做业务分析 |
| `report_memory` | 引用当前memory_revision盘点已提交稳定记忆，不假称待写已成功 |
| `decline` | reason_code=scope/authorization/privacy；constraint_quote逐字引用适用当前限制、有效合同或Host验证的边界摘录，reply仅解释该边界 |
| `ignore` | reason说明为何没有待答复或待执行请求；直接web请求不能用其结束 |

`task_finished`只允许 `report_result(result_ref,reply)` 或 `ignore(reason)`；当前result_ref来自Host，不可另造、拿旧结果替代或重新计算业务结论。结果回报同样接受独立审查，忠实回报执行器当前结果不属于入站抢答。

工作purpose最多240字符、可选context最多500；普通动作reply最多600，记忆/结果回报最多1800，总reply最多2400。字数预算不允许省略用户目标、边界或尚未处理的请求；超限必须在有限动作与真实覆盖范围内重新组织。

Host逐项校验kind专属字段、引用、目标、作者及整窗覆盖。一句话可含多份请求，多个动作可引用其同一source_ref；不能用尾部谢谢或某一项完成吞掉另一项。混合窗口可澄清一份请求并提交另一份明确工作，但同一缺口未解决的请求不能又澄清又提交。只有同一交付物的实质answer/change/retry才续接；问候、进度、催促及重复已接受请求都不授权重新执行。

所有副作用沿正常域服务路径提交。start_work/continue_work的reply在对应工作真实入库/排队后才回传。审查输入声明该Host保证，因此允许合法受理/排队回执，不要求候选生成时任务已执行；该保证不等于实际开始执行、完成或外部送达。容量按最多两项的批次处理，保留完整计划与每项outcome；部分成功恢复不得从头重放，未处理请求不能静默丢弃。已持久化的旧checkpoint保持既有幂等效果，不因升级强制失效。

终结审查只呈现唯一candidate.actions视图；Host为每项分配action_ref=aN，工作动作使用实际提交时的规范化purpose/context，不同时展示旧action/items/non_work_refs投影。review返回 `verdict=allow|revise`、从Host本轮 `quote_options.requests[{ref:qN,text}] / candidates[{ref:cN,text}]`选择的必填 `request_quote_ref / candidate_quote_ref`、最多160字符reason、未处理 `missing_source_refs[]`及必填 `work_checks[]`。每个start_work/continue_work恰好对应一个 `{action_ref, deliverables: single|multiple|none}`：single为一个独立交付物（可含相关步骤/修正），multiple为合并了无关交付物，none为没有实际工作。非工作动作不填检查项，纯非工作必须为空数组。Host校验引用与恰好覆盖，allow携带multiple/none不放行；交付物语义仍由LLM判断，不能据此声称Host已确定理解用户意图。真实边界导致revise时可增加上述200字符constraint_quote。Host只接受本轮选项中的引用ID，绑定其原始内容并继续记录 `RequestQuote / CandidateQuote`，不再让模型自由转录引文。完整window仍是语义全集，选中的短证据不能缩小请求范围。Host严格验证引用、verdict/missing_source_refs，只有满足上述work_checks一致性的allow才可提交；可选constraint_quote无效时按上述独立丢弃规则处理，decline动作本身仍需真实适用边界。空原窗/无文字ignore的哨兵由Host选项提供，模型仍选择对应qN/cN；既不重新开放旧模型动作，也不因换行/转义重抄错误而丢失有效裁决。

工作审查核计划能否授权启动，不要求未来检索已有答案；只起草不能改成发送。非工作审查核每个reply是否属于其kind：不能把未经查证的产品结论、专业分析或空接单承诺塞到acknowledge/能力说明/状态中。decline须有真实适用限制，不能编造缺口或拒绝正常工作。task_finished核当前result_ref及目标场景送达事实，不能把忠实结果回报当成需要新研究的业务问题。格式、口吻、状态灯及完整报告要求不用于拒绝合法短协调动作。

审查失败/超时/非法必填输出不放行，也不自动派发猜测的任务。修复上下文保留未执行的最近提案与具体Reason，不能只剩一段限制原文让模型猜测哪个action出错；提案omitted时明确要求从完整当前窗口重新组织。缓存绑定实际上下文；新证据后不能复用旧裁决。固定词黑名单及一次hint后放行继续保持撤回状态。

## 6. 窗口、回执与任务完成

collect 只合并正在输入的消息：4 秒静默，创建起最多 12 秒。封窗、已 claim、重试或挂起的窗口不再吸收新消息。同 scene 同时一个 Coordinator 窗口，沙箱执行仍受两槽保护；容量不能阻止新窗口判断聊天。collect/park 不提前 sync-silence 完成，回执随真实处理关闭。

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

正式合并保留现有字段修复提示：缺工作动作 reply 时仅提示补齐该字段；purpose 可点名 DWS身份/MCP/Skills，仍拒绝 CLI 命令、data-auth 与 openConversationId 泄漏。该义务迁移到有限 actions 协议，不恢复旧 items/text schema。
