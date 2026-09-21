# Coordinator 发起人决策模式实施状态

状态：产品接线已实现，准备预发部署与真实验收。尚无产品链路通过结论。

## 已落代码

- Agent API / SQL / 前端共享类型与设置：`inbound_coordinator_user_decision` 默认 false；关闭 Coordinator 同时清除；设置变更递增响应策略版本。
- 冻结候选快照与执行 checkpoint 分离，候选不能进入 SavePlan。注册独立 policy；生成至多三条续接、新建和具体直接回复。提交解释锁定动作类型与工作目标，重用 Host 校验和 finish_check。
- PostgreSQL 决策/事件表与独立并发索引迁移；事务接收、事件幂等、有效操作者及环境/组织/会话/卡片/版本核对、超时和发送结果不明状态。
- 新旧 A2UI 事件解析，操作者不读取 action context；卡片无预选、不包含内部计划；DWS 发卡、更新、ready 感知的事件流适配器。
- 尚未接线期间的入口保护：开启后明确提示服务未就绪，不能静默自动执行；网页聊天明确提示渠道尚未支持。旧已提交计划仍允许恢复。

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

代码已提交；部署、迁移与真实验收状态将在完成后补充。
