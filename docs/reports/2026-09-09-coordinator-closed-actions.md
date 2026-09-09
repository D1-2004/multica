# Coordinator 有限动作与上下文控制

## 问题和修复方向

原线上 trace `605e116e29704bf294f653bb4508a684` 暴露了职责边界问题：用户要求查证 AI 听记机制，Coordinator 读到旧事项后，通过通用 `reply(text)` 给了业务结论，没有真正派发。上一轮增加独立 finish 审查并移出长岗位说明；本轮进一步从模型工具、Host 校验和上下文来源收口。

1. **回复必须属于明确动作。** 模型只接受 `finish({actions:[...]})`，删除旧模型 `action=reply/text/items` 解析路径。开始工作 `start_work`、续接 `continue_work` 都同时提供工作字段和自己的回复。澄清、状态、确认、能力、记忆、边界拒绝、忽略各自有不同字段；完成回调仅允许报告当前结果或忽略。
2. **模型不能用文案代替派发。** Host 校验当前窗口引用、真实召回目标、动作字段、状态读取引用和结果引用。工作全部提交成功后才发送合并接单回执；失败或容量不足仍保留既有幂等恢复位置。每个 Executor 只获得自己交付物的回复与上下文。
3. **召回只服务协调。** `assoc_recall` 默认 3 项、最多 5 项，不返回聊天事件、评论或业务结果。`work_state` 提供真实事项状态及短目标，不能把未加载的任务运行状态说成已完成。每次读结果具有当前 run 的 `rN` 引用；状态回复必须引用实际保留的读取。
4. **主循环不再累加工具长对话。** 每轮重新组装当前原始窗口、有效短合同、有限读取快照和最新修复反馈。读取总预算 8000 字，历史快照 3000 字；最近一次被拒方案单独限 6000 字、修复反馈限 800 字；淘汰与截断显式标记。当前用户原文、来源和原始历史水位完整保留。历史快照被淘汰后可重新投影，不靠旧状态猜授权。
5. **Agent 有显式短协调合同。** 新增 `coordinator_contract`，包含职责、必须委派的事、限制及澄清条件，总预算 1600 Unicode 字符（包含 JSON 和源 hash）。合同绑定完整 Instructions 的原始 hash；编辑岗位后未重新编写合同会变为 stale。API、CLI、Web 复制、Builder、Git/DTA、模板和 managed 来源链路均透传。执行器继续使用完整岗位说明。

## 为什么仍保留独立审查

有限字段可以阻止不存在的动作、非法目标和遗漏来源，但 `acknowledge.reply` 等自然语言仍可能误分类或夹带业务答案。因此每份候选均经过独立语义审查，不能仅靠字段合法就宣称正确。审查检查各动作的实际目的、当前授权和接单回复，包含多任务与澄清混合窗口。审查只看统一的 actions，不再混入旧的整窗 issue/items/non_work_refs 表示。每个工作动作以 work_checks 明确返回 single/multiple/none，Host 不放行多个或无交付物的工作动作。必填原文证明改为选择 Host 提供的 qN/cN 引用，由 Host 原样还原文本，避免 LLM 重抄换行与转义导致合法回复失败。

缺失／过期／读取失败的短合同不会被当成无限权限：旧 Agent 继续接受完整岗位规则审查。必要时审查返回有长度上限、经 Host 逐字核验的边界摘录，供 Coordinator 正确拒绝或修改计划，而不是重新注入整个岗位说明。连 Instructions 都读取失败时保留未决。

## 可观测与巡检

Langfuse 保留内部派发 action，并增加 `coordination_kinds` 和具体 `coordination_actions`；内部 `reply` 是提交域状态，不代表模型仍有通用 reply 工具。检查 `source_refs` 后继续核对真实 Issue / task / ack / 同 CID DWS 消息，不能以模型选择 `start_work` 当作业务执行或投递成功。

合同状态可查 `coordinator_contract_state / coordinator_contract_hash / source_instructions_sha256`。读取上下文可查 `read_snapshot_count / read_snapshot_runes / read_snapshot_truncated / read_snapshot_budget`。`rN` 仅是该 run 的读取引用，不是平台业务 ID。

已更新开发面 `inspect-daily-qa` 和 `inspect-langfuse-trace` Skill。此前巡检报告与原 trace 证据仍保留；本轮没有新发测试聊天。

## 验证与发布

核心实现已提交 `95072f39f`；最终引用协议与验证结果随本报告一起提交。分支为 `codex/feidi-daily-inspection`。

- 最终同版真实模型冻结回放 7/7 通过。所有业务工具为冻结的假只读工具，没有真实消息、任务或记忆写入。
- inboundcoord 全包 286 PASS、2 项显式 opt-in 跳过；服务端构建通过。合同/API/CLI/来源/派发与完成回调的本地窄测、36 项 TS 和 core typecheck 通过。
- 预发基线新增的 Portable v1/v2、Git/ZIP 预览、导出及本地包接管链路已补齐合同透传。无 hash 的作者合同绑定当前 Instructions；已有 hash（含 stale）保留；预览后合同变化使旧预览失效。真实本地数据库及纯来源测试通过。
- 结构检查保留 103 条来源、19 项义务、14 个模块；功能分支 36 个对照，预发合并保留旧 purpose/hint 回归后为 37 个。结构检查不替代模型或真实投递验收。

| 最终模型场景 | 结果 | 模型调用次数 | 耗时 |
| --- | --- | ---: | ---: |
| 原 AI 听记请求，旧 Agent 全文审查 | start_work | 3 | 7.99 秒 |
| 同请求，338 字显式短合同 | start_work | 4 | 13.21 秒 |
| 能力介绍 | describe_capabilities | 2 | 5.01 秒 |
| 当前任务结果汇报 | report_result | 2 | 5.34 秒 |
| 两项工作加一项缺信息请求 | 2 × start_work + clarify | 3 | 9.57 秒 |
| 一个动作合并两个独立交付物 | revise | 1 | 3.58 秒 |
| 同一产物的多个步骤 | allow | 1 | 3.05 秒 |

原 trace 初始 system+user 为 26,110 字符，新版为 6,287（-75.9%）。同 trace 两种合同模式总 token 为 19,895→15,345（-22.9%），审查输入为 12,584→3,180（-74.7%）。短合同一例错误调用了一次不存在的 start_work 工具，Host 拒绝后恢复；因此实测延迟没有下降，不能用理想轮数替代真实成本。

预发部署结果及构建 SHA 待发布后回填。

最终私有冻结证据：`/tmp/multica-trace-605e116e/replay-closed-actions-qrefs.json`。此前失败报告全部保留；修复的新增问题包括缺 intent、混合动作的旧投影误导、修复 Reason 被边界摘录覆盖、缺失上次候选、交付物独立性只藏在审查 prose、原文转义重抄失败。

## 仍需关注

- 尚未给现有线上 Agent 自动编写或发布短合同。未配置的 Agent 主循环已减重，但独立审查仍消耗完整 SOP 上下文；不能宣称全部调用都有 1600 字岗位上限。
- Qwen 偶尔将动作 kind 当作工具名，当前 Host 会拒绝并可恢复，仍需在真实巡检中观察多余轮数。
- 短合同是开发者显式维护的协调规则，源 hash 只证明版本关联，不能证明它语义上完整覆盖了全部岗位限制。
- 首个带短合同的 managed 来源定义应在服务端所有副本升级后发布；旧副本可能忽略新增字段。本次没有修改在线定义。
- 遵照用户要求未跑业务 E2E。模型冻结回放、Host 本地测试和部署健康检查分别证明各自边界，不等于线上真实任务及消息投递验收。

证据 SHA-256：`fa359bd9d0b23fe4f1005feace64ba9352e8c0b3e8dd7c1b5ad6f06d8952cb87`。
