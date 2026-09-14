# Coordinator 同类批量变更被误拆

policy_version `2026-09-14.1`，装配版本 `22`。现行合同见 [inbound-coordinator-loop.md](../inbound-coordinator-loop.md)。对照案例 `f10_same_kind_batch_vs_unrelated_outputs`。

## 触发事实（正式，2026-09-14 13:38，VOC决策助理/金龙）

群 `oa测试群`（`cidjpTppGMtZhM+I4mgBKSoqw==`），彭智韩(柏智)：

> @VOC决策助理(金龙) 钉钉文档的负责人改成@代成俊(璟琦(主用钉)) ，AI表格助理的负责人改成蓝派

| 时间 (UTC) | 事件 | 证据 |
| --- | --- | --- |
| 05:38:04 | 入站 Decide 开始 | `coord_trace_id=4f87da08-4058-490e-8809-0895fba189ca` |
| 05:38:11 | 主模型一个 `start_work`，purpose 含两类负责人；审核 `work_checks.a1=multiple`，reason「明确修改两类负责人配置，属独立交付目标」 | Langfuse `coordinator.finish_check.1` |
| 05:38:17 | 主模型拆成两个 `start_work`；审核仍把 `a1=multiple`、`a2=single`，reason「Two independent deliverables bundled in one action」 | `coordinator.finish_check.2` |
| 05:38:20–22 | 同提案缓存命中同一 reason 两次，`loop_stop_reason=review_deadlock` | SLS `inbound_coordinator_decided` |
| 05:38:22 | Host 兜底回复「这条我没接住…」 | `loop_stop_fallback=reply` |
| 05:39:58 | 用户改口「AI表格助理的负责人改成蓝派，钉钉文档的负责人改成随风」；一个 `start_work` 审核 allow，执行器一次提交两个 ChangeSet | `6479e3bf-9a9c-49cb-b71d-9340264b2374` |

根因：`finish_check_work` 把「分别请求的结果即使主题相同也要拆」用在了同一类配置变更的多个对象上；拆开后审核又把整窗的两条请求投影到第一条动作上。Host 在审核已是 `revise` 时原样保留该 reason，重试预算按同一 reason 三次停掉。

对照仍成立：产品查证与另起通知草稿是两个产出（`f10_independent_deliverables_vs_related_steps` / `f10_product_check_and_separate_notice`）。

## 改动

1. **规则**：`inbound@22` 同类批量（同一突变/配置改多个对象）算一个交付物；`finish_check_work@17` 只按该动作自身 purpose 判 single/multiple，兄弟动作不是 multiple；`window@3` 同步。
2. **工具 schema**：`finish` 的 purpose/`finish_check` 的 deliverables 写明 same-kind batch vs unrelated kinds。
3. **Host**：审核把某工作动作标成 multiple 时，始终改写为 Host 诊断。已有兄弟工作动作时，提示按该动作 purpose 分类，而不是再拆一次。
4. **合同与案例**：`COORD.F10` 义务、`f10_same_kind_batch_vs_unrelated_outputs`、本 Plan。

## 验证状态

| 层 | 状态 |
| --- | --- |
| 结构检查 `scripts/check-coordinator-policy.py` | PASS_STRUCTURAL_ONLY（policy_version 2026-09-14.1，89 条对照合同） |
| `go test ./internal/service/inboundcoord/` | 通过（新增 `TestFinishCheckRewritesReviewerMultipleWhenWorkIsAlreadySplit`、`TestFinishCheckAllowsSameKindOwnerBatchAsSingleDeliverable`、`TestFinishCheckMultipleOnLoneWorkActionStillAsksToSplit`） |
| 预发 live | pending：同类「改两类负责人」应一个 start_work 通过；查证+另起通知草稿仍须拆 |
