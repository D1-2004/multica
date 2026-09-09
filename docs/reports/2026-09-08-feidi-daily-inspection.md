# 菲迪问答、Coordinator 与场域记忆巡检

巡检窗口：**2026-09-08 00:00–2026-09-09 00:00，Asia/Shanghai**。任务在9月9日零点后发起，按刚结束的9月8日作为主报告；未将零点后数据混入。实际问答检查正式环境，平台观测同时检查预发；本次改动只发布预发。按用户要求，没有发测试消息，没有做 E2E。

## 结论

发现一个导致反复失败的确定性 Coordinator 缺陷：**日志链接里的数字 cid 被误当钉钉会话ID**。菲迪当日12次 deferred 中11次由此引起，3条入站共消耗100次 Coordinator 模型调用。已修复代码和回归协议。

场域记忆也有持续不收敛的问题：超1600字后缺少有效修复时间，部分场域累计重试数百次；菲迪3个群的最后一次刷新仍因DWS读取失败而未提交。本次改善了压缩与修复预算，修正提交水位和错误信息的观测，但**未证明旧场域已恢复**。

仍有两个明确的实际回答错误没有修复：**截断后补造名单/分数**，以及**把访谈人问题答成学生对象**。不能将本轮部署视作问答质量全部验收通过。

## 取数与覆盖

身份通过 `dws-env as 教练` 验证，profile为钉钉的`NHI_100001`；对应Multica智能体`a9ce26da-e5fd-4c16-86ef-7fa0f46386bc`。DWS原网关为pre；pre列会话/详情失败后，临时切prod取得真实消息，结束后恢复pre。

| 来源 | 取得的数据 | 覆盖边界 |
|---|---|---|
| DWS全会话按日列表 | 5页，248行，247个唯一消息，25个会话，59条菲迪出站 | 接口自报完整仍遗漏2条SLS入站；按ID补读成功后为249条唯一消息、27个会话 |
| 菲迪正式SLS | 50条decided事件、39个coord_trace_id | 包含重试：reply22、issue14、silence2、deferred12；事件数不等于用户请求数 |
| 菲迪交叉关联 | 39/39条入站可DWS回读；39/39个trace可Langfuse读取 | 出站需另查实际消息；此比例不等于所有请求正确回应 |
| 正式Langfuse项目 | 35页，3434原始行，3415唯一trace | 19行为同ID不同版本；按最新版本去重，不把API totalItems当唯一trace数 |
| 菲迪Langfuse | 按agent tag计926条：Coordinator39、Memory242、Task645 | 按metadata.agent_name过滤会漏7条；分类用tag和实际根observation |
| 预发Langfuse | 108条：Coordinator33、Memory21、Task54 | 此处为列表分类；SLS差集另列，不能拿列表name推断完整链路 |

正式列表中2612/3415条input缺失，2674条output缺失；1189条Memory中881条status缺失。**列表不能直接计算真实失败率。** 菲迪242条Memory列表可见65条`error:`、10条committed、1条partial、166条缺status；14个场域当天最后一条详情中11条提交、3条DWS失败。此为末次详情抽查及错误下界，不是242次刷新完整成功率。

预发SLS的42个非空裁决ID中35个能关联Langfuse；剩余7个精确ID查询均404，但6个为`switch_off=true`，1个为Host `already_told_scene`静默，源码在startTurnTrace之前返回，属于当前无模型短路边界，不能判作导出丢失。另有空ID日志无法关联，单列为观测缺口。

完整问答、提示词和日志保存在本机权限受限的`/tmp/multica-inspection-20260908`；不将原始员工日志、人员评分或凭证提交Git。可用新Skill按时间和ID重新取证。

## 主要问题、原因与处置

### 1. P1：日志来源参数触发错误召回门禁

21:25–22:03的三条成长日志入站中，旧正则把URL中的`cid=75953554200`视作命名会话。Host要求先召回该伪ID；模型即使成功召回真实当前场域，finish仍被拒绝。

| trace | 当日行为 | 模型调用/错误 |
|---|---|---|
| `494ab191-71af-4651-9c02-6e2d1b04d8fe` | 22:00:16–22:02:48，6次尝试均deferred；DWS仅见入站日志 | 48次generation，28次named conversation拒绝 |
| `ba38877e-19b4-4bdb-b8f8-8dbd4fe95fa5` | 4次deferred，第5次变silence | 38次generation，21次拒绝 |
| `3cfa8e67-f305-4e6c-bbda-43e4235c9eb0` | 1次deferred，第2次变silence | 14次generation，7次拒绝 |

[主要失败trace](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/494ab19171af46519c026e2d1b04d8fe)。这100次调用不能归因成供应商模型故障；其中大量轮次是在修复Host制造的无效要求。只统计最后action还会把后两条的错误过程掩盖成正常静默。

已修：收紧CID语法和边界，排除数字`cid=`，保留正文及真实会话链接里的openConversationId；hint指明实际缺少召回的ID。没有取消明确跨会话查询的召回义务。6组针对性Host回归通过，未做真实模型/E2E。

### 2. P1：重发名单时编造被裁掉的信息（未修复）

14:09:45，DWS消息`msgUO/9kDOV1x5hl3omrhKT2A==`已有完整9人名单。14:11:03，用户问为什么此前未主动反馈，菲迪在`msgNrDOPoRT71khq+LlnW6dCQ==`中声称重发完整名单，却只列3人加“其他6位”，还把其中两人换成不在原名单中的人并生成分数。

当时policy `2026-09-08.2`、assembly `1`。模型实际调用了history、assoc_recall、issue_get和issue_comment_list，但原名单都被截成首人片段：history标有截断，issue评论则仅200 rune且不声明截断。**它读取了工具，不等于它取得了完整名单。**

[名单错误trace](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/32ba8f9e1c63433db756d1724231afdb)。当前`tools.go`仍使用description240/comment200预算，缺少正文完整性标志与按需全文补读。后续应补足这个Host数据合同，并验证截断时只报告已知部分、按需取回原文；不能仅再加一句禁止编造。

### 3. P1：场域记忆持续重试，稳定知识无法更新

菲迪3个群末次刷新仍因DWS历史查询失败：

| trace | 当日末次attempt（累计，非当天次数） | 已提交水位 |
|---|---:|---|
| `a2b0443705e585bc29c16a349f16afb8` | 418 | 9月4日17:17 |
| `d7c0bb58366752b61e9c913f3175080c` | 113 | 9月4日05:26 |
| `75f59f3aee110803077ecc027c8ac109` | 15 | 9月4日00:17 |

[菲迪记忆失败trace](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/a2b0443705e585bc29c16a349f16afb8)。旧CLI错误先被截80字符，保留下来的只是actions建议；Langfuse状态为`error:`，缺真正服务端code/traceId。已改为先解析白名单诊断字段再裁剪，未知历史错误归`HISTORY_UNAVAILABLE`，进入LF和可聚合SLS事件。**旧错误内容无法回填，三个群服务端根因仍待新观测闭环。**

同平台的金龙VOC场域`feedfb32f48920e3b8ed491f2cb31dc4`累计attempt287：首轮29.428秒产生2190字符，超1600后第二轮只有20.569秒而超时；另一场域`29863cfc91079144819fe32fa4050ba4`连续两次1756字符，第三轮只剩4.704秒。旧4轮共享50秒预算，修复不易收敛。[超长与超时trace](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/feedfb32f48920e3b8ed491f2cb31dc4)。

已改：全文1600硬上限包含标题/引用，目标1200；Host返回实际字符数与可执行压缩提示；修复轮独立50秒但整job仍受原120秒截止约束，预留5秒提交；相同被拒草稿结束本claim，保留dirty和既有退避。真实模型能否稳定压缩尚未验收。

另有须莫场域`d4c1d31626a3642e8fb84f85e209981a`累计attempt440、0events、claimed evidence不可见。未跳过历史水位，也未执行reset-memory；缺证据根因仍待查。

也验证了一个已有的正常读取链：冬翔场域17:08刷新`78c3f30140b51112a94120d0010b1932`提交revision22；21:17下一次请求`816a96e0-2dd2-4ea6-9c5e-64ddaf23c6e6`的SLS显示loaded/version22，Langfuse同轮generation含对应稳定知识。说明该场域已有记忆会进入后续模型上下文；不代表本次新版本已验收。该快照仍把14点已交付的背景挖掘留为“待确认”，也说明记忆不能代替最新任务状态。

### 4. P2：访谈人问题答成学生对象，未反映停办状态（未修复）

17:50:20，用户在已经问清学生对象后追问“谁和她one-one？”。菲迪再次解释学生是谁，没有回答访谈人。实际出站`msgNtdkxEl2cmjmMRSHIaNxuw==`；[对应trace](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/b9e11b1d943a416a94b442bbd1bbda00)。

本轮history未加载，也未context_read；记忆将此前问题概括成“询问one-one具体对象”，可能增强了复述倾向。issue已blocked，最新评论有停外发要求，答复仍说等待主管确认。不能断言记忆是唯一原因；它没有在本轮实际新发消息给第三人。后续需要关系语义与最新工作状态的正反例验收。

### 5. P2：旧事项和岗位约束误带当前请求

11:33–11:46，用户要随机日志原文，旧每日关怀文案事项以rank1被召回，菲迪多次回答为文案，并将快循环不能直接查日志说成“没有权限”；用户明确纠正后才派发正确工作。11:52要求按产出和思考识才，又触发黑客松的固定禁评答复。

这些是当天真实事故。当前分支已继承当日晚些时候加入的交付物/识才与评比对照规则；21:17的自然请求已能派发并收到日志原文。但该一次成功不能证明所有短确认、旧事项和岗位指令场景已修复。

19:57名单顺序纠正`696a1ab4-3d31-4ab1-a4fd-1224f32e2fe4`还尝试调用快循环未开放的`fde_mission.py`，产生4次stage不可用后deferred，重试固定拒答。岗位执行指令与快循环工具范围仍有张力，尚未在本轮调整员工本体/岗位Skill。

## Langfuse到底好不好查

**按稳定ID查询可用；仅靠列表和默认脚本，很容易漏查或读错。**

- 旧查询存在8页/400候选或6页/300候选隐形上限，缺结束时间；按人名metadata筛选漏掉菲迪7条；同ID多版本使计数重复。
- 共享trace顶层input/output/timestamp可能被后续agent_task覆盖，必须读Coordinator根observation，并分清首次输入、各次尝试和最终输出。旧脚本按trace仍扫近期列表，历史ID不可靠。
- 记忆history倒序页的首尾被误当oldest/newest；失败trace显示的是计划cursor。已改时间min/max，分开`cursor_at`/`planned_cursor_at`，只有根`committed=true`才证明数据库提交，工具accepted仅证明草稿通过校验。
- 现有Langfuse的metadata JSON filter不生效、userId过滤不可依赖。用agent tag/session/trace/idx事件；缺列表input时有界并发水合，不凭缺字段作否定。
- SLS自身也有证据截断：21:17日志请求`816a96e0-2dd2-4ea6-9c5e-64ddaf23c6e6`的user_prompt恰好8000字，岗位说明占据大部，记忆revision22只露前缀，current_message分界未出现。本次未修改此日志预算；已在Skill要求对比实际长度/user_prompt_runes，再以同轮LF generation补齐，不能声称SLS已经证明全文召回。

本次查询工具统一分页、去重与水合，支持`--from/--to/--environment`及扫描/水合/页数预算，stderr披露raw/unique/duplicate/截断统计，stdout保持JSON兼容。精确trace改为一次GET；详情摘要保留每次Coordinator尝试。

实测同8个候选、同8次详情请求，串行 **5.57秒** → 4并发 **1.93秒**，减少约65%（热缓存样本，非SLA）。精确trace单GET **1.325秒**。完整39候选文本检索已命中名单错误，39条全部水合、`truncated=false`。首次冷列表约14秒、详情抽样中位约3秒；不能把不同缓存条件混算成加速结果。

## 本次交付与验证

- 分支：`codex/feidi-daily-inspection`，已推送origin与aone。
- 应用改动提交：`423b434e8`，包含CID修复、记忆修复/观测、DWS诊断和查询工具。
- 新Skill：[inspect-daily-qa](../../.agents/skills/inspect-daily-qa/SKILL.md)，使用真实消息→SLS→Langfuse→下一轮记忆召回的顺序；只读默认，授权修复/发布时继续。配套`build_index.py`支持多份DWS补读账本和跨源ID关联。
- policy版本：`2026-09-09.1`；103条历史来源映射、19条义务、24组对照合同结构检查通过。结构检查不证明模型正确。
- Go受影响包`dwsclient`、`scenememory`、`inboundcoord`窄测通过；查询脚本21个单测及语法检查通过；新Skill校验、SLS真实固定窗口查询、LF真实文本/ID查询、真实DWS索引执行通过。
- 没有E2E，未修改正式环境，未重新执行历史用户任务。名单补读、关系语义、三个菲迪群DWS根因及缺claimed evidence问题保持待办。

预发部署已于 **2026-09-09 01:25:25 +08:00** 成功。

- [变更单36035346](https://cd.aone.alibaba-inc.com/unite/micro/cr/app/342160/36035346)；[预发流水线](https://cd.aone.alibaba-inc.com/unite/micro/publish/app/342160?flowId=1005452)，run `3107363464`。
- 构建job `171082354`核对源提交为`b419ad5bcc72f613c3fa5603a287057dbd36e892`，包含应用修复`423b434e8ddebe421fa49440ca58c8c80903845f`。
- 预发集成分支仅在两份policy JSON发生相邻新增冲突；在隔离目录保留预发现有finish字段/purpose规则和新增巡检案例，合并后4个Go包与policy结构检查通过（集成分支25组案例，巡检分支24组）。未将其他CR带回巡检分支。
- 代码合并、构建/制品扫描、预发部署、流水线预发集成测试均SUCCESS；停在人工“预发验证”，没有推进该门禁或正式环境。流水线阶段成功不等于业务E2E。
- 部署完成后`/health`与`/status.taobao`均HTTP200。Nginx将后者映射到后端`/healthz`；外部`/readyz`未映射而404，不能当作后端故障。二进制来源以流水线构建SHA为证据，未通过发送消息测试业务行为。
- 后续报告与Skill取证说明的文档提交不改变本次已部署应用代码。
