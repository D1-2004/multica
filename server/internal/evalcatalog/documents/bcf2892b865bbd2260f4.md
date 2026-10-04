# Task 失败重试的事务与幂等边界

同一失败父 Task 的直接失败处理、completion reconciler 与 orphan recovery 可能同时要求重试。唯一索引是最终约束，不能代替这些入口的共同串行化与效果查账。

所有可能创建 retry child 的生产入口须保持一致锁序：当前 workspace/fence 资格→父 Task 行锁→重读当前状态与预算→按父 ID 查询已有 child→必要时创建 child 和关联领域事实→提交。调用者带来的父快照只作线索，不能代替锁内状态。

已存在 child 时返回同一个 child，无新插入、无第二次 queued 通知。新 child 必须继续保留 failure reason、resume 安全边界、retry backoff、有效 attempt ceiling、runtime overlay 与当前归属；autopilot 仍用自己的重跑语义。MCP 等外部读取在锁外准备，不持数据库锁做外部 I/O。

只在事务提交成功且本次首次创建 queued child 时广播 queued 并唤醒。deferred child 保持静默，原 PromoteDueDeferredTasksForRuntime 负责到期晋级和唤醒。并发冲突或数据库错误不可泛吞；所有存在性结论都要能证明 child 属于同一 parent。

这次修复复用本仓 task completion finalization 的父行锁和查账模式。研究参考：

- [Temporal Nexus 幂等执行](https://docs.temporal.io/nexus/operations)：以稳定逻辑操作身份约束重复执行；本仓用 parent Task ID 与持久 child 查账实现同类约束，不引入 Temporal。
- [PostgreSQL 17 行锁](https://www.postgresql.org/docs/17/explicit-locking.html)：并发事务在父行 FOR UPDATE 上串行化，随后读取更新后的状态；不能只在 workspace 粒度或进程内加锁。

验证至少覆盖直接重试与 completion reconciler 竞争、已有 child 重放、延迟重试预算与通知；原 `TestFailedTaskFinalizationSerializesRetryAndReconciler` 是实际失败证据。修复无 schema 变化，可随普通版本滚动发布；混版期间旧入口仍可能报原竞态，不声称仅部署一半副本就已解决。
