# COL-03 原生取消收集接线

2026-10-04，承接 Plan 18，基线 fbe009fcd2，独立 employee/codex-collection-cancel；只改收集/停止控制与相关 read_task 输出，不动 G5 的 dispatch description 或 TaskBrief。

## 真实失败

`CODEX-CLOSEOUT-20261004-ROOT/col03-cancel-trace.json`：请求人 01:20:54 取消新增客户收集；模型先 read_collection 再 dispatch_task 新建「取消/撤回」后台任务，ACK「这就取消」。收集仍 open，01:22 的迟答使原目标 succeeded 并再次汇总。取消 ACK 先于迟答，不是时间误读。

领域已经具备正确能力：StopDirectTaskTx 在一个事务关闭目标、open waits、所有 active collections、ready intents；无 Run 可以正常停止。有 Run 仍独立检查真实进程退出。缺口是 native 工具合同未说明 waiting collection 的控制方式，read_collection 只有进度，模型转向后台而没有执行取消。

## 实现合同

- 新 `cancel_collection` 仅用于请求人本场域的 active collection Task；与 stop_task 共用 source-bound `task_ref` 与本 wake `read_task` 的 `read_ref`，外层原句、当前 principal、任务 requester、scene/tenant 与 Task version CAS 继续校验。模型判意图，Host 不匹配中文取消句。
- read_task 给出当前收集状态与安全控制说明。read_collection 说明取消须 read_task→cancel_collection，不能派新的后台取消任务。
- 新工具直接复用 StopDirectTaskTx 和 CloseCollectionTx；整体在工具 savepoint 内，包括关闭后 receipt 的回读，任一失败不留取消效果。只能取消有 active collection 的目标，不新增grant、不创建执行/后继 Task/Run。
- ACK 来自已提交 receipt：Task cancelled + collections cancelled，迟答不形成输入/汇总、pending invitation与reminder被原 BeforeSend门阻止、旧 ready/summary commit因revision/Task stop拒绝。已送达提问不宣称撤回；运行中的进程仍只宣称已请求停止，退出必须有真实证据。
- 无新表/新数据格式；新冻结工具表旧worker不认识，故 EmployeeLoop marker升16，router检查同一常量、docs同次更新。loop当前使用精确marker：15/16混版时新受理和旧job恢复均暂缓not_ready，可恢复且不丢；不是不中断承诺。两pod都16后恢复原冻结snapshot/journal，不热改旧工具列表。memory独立累积marker不变。

参考 GawkBot 固定71e82a18的 source task_addressing / Host task_ledger，以及本仓 taskinput.CloseCollectionTx / StopDirectTaskTx成熟事务边界：控制目标与效果归属以可信源与当前读取决定，收集不是新的后台执行。

## 高价值验证

原生模型2轮read_task→cancel_collection，真实隔离PG检查无Run、Task/collection/waits同Tx关闭、迟答无ready/summary、重播零新增generation、权限/无read/staleVersion/非collection拒绝、partial close后取消使已排ready失效。取消与ready并发用独立连接验证；原 activeRun stop/exit屏障定向回归，build/vet。

主代理统一集成/发布，以双pod16开始新的有效验收窗口；原COL-03两人迟答和IM/API/SLS/LF复验才是产品通过。本代理不发IM、不改预发、不push/deploy。

## 本地交付结果

隔离库 `multica_codex_collection_cancel_563` 由本分支 full migrate fresh up 成功建立，未读写其他会话测试库。原生模型、provider均用测试fake，真实PG/工具事务/worker/outbox路径。

- `collection-native`：11 PASS/0 SKIP（open和partial-ready取消、已读source/quote/ref/权限/非collection拒绝）。
- `collection-native-extra`：8 PASS/0 SKIP（无Run场景缺read/staleVersion/改变principal/requester拒绝；独立连接的cancel vs last-answer-ready及ready-first）。
- `collection-native-race`：11 PASS/0 SKIP，覆盖上述等待/迟答/权限/ready竞争。
- `collection-stop-regression`：22 PASS/0 SKIP；真实activeRun stop、replay、真正exit、late结果、before-send、已提交provideraction、单连接、组内响应资格保持。
- `collection-domain`：4 PASS/0 SKIP；partial-close后cancel、late输入、并发最后答复与partial权限/审计边界。
- handler/taskinput/employeetask/server定向vet、handler/server build均退出0。日志在 `_shared/logs/codex-collection-cancel/`。

取消成功native用例恰好2次模型且0 Run，1 Task、1 stop entry；目标/收集/waits取消，receipt带cancelled collection，原job复播0新generation。两人取消后迟答无input/ready意图，不再生成汇总；partial已有ready被superseded，原revision Complete拒绝。无provider外部发送或账户调用。

本地完成；产品验收待主代理批量发布后，在两个live pod均16的有效窗口复跑原COL-03，核对cancel_collection receipt、取消ACK真实送达先于迟答、原Task与collection不复活、reminder/send门禁和无汇总；LF仍可能暴露模型选择错误，不能以fake模型测试宣称预发质量通过。
