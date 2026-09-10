# Coordinator钉钉快回复缺失：根因、修复与实测

对象：指定预发Agent e2293e9e-1e79-4926-b0e6-da4cb693add0。全部时间为2026-09-10北京时间。原始诊断只读；后续修复和验证分列记录。

## 当前核心问题

用户最新关注的是Coordinator已经生成受理reply，钉钉却未收到。原202异步受理不含Task ID，真正派工后的execution-update要求Router已建立externalTaskId映射；两者协议不一致，导致受理reply被HTTP 400拒绝并进入dead letter。工作台显示的“7s内回复”只证明Coordinator决策结束，不证明钉钉已送达。

确切证据：13:06:29.353，request_id=multica-coord-issue:ce732bb6-f9f9-4956-b629-22a231f0ad5c，execution_update_id=520d60ba-eb3a-4e0c-9cdc-c5dc01197c93进入dead letter，错误为Router HTTP 400。Router trace438e0b4f10b0e7e9b3df9557cbb7cd6ced17341351bb2a43c41c4b61afb05549实际externalTaskId=null、没有executionHandoff。

修复只在合法首次Coordinator派工时原子建立映射并记录handoff，沿原发送路径立即投递内聚reply；已有映射不能覆盖，重复回调不重复发送，不把工作排队冒充执行完成，不增加LLM调用。Multica的受理措辞同时收敛为围绕用户目标的短句，默认不提沙箱，保留必要命令名和用户要求的技术细节。

本地39项Router协议/数据库回归通过，含真实PostgreSQL并发CAS与最终结果兼容；Router预发实际快回复已通过，证据见下。Runtime已完成的工作仅作为历史记录，不再扩展沙箱范围。

## 钉钉快回复验收

Router修复efc81aec已随run3107631533部署，release为69eac012。原run3107630977在代码合并阶段取消，未部署；后续实例包含同一修复，部署和集成测试均通过。Multica文案fdac28b0a随run3107626052部署，release为670acb953，策略2026-09-10.3/assembly14。

同一“冬翔 ↔ 东翔测试号”预发会话完整回读证明：

| 时刻 | 事实 |
|---|---|
| 14:44:49 | 收到重新执行状态检查的请求，msgr6l+YZm9Zie91hUGm0n2sQ== |
| 14:45:07 | 任务71c2e84c入队，同秒钉钉收到“收到，我将重新执行 dws auth status 并返回原始状态输出。”，msgjW8FQctny3sQ9o7tdtYP5Q== |
| 14:45:13 | 收到修改当前结果展示格式的续接请求 |
| 14:45:30 | 钉钉收到一次明确等待说明，msgkS4ql+hRLzKetWtSsmb8/Q== |
| 14:45:43 | 第一轮原始状态结果真实送达，早先受理回复并未等待该结果 |
| 14:47:15 | 续接派工后收到该动作的受理回复 |
| 14:49:01 | 三条要点格式的实际结果送达 |

Router实际trace 870bdbf55f10ecb698ed5bd6a300b1580c8e8a92bbdbd903468bd3daa0b0f3a2已建立externalTaskId=71c2e84c-5d93-48ad-88a1-a469a2275d0b，executionHandoff保留原requestId、同Agent/Task与完整受理文案。每条受理及等待说明在本次回读各出现一次。结果阶段仍有额外收尾总结，不将其宣传为所有类型消息已消除重复。

用户可见首次回复约18秒：入站至Coordinator开始约5.8秒，Coordinator约11.6秒，派工与受理送达同秒。“钉钉快回复丢失”已修复，但18秒仍有优化空间，不将工作台决策时长当作端到端时长。

## 重复读取的对照验证

真实trace a6d66e8e144545368acb52143e75fb26中，同一个Issue的work_state连读三次，结果仅read_ref变化；各轮已看到前一结果，未发生快照裁剪。现有重建上下文没有原生工具调用历史，缺少明确的已读/复用说明。工具旧埋点在读取后才开始，不能把其0ms称作精确数据库耗时。

最小修复复用同run成功、同参数且可信的有界快照，保留read_ref；失败或不可用仍可重试，partial/unknown不升级。借用现有反馈位说明重复读取不能展开摘要，不覆盖审核或历史前置要求。读取计时起点已移到真正读取之前，复用另有观测标记；不扩上下文预算、不新增LLM调用。

同模型qwen3.7-plus、同一冻结输入各3次实际模型链对照（无数据库/派工/DWS写入）：

| 指标 | 修复前 | 修复后 |
|---|---|---|
| 主模型轮数 | 7 / 4 / 4 | 2 / 2 / 2 |
| 含审核的模型调用 | 8 / 5 / 5 | 3 / 3 / 3 |
| work_state实际读取 | 6 / 3 / 3 | 1 / 1 / 1 |
| 处理耗时（秒） | 16.792 / 10.881 / 10.856 | 7.199 / 6.737 / 6.599 |

三次均正确续接同Issue，保留“不修改登录配置”、truncated=true/complete=false及delivery=not_loaded。中位处理耗时下降约38%，不代表线上分位数或固定SLA。修复后模型没有再请求重复读取，因此真实模型收益来自说明减少了重复轮次；cache-hit/read_ref复用分支由Host单测证明，不冒称这3次真实模型命中了缓存。三链总token从97,572降为47,448，减少约51%。

## 最新预发实测与最终交付

性能提交4107d664b已随run3107647877部署，release为36aefdff1，保留同期二维码绑定更新57d3af718。部署、集成测试均SUCCESS，仅人工预发验证节点等待；没有发布生产。

- 实际策略2026-09-10.4/assembly15，trace2513240e2c8043edb7bc9e1894cb63a3。
- 15:46:42，主角发送相同请求，msgF3NaKLCNC6TRTS5a8/ErLA==。
- 15:46:49.099至15:46:56.079，Coordinator用时6.979秒；主模型2轮，work_state仅1次，审核1次。新读取span记录了约3ms实际读取，而不是旧的后置0ms。
- 15:46:56，Task50ff87ee入队，同秒钉钉收到“我将重新执行 dws auth status 并回传原始状态输出。”，msgEClJRNoD+1/SvIugXfi6ZA==。没有默认提沙箱，保留了所需命令与交付内容。
- 15:47:47，原始结果送达，msg433HL9MibD3sYuV0r9yD9A==；受理回复不再等待执行结束。

本轮端到端受理约14秒，较前一轮18秒减少约4秒。Coordinator自身从11.587秒降至6.979秒；这是两次实测点，不是全量分位数。剩余约7秒主要发生在进入Coordinator前的消息接入、收集和排队。工作台处理耗时不能替代端到端口径。

提交分支：Multica为codex/dws-auth-execution-recovery（CR36063548），Router为codex/coordinator-quick-reply（CR36066061）。主要交付为派工后立即投递内聚reply、忙时明确等待、简短文案与减少重复推理；Runtime已验收后按用户要求停止扩展。未合入主干，未推进生产或人工发布确认。

证据：所列DWS消息ID；/tmp/coordinator-quick-perf-live-messages.json（complete=true）；/tmp/coordinator-quick-perf-lf-full.json；/tmp/coordinator-quick-reply-router-detail.json；/tmp/coordinator-read-reuse-20260910/report.md及comparison.json。原始输入及凭证相关上下文仅留在私有证据目录。

## 原始长等待（历史定位）

约13分钟的用户等待，主要来自工作派发等待旧任务与执行器的额外操作。Coordinator本次只用了7.070秒完成两轮决策，dws auth status最终正常返回，并不存在该命令持续执行十几分钟的证据。

本次trace：b28d4b671b4d4ca5b236ad1df34fdc6a，policy2026-09-10.1/assembly10；本地代码对照b509047ee。

## 精确时间线

| 时间 | 事实 |
|---|---|
| 11:39:40 | DWS收到“继续重新打印 dws auth status” |
| 11:39:43.011 | Server持久化接受入站job |
| 11:39:47.079–11:39:54.149 | Coordinator决定continue_work/retry，指向原事项e7e0258a；约7秒 |
| 11:39:54.155–11:48:52.373 | 场域已有两个在途事项，103次park（其中首次来自409） |
| 11:48:57.572–11:50:12.708 | 原事项仍有pending AgentTask，409后又15次park |
| 11:50:14 | 原任务d1c67013完成，释放同事项执行条件 |
| 11:50:17.9 | 新任务f2b9f1b6真实入队，Coordinator job结束 |
| 11:50:27 | 新沙箱任务开始；启动过程约9秒 |
| 11:51:09 / 11:51:15 | 新任务两次执行dws auth status，均返回success/authenticated/token_valid=true、退出码0 |
| 11:52:07 | 旧消息回复命令失败，包含uuidgen缺失及topic_quote_guard_unavailable |
| 11:52:39 | 改用messages-reply后，DWS实际读到重新打印的结果 |
| 11:54:21 | Task最终completed |
| 11:54:26 | 又收到一条完成说明 |

从决策结束到任务入队约10分24秒；从用户原消息到结果送达12分59秒。期间11:41:10的问候仍在11:41:21得到回复，说明等待集中在该工作请求的提交/执行链路。

SLS固定窗口11:39–11:51分页为100+100+67条，park共118次。日志中attempt持续为1；这是5秒挂起等待，不是118次LLM推理。新请求保存的计划被继续使用。不能把本次误判成旧的8轮引用校验死循环。

## 原任务为什么久久不结束

原事项任务d1c67013于11:37:00开始。最先执行multica issue get、metadata和comment读取时，反复遇到：

`agent execution context requires MULTICA_TOKEN to be a task-scoped mat_ token`

实际shell显示MULTICA_TOKEN为空，任务配置目录起初没有可用凭证文件；Multica CLI版本报告fc-e2b/5a364303。执行器随后花数分钟检查环境、配置目录、本地代理、帮助、进程与认证配置，直到11:42后平台读取才恢复。11:44:12才首次执行目标dws命令并拿到正常状态；任务继续做身份/发送/评论等收尾，直到11:50:14完成。

这说明Multica任务API鉴权与运行时shell环境存在衔接问题。DWS状态返回正常，不应将上述mat_错误解释为DWS登录过期。Daemon源码负责注入任务级凭证，DSH执行shell中未见它；现已定位：NewDSH 使用的 Runtime 源码5a364303中，dsh-runtime/patch-terminal-bash-dws-identity.py只向shell转发DWS身份，遗漏任务级MULTICA_TOKEN。Runtime主线dacbd34已有修复，旧候选分支未包含它。补回该窄修复，不改变CLI的mat_门槛。旧镜像CLI main.commit误填Runtime SHA，不能将5a364303当作Multica源码SHA；本轮同时修正构建来源标识。

同一时间另有Web入口的“打印dws auth status”任务cbe3c2f4，11:39:17已拿到状态，却继续查身份与送达。它是另一任务，只作为相同运行时问题的旁证，不当作本次续接已经完成的证据。

## 后续任务也有额外开销

新任务f2b9f1b6运行时ID为8477c923，原任务使用9d5b7bdd；不假设两次执行环境完全相同。新任务先读取Issue、metadata、评论与关联，再执行目标命令。拿到输出后继续读DWS帮助、尝试发送、修正命令、关联、清理消息reaction、写评论及终态。

本次新任务有24次工具调用；299条trace事件中237条是thinking流式片段，不能称299次工具调用或299轮模型请求。命令相关use/result在日志中紧邻，只能确认结果当时已经返回，不能用服务端写入时间差声称精确毫秒执行耗时。

## 为什么前端看起来一直卡着

普通入站的continue_work遇到busy Issue，在写追加comment之前返回409；worker把整条job挂起5秒并保留回调，因此会出现“LLM已决定、没有新Task、用户仍在等”的状态。该路径不是proactive专用follow-up队列表，不能用continue_work字样推断已经排入独立执行队列。

同场域两在途事项限制进一步延长等待。当前前端没有充分暴露等待原因；相关follow-up展示也有“已关联任务”与真正“执行已入队”区分不足的问题。生成的继续执行文案不能作为新任务已开始的证据。

## 修复优先级

1. 修复运行时/DSH与Multica CLI的任务级鉴权衔接，避免Agent自行展开认证环境维修。
2. 忙事项续接需要明确、真实的等待状态与用户反馈，设置可观察的等待/超时策略；保留同事项防并发与去重。
3. 明确单命令任务应尽快执行并返回；按照Web/钉钉入口选择交付路径，减少无关资料读取、发送命令试错和已取得结果后的重复操作。当前任务描述无条件加入钉钉身份/实际发送要求，也会将Web入口带入额外身份查找。

## 证据与源码

私有证据：/tmp/coordinator-target-regression-20260909/auth-*，含入站页面API、完整task记录、3页SLS、Langfuse及DWS会话回读（complete=true）。DWS结果消息msgz7YB7Uc9m29yM+Fq9Blgtg==，对应续接原消息msg6VNxERlhMOLfnd0rgolNqw==。

源码：server/internal/handler/inbound_coordinator_job.go 的parkIfSceneWindowBusy/park；issue_comment.go的busy检查；server/cmd/multica/cmd_agent.go的newAPIClient任务凭证门槛；server/internal/service/inboundcoord/coordinator.go的任务说明拼接。

## 本轮实现与验证（进行中）

- Server基于预发f6eb46d8b，分支codex/dws-auth-execution-recovery。已保存计划遇到容量/同事项busy时，用Host一次性等待通知说明尚未开始，保留原工作计划与完成回调；有可信managed回复路由时复用response outbox，否则只展示当前会话。发送前检查原协调job仍有未提交工作，避免已过期通知。通知失败不阻断工作恢复。
- IssueDescription按Web、钉钉、未知来源生成交付要求，保留用户显式授权的外发；移除Web任务强制寻找钉钉接收人的错误义务。
- Runtime独立候选分支codex/dws-auth-runtime-compat-20260910基于旧5a364303，只补回主线任务MAT透传和来源标识；保留dws_message_policy_v1。固定Multica源码16a867e887a079a420d7fea345d9ced33904b868，包含旧819fc10的消息策略能力。
- 本轮不提高同场域并发上限，不自动取消用户旧工作，不切换用户已改成PI的Agent。
- 本地结构/Host检查、候选镜像、部署与真实任务证据将分别回填；代码通过不能替代送达与耗时证据。

## 第一轮预发实测与补正

Server ad95d535a随run3107611243完成预发，release ffbeb47e7包含该提交。原PI Agent首次请求13:06:16，任务13:06:39开始，命令13:07:07成功，IM原始结果13:08:05送达（109秒）。追加请求13:07:07，工作台13:07:22已有等待说明，但完整DWS回读没有该通知；串行第二次结果13:10:25送达。首个Task仍因旧reply/uuidgen失败重试、关联查询及reaction收尾运行至13:09:40，不能将此轮称作全部修复。

首条generation 4d7efe277e2db4fc（trace 942f19e60aee498fa04b55da75580a32）证明：common教程给出旧reply和uuidgen，当前交付上下文同时给正确的+messages-reply，却又默认要求查原委托人和映射角色。当前输入还包含非managed的reaction义务，证明生效的是旧交付链路。数据库只读连接超时，因此未断言原始wire究竟缺responsePolicy还是显式legacy。

补正：新入站独立冻结Host等待投递资格，使用当时回复开关/revision和已认证目标，支持非managed链路而不接管其结果回执；不同资格不合窗，旧legacy无快照不追溯授权。当前可信sender/CID/消息与回复hint完整一致时，无需默认查关联图找人；缺目标、冲突及转达仍追溯，当前Issue和最新评论约束保持必读。

## Runtime交付证据

- Runtime提交d41cd4ab5c7295e898c78bf40b3faa513eb14ba1，Multica源码16a867e887a079a420d7fea345d9ced33904b868。候选CI70847636/pipeline301296 SUCCESS，33项契约测试及镜像sandbox smoke通过。
- READY Template t5o562qxc8lwn1n59glp，alias multica-m7-vdd95d8b615567a87-r1-d41cd4，provider fingerprint dd95d8b615567a87。
- 独立Runtime5d20e728：冷Task1e207298首次Issue API直接成功，续接Task1f39cccf再次成功。SLS证明两轮使用同一sandbox sbx-7c4e169b-4d18-4427-9e6a-b28522876971，分别cold_start=true/false，均引用本Template。
- 原NewDSH9d5b7bdd从pz27zf41o5r2vz53plfa切到本Template；私有测试Agent在该Runtime上的Task8d9a6535再次通过真实CLI读取，输出DWS-MAT-CUTOVER-9435及CLI commit16a867e8，verify-task校验通过。
- 三次任务均无任务级令牌缺失错误，未显示或修改凭证。没有共享Daemon/wire变更，不声称已跑local Daemon滚动矩阵。用户原Agent仍使用PI8477。

## 托管提示词配置

Diamond pre单元的dt-fde-multica.json / DEFAULT_GROUP中，common更新前与真实generation逐字匹配。仅替换旧DWS教程及其优先规则，common从3828字降至2536字；issue/chat/auto三个配置段逐项不变。发布结果published=true，独立回读与候选JSON一致，内容SHA256为126be20542f947b0268c0b32216e3f7938cd7453246424050192c19f8f57e3f3。没有修改生产配置。
