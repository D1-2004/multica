# 预发 dws auth status 延迟根因与修复

对象：指定预发Agent e2293e9e-1e79-4926-b0e6-da4cb693add0。全部时间为2026-09-10北京时间。原始诊断只读；后续修复和验证分列记录。

## 结论

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
