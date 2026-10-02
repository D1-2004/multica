# EmployeeLoop R2 实施与交付记录

用户于 2026-10-02 授权开发、分批交付到 `feat/tag-multitenant`、部署预发并验证。实现基线更新为远端 `78bfa78fa`；隔离开发分支 `codex/employee-loop-r2`。仅部署预发 pipeline 66，不发布正式。

## 批次与状态

| 批次 | 内容 | 状态 |
| --- | --- | --- |
| B0 | R2 方案归档；修复新库历史迁移依赖顺序 | 已推送并完成预发部署 |
| B1 | EmployeeTask 目标/追加记录/Run 持久化 | 已推送并完成预发部署 |
| B2 | Work Packet、Issue/Direct 接缝 | Direct 文本执行及权限回归通过、复审中；Compiler / IssueBackend 开发中 |
| B3 | GawkBot 内核、首轮回复与 3 调用上限 | 内核已推送并完成预发部署；生产消费另批接线 |
| B4 | 隔离 memory/learning、入口及回报闭环 | 隔离记忆基础验证通过、准备交付；入口及回报开发中 |
| B5 | 配置开关、预发真实场景与 Runtime 验证 | 配置开关本地验证通过；真实账户验证等待预发 PAT 刷新 |

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

## B1 本地验证

- 新增 `employeetask` 目标、追加账本、Run/队列映射与可选 Issue 绑定；9600–9609 为独立并发索引迁移，无 FK。
- 先在已连通并迁移的隔离 schema 中观察 8 项明确行为断言失败，再实现到通过。最终覆盖 12 项顶层用例及 7 项删除竞争子例。
- `go test -race ./internal/employeetask -count=1 -v`、`go vet ./internal/employeetask` 通过，无跳过。
- 工作区删除新增真实记录夹具，先复现 Task/entry/Run 残留，再加同事务清理。清理隔离、事务回滚和两个已有工作区用例共 4 项 race 通过。
- 独立审查发现删除竞争：没有 FK 的新表不会自动获得父行锁。已补全部写入先取 workspace FOR KEY SHARE，并用两连接验证删除先行/写入先行，复审通过。
- 本机原 55439 是另一个会话的进程，期间关闭。本任务已创建自己的 PostgreSQL 17 cluster `/private/tmp/employee-loop-r2-pg`，端口 55462；重新全量迁移及全部定向测试通过。
- 本批还没有用户入口、实际派发或运行流量；同 revision retry/完整控制/等待属于后续接线。

## 预发交付进展

- B0 已推送目标分支，commit `685f68c73`；基线同时保留其他人的 `d11fe440b`（FC 场域/触发者复用）。
- 触发 pipeline 66 / CR 36355253，实例 `3110299956` 后被外部操作取消。新实例 `3110300031` 正在构建，最终验收需核对实际 revision。
- 预发 DB 直连演练因网络超时未完成；没有执行或提交预发迁移 SQL。正式迁移仅由既有 Aone release order 自动执行；后续以部署及服务日志验证。

## B1 预发与 B3 内核证据

- B1 干净交付分支 rebase 后提交为 `97cb044ae`，目标 `feat/tag-multitenant` 已推送。pipeline 66 实例 `3110300728` 的构建、部署、集成测试均 SUCCESS，停在人工预发验证；bootstrap 回读 `9600_employee_task` 已应用。
- GawkBot 固定提交的内核、队列、工具注册、会话和 prompt/voice 片段实际移植，LICENSE/NOTICE/SOURCE_MAP 随代码保留。
- 内核支持首轮直接回复、首轮 Host 已受理派发后回复、原生工具 ID 与完整批次、3 次真实请求预算、ctx 取消；不调用旧 finish_check/composer。
- 独立审查两次捕获并复现部分成功问题：多个 terminal 提前退出、后续 effect 无 receipt 错误被整体成功覆盖。均新增行为反例红绿修复，逐项 outcome/receipt 保留；独立 overlay、race/vet 复审通过。
- 可选 `employeeintegration` 模型 smoke 需要显式环境开关，默认不联网；本地 HTTP fixture 验证两场景与真实请求总数上限。尚未声称真实模型或 Host 副作用验收。
- Runtime 预检：默认预发 profile 返回 401 invalid token，已请用户本机刷新登录；私有 canary/真实 FC 与本地 Daemon 验证待有效 PAT。

## B3 部署及 B4 隔离记忆基础

- B3 开发提交 `54c4afe33` 经干净交付分支集成为 `6e1cf1fce`，已推送目标分支。实例 `3110304218` 的构建、部署和集成测试均 SUCCESS，后在人工验证阶段被外部取消；后继实例 `3110304361` 同样完成上述三阶段，停在人工验证。没有把取消状态误记成整条流水线成功。
- 新记忆包实际移植固定 GawkBot 的 learning、scope matching、recovery、distill 和 lookup/capture/promote 记录逻辑；独立表 9620–9624，与旧 Coordinator memory 无共享内容、revision 或 reset。
- 两阶段审查中的三个反例已修复：中文标题碰撞、reset 后改变模型 key 重放复活、模型冒充 Host Task/Run 来源；真实 PG race 回归通过。
- 独立提交快照在新建 `employee_memory_delivery_test` 数据库完成全量迁移；记忆整包真实 PG race、vet，以及 workspace 删除隔离/回滚回归通过。新增表的删除跟随 workspace 锁和事务。
- 扩展执行的全库 deletion manifest 检查暴露原分支大量已有未分类表；本批新增两表均已登记。该总表审计没有通过，不把定向通过描述成全库通过。
- 当前批次是存储及机制基础，不代表后台消费、模型提炼、实际内容晋级或真实发送已完成。实际模型、FC canary、持久设备滚动验证仍单独记录。
