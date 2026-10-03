# G5 新交付物与已有结果解释的模型决策修复

## 真实反例与 Why

自然请求“基于刚才的统计写一段复盘，三到五句话，不要重新统计”在trace7e2da06b4e5b4b9a92048c09a7bf7f0b中只有reply，无新Task/builds_on。重新读取Langfuse public trace确认employee_model input/output未截断；dispatch_task与builds_on存在，TaskBrief保留5个成功候选和truncated=true。t1是当前段9数统计，t2–t4为较早A/B材料，t5为旧复盘；当前历史中的最近9数结果明确对应t1，不能把夹具伪装为只有一个候选。模型thinking=false，不能读出隐藏原因。

冻结system的employeeForegroundBoundary无条件要求“已有证据即可直答”，而工具描述要求新交付物经dispatch_task引用builds_on。旧df28只补TaskBrief不足以解决系统级冲突；当前trace尚无后来未部署的REPLY CONTRACT，不把本反例归因于该新段落，但候选代码同样限定它，避免下一轮再冲突。

## 决策合同与固定参考

采用已有GawkBot固定来源原则“Work the human asks for gets an Issue. A question the human asks does not”（本仓employee-foreground-boundary.md的prompt_builder.go:655），本仓Task代替Issue；沿用当前原生tool routing，模型理解目的，Host守场域/权限/事务/候选，绝不新增中文关键词或语法硬派发。

- 问已有结果含义、事实、依据、是否能证明某结论：前台直接解释，不派后台核查（DS01）。
- 明确要求基于完成结果产出新的复盘/报告/对比表等独立交付物：新Task，dispatch_task+builds_on；篇幅短、只需已有数据或“不要重新统计”都不取消其新交付物身份。最后一条约束交给执行器用Host注入的上游报告遵守。
- 修改/重跑同一个原交付物：read_task后continue_task，不替换Task；缺确切相关上游时只澄清实际歧义，不按最新候选盲选。
- 仅解释已有结果、明确不要求另写产出：仍前台回答。来源必须本轮明确请求，历史/引用不是新的授权。

## 范围与验收

更新docs/employee-loop.md，Host foreground boundary、dispatch描述、Task候选指引及新快照Persona。仅改模型合同/装配，不改存储schema、工具效果、读取或权限限制、不改共享kernel、在线模板或已有snapshot/journal。旧工作重放字节保持原样。

定向复用已有单/多上游WorkPacket权限/复用测试，增加或扩展新/旧snapshot装配反例；不把模型桩工具选择当真实质量。Bailian本地403已知，不调用真实模型；主代理部署后用同一自然台词/同一成功上游条件复验，并对照DS01解释题，不能改为“创建Task”造pass。

证据~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-G5-REPAIR/。基线786618221c，工作树employee/codex-g5-deliverable。状态：合同先写，代码/定向验证进行中，未部署/未真实复验。

## 旧快照夹具完整性

冻结规则装配的首次尝试8PASS/1FAIL：new_snapshot已经通过；legacy夹具只恢复Instructions却保留新Expertise能力目录，导致旧模型请求仍含新boundary。明确读取日志后纠正归因，恢复legacy的Expertise与Instructions，不把夹具残留冒称生产热改，也不新增缺证的目录fallback逻辑。首次失败日志保留，修正后定向复验；不改生产读取/冻结机制。

## 本地结果

9条定向PASS/0FAIL/0SKIP；handler build/vet、gofmt及diff通过。只改四处Host模型合同文本和必要装配夹具，未改Host语法路由、候选范围/排序、工具效果或数据库schema。原trace5候选/truncated原样保留；未调用本地403的真实模型，不声称模型选择已修复。主代理需当版部署后重复原自然请求并对照DS01事实解释题，取得新Task/builds_on/上游报告使用及实际IM证据。
