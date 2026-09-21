# Coordinator 发起人决策模式实施状态

状态：58b3fd150 已部署预发（run3109348064、deploy161713114、allEnd=true）。既有内部群原生四条主路径、R14大快照、两实例四项真实PostgreSQL检查及实际去重告警已验证；Qwen参数兼容修复后四个真实模型场景通过。新R15b已原生入站并冻结候选，但上游DWS PREPARE_CALL_TOOL_ERROR阻断发卡，仍为send_unknown、未提交/未执行。完整方案尚未全部验收；下方早期记录为历史证据。

## 已落代码

- Agent API / SQL / 前端共享类型与设置：`inbound_coordinator_user_decision` 默认 false；关闭 Coordinator 同时清除；设置变更递增响应策略版本。
- 冻结候选快照与执行 checkpoint 分离，候选不能进入 SavePlan。注册独立 policy；生成至多三条续接、新建和具体直接回复。提交解释锁定动作类型与工作目标，重用 Host 校验和 finish_check。
- PostgreSQL 决策/事件表与独立并发索引迁移；事务接收、事件幂等、有效操作者及环境/组织/会话/卡片/版本核对、超时和发送结果不明状态。
- 新旧 A2UI 事件解析，操作者不读取 action context；卡片无预选、不包含内部计划；DWS 发卡、更新、ready 感知的事件流适配器。
- 入口保护：服务不可用时明确提示未就绪，不能静默自动执行；网页聊天明确提示渠道尚未支持。旧已提交计划仍允许恢复。

## 本轮完成的接线

- 候选快照与原 job 在同一事务中持久化和停放；按可信作者拆分收集窗口，回调恢复原 job。
- 每个发卡身份由 PostgreSQL 租约管理事件订阅；独立心跳、ready 后发卡、失联重连、身份定期更新。
- 内部鉴权事件入口、首次有效提交事务去重、快速回调与发送确认竞态处理；卡片更新失败独立重试。
- 大快照进入 OSS，JSONL 导出经过工作区鉴权、脱敏与容量限制；模型提案、人类选择、复核字段独立保存。
- 执行状态从原 job 与任务表关联回写；过期、停用、删除取消未执行请求。
- 服务端 DWS 升至官方 1.0.62-beta.8。Runtime 远端 master 已含相同版本，本轮另行核验预发实际产物。

## 预发验收待办

真实数据库并发、模型场景、内部群四条路径、身份/连点/双端/重放、重启恢复、客户端空提交和提交中状态、卡片摘要、数据集与任务关联。未知发送结果保留原标识待核对，不能盲目重发。

## 已运行验证

- `go test ./internal/service/inboundcoord ./internal/service/userdecision ./internal/dwsclient`：通过。
- `MULTICA_HANDLER_UNIT_TESTS_ONLY=1 go test ./internal/handler -run '^TestUserDecisionUnavailable'`：通过（未连接本机数据库）。
- `pnpm --filter @multica/core exec vitest run api/agent-response-schema.test.ts`：38 项通过。
- `pnpm --filter @multica/core typecheck`：通过。
- `python3 scripts/check-coordinator-policy.py`：PASS_STRUCTURAL_ONLY，不能代表模型行为认证。
- 定向 sqlc 再生成 `--check`：通过；完整 sqlc 存在已有迁移 271 的排序依赖，定向生成脚本仅为分析调整顺序，不更改发布迁移。
- `@multica/views` 类型检查：通过；最初缺失 `@pierre/diffs/react`，执行 `pnpm install --frozen-lockfile` 补齐现有锁文件中的依赖后通过，未修改依赖版本。
- 最初前端测试命令意外运行 core 全套：150 文件通过、4 文件失败；含 localStorage 环境错误。此结果不能写作全量测试通过。随后精确运行相关 schema 测试通过。

## 2026-09-21 预发证据

- CR 36250393；流水线 3109256218 的代码合并、构建、部署、集成测试均 SUCCESS。9291–9296 迁移通过发布入口自动应用并在启动日志读回。手工验证关卡不表示产品验收通过。
- 仅测试 Agent `79ab6405-7947-46bb-bc2e-a280b28348ca` 开启开关。企业内部群 `A2UI 内部群回调验证` 尚未进入绑定账号的 Router 监听范围，当前仍只有原群；已请求用户补充范围，不改成监听所有群。
- 真实 Qwen 3.8 max 回放发现选项类别和回复标签不合规。新增最多三次发卡前结构修正并保存原始尝试；Host 用模型计划中的正文渲染直接回复选项。随后模型端出现 429，不能据此报告模型验收通过。
- 决策 ID 关联原提案与提交解释 trace；增加 propose_choices / interpret_submission generation 记录。

## Runtime DWS 独立验收

- npm 当前 beta 标签为 `1.0.62-beta.8`。Runtime 远端 master `8c15d992c505fa44ea492b3f286b117a14d230e0` 已固定这一版本；此轮没有重复发布相同镜像或改动生产。
- 两个 master PUSH 构建 74519358（FC）/74519359（ASB）均 SUCCESS；预发稳定通道已指向该提交产物。FC 本轮任务使用已有私有 Runtime 的同一产物，不能冒充 FC stable 绑定验证。
- FC 任务 `d545e83c-fb7d-414c-bb7e-db8f9aa51cae`、续接任务 `e8add0fc-dfd1-4967-b29a-7f6ac8ecbacc` 均完成；trajectory 证明同一 DSH session，实际 DWS beta.8/open、卡片事件 schema、更新命令和当前任务身份 get-self 成功。
- ASB stable 任务 `ea50841d-c83e-4ebd-9029-ec008e306d79` 及原事项追加执行 `666fe1db-fd55-4e2d-a4dd-bb2b36719ae7` 均完成，实际工具结果与上述 DWS 检查一致。该 Agent 的 chat_session_resume=false，两个 session 不同；不标为同会话 warm 通过。聊天续接曾在 Coordinator 阶段失败，随后通过原事项显式续接完成独立 DWS 检查。
- 以上只证明镜像内 DWS 命令与任务身份可用，不能代替 A2UI 发卡、回调、派发、结果及数据集的完整验收。

## 最终预发与模型检查

- 功能提交 `a004d1084`；流水线 `3109264514` 的代码合并、构建、部署、集成测试均 SUCCESS。发布单 `161674779` 为 SUCCESS，`allEnd=true`；手工「预发验证」关卡保留 RUNNING，不冒充完整产品验收。
- 9297 重复投递审计迁移已通过自动发布应用，启动日志有 `up 9297_coordinator_user_decision_delivery_audit`。重复事件保留原选择与原处理结果，另存次数、最近接收时间与重复原因。
- 当前预发读取测试 Agent 开关为 true。未鉴权卡片回调实测 401；鉴权数据集导出实测 200、JSONL、no-store，当前样本 0。
- Qwen 3.8 max 服务在先前限流后恢复；冻结模拟上下文与只读工具回放通过：无可续接任务时新建、已有任务续接、具体直接回复、两份相似报告的两个真实召回候选、仅补充说明、选择后补充正文、否定取消、明确选择与说明冲突、指代与否定定位第二份报告。冲突返回不可执行，Host 不执行其附带的澄清候选，不新增用户询问。
- 上述模型检查均断言不进入 SavePlan；仍是预发模型上的模拟资料回放，未发送真实业务消息，不等同于产品群聊 E2E。
- 本地受影响 Go 包、纯 handler 用例、server build、policy 结构和定向 sqlc 检查通过。真实 PostgreSQL 并发用例因本机到预发数据库连接不通未运行成功，不能写作通过。
- 尚待内部群绑定范围完成后验收：默认关闭的真实自动流、一次卡片询问、四类真实提交、非发起人/连点/双端/重放、过期/服务重启/消费重连/更新失败、客户端提交中及禁用状态、列表摘要、数据集—trace—任务—群结果一致性。当前监听范围仍是原群，没有擅自放开所有群。
