# EmployeeLoop R2 实施与交付记录

用户于 2026-10-02 授权开发、分批交付到 `feat/tag-multitenant`、部署预发并验证。实现基线更新为远端 `78bfa78fa`；隔离开发分支 `codex/employee-loop-r2`。仅部署预发 pipeline 66，不发布正式。

## 批次与状态

| 批次 | 内容 | 状态 |
| --- | --- | --- |
| B0 | R2 方案归档；修复新库历史迁移依赖顺序 | 本地验证与两阶段审查通过，准备部署 |
| B1 | EmployeeTask 目标/追加记录/Run 持久化 | TDD 实施中 |
| B2 | Work Packet、Issue/Direct 接缝 | 未实施 |
| B3 | GawkBot 内核、首轮回复与 3 调用上限 | 未实施 |
| B4 | 隔离 memory/learning、入口及回报闭环 | 未实施 |
| B5 | 配置开关、预发真实场景与 Runtime 验证 | 未实施 |

## B0 已观察的证据

- 目标分支更新：远端从 `7d1a398bb` 前进到 `78bfa78fa`；采用新基线，未覆盖他人提交。
- 本地 Go：1.26.1；独立 PostgreSQL 17 测试数据库 `employee_loop_r2_test`，不使用预发业务库跑测试。
- 原始 `go run ./cmd/migrate up` 在 `271_task_completion_canceled_status` 失败：其依赖的 `task_completion_outbox` 到 9025 才创建。
- 新排序回归用例观察红灯：271 小于 9025。修复迁移移到 9540，并按仓库约束保留旧 271 alias；新增真实 PG 回归先复现 down 后旧 alias 残留，再修复成功撤销时一次删除当前及旧名称记录。
- 修复后本地 migrate up 完成；scene/eventrouter 的实际 PG 测试和迁移定向回归通过。
- `cmd/migrate` 整包测试通过。`internal/migrations` 整包存在原分支多组 9093/922x/926x/929x/930x 重复编号 lint 失败；本批没有隐瞒或扩大修改范围，不宣称全库测试全部通过。
- 开始工作时预发实例 `3110298726` 是其他已提交改动的运行；其构建、部署与集成测试均成功，停在人工预发验证。不能当作本任务部署证据。

后续每批记录确切 commit、测试命令/结果、审查结果、预发实例/发布 SHA、健康与业务验收。未发生的模型、Runtime、真实发送验证保持未完成。

- B0 两阶段审查通过；真实 9540 SQL 在本地事务内执行 down/up 后回滚，约束从 completed/failed 恢复到 completed/failed/canceled。
