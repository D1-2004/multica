# 20 条 P0 Golden 组合回归

每条 Golden 组合多个办公场景；字段为角色、验证目标、验证方法。Golden 引用具体分场景用例，不代表覆盖该场景的全部反例。运行结果独立记录。

## G01 群聊接入、@对象与在场回应

- 需要什么角色：主持人、请求者、旁观成员、数字员工、独立验证者

- 测试验证的是什么：消息进入正确群；可信 @ 员工得到回应，@别人和无参与资格的闲聊不抢话；不误建后台工作。

- 怎么验证：新建测试群并回读成员和员工绑定。按 @别人、普通闲聊、@员工询问在场的顺序发三轮消息；分别等待处置结算。对照可信 @、前台终态、群消息和 Task/Run 增量。

- 场景引用：group-participation

- 用例引用：office-at-other, office-at-employee, office-bare-group-greeting, office-presence-while-busy

## G02 缺信息澄清、补齐与一次派发

- 需要什么角色：主持人、请求者、获准收件人、数字员工、独立验证者

- 测试验证的是什么：缺正文时澄清且不外发；正文和目标补齐后，仅按确认范围执行一次。

- 怎么验证：群内要求通知已授权测试收件人但省略正文，回读澄清及零发送效果。下一轮补齐正文与目标，检查原授权、执行、收件人独立回读和重复次数。

- 场景引用：clarification-reference, group-authority, work-planning

- 用例引用：office-missing-body, office-accept-ack-state

## G03 群内多个对象与混合消息窗口

- 需要什么角色：两名发言者、数字员工、独立验证者

- 测试验证的是什么：同窗混合消息只处理有参与资格的工作；来源归属准确，闲聊不转成任务。

- 怎么验证：在固定收集窗口内发送给别人的话、员工工作请求和闲聊。等待窗口结算；逐条核 source refs、工作目标、实际回复及任务数量。

- 场景引用：group-participation, clarification-reference, work-planning

- 用例引用：office-mixed-recipients, office-mixed-old-new-work

## G04 冲突历史、指代与单项更正

- 需要什么角色：请求者、数字员工、独立验证者

- 测试验证的是什么：保留旧冲突历史后使用最近明确口径；指代正确，单项更正不污染其他对象。

- 怎么验证：群内先给两人旧颜色，再重设蓝/绿；问“后者”，仅更正其中一人为紫，再问两人当前值。逐轮核可见回答、模型输入和零不必要派发。

- 场景引用：clarification-reference, continuation-steer

- 用例引用：office-latest-reset, office-one-field-correction, office-ambiguous-reference

## G05 格式约束与文件静音交付

- 需要什么角色：请求者、数字员工、文件接收者、独立验证者

- 测试验证的是什么：严格 JSON 输出合法；仅文件交付不追加总结，文件内容和当前来源准确。

- 怎么验证：先要求只返回固定合成数据的 JSON，解析完整回复。再要求生成指定 CSV 且只发文件；从实际消息下载并核行列/hash，跨过终态检查无额外文本。

- 场景引用：clarification-reference, files-documents

- 用例引用：office-json-only, office-file-native-delivery, office-file-no-extra-summary

## G06 真实计算、进度与成功交付

- 需要什么角色：请求者、数字员工、执行器、独立验证者

- 测试验证的是什么：实际工具计算有真实输出；运行中进度不编造、不重派；结束后结果只交付一次。

- 怎么验证：群内委托受控等待后计算平方合计，运行中问两次进度。对照 read_task、Task/Run/queue、工具 transcript、最终正确合计及独立消息次数。

- 场景引用：work-planning, progress-wait

- 用例引用：office-python-real, office-running-progress, office-accept-ack-state

## G07 同一工作补充、续接与收尾

- 需要什么角色：请求者、数字员工、执行器、独立验证者

- 测试验证的是什么：补充和成功后续接保持同一 Task；必要新 Run 唯一；致谢不重启工作。

- 怎么验证：完成首轮计算后要求在该工作基础上求合计，再补充展示格式，最后致谢。核 Task ID、每次 Run、输入归属、实际结果和致谢后的零副作用。

- 场景引用：continuation-steer, progress-wait, group-participation

- 用例引用：office-continue-success, office-addition-same-work, office-approval-vs-closing

## G08 运行中纠正、退出屏障与新结果

- 需要什么角色：请求者、数字员工、执行器、独立验证者

- 测试验证的是什么：旧执行取消后真实退出；后继在退出证明后领取；只交付最新清单。

- 怎么验证：启动带等待的旧清单任务，运行中替换数量并删除一项。关联控制来源、前后 Run、进程退出证据与后继 claim；下载新产物，延后观察旧结果不再外发。

- 场景引用：continuation-steer, stop-control, files-documents

- 用例引用：office-steer-exit-barrier, office-steer-delivery-inheritance, office-continue-own-file-receipt

## G09 否定停止、明确停止与停止后消息

- 需要什么角色：请求者、数字员工、执行器、独立验证者

- 测试验证的是什么：“不要停止”无控制效果；明确停止先 stopping 后 stopped；停止后不被普通消息复活。

- 怎么验证：运行长任务，依次发“不要停止”、明确停止、查询退出、致谢和普通补充。核原 Task/Run、退出 proof、状态与零后继；观察跨过原定完成时间。

- 场景引用：stop-control, progress-wait, continuation-steer

- 用例引用：office-negative-stop, office-explicit-stop, office-stopping-not-stopped, office-stop-old-output-suppressed, office-thanks-after-stop

## G10 附件内容读取、引用与错误资源

- 需要什么角色：资料提供者、请求者、数字员工、独立验证者

- 测试验证的是什么：回答来自实际材料；引用定位可信；坏文件、越权资源或未就绪视觉路径不伪造答案。

- 怎么验证：在附件藏随机码后提问，答案不出现在问题里；再用可信引用续接材料任务，并给一份错误或无权限资源。核文件字节、读取来源、引用关系、回复和禁止旁读。

- 场景引用：files-documents, group-authority

- 用例引用：office-attachment-hidden-code, office-reference-no-authority, office-resource-bad-large, office-resource-permission

## G11 三人收集、乱序答复与一次汇总

- 需要什么角色：主持人、请求者、三名获准参与者、数字员工、独立验证者

- 测试验证的是什么：乱序输入准确归属，2/3进度真实；收齐仅向授权发起群汇总一次，不泄露私聊原文。

- 怎么验证：群内委托向三人收集7/11/13；按第三、第一、第二人答复，中途问进度并重投一次来源。核 invitation/input/ready/action 和群内合计31及次数。

- 场景引用：collection-input, progress-wait, cross-scene-tenant

- 用例引用：office-collection-out-of-order, office-waiting-input-count, office-collection-replay-count, office-collection-answer-not-learning

## G12 同人多任务、歧义与迟答取消

- 需要什么角色：两个请求者、同一参与者、数字员工、独立验证者

- 测试验证的是什么：明确邀请引用绑定正确 Task；歧义澄清；取消或撤权后的迟答不复活任务。

- 怎么验证：同时建两项收集，令同人按不同 invite 引用乱序答复，再发无引用歧义消息；取消一项后补迟答。核输入归属、计数、澄清、授权和零错误推进。

- 场景引用：collection-input, stop-control, group-authority

- 用例引用：office-collection-two-task-person, office-collection-unreferenced-ambiguity, office-collection-late-after-close

## G13 跨群与跨组织读取/发送边界

- 需要什么角色：两群主持人、请求者、另一组织成员、数字员工、独立验证者

- 测试验证的是什么：两个 scene 与组织权限保持隔离；可见上下文不扩大执行或外发权限。

- 怎么验证：在群A保存随机事实，在群B询问；加入仅有read grant的跨组织来源请求外发。核 scene.Ref、tenant fence、模型实际输入、记忆和发送拒绝/授权路径。

- 场景引用：cross-scene-tenant, group-authority, office-memory

- 用例引用：office-same-people-different-groups, office-dm-group-private-isolation, office-cross-org-read-not-contact, office-org-unbound-current-fence

## G14 等待提醒、真实活动与授权后续

- 需要什么角色：请求者、待答参与者、数字员工、执行器、独立验证者

- 测试验证的是什么：仅对获准等待对象一次提醒；真实进展解除停滞；预授权后续唯一推进并在预算到限收束。

- 怎么验证：制造收集2/3和专用执行无进展，核两类提醒归属；分别答复/产生实际输出。再完成明确多步计划，核 episode/action、无多余Run、后续修订及终止预算。

- 场景引用：reminder-follow-up, progress-wait, collection-input

- 用例引用：office-waiting-target-reminder, office-watchdog-no-progress, office-authorized-terminal-followup, office-followup-budget-bound

## G15 Cron真实到点、改指令与暂停恢复

- 需要什么角色：配置者、数字员工、执行器、独立验证者

- 测试验证的是什么：真实 occurrence 一次效果；已受理指令冻结，下一次采用新版本；暂停跨时点无执行。

- 怎么验证：建专用例行任务，在真实到点回读结果，重投该 occurrence；受理后改指令，暂停跨下一时点，再恢复。核 occurrence/配置revision、Task、工作包、消息和完整窗口。

- 场景引用：cron-office, group-authority, office-recovery

- 用例引用：office-cron-real-due, office-cron-occurrence-replay, office-cron-instruction-freeze, office-cron-pause-resume

## G16 Webhook验签、同源冲突与禁用

- 需要什么角色：endpoint配置者、事件发送者、数字员工、独立验证者

- 测试验证的是什么：合法事件一次执行；同ID重投不重复，不同内容冲突拒绝；错签、伪造权限和禁用不扩权。

- 怎么验证：向专用endpoint发合法签名、同ID同内容、同ID改内容、错签、越权payload及禁用后事件。核 admission、冻结来源、Task/Run/action 数量和实际目标效果。

- 场景引用：webhook-office, group-authority, office-recovery

- 用例引用：office-hook-signature, office-hook-idempotent-replay, office-hook-content-conflict, office-hook-disabled, office-hook-payload-scene

## G17 记住、纠正、遗忘与跨群隔离

- 需要什么角色：记忆owner、另一群成员、数字员工、独立验证者

- 测试验证的是什么：事实按当前private scope保存；更正和精确忘记有效；旧来源重投不复活，另一群不召回。

- 怎么验证：群A私有scope记随机值A，更正B，下一轮独立询问；在群B做隔离问句，再定向forget并重投旧来源。核原文来源、supersedes、tombstone、真实模型输入及回答。

- 场景引用：office-memory, clarification-reference, cross-scene-tenant

- 用例引用：office-memory-capture-private, office-memory-correct-supersedes, office-memory-forget-specific, office-memory-source-replay, office-same-people-different-groups

## G18 验证产物、经验提炼与新任务复用

- 需要什么角色：请求者/经验owner、数字员工、Host验证者、执行器、独立验证者

- 测试验证的是什么：只有真实本Run产物通过可信spec才晋级；同scope新Task实际使用经验，假成功不得verified。

- 怎么验证：TaskA计算并生成sum=385的JSON，用file/expected checker验证；等待提炼。新TaskB求sum=2870，核ContextUsed、请求、工具及产物；加入错目标/假输出反例。

- 场景引用：office-experience, files-documents, work-planning

- 用例引用：office-experience-host-proof, office-experience-fake-success, office-experience-real-reuse

## G19 共享晋级、撤回与来源重投

- 需要什么角色：经验owner、获准群成员、未获准成员、数字员工、独立验证者

- 测试验证的是什么：共享只在owner grant内可用；撤回后新工作不召回，旧source重投不使经验复活。

- 怎么验证：把已验证测试经验受控晋级到获准群，分别从有/无grant身份读取并使用；撤回后新建Task，再重投原来源。核scope/grant、learning/manifest、撤回墓碑及实际结果。

- 场景引用：office-experience, office-memory, group-authority

- 用例引用：office-experience-owner-grant, office-experience-revoke, office-experience-old-evidence-fence

## G20 丢通知、并发重投与重启恢复

- 需要什么角色：隔离环境owner、请求者、两个消费者、数字员工、独立验证者

- 测试验证的是什么：PG保留Task事实，缓存和通知故障可恢复；两消费者只领取一次，同源不产生重复执行和外发。

- 怎么验证：在专用PG/Redis/Runtime上制造commit-before-notify丢通知，分别重投原来源、删本fixture缓存/到期和重启消费者。并发领取并观察完整poll/TTL/重试窗口，核唯一Task/Run/queue、账本、输出和通知。

- 场景引用：office-recovery, work-planning, progress-wait

- 用例引用：office-commit-before-notify, office-cache-expiry-eviction, office-consumer-restart, office-source-replay-vs-message
