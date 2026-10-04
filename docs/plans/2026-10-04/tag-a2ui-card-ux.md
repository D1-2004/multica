# Tag 人工卡片交互修正

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
