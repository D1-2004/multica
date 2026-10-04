# BASE-TASK 明确追加工作与执行方法继承

2026-10-04，基线fa8302ea67，独立employee/codex-task-continuation-policy。本片只改当前Task政策、native continue描述、现有trusted ForegroundBoundary与对应装配测试/文档；不改Persona、隐私、collection、harness或执行器。

## Why：原反例仍失败

原Director DM 05:25新e0e27613/Run0e8实际Python sleep12并平方；旧ff4/f1仍在同requester候选，来源关联已出现。05:27「继续刚才这项蓝杉验收：把这些平方加起来，告诉我合计」被前台直接回答55，当前e0仍1Run。progress也纯正文，无read_task。完整LF 992db5(start)/755a603(progress)/1d375634(continue)，API及IM在TASK-G5-HOTFIX。

此处不是所有加法都要派后台：原工作明确Python实际执行，当前source明确对该事项追加计算步骤；实际执行方法应继续约束这一步，口算不能代替。standards10的「首轮能完成就完成」适用解释/普通答案，不豁免明确工作和其执行方法。独立的「这些数加起来多少」与「报告里的55代表什么」仍可direct。进度所需current read与复述已经收到的报告是两个问题，快照state不能当当前执行事实。

## 根因和最小合同

System的employeeForegroundBoundary当前只区分解释结果/新产出/修改原交付物，没有既有Task追加步骤或继承方法规则；data TaskBrief里的强制read口号不能可靠胜过trustedSystem的泛化direct-answer边界。原native continue工具虽说further step，没说明已有方法仍生效。修复把一般规则放已有trusted boundary，再同步data提示/native描述，而不把用户/Task数据升成system authority。

- 解释已交付结果：direct；仅问某数字含义/已有证据说明什么、普通独立口算，都不是自动续接。词「继续」「合计」本身不授予新工作，也不由Host做词匹配。
- 当前source明确追加原Task的一步、扩展/调整/重做其交付：先source-bound read_task，再continue_task，保留原实际执行方法与约束，除非请求人明确放弃/改变方法。短步骤、有现成数据/可口算不替代真实执行。只执行这一步，不无故重跑sleep/旧步骤。
- 询问现在进度：read_task；快照候选/上轮成功不能当当前读取、进程退出或新执行。复述已经送达的报告内容可direct，但须明确是那份已有结果。
- 新独立交付物仍dispatch_task+builds_on；不借此继续旧统计任务。

规则进入新snapshot冻结的Persona.Expertise里的现有boundary字符串，BuildPrompt和Persona文件不改；旧快照与旧tools描述仍原字节，marker16不变。source/read_ref/当前权限/CAS、2轮native预算、继承纠正与旧Task execution path不变；不增Host语言分类器、执行器或隐藏模型。

## 验证

最小反例先RED：实际新snapshot的System必须有继承/当前read边界，data注入保持user role；旧snapshot保留原policy。native继续描述要同时覆盖短真实执行步骤与报告解释direct的对照。原当前Task/旧快照与真实PG read→continue相关测试定向回归。local验证只证装配/事务边界，不证LLM选择质量；第三统一候选部署后原DM再用原失败台词复验，不修改台词躲失败。

## 本地交付结果

fresh隔离库 `multica_codex_task_continuation_policy_721` full migrate up成功。最小baseline RED：实际system缺规则（new_snapshot fail，legacy仍pass）及native描述缺继承边界。最终实际provider wire检查与当前Task/权限/续接/CAS/原冻结快照回归23 PASS/0 SKIP；handler build/vet均退出0。日志 `_shared/logs/codex-task-continuation-policy/task-inheritance-{red,final}.{log,sum}`。

测试只证明规则真实送入system与可用native工具、数据未提权、旧政策字符串不改及原事务边界；不以mock选择结果宣称LLM质量通过。未调用provider/发IM/改预发配置/部署。第三统一候选仍须原DirectorDM以同失败台词复验实际read_task→continue_task、初始Task同ID≥2Run及真实stdout55，同时报告解释/独立合计保持direct。
