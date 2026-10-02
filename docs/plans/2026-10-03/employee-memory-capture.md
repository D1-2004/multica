# Employee 私有记忆写入与纠正 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development to implement the tasks below with independent review. Existing user authorization covers implementation and pre-release E2E.

**Goal:** 打通“明确记住 → 下一轮召回 → 纠正 → 只召回新值 → 定向忘记”，不创建后台 Task，不扩大到其他场域或请求者。

**Architecture:** 复用 GawkBot 固定提交 `71e82a1809565281cbd0bf8185d3c125b715d934` 已移植的 `employeememory.RecordTx/Search`、同 key 的 supersedes、衰减和 reset fence。Employee Host 从冻结 source 构造 scene.Ref、RequesterRef 与 evidence，模型仅提出键、类型和外层原文引用。工具与学习写入加入同一 journal 事务，前台仍最多三个真实模型请求。

**Tech Stack:** Go、pgx/PostgreSQL、现有 employeeloop / employeememory / employeeentry、Langfuse、DWS IM。

## 已通过的前置门槛

Task/EmployeeLoop 基础闭环证据见 `employee-loop-parity-acceptance.md` 的 06:10 结论：实际文件下载、纯 Direct、MCP 读写与恢复、首轮回复、Host 唯一最终文字发送、失败回报、通知抑制日志。

## 1. Host 记忆工具与证据

文件：新增 `server/internal/handler/employee_memory_tools.go`、对应 `_test.go`；修改 `employee_scene_entry_host.go`。

- [ ] 先写真实 handler/PG 失败回归：原话写入、下一次 worker/provider 输入出现探针，且没有新增 Task/Run。
- [ ] 注册 `memory_capture`（Effect，无独立终态）、`memory_lookup`（只读）、`memory_forget`（Effect，无独立终态）。沿用普通工具结果后的 reply，不添加审核或提炼模型。
- [ ] 工具参数不包含 actor、scope、trust、confidence、TaskID、ExecutionID。`memory_capture` 只接收 source_ref、key、type、quote；quote 必须是选定 source 的外层 Text 原文，不能来自 Reaction、ReferencedMessage.Text 或任务/工具结果；引用回复的外层纠正仍可保存。类型限现有 LearningType，key 遵循现有规则。
- [ ] Host 写入使用以下固定语义，实际变量必须从已校验的 job/source 得到：

```go
record := employeememory.LearningRecord{
    Type: kind, Key: key, Insight: quote,
    Source: employeememory.LearningSourceObserved, Confidence: 4,
}
evidence := employeememory.TrustedEvidence{
    SourceID: "employee-message:" + source.ReceiptID,
    EvidenceID: source.Message.OpenMsgID,
    ActorID: source.RequesterRef,
    OccurredAt: receiptCreatedAt,
    HumanStated: false, VerifiedExecution: false,
}
record, err := memory.RecordTx(ctx, tx, scope, record, evidence)
```

- [ ] `scope` 固定 `ScopePrivate`，principal 为 source.RequesterRef，scene 只用 job 的 scene.Ref。入事务后重验 scene/tenant、当前 principal 权限与 source 消费绑定。时间取对应 `scene_event_receipt.CreatedAt`；不使用重试/写入时间。
- [ ] 一条原始消息只有一个稳定 learning evidence；不能将模型的 key/type 拼入 replay identity。重复调用返回原结果，换 key/type/quote 也不能绕过既有 evidence 或 reset。同事务返回实际保存记录及 active/forgotten/superseded 状态；墓碑重放不得声称重新记住，不得用提案冒充保存结果。
- [ ] 现有协议无法证明真人或语义原创，明确保持 `Trusted=false`。仅保存账户归属的观察记录，不调用 HumanStated、VerifiedExecution、Distill 或 promotion。

## 2. 读取、纠正与定向忘记

文件：复用/最小扩展 `server/internal/service/employeememory/store.go`，新增 `private_entry.go` 与对应测试；更新 `brief.go` 仅在模型需要定位记录时展示 id/type。

- [ ] 同 type/key 的新消息形成 supersedes，旧值退出 Search/Brief；非可信记录不能覆盖可信记录。明确写入使用专用 RecordPrivateObservationTx 顺序策略，不改变后台学习 RecordTx。按 Host 首次 receipt 时间排序；晚到的旧 source 保存为 superseded 墓碑，不能覆盖新纠正，换 key/type 重放也不能复活。相同时间的不同 source 保留当前记录。比较时包含同 key 的 forgotten/superseded 墓碑，避免“新值忘记后旧 source 首次晚执行”复活；旧记录缺少证据时间时采用明确且经回归的保守规则。
- [ ] 为 journal 事务提供 SearchTx，提取并复用现有授权、匹配、排序逻辑，不在持有连接时再从同一 pool 取连接。
- [ ] lookup 使用短字面关键词、最多 8 条；private 查询要求当前窗口为同一已知 requester，混合/未知身份窗口不开放私人内容。全部记忆仍作为 user-role 的背景数据，不成为 system 权限或指令。
- [ ] 定向 forget 使用 record_id 和 source_ref，在同一 private namespace 中将精确记录标记 forgotten，保留 replay tombstone；只在实际变化时推进 revision。不提供跨 scope 参数，不以 reset 清空测试场域旧记忆。
- [ ] effect 返回真实 durable receipt；已忘记/重放返回明确的实际状态，不虚构新写入。相同 NativeToolCallID 的 journal 缓存重放也必须重新验证当前授权/状态，不重做副作用，不把历史 active receipt 冒充当前状态。忘记不得删除其他人的记录或并发产生的新记录。

## 3. 回归与发布

文件：更新 `docs/employee-loop.md`、memory/loop SOURCE_MAP 与必要的 replica marker/fixtures；新工具需滚动版本门禁。

- [ ] 真 PG 回归覆盖：两轮记住/纠正及下一轮模型输入、源重投、换 key 的同 evidence 重放、缺失/伪造 source、跨 requester/scene/org、混合窗口不读取私有内容、可信记录保护、reset 后旧 evidence 不复活、定向忘记、工作区删除竞争。
- [ ] 验证不增加隐式 LLM：capture→reply 两次、已有 brief 直接回答一次；lookup→forget→reply 不超过三次。单请求不能产生后台 Task。
- [ ] 验证同 journal 事务和 pool 单连接场景，禁止嵌套取连接造成死锁。

```sh
cd server
DATABASE_URL='postgres://mac-m3@127.0.0.1:55462/employee_memory_capture_test_20261003?sslmode=disable' /private/tmp/go-sdk/go/bin/go test -race ./internal/handler -run 'TestEmployeeMemory' -count=1
/private/tmp/go-sdk/go/bin/go test -race ./internal/service/employeememory ./internal/service/employeeloop -count=1
/private/tmp/go-sdk/go/bin/go build ./cmd/server
```

- [ ] 独立复审 → 提交 → 子代理同步最新 feat/tag-multitenant → 预发 → 实际 IM。
- [ ] 冬翔→Qwen 单聊写入一个随机 ASCII 偏好值，下一轮不提供答案的问句召回；另发纠正，再问只得新值；各种TAG群询问同一键应不返回单聊值。最后只忘记本次记录并再问确认移除。
- [ ] 以对应 employee_loop generation 的 Host memory 段与存储证据证明召回，不能把当前消息或历史复述算通过。保存 case、消息、job/record ID、调用次数与回读结果，不保存凭据。

Execution Event、Cron、Webhook 继续使用既有事件 admission、scheduler 与 webhook_delivery；此增量不创建第二套调度器，也不提前宣称通用唤醒、verified distill、workflow promotion 或完整恢复完成。

## 实现与本地验证（发布前）

实现已完成，独立复审 APPROVE。补齐同 NativeToolCallID 状态重验、遗忘墓碑时间栅栏、重复 source_ref 拒绝、正常 lookup/forget 的冻结结果恢复，以及状态变化后的有限失败收束。工具 trace 区分已提交的业务失败与事务回滚；marker 为 5，旧 job 工具 schema 不变。

真实 PostgreSQL race：作者 handler 相关集合 24.852s，root 独立数据库 24.166s；employeememory、employeeloop、employeeentry 通过。server build、相关 go vet、diff-check 通过。Coordinator policy 检查为 PASS_STRUCTURAL_ONLY，不代替行为验收。预发与下面 7 条 IM 剧本尚待执行，不宣称线上记忆闭环已验收。

## 预发真实 IM 第一轮（07:11—07:20）

服务器 `0a367d4f91fbf78e237307facd34bd35413514ad`、预发流水线 `3110328443` 的构建/部署/集成测试成功，两个在线副本均为 marker 5。冬翔 → Qwen-DWS 单聊与各种TAG群共 7 条真实 IM 的记忆机制通过；后台任务数 69 → 69，无新增 Task。

| 步骤 | Employee job | 模型调用 | 实际回复消息 |
|---|---|---:|---|
| 写入 | `ba7793d7-9e77-492c-ae18-af0ed4e6400d` | 2 | `msgLoHPA/Fs7zgpbE0LJQ0qMg==` |
| 下一轮召回 | `3981f6ae-18b8-4405-8b49-14a2ee7c4ad4` | 1 | `msguPM5hKrEVF/wFNTlthgbJQ==` |
| 纠正 | `3262ee85-a01a-48cb-8526-1cc29cacbb6e` | 2 | `msgzyz0oIbP9Ra2Ft/sd7SVZg==` |
| 纠正后召回 | `6d1f0c3b-1b87-4f5d-90fc-74e4b954b482` | 1 | `msgbfmMpyxiHoobEZnLS/P9TA==` |
| 群聊隔离 | `53a6e6a1-9f0b-4772-8430-2a1542d448fb` | 2 | `msg8e0rbtzqi5N2w/Btv9fEWw==` |
| 定向忘记 | `90d2e4ff-a4c5-4028-9944-0fd6a9a961e1` | 2 | `msgZPJO0Yyff6USBKcGRK9aVQ==` |
| 遗忘后查询 | `62a40264-c174-4150-ae79-d4708f310fe1` | 2 | `msgqHabr/UKKNOhtxCf/HROQg==` |

写入记录 `ae3feafd-9073-4436-accf-08d96bd30ae1`，纠正记录 `5766fe46-f481-4e1e-ad70-8475b1369695` 的 supersedes 指向前者，两次 capture 与 forget 的 Langfuse tool 均 `journal_committed=true`。纠正后的下一轮 memory snapshot 只有新值，当前问句没有答案；群里的 snapshot 新旧值均不存在且 lookup 为空。定向忘记后 snapshot 不再出现探针或新旧值，lookup 为空。原有其他记忆仍保留；未使用 reset。

表达验收仍有缺口：精确值回复多加解释；空结果列举无关记忆并主动建议查询其他群；忘记确认复述被忘值并输出内部 forgotten 状态。机制通过不代表表达通过。小补丁仅在新 input snapshot 冻结输出约束及工具说明，保持历史 journal、权限与三轮预算；部署后另跑表达回归。

## 表达复测（07:36—07:43）

小补丁目标 `166b3381a13862f54e6cf369de52f4fbc0a4686e` 经 `3110328677` 构建、部署、集成测试通过。实际 generation 确认 `MEMORY REPLIES` 与新的工具描述已生效。

- 单聊只输出值：job `6d08253f-8d1e-47f4-8d45-5f176bce9942`，一个 generation，消息 `msgrINwWLXRfLI420gafeQnfw==` 仅返回代号；07:37:57→07:38:01。
- 定向忘记：job `4c7784ec-9c75-4a7c-83e1-f1574821c85b`，两次 generation，只忘记 `420aa421-0301-499e-ba12-8dbc0c525b9a`。消息 `msgWNNYxIscZNoD5ORzwrgaSQ==` 自然确认，不再重复值或内部 state。
- 忘记后：job `34d3ae70-a569-4752-9650-9e5a624e5cb7`，两次 generation，有效 snapshot 无探针，lookup 为空；消息 `msg9PW1ruisYtT54WFORMX2xA==` 不再旁列其他记忆。任务仍为 69 条，无新增。
- 尚未通过：群聊 job `0f88cb06-45a8-47d8-a58c-8a964c7e4f18` 虽然 lookup 为空，仍列举旧 EL2/6/7/8。两轮请求均确实带有新规则；不能解释为部署未生效。

根因接缝：新群聊 snapshot 无条件取 `Brief(query="")` 的最近四条 requester-private 记录，工具空结果并没有移除初始背景。Gawk 原设计有基于当前 notification 的检索；直接接全文 query 又会被 @mention 等非业务文本干扰，本次不扩大排名器。下一窄改按可信 scene.Kind：DM 保留私有 brief 首轮召回，group 不自动注入私有背景，当前问题通过既有 memory_lookup 按需查询；shared 层和旧冻结快照不变。必须再验证群里仍能写入、按需召回、三轮内忘记及未知项不旁列。
