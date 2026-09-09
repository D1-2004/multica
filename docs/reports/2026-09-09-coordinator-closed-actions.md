# Coordinator 有限动作与上下文控制

## 问题和修复方向

原线上 trace `605e116e29704bf294f653bb4508a684` 暴露了职责边界问题：用户要求查证 AI 听记机制，Coordinator 读到旧事项后，通过通用 `reply(text)` 给了业务结论，没有真正派发。上一轮增加独立 finish 审查并移出长岗位说明；本轮进一步从模型工具、Host 校验和上下文来源收口。

1. **回复必须属于明确动作。** 模型只接受 `finish({actions:[...]})`，删除旧模型 `action=reply/text/items` 解析路径。开始工作 `start_work`、续接 `continue_work` 都同时提供工作字段和自己的回复。澄清、状态、确认、能力、记忆、边界拒绝、忽略各自有不同字段；完成回调仅允许报告当前结果或忽略。
2. **模型不能用文案代替派发。** Host 校验当前窗口引用、真实召回目标、动作字段、状态读取引用和结果引用。工作全部提交成功后才发送合并接单回执；失败或容量不足仍保留既有幂等恢复位置。每个 Executor 只获得自己交付物的回复与上下文。
3. **召回只服务协调。** `assoc_recall` 默认 3 项、最多 5 项，不返回聊天事件、评论或业务结果。`work_state` 提供真实事项状态及短目标，不能把未加载的任务运行状态说成已完成。每次读结果具有当前 run 的 `rN` 引用；状态回复必须引用实际保留的读取。
4. **主循环不再累加工具长对话。** 每轮重新组装当前原始窗口、有效短合同、有限读取快照和最新修复反馈。读取总预算 8000 字，历史快照 3000 字；淘汰与截断显式标记。当前用户原文、来源和原始历史水位完整保留。历史快照被淘汰后可重新投影，不靠旧状态猜授权。
5. **Agent 有显式短协调合同。** 新增 `coordinator_contract`，包含职责、必须委派的事、限制及澄清条件，总预算 1600 Unicode 字符（包含 JSON 和源 hash）。合同绑定完整 Instructions 的原始 hash；编辑岗位后未重新编写合同会变为 stale。API、CLI、Web 复制、Builder、Git/DTA、模板和 managed 来源链路均透传。执行器继续使用完整岗位说明。

## 为什么仍保留独立审查

有限字段可以阻止不存在的动作、非法目标和遗漏来源，但 `acknowledge.reply` 等自然语言仍可能误分类或夹带业务答案。因此每份候选均经过独立语义审查，不能仅靠字段合法就宣称正确。审查检查各动作的实际目的、当前授权和接单回复，包含多任务与澄清混合窗口。

缺失／过期／读取失败的短合同不会被当成无限权限：旧 Agent 继续接受完整岗位规则审查。必要时审查返回有长度上限、经 Host 逐字核验的边界摘录，供 Coordinator 正确拒绝或修改计划，而不是重新注入整个岗位说明。连 Instructions 都读取失败时保留未决。

## 可观测与巡检

Langfuse 保留内部派发 action，并增加 `coordination_kinds` 和具体 `coordination_actions`；内部 `reply` 是提交域状态，不代表模型仍有通用 reply 工具。检查 `source_refs` 后继续核对真实 Issue / task / ack / 同 CID DWS 消息，不能以模型选择 `start_work` 当作业务执行或投递成功。

合同状态可查 `coordinator_contract_state / coordinator_contract_hash / source_instructions_sha256`。读取上下文可查 `read_snapshot_count / read_snapshot_runes / read_snapshot_truncated / read_snapshot_budget`。`rN` 仅是该 run 的读取引用，不是平台业务 ID。

已更新开发面 `inspect-daily-qa` 和 `inspect-langfuse-trace` Skill。此前巡检报告与原 trace 证据仍保留；本轮没有新发测试聊天。

## 验证与发布

实施和验收中，最终模型回放、分支提交和预发构建来源待回填。

## 仍需关注

- 尚未给现有线上 Agent 自动编写或发布短合同。未配置的 Agent 主循环已减重，但独立审查仍消耗完整 SOP 上下文；不能宣称全部调用都有 1600 字岗位上限。
- 短合同是开发者显式维护的协调规则，源 hash 只证明版本关联，不能证明它语义上完整覆盖了全部岗位限制。
- 首个带短合同的 managed 来源定义应在服务端所有副本升级后发布；旧副本可能忽略新增字段。本次没有修改在线定义。
- 遵照用户要求未跑业务 E2E。模型冻结回放、Host 本地测试和部署健康检查分别证明各自边界，不等于线上真实任务及消息投递验收。
