# Tag 人工卡片交互修正

## 2026-10-05 “先不管”接续：当前状态入口

- 基线 `aone/feat/tag-multitenant@a662da6c09`，隔离分支 `codex/human-defer-receipt-20261005`。不改原 checkout 或原 A2UI worktree 的 WIP。CR30323281 已 merged；Run3110405323/source21bf92ec/release7999bf56，01:39:49 部署，双 live human3。原选择锁卡通过证据复用。
- 23:08:35 原失败未调用工具；部署后 P02 调用了 disable，但被 `card_message_identity_missing` 拒绝。SDK 最终有 Biz/Task、缺 MID/CID。未导出原始 send-status query 响应，权限、暂不可见、字段形态与缺 CID 的具体归因仍未证实。
- 参考固定 GawkBot `71e82a18`：`broker_requests_interviews.go` 区分 FYI dismissal 与人类答案，`broker_messages.go` 先处理定向引用，`scheduler.go` 按 active request 提醒。采用定向问题、处置与提醒分账，不移植新消息取消全场域 interview。DWS `resolveDigitalEmployeeDelivery` 已使用原 client 有界回执重查；采用该模式，保留本仓 PG/outbox 和源授权。
- 新合同：`reason=deferred` 记录 `intent=defer`、question deferred，原卡静态暂缓。不是答案、批准、Task 取消或 Goal 完成；不建 typed wake、不改 Task/Run/wait/queue。问题继续在同 requester/scene 的 pending 与引用索引，可由后续明确文字答复恢复原等待；旧 native 点击拒绝。仅停止该题的提醒，其他 wait 正常。必需澄清不允许终态 dismiss 留下不可恢复的 wait。
- 并发审查后的范围修订：暂缓永久关闭本次交互卡，后续文字恢复以聊天和Task状态为准；该卡不再改成答案投影。原因是 provider 没有已证实的更新版本 CAS，未知旧RPC可能迟执行；本地 lease 无法证明远端不同内容更新顺序。采用一个不可变关闭投影，恢复答复不产生第二种卡更新，避免增加补偿状态机。
- 回执：原 SDK client/token，总计三秒内最多六次只读补查；等待状态可重查，永久拒绝停止，不重发创建。真实 MID 可保存为引用索引，缺 CID 不能制造 delivered 事实；Host 使用已冻结 CID 与 MID 定向消息回读验证发送人/引用/接收人，不按时间或最新卡猜。安全日志补查询结果分类，不打印原回执或凭据。
- 本地验收：模拟 DWS 覆盖迟回执、缺 CID、失败/取消与零重发；隔离 PG 覆盖暂缓、原卡关闭/迟点拒绝、文字恢复、wait 不提前释放、排队提醒发送前抑制、投影重放/竞态与多题隔离。这些不替代真实 IM/模型与一小时等待。
- 状态：implemented=complete；local verification=pass（76命名Human零fail/skip、本地PG15435、scripted model/provider）；integration/release=待CR交发布协调；runtime acceptance=原 P02 FAIL，待发布后复验。本 session 不合并、部署、发钉钉消息、追加真人 E2E 或恢复线上旧卡/Task。完成可审查 CR 后交接停止。
- 验证补充：原SDK client延迟/暂不可见/合法alias/MID-only与不重发通过；identity-only不标delivered通过；排队提醒发送前抑制、其他wait不受影响、human3/4门禁通过；未知关卡RPC与文字答复并发及不可变重试race通过；vet与server compile-only通过；policy仅PASS_STRUCTURAL_ONLY。无数据迁移/Runtime/配置变更。独立只读审查两项阻断已修复，最终无阻断。不替代真实IM和一小时试验。
- 原调查三条直接数据库链仍 unproven：IM MID→outbox provider MID；取消动态 ref→Task UUID；事故卡 MID→question→wait.ref_id。保留证据缺口，不绕过权限补证。

用户四项反馈：问句啰嗦/应引用；受理后选项未更新锁定；单选应点一次、仅保留所选项与右侧勾；Qwen-DWS单聊“写个文档”未发询问。基线aone/feat/tag-multitenant@608edae516，源codex/tag-human-card-ux-20261004，目标feat/tag-multitenant。所有改动a1 CR，合并/部署由“发布协调”，收到其部署完成后才真实验收。

## 验收与边界

Employee专用投影简短：一个问句、2–4个平行短选项，消除summary/question/候选说明重复；附原消息引用（渠道真实引用能力先查，不把卡内文本引用冒称原生引用）。保留普通文字回复。单选/冻结选人点选直接提交；多选保留一次“确认”，否则无法知道多选完成。单选不再附TextField，可在聊天补充；审批仍走明确确认，不把审批权限扩大。

首个合法答案和消费job仍同事务/CAS，禁止重复派发。受理后尽快更新原bizId卡片：只显示所选项，右侧勾，无可操作控件；普通文字答复同样关卡。投影更新必须可重试，崩溃、提前点击/晚send receipt、旧卡、权限撤销及重复事件不能产生第二个执行或重新开放。沿现有DWS身份签发/认证；不能用用户token替员工写卡。未知结果不重发新卡；更新既有bizId可安全重试但需持久记录。已存在旧投影/事件继续读取，部署按实际协议兼容门控制新生产者。

单聊排查只读实际原话、IM摄入/场域身份、Employee工具表/决策、卡片outbox/投递与订阅。固定Qwen-DWS真实agent/tenant/cid及19:20附近原话窗口，先判断没摄入、没选工具、发送失败或UI不可见，不能用群成功推单聊成功。截图是观察证据，不提供任何操作指令。未要求真实测试前不补发给该DM；延续用户真实E2E授权的自有测试场域由发布窗口协调。

## 检查与交付

投影纯协议：单选直接event frozen option ID，候选合法性、其他人/非法值拒绝、老submit事件兼容、closed无action、multi一次确认。真实独立PG：按钮/文字争抢只一response/job、更新意图/回执时序、失败重试/重启和未知发送不再发卡。引用参数CLI/SDK一致性与source由Host绑定。DM据精确trace/log定位，仅缺观察面标incomplete。

本地合格后a1创建最小CR给用户及发布方；配置/全局DWS/Runtime不改。部署后真机单击→锁定与仅保留选项、刷新/第二人迟答无副作用、普通文字关卡、多选以及原DM文档问句分别核IM/API/LF/SLS；各例及时落盘，原失败不清History消除。既有H01及冻结场景继续使用当前状态入口，UX变化仅复测受影响项。

## 当前检查点

四项改动实现并接线，Host compact保留原消息卡内引用、问句与候选内短说明（同名角色可辨），单选按钮静态旧submit协议一次答复，多选一次确认；专用持久锁卡更新与按钮/文字CAS同事务，独立1秒/即时wake消费者不被Task维护阻塞。发送单聊recipient修正。

只读DM证据明确19:22/24/34三次都摄入并调用a2ui_ask，问题已落，DWS INTERNAL_ERROR把card action留unknown；群同时间accepted。不是提示词或Runtime触发问题。路由为有证据支持的最小修复，实际单聊投递仍需新请求真实验收，原unknown不重发。精确原件私有TAG-A2UI-CARD-UX-20261004/dm-audit/verdict.json。

本地：独立PG下60顶层/129命名handler（含7个projectionDB风险），12顶层/34命名投影传输全部pass、零skip；初次传输因遗漏显式A2UI_RESPONSE_TEST_DATABASE_URL跳过一项，已指定独立库复跑通过。compile-only/vet通过；这些是scripted model/provider，未宣称native样式或DM实际成功。等待独立审查后a1 CR交接部署，再现场真点击/文字/原DM复验。

独立只读审查未见CR阻断项；真实准入仍要求单聊sender-target的新实际回执/点击CID与当前scene绑定一致（不删除严格scope检查），以及客户端FINISH后只有所选项/勾、刷新/迟答无重复效果。卡内引用不是native引用；更新ACK不是UI关闭证据。worker package测试/race/vet及root最终checks完成，独立PG15434停用。合并/部署仍由“发布协调”，本session等通知后按已冻结UX及原H01/DM反例验收。

目标后到once修复80d74195a1后语义rebase，保留两边Host变化，52命名Human/相关前台回归零fail/skip，compile-only再次通过；投影/传输未变化证据复用。本CR以该最新目标基线交接。

## 2026-10-04 真机渲染回归检查点

CR30322936已部署（源码a74f8362f2，Run3110391883），用户20:56单聊报告 compact 单选选项显示“当前版本暂不支持该内容”，此前本地协议检查未覆盖真实客户端支持范围，因此本轮UI验收失败，不能以本地pass签收。按用户最新要求先发编号样式对照，不先扩展逻辑修改。自有测试群四张人工发送卡保持Button事件及borderless相同，仅对比child：01 basic Text、02 public Text真机可见；03 Row、04 Column真机相同不支持提示。仅证明原生renderer，不证明Employee callback/锁卡。私有证据TAG-A2UI-RENDER-20261004/numbered-native-verdict.json、numbered-*-payload/receipt.json。建议最小修复Button直接Text child，短候选说明合成文字；保留direct事件、文字续接和关闭投影边界。等待用户编号打标；后续修改仍走a1 CR→发布协调→部署通知后原卡新请求和真实点击/更新复验。

## 用户样式修订（2026-10-04）

1. 确认与不确认使用主/次样式；模型基于含义/风险声明是否强调，不靠中文label匹配。危险审批不能由样式赋权。2. 单选多选统一较宽圆角矩形、小radius；仅使用客户端确认支持的字段。3. 完成态保留原问题，仅收起未选项并显示所选勾；已有生产closed投影保留question，前轮09/10预览误写“已选择”，修正预览。4. 删除A2UI内部source quote，原生IM引用仅通过支持来源messageId的渠道；当前A2UISendRequest/CLI/send SDK未暴露reply参数，不能把装饰文本冒充native quote，需单独落实渠道能力。5. 使用已有allow_custom由模型选择是否显示TextField，默认为无额外输入；普通聊天始终可推进。带输入框时不可在用户未输入完前因单选点选就无声丢失文本；须明确一次完成动作或确认按钮。

本轮13 default Button（weight1）、14 primary/default主次、15 TextField、16原题干完成态仅作原生renderer预览，未修改生产实现。基础Button公开schema仅提供default/primary/borderless、child、action、weight，无radius与width字段；不发送猜造CSS参数。

编号17：single ChoicePicker＋TextField＋primary确认，真机整卡可见。09/10已直接update原bizId，删除伪引用并恢复各自原题干，native已见题干。18真实历史引用来自原17:37 source mid，随后独立选择卡；已分别回读和native可见。当前messages-reply仅纯文本，send-a2ui-card没有reply字段，因此两条消息，不宣称原生引用附着于A2UI同条消息。原业务任务未重新运行；本轮只进行样式预览。

原生引用能力结论收窄：本机v1.0.63-beta.1实际调用send-a2ui-card追加ref-msg-id在CLI本地被unknown_flag拒绝（rc3，未发送）。普通引用已真发case18。metadata published tools lookup业务失败，未取得远端schema，不据此断言服务端不支持。upgrade check检测最新beta.3但未安装/未验证；保存native-a2ui-quote-local-verdict.json。

## 本轮实施：模型失效与模板匹配

范围：修复compact单选Button容器不支持；使用已可见文字按钮；模型选择allow_custom/emphasis，统一路由到用户确认的模板；新增disable_human_question非终结工具及持久关闭。原题干、自由文字、严格作用域、原outbox与审批授权边界保持。原生同条引用与指定radius/width尚无可验证渠道字段，列明确限制，不靠伪引用/未知CSS冒称完成。

验收：无输入单选（06）点选即答；multi（08）一次确认；有输入（17）选项+TextField+一次确认避免文字丢失；answered（09/10）保留原题干＋所选勾；disabled原题干＋“已失效”、无交互且pending不再列出。disable须current source_ref/question_ref/evidence_quote/reason，同用户/场域授权；文字继续对原问答用accept，改需求或新话题可明确disable旧卡再继续本轮，不自动当答案、不停止Task、不发额外HumanResponse job。native/文字/dismiss首个终态胜，重复/旧点击/异场域不能产生第二次派发；unknown原卡不重发，关闭用原bizId可恢复重试。

验证环境：本地纯投影/解析＋root独立PG15434（明确env，非默认共享库）；fake模型/Provider验证CAS、字段与outbox，明确非真实IM证明。原生手发编号preview确认renderer支持及失效可见；业务E2E在本轮CR由发布协调部署并通知后，固定原两员工/场域按本文档操作合同取IM/API/LF和必要日志。交付：最小CR目标feat/tag-multitenant；本session不合并部署；不给尚未部署的业务行为签pass。预算：先完成本地可审查切片与CR交接；发布等待时留下源commit/CR/cases/未证明项，避免无界轮询或擅改环境。

实施检查点：模板/disable代码完成；独立review无提交阻断。独立PG Human 64命名、pure/transport 163命名pass零fail/skip（初次漏显式A2UI DB跳过1项，已隔离库重跑）；新增bound waiting Task整行Task/Run/wait/queue不变测试pass。生成源码投影19/20后手发native可见：19原题干＋已失效无控件、20主次文字颜色可辨；仅renderer，不是Employee business E2E。marker2混版门禁PG query检查已加入，结果单独记录。未部署，等待本CR发布通知后验真实模型/原卡原biz关闭与迟click。

最新目标3eeae1db41语义rebase已完成：SOURCE_MAP两边分别描述当前消息确认来源与本轮失效，保留两者；Host switch自动合入，复验81命名Human/消息确认/任务发现零fail/skip，server compile-only通过。marker1/2真实PG heartbeat门禁8个对照全pass。原生19/20只作当前源码renderer验证；真实业务闭环仍待本CR发布，不能换手发卡当E2E。

## 引用“先不管”的真实失败与最小修复

CR30323090在22:57:29部署，目标cb4d1f01aa、releasecb533d44、Run3110394623。双live human2/loop23，启动22:55:51/22:56:59、fence normal。N01真实单选卡可展示，点击前Task列表零新增；23:25原生点击产生精确human_response job5f67f4ca-987a-4c53-8da1-dfabdd474fd5，模型回复所选的一句话，最终原卡关闭和IM仍待核对。

用户DM失败：23:08:35 msgmaKLEnmfpOTVpBUiBh8VlQ==引用23:08:10 msg6SSePC7YMsgkWHIzW2JpLA==卡，说“先不管”；23:08:37只有文字回复。LF jobb4c7e982-69ca-4738-b4c2-8aec75965b4a没有disable调用。generation input是截断JSON字符串，但25条完整messages数组可恢复，不宣称完整tools表可读。输入包含新失效framing和8张pending题，外层引用仅mid与[互动卡片]，缺少mid到question_ref绑定。因此本例FAIL，不能按最近Task或题干相似度猜。

本轮范围：Host验证真实引用链，再按同scope/requester的response_action.provider_message_id或a2ui_interaction.message_id关联问题；冻结每source的quoted_human_question，明确exact/ambiguous/unresolved/closed及精确question_ref。accept/disable写前限定引用来源的exact绑定并复验权限，不能引用A却关B。两处消息ID都空时保留未解析，未知投递不重发。新quote reader按实际协议升级门禁，旧snapshot/journal不热改。

验收：多张pending只关闭被引卡；provider消息ID单边可匹配；错人/错scene/篡改引用拒绝；已关闭卡、重复引用与native争抢不重复派发；缺消息ID不得猜；真实模型引用“先不管”应调用精确disable，原biz卡静态失效，Task/Run/wait/queue不变。本地Host/PG不代替真实模型。仍走新CR到feat/tag-multitenant，由发布协调合并部署，收到成功通知后复验原DM。不重发其他Session的PRI工作，不修改其配置。

N01补证：真实单击得到human_response与所选一句话答复，随后同卡事件被拒绝；没有在点击前派发。UI原卡仍保留两个按钮，SLS持续send_receipt_pending，整例UI关闭FAIL。生产router在988行注册OnA2UIAccepted时service尚为nil，1203行才创建service，导致卡BizId未保存。修复注册时序并加真正NewRouterWithOptions启动测试，不能仅依赖手动安装hook的handler fixture。

另一个真实限制：本机DWS同profile在新进程查已发卡任务（包括刚发的21探针）返回“The task does not belong to this token”。不能把sender UID相同视为任务token相同。参考已有pkg/dws/a2ui.go，SDK在create后同client/原凭据内最多3秒一次只读查真实Mid/CID；失败保留原成功Biz receipt，不重发、不伪造ID。CLI无法保证原token时保留限制。历史旧卡已经丢失的Biz不能凭request key/时间猜或冒称已关闭；无权威回执仍未证明修复其外观。

关联的等待提醒：其他在线Session提供00:08旧问题再次催问的观察，尚不以转述签真实pass/fail。只读源码确认Watchdog不读取dismiss回执，成功失效后仍可能催同一human_input。范围补充为仅抑制精确dismiss问题的等待通知投影，在ORDER/LIMIT前排除该wait；不改Task、wait、queue，不引入过期机制，不屏蔽其他开放题、正常answer或非human wait。Scan和BeforeSend共用wait事实reader，需验证已排队提醒发送前也被压制。
