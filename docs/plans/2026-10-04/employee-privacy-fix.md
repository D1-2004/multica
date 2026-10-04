# Employee TaskWake 遗忘遗漏修复

## 范围与参考（先文档后代码）

仅修新建 TaskWake 输入的私人历史资格遗漏，不改变旧冻结 input_snapshot、记忆来源、取消、任务归属或 Runtime。

参考本机 gawkbot `71e82a1809565281cbd0bf8185d3c125b715d934`：
- `internal/team/context_assembler.go` 的 task-scoped knowledge injection：在工作 packet 组装边界按当前任务选择上下文，避免各入口散落不同选择策略。
- `internal/teammcp/server_memory_tools.go:handleTeamMemoryQuery` 的 `resolveSlug` + `privateMemoryNamespace`：私人记忆由已解析的调用者身份确定，不能由搜索文本或显示名称选人。

Multica 已有完整 scene fence 与 TaskOrigin PostgreSQL 读者，无需移植 gawkbot broker。采用其边界原则：提取单一 TaskWake 本人资格，由 brief 和 RecentConversation 共用。负责人身份仍为内部 admission principal；记忆本人为 org-qualified requester ref，两者不混用。

## 接口与行为

`employee_memory_input.go:employeeTaskWakePrivateRequester` 只接受 fenced DM、同一 scene anchor、conversation origin、Anchor.RequesterRef 与 Task.RequesterRef 精确相等、非 automation、当前 tenant 的 uid/open_id PersonViewRef。其他 owner、群、automation、staff-only、跨组织身份均返回空资格。

`employee_task_wake.go:buildTaskWakeInput` 的历史读取同时传递已验证的 HistoryPrincipalID 和上述 MemoryPrincipal，使 `employeeentry/recent_reply_withdrawals.go` 已有按 owner/tenant/agent/workspace 的撤回派生答复过滤生效。

`employee_memory_input.go:freezeBrief` 使用同一 helper，避免 brief 和历史资格分叉。不会扫描其他 owner，既有 scope fence 和过滤上限不变。

## 验收方法与限制

高价值测试覆盖 helper 的拒绝矩阵、新 TaskWake 输入确实收到本人过滤（以本人 private reset 导致旧历史消失验证），复用现有跨源撤回及派生答复测试证明旧私人答复不会复活且独立来源保留。只在独立本地数据库运行；无 IM、预发部署或新 Runtime。

旧冻结 snapshot 保持原字节；修复仅覆盖新冻结输入。旧已保存输入可能仍包含私人内容，处置需要独立授权和可审计策略，本轮不修改历史审计对象。

## 本地结果（2026-10-04）

- `TestEmployeeTaskWakePrivateRequesterQualification`：11 个子例全部 PASS。
- `TestEmployeeTaskWakeHistoryUsesPrivateOwnerReset`：真实 builder + PostgreSQL history store PASS；本人 reset 隐去旧对话，其他 owner 的 reset 不影响本人。暂时去掉实际调用的 MemoryPrincipal 后同一测试明确 FAIL（旧 `Summarize the findings` 重新进入 History），随后恢复修复源码；此证明不是仅测试 helper 或空测试名。
- 复用 `TestDMWithdrawsExactOwnersCrossOriginPrivateReply`（manifest/lookup/deleted-origin 共 3 子例）与 `TestDMCrossOriginWithdrawalNeverWidensOwnerOrPublicScope`（9 子例），全部 PASS；直接、派生答复过滤、独立来源保留、workspace/agent/tenant/owner 隔离，以及旧 frozen snapshot/审计不被修改均有断言。
- 证据：`/Users/yuanzhan/d1/employee-e2e-evidence/EMPLOYEE-FOUR-FIXES-20261004/privacy/{handler.log,withdrawals.log,regression-without-principal.log}`。
- 测试环境：独立 `employee_privacy_fix_1004`，仅复制已迁移本地 closeout 库 schema（无业务数据），添加 normal fence seed；测试后删除独立库。最初默认本地 schema 缺 coordination_mode，不算产品失败，也未修改默认库。
- 验收级别：本地定向集成通过；未新增 IM、部署、Runtime，未宣称真实 E2E 或旧已冻结 snapshot 处置完成。
