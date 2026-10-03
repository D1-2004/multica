# 四条 service 失败修复切片

2026-10-04，承接 [15 接续核对](15-codex-resume-assessment.md) 的失败归因。本轮实际四条 FAIL，证据在 `~/d1/employee-e2e-evidence/CODEX-RESUME-20261004-SERVICE/assessment.md`。

## 目标与 Why

两条 ASB 测试用 user UUID 当 agent UUID，FC trace 测试期待已退役的直连 sink；应修测试以验证当前真实合同。retry/reconciler 则是两个真实生产入口不共享父行锁，竞争插入 child 触发 23505；要修唯一子执行的事务边界，不能吞数据库错误。

## 设计与研究依据

先更新 [Task 重试事务合同](../../../task-retry-concurrency.md)，再实现。采用本仓 `finalizeFailedTask` 已有的父行串行化与 existing-child 查账模式；参照 Temporal 按逻辑执行标识去重、PostgreSQL 行锁串行化，不引入框架依赖、不扩展重试类型/预算。保留通知在提交之后，仅 child 首次创建且 queued 时唤醒，deferred 由原调度器唤醒。

## 所有权与范围

- 主代理：本 Plan、合同、集成和发布。
- `code_gap_audit`：独立 service-retry worktree 的 task.go/task_completion.go 相关重试边界，以及 asb_launcher_test.go、fc_e2b_test.go、task_completion_test.go 内必要修正；不得改其他运行时生产能力。
- 无新迁移，无预发数据/配置写入，不用真实账号或 CLI 做失败注入。主 checkout 其他会话 WIP 不动。

## 验收

- 原四条定向测试在独立库实际运行，无 skip；ASB 验到真实 attach/warm 路径，FC identity 不因错误 sink 期待提前中止。
- retry/reconciler 原并发反例无 23505，同一父执行只有一个 child；已有 child 重放不重复通知，保留 backoff/effective budget 和 autopilot 排除。
- 增补测试仅覆盖未被原反例覆盖的真实退化风险；不写文字/实现镜像测试。gofmt、受影响构建和 diff 检查。
- 通过后归入下一批；本地成功不更新预发 e2e_verified。集成/部署仍按 `docs/employee-delivery-workflow.md`。

## 状态

合同已写；实现与复验进行中。完成后回填代码 SHA、实际测试结论、集成/部署状态和仍未处理项。
