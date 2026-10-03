# 共享上下文、已完成基线与不可破坏合同

本文件可脱离原对话使用。完整仓库路径均相对当前工作树根目录；源码表中的包缩写以 `server/internal/` 为前缀，`employeeloop`、`employeememory` 位于其中的 `service/`。机器绝对路径只作为本机接手线索。

## 1. 从哪里开工

- 目标分支：`origin/feat/tag-multitenant`，真实远端 `git@gitlab.alibaba-inc.com:dingtalk-ai-lab/dt-fde-multica.git`。当前仓库仅配置 origin；不要照旧脚本假定还存在 aone remote。
- 用户原始目录：`/Users/mac-m3/d1/dt-fde-multica`，目标分支，可能有 `.omx/` 未跟踪内容，不清除。
- 本会话 delivery 工作树：`/Users/mac-m3/.codex/worktrees/employee-loop-delivery/dt-fde-multica`，供集成者复用。其他开发者另建 `codex/employee-<package>` 工作树；不得占用该工作树。
- 本机 GawkBot：`/Users/mac-m3/github/gawkbot`，固定 SHA `71e82a1809565281cbd0bf8185d3c125b715d934`。其他机器按仓库及 SHA 获取，不能依赖本机绝对路径。
- 当前 Go 在 PATH 不可用时，本机可用 `/private/tmp/go-sdk/go/bin/go`。其他机器按项目 Go 1.26.1 环境运行。
- 独立数据库由开发者按工作树配置创建，不能共享同一 handler/service 测试库同时跑不同包。真实 Redis 也用专用实例/测试 DB，不 flush 共享 Tair。

开工登记以下真实输出，随后把记录交 I：

```bash
git fetch origin feat/tag-multitenant
git rev-parse origin/feat/tag-multitenant
git status --short
git branch --show-current
git remote -v
```

必须阅读：`AGENTS.md`、`CLAUDE.md`、`docs/employee-loop.md`、`docs/agent-scene.md`、`docs/event-scene-router.md`、本目录及自己的任务包。涉及 Coordinator/assoc 共用代码时，再读 `docs/inbound-coordinator-loop.md` 和 `server/internal/service/inboundcoord/policy/registry.json`；跑政策检查只证明结构一致。

之前的架构上下文：先读 [R5 方案导读](../../2026-09-30/employee-loop-overview.md) 和 [R2 EmployeeLoop / Task Service 设计](../../2026-10-02/employee-loop-task-service-design.md)，按需查 [R5 详细架构](../../2026-09-30/employee-loop-design.md)、[HTML 版](../../2026-09-30/employee-loop-design.html)、[十三任务拆解](../../2026-10-01/employee-loop-delivery-plan.md) 与 [R2 交付路线](../../2026-10-02/employee-loop-task-service-delivery.md)。它们说明设计为何形成；当前已实现/验收状态以 [实现合同](../../../employee-loop.md) 和 [真实验收记录](../employee-loop-e2e-results.md) 为准。不要用旧方案的未实施标签重写已完成模块。

## 2. 已完成，优先复用

| 已完成事项 | 主要源码入口 | 最有价值的现有测试/证据 |
| --- | --- | --- |
| 持久事件消费/聊天窗口/lease/journal | `employeeentry/{store,types}.go`，`handler/employee_scene_entry_worker.go` | `employeeentry/store_test.go`，`handler/employee_scene_entry_test.go` |
| 真实对话轮次与遗忘过滤 | `employeeentry/recent_history.go`，`employeeloop/history_presentation.go`，`handler/employee_recent_context.go` | `handler/employee_history_presentation_test.go`，v18 真人冲突剧本 |
| 模型共享与快请求 | `modelregistry/employee.go`，`handler/employee_model_route.go` | `employee_model_route_test.go`，`employee_request_profile_test.go` |
| 独立 Task 与 Compiler | `employeetask/{store,compiler,packet,current}.go` | `store_test.go` 在无 Issue/queue 表 schema 中跑核心生命周期 |
| Direct / 成功续接 / 运行中纠正 | `service/direct_task*.go`，`service/employee_task_steer.go`，`handler/employee_current_tasks.go` | `employee_task_combined_test.go`，v15/v19 IM |
| 独立停止与真实退出 | `employeetask/stop.go`，`service/direct_task_stop.go`，`handler/employee_stop_task.go` | `direct_task_stop_test.go`，`employee_stop_task_test.go`，v21 IM |
| 通知与仅文件交付 | `handler/employee_run_notice*.go`、`employee_inherited_notice_policy.go`，`dwsclient/message_file.go` | 文件下载 SHA，v20 两次静默、stop 的发送前门禁 |
| Execution Event 终态事实 | `handler/employee_execution_event*.go`，`employeeentry/execution_fact.go` | `employee_execution_event_native_test.go`，旧误标自动恢复 + 新成功/取消 |
| 私有记忆和捕获 | `service/employeememory/`，`employeelearning/`，`handler/employee_memory_tools.go` | `employee-memory-capture.md` 真实单聊/群写入、纠正、忘记/隔离 |
| 场域能力与自管理 | `handler/employee_scene_capabilities.go`、contextcap、场域 MCP | `employee-loop-parity-acceptance.md` 实际读/写/清理；当前 Host 链接时效修复 |
| PG + Redis 丢通知恢复 | `service/direct_task_recovery_test.go` | 四种真实 PG/Redis 恢复 + Go overlay 反向验证 |

权威验收索引：`docs/plans/2026-10-03/employee-loop-e2e-results.md`；详细早期证据分别在 memory / execution / parity 文档。部分历史文档开头状态未更新，`employeeentry/README.md` 还写 continuation/control 未注册；先读当前代码和最新证据，禁止据过时段落重写已交付实现。

## 3. 四层状态必须分开

| 对象 | 含义 | 不能据此断言 |
| --- | --- | --- |
| Provider receipt / event consumption | 受理事实、场域与业务 owner | 用户已经收到回复；事件授权一切动作 |
| Employee job / wake | 一轮理解或确定性处理，模型请求 ≤3 | 整件 Task 已完成；后台执行已退出 |
| Task | 持久目标、输入与协作关系 | 某一 Run succeeded 就满足所有依赖 |
| Run / queue / process | 一次实际执行、结果、物理 writer | completed 等于文件送达；cancelled 等于进程退出 |
| Outbox / provider delivery | 意图、提交、查询对账和送达证明 | HTTP 2xx/本地写入等于真实渠道效果 |

现状：`employeetask.State` 同时供 Task/Run 使用，`RecordResult` 对当前 goal revision 的 active Run 直接设置 Task 成终态；`employee_scene_job.message_count` 要求 1–32，worker 将所有 item 解码成 Dispatch envelope。P 必须解决这两个共享接缝。

## 4. 存储与幂等

PG：Task、Entry、Run、邀请/输入授权、等待、控制 fence、版本、来源 receipt、job/lease、journal、验证、投递意图和审计。Redis：既有通知、扇出、负缓存和短期协调；没有第二份 Task 真相。

- 同源重投先找到冻结的输入/效果；同身份不同有效内容返回冲突，不能仅以 body hash 作为业务身份。
- 消息窗口最多 16 receipts/32 messages，pending 的前 250ms 可合窗，无强制首轮等待；claim 后新消息进下一窗口。
- 每场域单个前台 job，长任务释放前台；同 Task 单 active Run/writer，取消前驱的退出屏障仍适用。
- 三次模型预算跨重启累计，实际 provider 失败也计数；单请求 ≤20 秒，wake 总预算现为45秒。保留 profile `employee-fast-v1` 和真实 native request/journal 一致性。
- 正常终态无额外模型；跨场域最后一答若触发 B 接受 wake + A 汇总 wake，按两个 job 分别计数，链路最多6次，不能藏成“全链3次”。
- PG/网络失败保持可重试，不能把暂时故障记成永久 held/skip。明确拒绝和缺少映射才持久 reason。
- EmptyClaim 缓存 TTL 当前3分钟；遗漏失效可能延迟到过期后的正常 poll，不能承诺立即恢复。缓存故障不能丢事实或生成第二次执行。
- 锁序按具体事务合同：admission workspace→source/scene→job；执行控制 workspace→queue→Task→agent 等既有顺序。不能随意统一成一个口号；跨场域 B 输入提交与 A 入场分两个可恢复事务，避免持锁交叉。
- 不新增外键/级联。索引 CONCURRENTLY，每个索引一个单语句 migration；编号由 I 唯一登记，sqlc 一人生成。

## 5. 场域、身份、记忆与发送

- 场域唯一键是 Host 经 scene.Resolve/Lookup 得到的 scene_id；DM 以会话定位。CID、staffId、UID、标题均不能直接作 scene key。
- provider route legacy/unified 与 owner_loop coordinator/employee 是不同轴。已受理 owner 冻结，切换设置只影响新工作。
- Host 的 workspace/agent/tenant/principal 与当前授权来自持久受理/资源绑定。payload actor 或模型 task_id 不能扩权。
- 同事的答复仅授权输入自己的邀请；发起人可读该 Task 获准的输入。其他参与者的答案和记忆不得进入回复者 Loop。
- 跨 App 裸用户 ID 不等价。调用现有可信身份桥；无法验证同一人则澄清/拒绝，不能按显示名拼。
- 新的原生身份映射入口可参考 `handler/dws_native_person.go` 和 migration `9787_dws_open_identity_staff`。`dwsclient.RenewCrossOrgRead` 的 `chat_permission_grant` 是跨组织读取权限，不是发送权限或 Task participant grant；不能用它代替邀请授权。
- Direct 保持纯岗位/场域/实际能力，不恢复通用 Multica/Issue/Chat/Mika 指令。场域 MCP、自定义技能原文、DWS 身份与现有能力装配继续保留。
- `final_text_owner=host` 与 SDK 硬守卫保证来源会话最终文本唯一发送；文件和显式授权外联仍可以发。通知 `enqueued` 不是送达。
- 仅文件策略按原授权根继承，每个新 Run 必须有自己的真实文件回执；旧 Run 文件不证明新交付。失败/取消/未知送达仍准确告知。
- 私有临时历史与长期记忆分开。群不自动注入 requester-private brief；capture/correction/forget 的 reset/supersedes 墓碑保留。
- `workflow.promote` 只是记录；不是实际共享晋级。queue succeeded 只提供 inferred candidate，不是 VerifiedExecution。

## 6. 环境与运维线索

开发测试只能用显式预发 profile，固定并断言 `https://pre-fde-workbench.dingtalk.com`；本机 profile `~/.multica/profiles/employee-pre/config.json`。全局 `~/.multica/config.json` 曾被其他 session 改为正式，不可依赖。

已授权基础 E2E：冬翔 → Qwen-DWS 单聊及「各种 Tag」测试群。workspace `5f8b5b73-f912-4879-9a29-b763d103fedf`，agent `23cbd386-9498-4848-a112-a6953b4aaef5`，Tag tenant 配置 `4ab3b895-5ae8-4e5c-a23b-69e6c2d5284b`，org `439446171`。这些是业务定位线索，不能替代当前权限校验；跨场域另三对象由 Q 登记。

候选 Runtime 本轮证据：runtime `b706c1f6-186d-476a-847a-8b36b4aba05c`，FC template `0j2u5s2j4qozfrt5r3lo`，runtime repo `53b4e29f83a4fb10452178c081005f524e1b84e1`，Daemon pin `889e9b2df22ff0418ea0b3bcdebbf99f6eb3d09a`，shortcuts `d262250ee58743c3e7492d04fca4b8f21fbcb7b0`。新任务开始前读回确认，不能拿旧线索宣称当前部署。

- Server 预发 app342160 / pipeline66。每次先检查最新运行，避免取消其他开发者流水线；最后人工“预发验证”不是发布正式。
- intranet a1/Normandy 调用先 unset ALL_PROXY/all_proxy/HTTP_PROXY/http_proxy/HTTPS_PROXY/https_proxy。
- 凭据经现有本机 profile、运营 secret provider 读取，不打印值、不存 Markdown、不复制截图 secret。
- Langfuse 看实际 generation I/O；大于64KiB会截断，带 truncated 的记录不证明完整 prompt。SLS 查全副本，单 pod 没搜到不是全局未发生。
- 临时脚本/原始脱敏前证据在 `/private/tmp/employee-e2e-20261002`，接手不要只依赖这个目录。Q 将可复用脚本和脱敏证据格式纳入仓库，原始含凭据/链接响应仍私下0600存储。
- Cron WIP 备份 `/private/tmp/employee-cron-source-reader-paused-20261003-102555`：仅供比较，无完整实现或可直接还原承诺；先看 README/patch，逐项接到最新代码。
- 不手动迁移预发，不本地构建 Daemon 镜像。Runtime 代码提交到内部 runtime 仓库触发远端构建，再按 fc-runtime-dev-loop 验证。

## 7. GawkBot 的借鉴与取舍

| 固定源码 | 采用机制 | 本地适配 / 测试要求 |
| --- | --- | --- |
| `internal/bot/session.go`、`loop.go`、`queues.go` | 会话轮次；human/steer/follow-up；done 有待办才继续 | PG journal/lease、不锁住外部 I/O；恢复不重置模型预算 |
| `internal/team/task_addressing.go` | 线程/来源 task/明确引用；bare @不代表Task | Host可信映射与requester fence；不按最近任务猜多个候选 |
| `task_definition.go`、`notification_context.go` | 目标/交付物/成功条件/人工纠正、ContextUsed | 已有Compiler；全约束不裁尾，memory/正文不授予能力 |
| `task_ledger.go` | 从真实动作装配进度，不让模型自总结 | 追加PG记录保完整审计，仅限制每次注入 |
| `broker_scheduler_routines.go`、`scheduler_runtime.go` | occurrence和配置分离、暂停、CompleteSchedulerRun | 既有PG scheduler、真实APRun、原子adapter；无第二timer |
| `broker_task_stall.go` | 排除系统活动、一次诚实停滞提示 | 不照搬Task.UpdatedAt全部算进展；PG episode+outbox去重 |
| `task_distill.go`、`memory_workflow*.go` | passing机器证据才distill、引用和晋级产物恢复 | VerifiedRun来自独立Host证据；PG持久消费者而非goroutine/single-flight |

现有 SOURCE_MAP/许可文件继续保留。新复制符号也登记固定 SHA、改动和许可证；GawkBot 使用 Sustainable Use License，不将复制部分误标成项目通用许可证。这里记录现有来源约束，不额外创造发布审批。
