# COL-03 关闭邀请提示模式隔离

基线fa8302。根第二波真实复验：Director quiet正确；dxxh trace54ba75a1f2ca4300aa5372e0648492e2已收到本人cancelled closed_questions、accept工具已移除，却通过普通reply说“收到4家，我记下了”，无accept/memory效果。独立判断为业务记录暗示不实，取消账本并未失效。当前guidance先灌开放作答/short-thanks再禁关闭收答，存在模式混合；其因果贡献仍以第三波真实复验判断。

## 最小范围与合同

仅employee_collection.go invitationContext的guidance按mode构造、既有定向测试和文档。不改worker、Persona、任务来源、Host分类或固定中文台词；旧冻结快照不重建。

- closed-only：只说明旧问题已关闭，迟到内容不算已收答/记入/汇总或记忆更新，不授予旧工作效果；可自然回应，不声明没有发生的记录。
- active-only：只给开放绑定/澄清/成功收答后的简短确认指令；不加入没有closed事实的关闭模式。
- mixed：只有invitation_context的有效开放binding可收答；closed_questions只约束旧关闭问题。新的独立实质请求走原授权与实际效果，不能复活旧问题。
- 本人/scope/24h/5条、actual delivered、accept工具权限与所有写时围栏不变。

## 有价值验证

真实PG关闭事实输入不再含active“if answers callaccept/shortthanks”；closed-only仍无accept工具/绑定/输入写入；active-only保留绑定/工具且无closed mode；mixed保留开放效力并分清闭邀请约束。沿用已验证scope/cancel测试不扩大无关回归。local验证只证明上下文合同，真实话术由root统一部署复验。

状态：最小实现已完成，生产仅invitationContext guidance构造（新增data guidance_mode），无SQL/worker/Persona/任务来源变化。closed-only、active-only、mixed与原late工具门禁4项定向PASS/0FAIL/0SKIP，不扩大已过的scope/cancel回归。专用local DB multica_codex_col_late_20261004，无预发/IM写入。原失败54ba75a1仍保留fail，因果与自然话术待root第三候选统一发布/复验。证据 CODEX-CLOSEOUT-20261004-COL-MODES/tests.log。
