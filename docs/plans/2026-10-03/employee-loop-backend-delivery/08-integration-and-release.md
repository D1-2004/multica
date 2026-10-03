# I/Q 包：接线、独立验收、预发与完整交付

> **执行方式：**I持续集成各包，Q独立核验。只有I安排共享部署和真实IM时段；每次交付仍派同步智能体检查目标分支。Q不重写已完成stop生产代码。

**目标：**把领域/reader/producer分片交付为已部署、已真实验证的完整后端，保留可重放的证据和明确回退步骤。

先满足 [Step 0](00-step-0-environment.md) 对相应测试层级的准入，按 [最终交付标准](10-delivery-standard.md) 判断业务结果。本手册的case/命令/波次是参考验证路径；主代理可调整实现与每轮范围，但不可省略真实效果、权限/恢复证据或把pending当通过。

## 1. 所有权和接线原则

I持有P共享列表、工具schema、job reader、route、sqlc、migration分配、replica marker、SOURCE_MAP和当前合同更新。Q拥有拟新增`scripts/employee-e2e/`剧本/只读证据校验、发布manifest及自己的独立测试文件；具体脚本名前先查仓库同类实现。

每包的adapter接口先提交，让依赖方基于合入的合同开发；不要让多个开发者同时改Host switch、worker.Run、autopilot dispatch和通知选择。领域包禁止外部I/O留在数据库锁内，I核工具journal事务、savepoint、单连接池和提交后通知。

**新工具批次预检：**新副作用+互斥终态不能先执行一半再报错；被接受部分保留真实receipt，后续失败不抹去已提交效果。read_ref和当前权限校验仍在每个新工具/缓存恢复入口，native call/result必须配对。

## 2. 开工登记与集成板

每个子项状态只从下列枚举推进：planned→assigned→contract_ready→implemented→locally_verified→integrated→deployed→e2e_verified。blocked须写具体缺项，例如三actor名单、视觉后端未配置，而不是泛称环境不好。

I维护包ID、owner、base/head、文件所有权、接口revision、migration stems、gate/capability、依赖、验证日志、release/actual server SHA、E2E ID、未完成项。这个登记是协作材料，不另造产品Task库。

每次合入：

1. 读取开发者diff及RED/GREEN/权限故障证据；检查新查询workspace过滤、来源链、数据类型、旧snapshot兼容。
2. 独立审核关键事务/控制/发送，修复阻断后重跑对应回归。Q复审来自其他模型也要以代码/事实核对，不能只采纳其结论。
3. 原子commit，用heredoc真实多行body；返回`git log -1 --pretty=%B`。
4. 同步智能体fetch最新origin/feat/tag-multitenant，rebase拥有的提交，保留其他session变更，无force、无本次额外merge。冲突按合同解决，不stash掉merge元数据。
5. 集成后的新变化影响到哪个包就跑哪个包，不因docs-only提交重复全量或部署。所有检查失败都准确记录，不改测试期待掩盖行为。

## 3. 测试层次与准确命令

独立库开启后，在server目录执行；不能在没有DB配置的skip结果上声明PG通过。下面是命令结构，真实DATABASE_URL由当前开发者的独立本地库提供，不能指向预发/正式。

```bash
go test -race ./internal/employeetask -count=1
go test -race ./internal/employeeentry ./internal/eventrouter ./internal/service/employeeloop -count=1
go test -race -p 1 ./internal/service -run 'TestDirect|TestEmployeeTaskSteer|TestStopDirect|TestEmployeeRoutine|TestEmployeeWebhook|TestEmployeeWatchdog' -count=1
go test -race ./internal/handler -run 'TestEmployee|TestSceneRoutine|TestAutopilotWebhook|TestWebhookDelivery' -count=1
go build ./cmd/server
go vet ./internal/employeetask ./internal/employeeentry ./internal/service/employeeloop
git diff --check
```

新包出现后加`go test -race ./internal/taskinput -count=1`。memory包真实数据库启用`EMPLOYEE_MEMORY_TEST_DATABASE_URL`后跑`go test -race ./internal/service/employeememory ./internal/employeelearning -count=1`。service真实Redis测试需要专用`REDIS_TEST_URL`；memory与handler使用各自fixture要求，不假设一个环境变量能开启全部测试。

某测试场景用到了REDIS flush/DDL trigger时串行跑；不同handler/service包不能共用同一测试DB并行。并发语义用测试内独立PG连接/事务模拟。fixture不可伪造未受理Job、source timestamp或current config，只构造真实service入场路径。

涉及inboundcoord/assoc共享修改时从仓库根跑`python3 scripts/check-coordinator-policy.py`；PASS_STRUCTURAL_ONLY不能当行为证明。前端/API schema修改再跑相应pnpm检查；已有ja/ko baseline locale缺键不是本包自动修复范围，必须区分现有失败与新回归。

Q至少覆盖：CAS竞态、同源内容冲突、commit-before-notify、工具journal写失败、poolMaxConns=1、取消退出屏障、provider unknown、模型20/45秒截止/3次累计、旧快照恢复、registry不可用但已有cache恢复、全副本门禁。

## 4. 预发发布步骤

1. 检查目标最新提交、在线配置、当前pipeline状态及发布队列，登记本次切片和功能开关。
2. 读取aone-deploy技能和当前a1 CLI命令，intranet命令先unset六个代理变量。当前已验证查询/触发链路：

```bash
a1 cd-pipeline run get --latest --pipeline-id 66 --app 342160 --format json
a1 cd-pipeline run 66 --app 342160 --cr-id 36355253 --format json
```

CR `36355253`是本会话已用线索；触发前确认它仍对应目标与本切片，失效则按`make deploy`/技能解析现有合法CR，不能盲跑旧CR。等待build/deploy/integration全部SUCCESS，记录实际source/release/server SHA；最后人工预发验证gate不等于生产发布。

3. 核所有live replica能力和新reader/schema；不从单pod推断。Reader部署完再启producer，切片开关的scope精确workspace/agent/org。
4. 当前Tag必须读回EmployeeLoop/sharedmodel配置。不能拿另一个Tag后台或全局默认profile验证。
5. 若协议/Daemon/runtime变化：提交内部runtime仓库触发远端build，记录repo SHA/CI/image digest/Daemon pin/template；禁止本地镜像build。按fc-runtime-dev-loop做候选FC与持久本地设备滚动兼容，不能只靠FC一次任务称旧客户端兼容。
6. 预发migration随Aone打包runner执行。若涉及schema-breaking上游同步才按deploymentfence技能流程；业务增量不自行操作frozen，也不直接改数据库。
7. 由Q安排真实IM；失败则修复→commit→同步→部署→原失败剧本复测，再跑受影响门禁。新提交/部署不能覆盖既有失败证据。

本计划完整交付目标为预发可验收后端和正式发布材料。正式发布动作依用户后续明确环境/范围执行，不将pipeline66结果冒称正式发布。

## 5. 真实E2E与基线门禁

| ID | 操作与必须证据 | 通过条件 |
| --- | --- | --- |
| BASE-HISTORY | 旧紫→重设蓝绿→后者→单项更正→纯JSON | 保留旧冲突历史；actual用户收到正确值；零Task/learning副作用 |
| BASE-TASK | 实际Python等待/计算→询问→续接→致谢 | 同Task、必要新Run、真实工具输出，致谢零派发 |
| BASE-STEER | 长任务苹果3/香蕉2→苹果5/无香蕉 | actual FC/daemon exit proof后后继claim；旧结果抑制 |
| BASE-STOP | 长任务→不要停止→停止→查询真实退出→致谢 | stopping和stopped准确；无后继/旧结果；运行-纠正-停后继的前驱也等待退出 |
| BASE-FILE | 仅文件→同Task继续加一行 | 两次实际下载和hash/字节，新Run自己的回执，两次无额外总结 |
| BASE-MEMORY | 记住→纠正→DM/group隔离→忘记→再问 | 精确来源、墓碑、无旧值复活、零Task |
| COL-01/02/03 | 三人乱序、同人两Task、取消后迟答 | 输入归属/计数、隐私和一次原场域汇总 |
| CRON-01/02/03/04 | actual due、改指令、暂停恢复、decision | occurrence身份/工作包/调用数/通知owner准确 |
| HOOK-01/02/03/04 | 合法/重复/冲突/错签/禁用/decision | 可信绑定、真实一次效果、未获准payload无扩权 |
| WD-01/02/03/04 | 无进展、系统活动、真活动恢复、等待提醒 | once episode/action、诚实状态、无新Run |
| RES-01/02/03 | 隐藏附件码、像素和图形、错误资源 | 实际内容和provider request，不猜或越权 |
| REF-01/REA-01 | 引用续接/停止、旧命令reaction | 可信定位、当前外层授权、真退出、轻反馈无副作用 |
| FOLLOW-01 | 明确预授权多步任务终态触发后续 | 同Task、唯一推进、旧revision/取消不续行；达到约定步数/预算上限后正确收束 |
| MEM-01/02/03/04 | verification→distill→新Task使用→晋级撤回 | 真实proof、manifest、作用域授权、撤回后不召回 |
| FAIL-01 | 专用测试Runtime实际runner失败 | queue真实failed/Run failed/事实Event和准确通知一次；不能以坏shell被agent解释后succeeded冒充 |
| ROLL-01 | 新reader/旧reader窗口、关闭producer/恢复 | 无新源误处理、旧job可完成、新job不丢、不降级sharedrevision |

冬翔/Qwen基础DM与测试群已授权。新外联名单在发送前由Q明确登记；不要随机挑真实同事。失败注入和服务/Redis重启只在授权隔离实例，不能干扰共享预发。E2E唯一case marker + source idempotency；自然聊天复述同句和同source技术重投是两种不同测试。

每case核三组：用户实际消息/文件；PG Task/Run/wait/input/action事实；Langfuse/SLS实际generation/tool/event/exit proof。Observe跨过旧任务原定结束时刻再断言停止后无旧输出；长任务可控但不把没看到消息当唯一proof。

## 6. 已踩过的坑与必留反例

| 真实坑 | 应保留的反例 / 当前修复边界 |
| --- | --- |
| Employee绕过Coordinator主模型配置，45秒耗尽 | 新wake冻结shared plan/profile，20秒/45秒/3次实际请求；Trace根ERROR不能因错误回复入队变成功 |
| SDK/多层route隐式重试、隐藏总结 | actual请求与journal/Langfuse一致；replay零generation；所有额外视觉/后台调用明确记账 |
| 单JSON历史配时序提示仍读旧紫色 | 保留v14/v16失败；native user/assistant v18才过，不换人或清历史躲冲突 |
| forgotten memory借history复活 | exact evidence撤销与关联reply过滤、audit保留；不按value全场域擦除 |
| callback URL复用导致错会话历史 | URL+确切同步RequestID同时匹配，入队/模型outcome不当已送达 |
| legacy provider route误当非法Employee来源 | route与owner分层；合法legacy/legacy来源完整核验，unknown/unmapped仍拒 |
| terminal事实误判后永久skip | 单调proof_version/兼容marker，暂时故障等待；补事实不重执行、不重复通知 |
| queue context可伪造原来源 | receipt→consumption/job→committed tool journal→run_started→Run/queue逐链核对 |
| 旧Run结果/旧文件满足新Run交付 | actual effect source与original delivery root分开，当前Run的新回执必需 |
| 文件已发仍补长总结、执行器先发final再Host再发 | native file proof + inherited静音；final_text_owner端到端SDK守卫，失败/未知保留原输出 |
| 接单ACK先说沙箱已开始 | 无claim/start事实只能说已受理/已安排；不得预测未来交付或附加多余承诺 |
| stop.cancelled被当退出、queued后继遮住老writer | FC两次扫描/认证ACK及前驱链；completed本身也不是process exit proof |
| pool1死锁、持锁外部connector | Tx方法不再借pool；外部I/O前准备、提交前重验、savepoint回滚 |
| 长纠正仅取最后20条导致旧约束丢失 | 完整当前纠正独立读取，100条/64KiB上限显式拒绝；PG审计不删 |
| 群无addressed source也能控制 | current外层@/真实proactive配置门禁，fixture不伪造event enabled |
| 群privatebrief旁列无关记忆 | group不自动注入private；lookup按需，混合/unknown requester不聚合 |
| observer工具名单40条截断误认MCP缺失 | 带数量/truncated标记，实际工具执行与装配另验；trace64KiB同理 |
| Host/runtime AuthCode不同源，native上传报身份缺失 | 成对注入原生凭据、检查success:false；不补别人的userId或依赖另一来源code |
| 闭源/商业语境误称Gawk许可证通用开源 | 固定SHA与SOURCE_MAP，复制部分保留实际Sustainable Use License |
| 全局profile被其他session改成正式 | 显式pre profile+host assertion，业务API token不当log/fence operator token |
| 新配置冻结后replay改任务内容 | 已接受来源/plan/工作包immutable，凭据运行时重验；旧journal字节不热改 |
| 目标分支和发布分支不等于实际上线 | source/head/release/server SHA与pipeline各记；test-only无需deploy |
| 常驻routine/worker各自notify重复结果 | 唯一notice owner，unknown只对账；其余consumer不制造第二次IM |
| 同人在多场域/不同App ID被当多个或同一个人 | canonical identity bridge + invite refs；displayname/@/newestTask不授予输入归属 |

## 7. 回退、运维与最后交付

回退顺序：关闭新producer→记录已接受新job/run/wait/intent数→保留兼容reader让已接受工作结束或安全held→确认旧版本能读取剩余数据再部署旧兼容版本。切换Coordinator/Employee设置只影响新owner，不能搬迁已接受Task/记忆，不能停止reader后丢掉内部wake。

需要交付健康指标：pending/held及reason数、最老job/intent年龄、wake重复冲突、实际模型请求/timeout、activeRun/stopPending、waiting counts、outbox unknown/failed/送达延迟、episode数、verification失败/Distill backlog。指标不含正文/credential；pg事实与trace IDs可关联。

最终材料：所有包manifest、接口/source map、migration与rollback说明、Runtime provenance/rolling矩阵（触及时）、每个必需case证据、线上定位runbook、未承诺能力清单、测试对象清理记录。Q确认表中必要项均e2e_verified，I才能宣布“完整后端交付”。H5登录/全部FE页面及跨租户/多Agent若未纳入承诺则注明独立范围，不能借其未完成抹掉已验后端，也不能声称验过。
