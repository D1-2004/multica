# Employee 发布安全修复切片

承接 2026-10-03 employee-loop-backend-delivery/18-closeout。开发 worktree employee/codex-release-safety，基线2f4e6005b4；主代理唯一集成/发布，本切片只本地提交和独立库验证。

## Why 与设计

1. 首次迁移中9821修改尚未由9930创建的employee_routine_occurrence。将decision迁移完整up/down stem移至9996，保持9930源不变；沿用cmd/migrate的full-stem alias，使已应用9821的库保留旧账本并补新账本，不重放已接受DDL。真正空库migrate up、重放、旧账本别名与down/up恢复分别验证；不以schema-only副本代替。
2. frozen-source入口写普通queued时，旧worker SQL会领取它，失去冻结绑定约束。reader-first就绪门只保护新受理，不能保护已排队行在旧reader回滚时的安全。新来源写独占queued_frozen状态，旧SQL只领取queued，因此不可能改路执行新来源；新reader兼容既有queued来源，所有live reader广告新marker后入口才受理新来源，混版返回503且无receipt/run。新reader的claim/retry/defer/complete支持两种状态，不把queued_frozen降回queued。

## 固定依据

复用本仓typed scene job可领取类型过滤、deploymentfence.AllLiveReplicasSupport的PostgreSQL副本事实及迁移full-stem alias；不信payload或进程局部开关。PostgreSQL17行锁/跳过锁定查询保证lease竞争；[PostgreSQL17 SELECT](https://www.postgresql.org/docs/17/sql-select.html)。既有workflow要求reader先于producer，但旧SQL无法改变，故独占状态提供持久隔离，不仅靠操作声明。

## 范围与验收

迁移、cmd/migrate别名；webhook通用入口、持久来源、worker/SQLc和必要server marker接线；新合同docs/webhook-source-release-safety.md。不重写H1/M3，不碰task retry或他人WIP。每项原子commit。

验收：独立空库全部migration up成功且重放无DDL；老9821full stem不丢；新down/up别名恢复。混版入口无新受理；旧claim SQL在新来源入库后零领取；新reader可处理/重试且保留独占状态；崩溃窗口变绑定拒绝而非改派。定向测试无skip，不访问预发库或真实Agent。

状态：9821→9996及full-stem alias完成；真正空库全部migrate up成功，第二次up全部跳过；迁移alias/reversible bookkeeping三条定向PASS/0FAIL/0SKIP。Webhook必要接线范围已协调，继续实现。证据：~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-RELEASE-SAFETY/。

## 完成证据与发布边界

Webhook隔离、marker/通用入口、API enum兼容与deployment fence lease计数已完成。9997先扩CHECK并安装精确JSON v1 DBguard，隔离旧producer和既有queued；9998独立CONCURRENTLY索引，9999独立VALIDATE。旧producer不受新HTTP门槛控制，只由数据库隔离工作；安装前旧consumer已claim的效果需主代理发布准入核对，不自动声明被撤销。回滚不将新状态迁回queued，down遇pending拒绝，保留reader或safehold。

真实独立空库全量up和重放通过；alias3PASS、Webhook9PASS、聚焦race3PASS、实际guard升级/down/恢复1PASS，均无FAIL/SKIP。handler/fence/server/migrate build/vet、gofmt/diff检查通过。SQL谓词与生成wrapper同步且形状不变；本机无sqlc CLI，不声称已再生成。证据目录包含原始日志、fresh-database.json和result.json。未改共享配置/未推送部署；预发验收状态由主代理推进。
