# 预发剧本

固定对象和身份见 `SKILL.md`。`DWS=python3 "$HOME/.agents/skills/dws-env/scripts/dws_env.py"`。先 `status` 确认 `environment=pre`。

出站：`as <谁> -- chat +messages-send --as user --chat-id <cid> --text '…' --yes --format json`，再用 `openTaskId` 查 `+messages-query-send-status`。`sendStatus=SUCCESS` 才算发出。

Coordinator：`scripts/query-coordinator-sls.sh --env pre --cid '<cid>'`（或 `--trace`）。看 `inbound_coordinator_llm_request`、`assoc_recall`、`inbound_coordinator_decided`。

开关未开时剧本会得到「旧短循环」结果，那不是失败，是切片未到。记录下来，C/D 后再跑同一句。

## P0 — A/B 骨架（无召回）

前置：A 已部署。B 未合入时只检查「不崩、不 Claim」。

| 步 | 动作 | 过线 |
|---|---|---|
| P0.1 | `as 主角` 给 DM cid 发「你好」 | IM ACK；SLS 有 `inbound_coordinator_decided`；`action=reply` 且不带 issue |
| P0.2 | 开关全关 | 无 `scene_memory` dirty（或 dirty 不被 Claim，因为 write=false） |
| P0.3 | 仅开 write，B 已合入 | debounce 后该 cid 行 `bootstrapped_at` 非空、`memory_text` 非空；SLS **无** Text 正文 |

## P1 — 单聊：纠正进记忆，不进事项（C）

cid：`cid+bEFv7ngm9n79Q1vL9HYJw==`。`as 主角`。开 write+recall。

| 步 | 说 | 过线 |
|---|---|---|
| P1.1 问候 | 「你好」 | `action=reply`，不带 issue |
| P1.2 纠正 | 「GoalMate 是工具，不是数字员工。下次别搞错。」 | **不得** `issue_comment_add` 到 WS-31 / `a2f6f860`。Flush 后 Text 含「工具」口径 |
| P1.3 召回 | 「GoalMate 是什么」 | prompt 有非空 `scene_memory_revision`；回复按「工具」；`assoc_recall` 的 cid 正确；issue_id 不来自 Memory |
| P1.4 事项 | 「帮我约冬翔明天下午半小时」 | `action=issue` 或 bind 新卡，不续 WS-31 |

P1.2 基线（2026-09-02，记忆未上）**失败**：`coord_trace_id=b4db44f2-9db0-4bc0-801c-db112dff3ec2` 把纠正评到须莫明早会议。C 之后必须翻盘。

## P2 — 事项仍走 assoc（C）

同一 DM，`as 主角`。

| 步 | 说 | 过线 |
|---|---|---|
| P2.1 | 「开个新 issue，问须莫明早有没有会议」 | 新 purpose / 新 issue，不并进「今晚会议」旧卡。Memory 不提供 issue_id |

基线已通过：`7f5655d7-a806-4dd6-8589-7e0e60b1a139`。回归时不得回退。

## P3 — 群隔离（C）

`as 配角`（与测试号同组织）建两个群，都拉测试号。群消息必须 @ 数字员工，否则可能不入站。

```bash
$DWS as 配角 -- chat +chat-create --name "sm-e2e-A-<stamp>" --member-query "东翔测试" --yes --format json
$DWS as 配角 -- chat +chat-create --name "sm-e2e-B-<stamp>" --member-query "东翔测试" --yes --format json
```

记下两个不同的 `openConversationId`。

| 步 | 谁 / 哪 | 过线 |
|---|---|---|
| P3.1 | 配角在群 A @员工：「这个群里 GoalMate 是工具」 | 仅群 A 的 `scene_key` 有该口径 |
| P3.2 | 配角在群 B @员工：「GoalMate 是什么」 | 群 B 不出现群 A 口径 |
| P3.3 | 主角在 DM 再问 GoalMate | DM 仍是 P1 口径，不被群覆盖 |

零命中/多候选就停，列出 `label`，不要猜成员。

## P4 — `/reset-memory`（C）

P1 的同一 DM cid。`as 主角` 发 `/reset-memory`（可带 @mention，必须是第一条 token）。

过线：

- 该 cid Text 空
- `bootstrapped_at` 仍在
- `source_cursor_at` ≈ now
- 该 cid 的 assoc 事项边清空（`assoc_recall` 无 waiting 卡）
- 下一句「GoalMate 是什么」不得直接用「工具」
- 不得从 reset 前历史立刻把口径 Flush 回来

## P5 — 开关与 UI（C 的行为 + D 的面）

| 步 | 操作 | 过线 |
|---|---|---|
| P5.1 关 recall | write 仍开 | ACK 正常；SLS 无 Text / 无 revision 注入 |
| P5.2 关 write | 再纠正一句 | 不再 MarkDirty/Claim；旧 Text 冻结 |
| P5.3 Flush 挂了 | Worker 无 Flusher 或 Block | IM 仍 ACK，Coordinator 仍能 reply |
| P5.4 开 UI | Workbench Agent 详情 | inbound Tab 能看见该 cid 的 Text 和 Flush 状态（pending/running/retrying/blocked） |
| P5.5 关 UI | 关 `scene_memory_ui_enabled` | inbound Tab 不展示 Memory |
| P5.6 权限 | 非 manage 身份打 Scene API | 拒绝 |

## 最近一次预发记录

把新跑的结果追加在表顶，不要改基线行的结论。

| 日期 | 切片 | 剧本 | cid / coord_trace_id | 结论 |
|---|---|---|---|---|
| 2026-09-02 | 记忆未上 | P1.1 | `7bd5ee60-4e74-43de-a889-d2137a9f9e96` | 通过，reply 无 issue |
| 2026-09-02 | 记忆未上 | P1.2 | `b4db44f2-9db0-4bc0-801c-db112dff3ec2` | **失败**，纠正进 WS-31 |
| 2026-09-02 | 记忆未上 | P1.4 | `49133eb5-7aaf-41f1-8e09-d4fb6eb2aaa3` | 通过，`action=issue` 空 issue_id |
| 2026-09-02 | 记忆未上 | P2.1 | `7f5655d7-a806-4dd6-8589-7e0e60b1a139` | 通过，新 bind |
