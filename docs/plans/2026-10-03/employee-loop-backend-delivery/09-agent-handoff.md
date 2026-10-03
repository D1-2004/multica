# 分派、接手与交付模板

本文件供用户直接发给其他智能体。不要求它们持有本对话；先定位同一Git仓库和最新目标分支。以下各段是开发指令，只有用户实际发给目标智能体才构成该智能体任务分配。

## 1. 通用启动协议

```text
你在 dt-fde-multica 仓库开发 EmployeeLoop 后端。
目标 origin/feat/tag-multitenant 持续更新；先 fetch，记录实际 base SHA，创建自己的 codex/employee-包名 工作树和独立本地测试DB。
先读 AGENTS.md / CLAUDE.md、docs/employee-loop.md，以及 docs/plans/2026-10-03/employee-loop-backend-delivery/README.md、00-context-and-contracts.md、自己包的文档。
架构前情先读 docs/plans/2026-09-30/employee-loop-overview.md 和 docs/plans/2026-10-02/employee-loop-task-service-design.md；按总控“设计沿革”查R5全文与实施拆解，旧文档完成状态以最新代码/验收纠正。
你不是独占仓库，不覆盖、回滚别的智能体修改，不清除 .omx/，不强推目标分支。
Task 独立于Issue；PG为事实，Redis为通知/加速；复用现有queue/executor/scheduler/outbox。
先写有业务价值的RED反例，再做最小实现、真实PG/并发/重投/故障回归；默认测试不执行用户安装的agent CLI。
共享Host/worker/router/marker/sqlc/state DTO由P/I负责；开工先提交所有权清单和拟接口，不在自己的包里各造一套推进器。
提交格式 type(scope): 摘要，正文是真实换行；用heredoc，提交后返回git log -1 --pretty=%B。
每次提交交集成负责人，独立同步智能体负责rebase最新目标、验证及非强推；你不自行占用共享预发/正式流水线，不本地build Daemon镜像，不任意联系同事。
你交付代码、RED/GREEN证据、接口合同、兼容/故障边界和准确未完成项；部署/E2E由I/Q统一安排，库完成不等于功能上线。
```

## 2. 可直接追加的包指令

### 给P/I基础负责人

```text
你负责01-foundation.md和08-integration-and-release.md的P/I实施。
先P1分离Task目标与Run终态、保留v1语义，再P2实现typed Task wake、P3来源reader/预授权后续工作、P4 Task读取API。
你拥有公共DTO、Task状态、worker、Host、router、sqlc、migration编号、marker和通知owner接缝；A/B/C/D/F/G提交adapter由你串行接线。
别让自动化或收集开发者把内部wake伪装人类消息；普通terminal保持零额外模型；reader先部署producer后启。
每个切片独立review→commit→同步→预发→对应E2E。维护部署队列、源/目标/实际server SHA和各包manifest。
```

### 给A协作开发者

```text
你负责02-collection.md，所有权taskinput新包和service/employee_task_inputs.go及测试。
立即做A1集合/邀请/授权输入/计数/ready意图，先交生命周期与DTO；A2等P1/P2合入，由I接工具和origin wake。
禁止把TaskID塞进IssueID。B回复者只见自己邀请和答案；收齐后PG意图在A原授权范围唤醒A Loop汇总，B不读取C/D答案。
用真实PG覆盖同人两Task、乱序/重投/并发收齐、撤权/关闭/取消、commit-before-notify和隐私sentinel。
三真实对象由Q指定；没有名单时继续领域/协议开发，不随机外联。
```

### 给B自动化开发者

```text
你负责03-cron.md，routine_execution_origin.go、employee_routine_task.go及约定的scheduler/autopilot窄接缝。
先验证Member/Agent creator、冻结occurrence和完整claim DirectPrompt+真实APRun组合；reader由P/I先发布。
确定性run_only零前台模型；同事务真实APRun/source/Task/Run/queue/start intent，planned_at幂等；不伪造人类消息或占位APRun。
再做显式employee_decide，复用typed wake和有限plan，不自动根据“如果”开分类模型。
交双scheduler/暂停恢复/受理后改指令/事务中断/notify丢失/通知owner回归；最终由Q实际等待时点验收。
```

### 给F Webhook开发者

```text
你负责04-webhook.md，employee_webhook_task.go、employee_webhook_origin.go及登记后的既有ingress/delivery窄改。
保留原始字节验签、provider ID去重和冻结ReceivedAt，payload actor/scene/mode不扩权。
先独立ingress合同，再P reader就绪后的run_only，最后decision/显式Task定向事件。复用自动化adapter，不复制Cron派发或伪造planned_at。
测同ID冲突、新ID有效、禁用/撤权、两worker/lease恢复、未知provider效果只查询；普通Webhook回归保留。
```

### 给C恢复/主动跟进开发者

```text
你负责05-watchdog.md，employee_task_activity.go、employee_task_watchdog.go及episode PG测试。
从真实输出/工具/材料/用户输入构造活动水位，heartbeat/扫描/提醒自身不算进展，Task.updated_at不能整体算进展。
持久episode和唯一确定性notice意图，P/I接现有periodic worker与outbox；不新增timer/model/executor，不自动杀进程或重跑。
等待输入/时点/停止确认分开描述；外部提醒只用A原invitation明确授权及次数上界。
真实故障注入只在授权隔离实例，不重启共享预发或关共享Redis。
```

### 给D资源开发者

```text
你负责06-resources.md，message resources/references/reactions模块和测试。
D1先做当前/确切授权引用消息的真实资源读取、类型/大小/数量和有限文本DTO，不信正文URL或文件名授予权限。
D2核实际配置vision路径，真实像素/图形E2E；OCR不能当图像理解，额外vision调用明确计账，不能在tool里隐式突破预算。
D3用可信provider message→Task映射定位引用，当前outer才授权控制；reaction旧命令/致谢零任务/记忆/取消效果。
P/I接模型及stop/continue共享schema，旧snapshot语义保持；原生上传与仅文件通知合同不退化。
```

### 给G记忆开发者

```text
你负责07-memory-evolution.md，Task verification、memory promotion和durable evolution consumer模块及测试。
现在做证据/治理；生产激活等Q确认本批Task/Loop验收。
现有inferred candidate、observed私有记忆、workflow.promote都不等于VerifiedExecution或实际晋级。
独立Host检查真实证据→PG verification→持久Distill意图→现有memory Store→另一Task实际使用；默认不扩大private范围。
共享晋级必须有源owner grant和目标scope/current permission，实际知识对象与receipt原子提交；撤回/reset/forget后不召回也不因旧源复活。
不另建memory后端、不加前台总结模型、不自动修改岗位指令，交完整四段真实闭环。
```

### 给Q独立验收者

```text
你负责08-integration-and-release.md的独立质量门禁、可复用E2E脚本和脱敏证据。
先冻结已经通过的history/Task/steer/stop/file/memory门禁；stop生产实现已验，勿重写。
审计来源/权限/版本/事务/真实退出/provider unknown/3次模型预算；检查多人和多App身份，不只看API200。
每case核用户实际收到什么、PG Task/Run/input/action、Langfuse/SLS实际调用及退出；造failed要真实runner失败而不是agent解释坏命令后succeeded。
登记三跨场域对象、vision/isolated runtime等执行前依赖；真实IM/部署排队，不能干扰别的session。
逐项填写implemented/local/integrated/deployed/e2e，不把库或模型桩当端到端。全部必须项通过才向I签收完整交付。
```

## 3. 首次接手与每次交付字段

在自己的分支保存一份实际填写的handoff记录，交I更新集成板。以下为空值schema，不是可以直接提交的完成报告：

```json
{
  "package_id": "A",
  "subtask_id": "A1",
  "base_sha": "",
  "branch": "",
  "head_sha": "",
  "owned_files": [],
  "changed_files": [],
  "contract_revision": 0,
  "migration_stems": [],
  "reader_capability": "",
  "producer_enabled": false,
  "red_evidence": [],
  "verification_commands": [],
  "verification_exit_codes": [],
  "database_identity_without_secret": "",
  "conflicts_and_resolutions": [],
  "source_task_run_queue_receipt_trace_refs": [],
  "deployment": null,
  "e2e_cases": [],
  "cleaned_test_objects": [],
  "remaining_scope": []
}
```

首次接手至少填package/subtask/base/branch/owned_files/contract_revision；每次提交填head/changed/RED/GREEN/compatibility；发布后deployment填actual source/release/server SHA、pipeline/Runtime provenance；真实case填对象授权、观察时间、实际消息/文件与证明链。不能留空却标对应阶段完成。

智能体更换时：旧owner交已提交HEAD及未提交diff清单，新owner核干净工作树/源合同后接手；别同时保留两个写者。外部智能体不能访问本机tmp凭据或脚本时，I提供脱敏源码/测试材料和官方访问方式，不复制secret到handoff。
