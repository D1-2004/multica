# FDE 教练线上场域记忆重试排查

检查日期：2026-09-07，以下时间均为 Asia/Shanghai（UTC+8）。

- 对象：[FDE 教练记忆页](https://fde-workbench.dingtalk.com/fde-coach-dta-20260720/agents/a9ce26da-e5fd-4c16-86ef-7fa0f46386bc?view=memory)
- Workspace：`3b46372c-e8ae-4030-a280-8fef45041d13`
- Agent：`a9ce26da-e5fd-4c16-86ef-7fa0f46386bc`
- 分支：`codex/investigate-prod-scene-memory-evidence`
- 本地核对基线：最新 `origin/develop`，`7180b0607`。最初 checkout 停留在 9 月 3 日，已 fetch 并 rebase 到该基线。线上 `/api/config` 未提供构建版本，本次没有确认生产二进制的精确 SHA。

## 结论

目标 Agent 的 48 条记忆中，45 条为 `clean`，3 条为 `retrying`。写入、召回、预热和列表开关均开启；浏览器与 API 一致显示截图中的错误。

PoC 群与黑客松群存在历史翻页续读和目标证据校验之间的进度死锁。被报为不可见的两条消息实际存在，教练账号按 ID 查询均成功。续读上界却早于目标消息，Worker 一直向更早处翻页，无法在本轮结果里找到目标；失败重试又保留同一上界，重复执行不能恢复。

口香糖小队是另一类历史读取失败；以教练账号复查得到 `CrossOrgPermissionDenied`。本次没有修改线上数据、权限、开关或部署。

## 线上状态与证据

API 与数据库快照采集于约 12:40–12:50；尝试次数取同次排查的 SLS/数据库快照，不代表完成报告时的实时值。

| 场域 | 记忆版本 | dirty / flushed revision | 尝试次数 | 状态 |
| --- | --- | --- | --- | --- |
| PoC主链路Demo筹备群 | 35 | 34 / 14 | 841 | `INCOMPLETE` |
| 黑客松项目群 | 31 | 193 / 80 | 784 | `INCOMPLETE` |
| 口香糖小队 | 3 | 6 / 3 | 276 | DWS history query failed |

数据库使用 `default_transaction_read_only=on` 和 10 秒语句超时，查询限定到目标 workspace、agent。未读取消息正文进入报告。

| 场域 | 已处理游标 | 保存的续读上界 | 待覆盖的目标消息 |
| --- | --- | --- | --- |
| PoC 群 | 09-03 23:25:44 | 09-04 09:19:50 | 09-04 15:36:23 |
| 黑客松群 | 09-04 15:24:01 | 09-04 15:28:46 | 09-04 17:44:46 |

对应标识：

| 场域 | Scene Memory ID | Conversation ID | 目标 Evidence ID |
| --- | --- | --- | --- |
| PoC 群 | `b6db5cf9-f19a-433a-8a3c-3293164cb89c` | `cidFFCQyD4+r2+kQvHl4ZEkZA==` | `msgmRs1yXiCjqPlHfvaSI0RWw==` |
| 黑客松群 | `6b1605fb-3672-4644-9b98-0c4b01e11c10` | `cidrXpFoPDQR+DIbpM/ABQKcQ==` | `msg7TGKd5zz6WwgaKreR6IGRg==` |
| 口香糖小队 | `d7df3103-6077-4fbe-a786-170b820f6919` | `cid90706kmBsKjgIl5HaD0d5w==` | `msgAmUHhhgmXNOJhfYle7751A==` |

通过 `dws-env as 教练` 使用账号 `钉钉/菲迪-FDE教练` 查询生产 DWS：

1. `chat +messages-mget` 查询 PoC/黑客松两个目标 ID，返回 `complete=true`、`foundCount=2`、`failedCount=0`，时间与数据库完全一致。
2. 按 Worker 保存的上界执行 `chat message list --direction older --limit 30`，两群各返回 30 条。PoC 最新一条是 09-04 09:19:46，黑客松最新一条是 09-04 15:28:37，均早于目标消息。继续向前翻页也无法读到更晚的目标。
3. 口香糖小队的 `chat +chat-messages` 返回 `CrossOrgPermissionDenied`，trace：`0b0fd96a17887563495364204e058b`。这是同一教练账号的本地 CLI 复查；生产 Worker 的错误正文被截断，没有完整 server code，尚未将其单次请求与该 trace 对齐。

DWS 查询结束后，已恢复本机原来的预发 MCP/terminal 环境。

## 为什么会永久重试

最新 develop 仍保留以下路径：

1. `server/internal/service/scenememory/dws.go`：达到分页/读取预算且尚未覆盖旧游标时，返回 `HistoryGapError`。
2. `flush.go`：将 gap 的最老时间写入 `history_resume_before`，本轮退出；已读到的新页没有作为后续调用的历史证据保留。
3. `historyStartBefore`：下次优先用保存的 `history_resume_before` 向旧消息翻页。
4. `planFlush`：要求本次返回结果包含 `lease_target_through_evidence_id`，或已处理游标已经覆盖目标。此时两项都不满足。`missingEvidenceBehindWindow` 只处理目标在本窗口之前的情况，对这次目标位于窗口之后的情况无效。
5. `RetrySceneMemoryClaim`：清除 lease、设置下次时间，但不改变 `history_resume_before`、已处理游标或待覆盖目标。下一次仍进入同一失败条件。

因此，错误中的 “not visible yet” 在这里不是短暂同步延迟，也不是目标消息已删除。

## 另一个已观察到的进度问题

正式 SLS 中，PoC 群在 10:55–11:07 多次提交 `event_count=24`、`caught_up=false`，游标始终为 `2026-09-03T15:25:44Z`，记忆版本始终为 35。黑客松群也出现固定游标的 24 条提交。

这证明曾反复消费旧批次，没有追平待处理范围。旧代码的 `includePendingWindow` 会把游标之前的窗口重新加入，符合这一症状。最新 develop 已将该函数改为不重新加入旧窗口；但上面的续读上界死锁仍可在最新基线上复现，不能把已有修复视为本次问题已解决。

页面 `last_flushed_at` 表示最近一次批次提交：PoC 为 09-07 11:07:56，黑客松为 10:52:41。这些时间不能解释为已更新到当时全部消息；实际覆盖范围仍以 `source_cursor_at` 和 `dirty/flushed revision` 为准。

SLS 查询使用正式 tag `acni_ag_dt-fde-multica_default_host`，project `dt-fde-multica-sls`，logstore `application-log`；未混入预发日志。

## 本地验证与修复边界

用生产只读数据库快照及两个群各 30 条真实历史返回，在最新 develop 上运行临时 Go 诊断测试 `TestInvestigationResumeWindowExcludesClaimedEvidence`。两组均重复得到 `claimed evidence is not visible yet`，证实最新代码仍存在该路径。此测试验证的是故障复现，不是修复通过；未调用 LLM、未写生产数据库。临时测试已移出仓库，本分支仅保留本报告。

后续修复需要先设计历史补读的覆盖状态，再修改 Worker：让“正在补旧窗口”和“已看到本次目标证据”能够跨批次协调，同时保持游标前进、迟到消息不丢、尚未覆盖目标时不误标 `clean`。应覆盖超过分页上限、保存续读位置后进程重启、迟到消息和 claim 期间新入站等情况。

单纯重试不会改变这两个场域的读取范围。清空记忆会删除已有知识；只清续读上界可能再次触发分页上限；直接忽略证据检查又可能错误宣称处理完成。本次未执行这些变更。

口香糖小队需要进一步核对生产 Worker 的有效跨组织授权，并让历史读取错误保留结构化 server code/trace，以便将权限问题与可重试的网络问题分别处理。

## 后续处理

2026-09-07 已实现正向分页与持久化进度，提交 `8c1bae600`，并通过 CR `35998752` 发布到预发。验证结果与尚未执行的生产恢复见 [分页修复计划](../plans/2026-09-07-scene-memory-pagination.md)。以上排查快照仍代表修复前的生产状态。
