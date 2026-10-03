# F 包：Webhook 可信输入、冻结受理与 Employee 执行

> **执行方式：**F1独立验签/回执测试先做；F2 producer依赖P自动化reader；F3 decision与Task定向事件依赖P2。F不复制B的派发核心。

**目标：**合法外部事件按端点授权触发一次独立工作或推进明确绑定Task；签名、重投、禁用、故障恢复与实际效果全闭环。

## 1. 所有权与已有事实

F拥有拟新增`server/internal/service/employee_webhook_task.go`、`handler/employee_webhook_origin.go`及测试。已有`handler/autopilot_webhook.go`、`webhook_delivery_worker.go`由F在I登记后窄改；公共autopilot和router由I接。

现有公网入口`/api/webhooks/autopilots/{token}`已经有token、可配置签名、body限制、durable delivery及lease。先读真实配置协议，不另造一个公网Webhook系统。

`normalizeWebhookPayload`带receivedAt且worker会重建envelope：新冻结输入必须用原delivery.ReceivedAt/首次归一化结果，不能每次重投取now改变fingerprint。已有body cap256KiB仅是ingress上限，不等于允许全部注入模型。

GawkBot本计划直接参考的是routine及structured events的边界；不宣称当前固定源码提供了相同DingTalk Webhook验签接口。

## 2. F1：来源与去重合同

endpoint绑定可信workspace/agent/tenant、目标scene或企业资源scene、creator/installation权限、执行选择、payload读取字段、发送目标、签名策略/密钥revision。payload内actor/org/scene/mode均是数据，不能覆盖绑定。

- Provider事件ID存在时，身份=endpoint/provider + exact event ID；同ID原始有效payload冲突拒绝。没有ID则使用配置明确的协议身份策略，不仅靠raw body hash吞掉合法同内容新事件。
- 验签对原始字节，不对归一化JSON；保留provider规范的timestamp/skew/replay策略。未要求timestamp的既有签名协议不虚构一个通用过期字段；新增端点策略显式登记。
- 合法signature只证明来源，不证明有读取/执行/共享权限。禁用endpoint阻止新受理；已受理记录冻结来源，使用时权限另核。
- Store持久delivery与selected headers，receipt/source绑定原接收时间，body/secret/link不输出到普通日志。

- [ ] 写合法签名、缺签/错签、过期/重放（协议具备时）、body过大/损坏、token错误、tenant绑定失败。
- [ ] 写同ID同内容重投同receipt；同ID改内容冲突；同资源新ID正常接受；endpoint禁用后不新建任务。
- [ ] 写delivery retry重建envelope不会改变首次receivedAt、selected payload或route。
- [ ] 先RED再实现；命令`go test -race ./internal/handler -run 'TestEmployeeWebhook|TestAutopilotWebhook|TestWebhookDelivery' -count=1`，新前缀TestEmployeeWebhook。

## 3. F2：run_only派发与恢复

沿现有delivery worker租约，调用P认可的自动化origin constructor与in-tx adapter。冻结webhook输入、receipt、真实已有AutopilotRun绑定（若走AP入口）、Task/Run/queue和发送意图；不得套一个假的schedule planned_at。

执行为确定性run_only时零foreground LLM，Compiler只取端点允许字段和规则。纯外部资源事件无会话时按enterprise定位；若endpoint显式绑定现有scene须当前目录/租户授权，不能从payload挑场域。

提交后notify；delivery/Run/queue状态与实际receipt对账。重投同source恢复旧输出，不再次执行。唯一通知owner沿真实来源选定，不能同时routine、message Run notice和Webhook callback各发一次。

- [ ] 双delivery worker、lease过期、commit-before-notify、PG失败rollback、runtime启动失败、撤权及绑定被删除的PG回归。
- [ ] 明确response状态只是受理/拒绝/冲突，既有HTTP协议不随意改状态码；任务已排队不能返回“业务已成功”。
- [ ] P reader全副本就绪后开启producer，ordinary Webhook与Cron/source privacy一起回归。

## 4. F3：decision与Task定向事件

employee_decide以新的typed wake理解获准字段，每wake≤3真实请求。普通Webhook无绑定Task时建自己的工作；只有endpoint配置保存了具体Task authority或已授权关联，才能推进既有Task。

Task定向事件还需绑定允许事件类型、resource ID、当前goal/plan revision、evidence边界；新增外部actor不得变成Task requester。未匹配条件记事实/quiet而不自动召回所有旧工作。与P的execution follow-up步数预算共享，不能靠新事件绕过同目标控制fence。

- [ ] 写decision quiet/dispatch/await、payload伪造actor与scene无效果、旧Task取消/撤权不复活、source重投唯一wake。
- [ ] 模型请求保存准确payload字段与工具schema，无额外分类/润色服务；完整usage与源trace关联。

## 5. 真实验收

HOOK-01：在隔离授权端点，用合法签名触发打印唯一标记的真实任务，核输出。HOOK-02：同ID重投零新Task/Run；同ID改内容冲突；新ID正常执行。HOOK-03：错签/过期（协议有该限制）/伪造场域拒绝；禁用后无新执行。HOOK-04：decision条件false→quiet，true→授权动作；定向Task引用正确，不能更改其他Task。

交付完整endpoint/delivery/receipt/APRun（若存在）/Task/Run/queue/action/trace链。实际结果和source证明均通过后才关闭F。测试secret只放0600本地文件/授权环境，文档不保存token URL。
