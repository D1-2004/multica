# 本地评测结果上报合同 v1

此接口接收本地评测产生的报告。接收成功证明已持久保存提交内容，不代表服务端执行或验收了测试。SPEC / EVALS 定义不因上报发生变化。

## 接口与身份

- `POST /api/workspaces/{workspace_id}/eval-reports`：提交不可变报告，首次 `201`，相同内容重放 `200`，相同运行 ID 不同内容 `409`。
- `GET /api/workspaces/{workspace_id}/eval-reports`：按接收时间倒序列出报告；`limit` 为 1–50，`before_id` 为上一页最后一条报告 ID。
- `GET /api/workspaces/{workspace_id}/eval-reports/{report_id}`：读取报告、冻结定义和计算后的汇总。
- `GET /api/evals/report-contract`：读取机器可读接口合同。
- `/evals?tab=reports`：仅展示当前登录人有成员资格的项目报告。

接口使用既有登录会话或人类 PAT。严格的人类身份检查在 workspace 成员解析之前进行；任务、云节点和 DTA 服务凭据不准借 owner 身份上报。workspace 来自可信路由与当前成员资格，提交人来自认证，不接收正文里的授权主体。所有读取均重验当前成员资格。

## 请求

Content-Type 为 `application/json`，最多 2 MiB。未声明字段拒绝，`schema_version=1`。请求不得含凭据、签名下载 URL 或原始秘密。

| 字段 | 含义 |
| --- | --- |
| `run_id` | 本地生成的 UUID；一次运行固定不变，重试复用 |
| `title` | 人可读的报告名称 |
| `execution_kind` | `real_e2e` / `mock` / `definition_check`，不同类型分别呈现 |
| `environment` | 实际执行环境名称 |
| `target_revision` | 被测版本，完整 Git SHA 或 `sha256:` 镜像摘要 |
| `runner` | 执行器的 `name`、`version` |
| `started_at`、`finished_at` | 带时区的 RFC3339 时间；结束不能早于开始 |
| `catalog` | 本地定义来源的完整 `revision`、`dirty` 状态及 `sha256` 文件摘要 |
| `selected_cases` | 本次计划执行的用例快照，至少 1 条，最多 1000 条 |
| `results` | 每个计划用例恰好一条最终结果，不接受缺失或额外结果 |

用例快照字段：`id`、`title`、`kind`（`p0` / `office`）、`scenario_id`、`roles`、`verifies`、`method`。P0 ID 为 `G01`–`G20`，`scenario_id` 为空；办公用例和场景 ID 使用稳定 kebab-case。本地新增或未发布定义可合法上报；历史报告展示收到的快照，不套用当前目录的新标题和标准。

结果字段：`case_id`、`status`、`summary`、`evidence`。`status` 为 `pass` / `fail` / `blocked` / `incomplete` / `skipped`。`summary` 写实际观察、失败差异或未完成原因；`pass`、`fail` 至少一条证据引用。证据包含 `kind`（`im` / `api` / `trace` / `log` / `artifact`）和 `reference`；只允许无凭据的 HTTPS 地址或脱敏的相对证据路径。服务端不自动访问引用，不把本地路径做成下载链接。

`catalog.sha256` 是按 `spec.json`、`p0-golden.json`、`office-scenarios.json` 文件名排序，将“文件名 + 换行 + 原始文件内容 + 换行”依次串联后的 SHA-256；它记录本地定义来源，不能单独证明执行。服务端另计算所选定义快照的 `definition_sha256` 和整个规范化请求的 `content_sha256`。

## 汇总与不可变性

汇总由服务端计算，不接收客户端总分或通过率。

- 分母为所有计划用例，包含 blocked / incomplete / skipped；不从分母删去未执行项。
- 任何失败使该次结果为 `fail`；无失败但有未完成项为 `incomplete`；仅全部选例通过时为 `pass`。
- 部分 P0 全通过只表示本次选例通过，另给出 `p0_selected` / `p0_total=20`，不声称完整黄金回归通过。
- `real_e2e_pass` 只计算 `execution_kind=real_e2e` 的上报通过项；mock 或定义检查不计入真实 E2E。
- 上述数字仍为本地上报结果，证据引用不等于平台已审查其真实性。

同 workspace/run ID 保存一次。用例和结果按 ID 排序、时间规范化为 UTC 后计算内容摘要；单纯对象字段顺序、结果数组顺序和时间等价表示不产生新报告。不同结论、定义、证据、版本或运行信息必须使用新的 run ID，不能覆盖历史。

报告包含服务端生成的 `id`、`workspace_id`、`submitted_by`、`received_at`、`content_sha256`、`definition_sha256`、`summary` 和冻结 `submission`。工作区删除在同一删除事务中清理报告；报告写入参加 workspace 与成员行锁，避免删除或撤销后产生孤儿记录。

首次启用写入需要全部存活节点支持 `[eval-report:1]`（包括报告清理）。混版或无法核对节点能力时，POST 返回 503 且不写入。回滚最低版本必须保留报告清理；不能直接回到没有清理能力的旧节点。需要更早回滚时，先停止上报、冻结业务写入并完成受控清理，再回滚。迁移使用可重放 DDL，三个并发索引登记既有 INVALID 索引清理 hook。

## 失败语义

`400` 请求/引用/选例不一致；`401` 未认证；`403` 非人类或无上报资格；`404` 工作区或 scoped 报告不可见；`409` 运行 ID 内容冲突；`413` 超过体积；`415` Content-Type 不支持；`503` 报告存储不可用。空列表与存储不可用分别呈现，不插入演示或伪造的通过记录。
