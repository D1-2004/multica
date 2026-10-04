# 一次性任务：首波评测与发布协作

状态：定义候选已提供给「发布和验收」及「SPEC+EVALS」，未执行真实 IM。机器定义为 [scene-one-shot-candidate.json](scene-one-shot-candidate.json)。它是给权威目录的贡献草稿，不是第二套 canonical catalog；正式归属沿用 `spec-collaboration → cron-office → G15`，保持 P0 总数20。

## 先跑简单且能区分错误实现的例

| 阶段 | 稳定 case ID | 主要风险 |
| --- | --- | --- |
| 首波 | office-cron-once-short-due | 3分钟可以创建；创建Run先结束，等待不占沙箱，原单聊只提醒一次 |
| 首波 | office-cron-once-source-context | 5分钟后原请求人、报告、输出约束不丢，原Task已完成仍有材料 |
| 首波 | office-cron-once-cancel-pending | 到期前取消；重复取消/迟到创建重放不复活 |
| 首波 | office-cron-once-reschedule-pending | 原3分钟时间静默，新5分钟时间一次；改期不改变source/创建身份 |
| 边界 | office-cron-once-subminute-owner | 45秒可处理；持久调度或有界短等待均保持一个交付owner |
| 隔离第二波 | office-cron-once-admission-load | 同刻到期排队、容量限制、普通交互与其他scheduler工作有进展 |

3分钟与5分钟不只是换数字：前者检查创建释放/等待资源与唯一效果，后者检查原执行终结后的上下文继承。负载项使用专用隔离环境，不能对共享预发直接批量灌压。授权撤销、跨场域隔离、重启/提交崩溃与混版恢复沿既有回归补后续case，不将本地单测算作真实IM通过。

## 时间与证据口径

每例冻结原 `occurredAt`、应有 `planned_at`、实际 `admitted_at`、worker start、provider accepted及IM可见时间。分别量化时间解析、scheduler排队、执行冷启动、模型/工具执行、投递；不能把ACK时间或首次工具调用当起点。当前scheduler默认30秒tick，加FC启动和队列等待，持久化once不意味着精确秒送达。

发布负责人已确认低负载3/5分钟首波时间门：due→admission≤45秒（默认30秒tick+余量），due→原场域消息≤120秒，二者分别核验，不当业务SLA。每轮事前写入manifest；实际cadence/冷启动不符合时先保存证据再调整后续attempt口径，不能事后放宽本次结果。45秒case另定短延迟预算；用户允许<1分钟在原沙箱内运行，不代表必须实现短等待双路径。当前统一持久once支持<1分钟，没有刻意保留沙箱等待。

IM必须完整回读并按消息ID分类，创建确认/ACK与到点结果分开计数。设置成功需要持久资源回执；只有一条结果需IM与occurrence/queue/outbox共同核验。取消/旧时点静默需跨完整截止窗口和scheduler结算，不把“暂时没消息”当证明。上下文项读取新Run实际packet及相关工具调用，正确文字本身不证明材料或工具继承。

## 调度负载的已知与未证明

- 已实现的等待资源合同：每条once只持久化时间/source，在到点前没有执行中的等待沙箱；并发admission与重复投递恢复复用同一occurrence。
- 复用现有 `TaskService.ClaimTask` / Runtime claim 的agent事务锁及 `max_concurrent_tasks`。FC容量429已有退避；这些源码不等于全局负载验收通过。
- `scheduler.Manager.runJob` 当前逐scope处理，`MaxPlansPerTick`是单scope上限，不是全局每tick预算。`ListSchedulableAutopilotTriggers`仍扫描整个eligible集合，不能声称已具备全局扫描分页、公平调度或全局sandbox并发上限。
- 因而隔离case必须检查到期突发时的扫描/锁等待、任务积压、活跃sandbox、claim上限以及其他job/交互是否前进。若实证有积压或饥饿，再在现有dispatcher加有界准入/公平批次，而非加另一套timer或用sleep隐藏问题。
- 负载验收未完成前首波只做低负载功能验收，不承诺无限任务数/精确秒/严格渠道exactly-once。

## 发布接手与恢复

代码源在本任务独立分支 `codex/scene-once-schedule-20261004`，完整实现及本地验证入口见 [Plan](../plans/2026-10-04-scene-one-shot-scheduling-proposal.md)。统一发布由「发布和验收」负责；本任务不抢共享发布窗口。权威评测目录归属由「SPEC+EVALS」审阅，本基线尚未包含其新目录，所以贡献草稿独立传递，由统一分支语义纳入。

集成必须处理两个已发现碰撞：本候选reader13不能覆盖统一reader20/21等累积能力，需保留所有reader并把once/source纳入实际mixed-version gate；原9977/9980/9981与A2UI候选9977–9982重号，源侧已核对共享repo历史、统一release/eval/A2UI/SSE分支及各worktree占用，改为尚未占用的10060/10061/10062，源码映射同批更新。它们仅DDL/trigger，不新增索引或专属migrator hook；不加未部署旧文件的生产stem别名。旧通过证据保留，发布穿过某例关键步骤时只标该segment环境变化，不重置所有case/History或重复发消息。

首波场域必须独立，避开dm_director/group_e2e及其他执行者；等统一发布精确版本和所有live reader证明后再运行。所有真实账号、场域、运行版本和driver在当波manifest填写，不写到永恒定义；每例记录pending/pass/fail/blocked/incomplete及证据引用。schema/单测为本地验证，真实IM仍not_run。结束按登记的真实resource id取消未到期测试安排并回读；保留取消tombstone、历史和证据，避免为清理而删除别人的任务。
