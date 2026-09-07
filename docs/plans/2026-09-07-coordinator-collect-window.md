# Coordinator collect 与后台等待分离

状态：修复已部署预发；真实 DWS e2e 因组织权限阻塞，尚未验收完成。分支：`codex/fix-coordinator-collect-window`。

## 事故与目标

2026-09-07 14:04 正式环境，同一知识沉淀请求被创建两次，两个在途事项占满容量。14:02:59 入队的催促窗口持续吸收 14:03–14:09 的消息，直到 14:11:33 才 Decide。Router 提前收到 sync-silence，显示完成但没有回复。

目标：将该事件保留为可重跑 e2e；collect 只合并正在输入的短消息；后台容量不阻塞催促和在场询问；回执反映实际处理进度。仅部署预发，不发布正式，不修改生产 Agent。

## 方案

1. collect 采用 4 秒静默期、12 秒绝对上限。只合并尚未被 claim、没有重试/挂起记录、收集截止未到的同场景同类别 job。过期窗口不会因 pending 而重新开放。移除 worker 吸收所有到期兄弟窗口的行为，保持每个窗口原有消息及回执。
2. 新窗口先 Decide，reply/silence 不等待沙箱容量。只有已判定需要执行但暂不能执行的窗口才走容量排队；继续保留每场景 Coordinator 互斥和最多两个在途事项的限制。
3. collect、busy park 不再立即发送静默完成。合并消息的回执随整个窗口真正处理完再关闭；排队状态不能被报为完成。
4. 新建事项前必须召回当前场景，防止同一请求重复建单。明确催促、重复原请求、在场询问不构成新的执行输入；查状态用只读工具并回复，有实质补充才续任务。
5. 不改变 Runtime/Daemon 协议。ASB 启动延迟作为独立观察项记录，不以扩大任务并发掩盖对话阻塞。

## 验收

- 数据库/worker 回归：4 秒内合并，超 4 秒分窗，12 秒封顶，曾挂起/重试窗口不再吸收；两槽占用时新窗口仍能判断；collect/park 前无完成回执；真实工作排队不丢失。
- Coordinator 回归：新建前召回；重复请求不重复建卡；催促不 issue_comment_add、不产生新任务。
- 预发真实 DWS：固定测试号 Agent，使用主角/配角，记录每条消息实际服务端到达间隔。覆盖忙时“你没干活啊/你说话/你/说话/你好”、同一事项重发、4 秒内分句、4 秒外独立窗口、ACK、第三工作排队后继续。
- 证据：预发部署 SUCCESS 与 SHA、SLS 窗口/决策、Router Receipt、同 cid 钉钉回读、Issue/任务数量。至少三轮主 case；不得以模型自述或 sendStatus 代替回复送达。
- 保留原事故失败样本和修复后结果，更新原 scene-window 合同中不再适用的“忙时静默完成”条款。

## 结果

- 已实现上述收集边界、先判断后限流、回执延后、召回门禁及多项不截断。
- 本地隔离数据库 `multica_collect_942f`：从本机既有结构和 migration bookkeeping 建立，再应用剩余迁移。未修改其它本地或预发库。
- `go test -race ./internal/service/inboundcoord -count=1` 通过。
- Handler 定向 race 回归通过：收集期限、真实 acceptance/job/outbox、过期与 parked 分窗、12 秒上限、两槽在途仍判断新消息、等待不发完成、失败关闭所有回执。
- `go build ./cmd/server`、e2e runner Python 语法和 case JSON 校验通过。
- 扩展 V2 测试暴露既有 `TestHandleAgentDispatchV2CreatesSafeIssueWithoutRequestIdentity` 仍断言 instruction 仅等于 ROUTER CONTEXT；实际已包含 DingTalk Conversation。已用修改前 HEAD 独立 worktree 验证基线同样失败，不在本次改动中修改这条旧合同。
- 全量 `sqlc generate` 被既有 271 号迁移先引用尚未创建的 task_completion_outbox 阻塞；维护本文件既有扫描封装及 SQL 对应改动，并用真实数据库执行回归验证查询。
- 预发真实 DWS 预检：BLOCKED，主角/配角均 `PAT_ORG_POLICY_DENIED` / `chat.message:list`；已请求组织策略放行。runner 在回读不可用时停止，不发送无法验证的测试工作。
- 固定 Agent 有效，404 原因是 `pre-fde` workspace 为浴发空间；已建立独立 `collect-pre-942f` profile 指向草帽星系。无 Agent 配置修改。
- 预发部署于 15:52:17 SUCCESS，平台集成测试 SUCCESS；`/api/config` 与首页均 HTTP 200，测试 Agent 的 Coordinator 开关仍开启。发布源快照 c18d8d0ff，集成提交 626ee98eb（保留同批其它 CR）。
- 三轮真实 IM 验收尚未进行，不能宣称体验已验收。组织管理员放开 chat.message:list 后，按 runner 逐轮执行并核对 Router、SLS、同 cid 回读和任务计数。
- 汇总证据：`docs/evals/results/2026-09-07-coordinator-collect.json`。

- 事故定向回归的红绿对照：原始 `a6f2d8c3e` 上 `TestCoordinatorFreshWindowJudgedAtCapacity` 对“你干了吗？”报 `parked=true` 失败；修复版同一数据库夹具通过。
- 预发 CR `36000322`，run `3107058477`。共享发布分支的失败决策记录与本次全回执关闭发生一处冲突；语义合并后 race 回归和构建通过，发布合并提交 `626ee98eb`，已继续原 run。
