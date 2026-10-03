# COL-03 取消后迟到答复的本人上下文修复

承接第五批收口。真实反例：loop16取消Task/collection成功，迟到Director的冻结输入没有已取消邀请，仍输出“7家，谢谢，我汇总给冬翔”；没有实际汇总，但不实承诺违反用户预期。精确trace337adc5d90ad4f46b2477bcd339579dd与ROOT四面报告保留。

## 范围与 Why

已有BindingCandidates严格只允许活跃本人邀请，关闭后移除accept工具是正确的执行边界；问题在于模型失去自己曾经询问而如今已关闭的事实。复用taskinput.ParticipantViewer的同workspace/agent/tenant/participant/scene限制，增加有界只读投影，保持权限及效果不变，不靠台词中的数字或词句做Host分类。

仅拥有employee_collection.go invitationContext/helper、employee_scene_entry_worker.go接受工具条件和定向测试。禁止编辑他人隐私历史过滤、task brief、全局提示词；主代理唯一集成与部署。独立worktree employee/codex-collection-late，基线f322868405。

## 合同

见 docs/employee-collection-late-context.md。只提供真实已送达给当前发言人、当前同一scene的过去24小时关闭邀请，最多5条；只含本人问题、关闭状态、时间，不含Task/collection/invitation ID、其他参与者答案或人数、发起人私有场域/目标/备注。根据collection/Task/邀请真实终态决定关闭事实。未送达和跨scope数据不显示。

关闭事实不生成accept binding。仅存在关闭事实时仍移除accept_collection_input；开放与关闭混合时只对真实开放binding保留原工具，guidance分开。关闭问题不能收答、催问、承诺转发/汇总/恢复；普通礼貌回复仍由模型决定。既有write-time状态/授权检查不变，冻结快照重放不新增模型或写入。

## 有价值验收

1. 已送达邀请经真实取消事务关闭，迟到本人同scene请求输入包含关闭事实、无accept工具/绑定；零新增输入/Task/ready意图。
2. 同人跨scene、同scene不同人、跨workspace/agent/tenant，以及从未真实送达的邀请均不能暴露问题；不泄漏另一参与者答案或origin SENTINEL。
3. 开放+关闭混合保留开放binding，closed只作状态资料；24小时/5条上限明确。
4. 原COL取消/末答竞争及LateAnswer工具门禁保持；本地专用DB，无真实CLI/IM。真实原场景修复→部署→复验由root负责，不以localfake模型证明话术效果。

状态：实现完成。专用local clone DB multica_codex_col_late_20261004，6项定向top-level + 1项open/closed混合绑定反例通过，0fail/0skip；取消/末答竞争含concurrent与ready-first，closed-only zero input/ready。原首轮nil测试参数及克隆Task source唯一键失败保留为新增fixture修正沿革，不归因为生产缺陷。读事实复用原冻结FollowUps，未改变写状态、Model预算或旧输入协议；已关闭collection自身结束时刻优先，不由后续Task更新刷新24小时。主代理仍需集成、发布和原Director迟到场景真实复验，局部fake模型不证明产品话术已通过。证据目录 CODEX-CLOSEOUT-20261004-COL-LATE。
