# Coordinator 发起人决策模式实施状态

状态：开发中，不能发布或开启验收。没有部署，没有真实模型或产品链路验收结论。

## 已落代码

- Agent API / SQL / 前端共享类型与设置：`inbound_coordinator_user_decision` 默认 false；关闭 Coordinator 同时清除；设置变更递增响应策略版本。
- 冻结候选快照与执行 checkpoint 分离，候选不能进入 SavePlan。注册独立 policy；生成至多三条续接、新建和具体直接回复。提交解释锁定动作类型与工作目标，重用 Host 校验和 finish_check。
- PostgreSQL 决策/事件表与独立并发索引迁移；事务接收、事件幂等、有效操作者及环境/组织/会话/卡片/版本核对、超时和发送结果不明状态。
- 新旧 A2UI 事件解析，操作者不读取 action context；卡片无预选、不包含内部计划；DWS 发卡、更新、ready 感知的事件流适配器。
- 尚未接线期间的入口保护：开启后明确提示服务未就绪，不能静默自动执行；网页聊天明确提示渠道尚未支持。旧已提交计划仍允许恢复。

## 必须继续完成

1. 将草稿候选持久化与原 job 原子停放接线；同窗不同作者拆分；附件、续接和主动群参与路径覆盖。
2. 服务生命周期管理的数字员工事件消费者：安全签发身份、组织和内部群确认、订阅持久化归属、跨副本租约/续租、就绪后发送、重连与告警。当前 CLI 流适配器不是已运行的产品消费者。
3. 内部鉴权回调入口与持久化转交；快速点击早于发送确认的事件竞态；服务重启恢复；首次有效提交后恢复原 job 并继续权限/任务有效性检查。
4. 待办发送、卡片更新及执行派发的持久化工作器。未知发送结果核对原标识，不盲目再发卡。Agent 停用/删除取消未执行请求。
5. 状态卡显示提交中/已收到与执行结果的区分，禁用已提交表单；客户端空输入校验与会话摘要验证。
6. 完整数据集：大体积不可变快照 OSS、模型解释与执行关联、脱敏 JSONL 导出/回溯 API、工作区授权、人工标签独立保存及按会话/任务切分。
7. 新状态的真实 PostgreSQL 并发/重启验证、真实模型对照、预发企业内部群全路径端到端验收。
8. 删除开发阶段未就绪拦截，改由完整服务判断可用性；只为已验证测试 Agent 开启。

## 已运行验证

- `go test ./internal/service/inboundcoord ./internal/service/userdecision ./internal/dwsclient`：通过。
- `MULTICA_HANDLER_UNIT_TESTS_ONLY=1 go test ./internal/handler -run '^TestUserDecisionUnavailable'`：通过（未连接本机数据库）。
- `pnpm --filter @multica/core exec vitest run api/agent-response-schema.test.ts`：38 项通过。
- `pnpm --filter @multica/core typecheck`：通过。
- `python3 scripts/check-coordinator-policy.py`：PASS_STRUCTURAL_ONLY，不能代表模型行为认证。
- 定向 sqlc 再生成 `--check`：通过；完整 sqlc 存在已有迁移 271 的排序依赖，定向生成脚本仅为分析调整顺序，不更改发布迁移。
- `@multica/views` 类型检查：通过；最初缺失 `@pierre/diffs/react`，执行 `pnpm install --frozen-lockfile` 补齐现有锁文件中的依赖后通过，未修改依赖版本。
- 最初前端测试命令意外运行 core 全套：150 文件通过、4 文件失败；含 localStorage 环境错误。此结果不能写作全量测试通过。随后精确运行相关 schema 测试通过。

没有提交、发布、迁移执行或新增真实卡片发送。
