# QwenTag SPEC & EVALS

页面 `/evals` 提供横向 `SPEC | EVALS | 评测报告` 导航。SPEC 说明员工应做到什么；EVALS 展示 20 条 P0 GoldenCases 和分类的通用办公场景用例；评测报告只读展示本地上报记录。场景和用例在本页展开；页面不运行测试或修改环境。

## 权威定义

- `server/internal/evalcatalog/spec.json`：员工行为要求、规范来源与对应场景。规范不表示能力已验收通过。
- `server/internal/evalcatalog/p0-golden.json`：固定 `G01`–`G20`，每条 `priority=P0`，引用具体场景及用例。
- `server/internal/evalcatalog/office-scenarios.json`：场景、展示分类与持续增长的用例，至少 100 条。每例展示需要角色、验证目标、验证方法。分类调整不能改动用例 ID 或场景归属。

Go 页面仅嵌入这三套定义。`suites.json`、`golden.json` 等旧资料保留来源引用用途，不决定当前页面或当前通过结论。Markdown 不作为页面快照，不需同步生成。

`origin=existing` 表示能追溯已有定义，`origin=defined` 表示按当前合同新增定义。两者都不表示 runner 已具备、已部署或已通过。`liveReady: true` 只表示当前预发可以用真实对话跑完，并且对错能在对话或收到的文件里核对。省略表示还缺运行条件，或核对点不在对话里。它不是通过结论，不写 `false`。

## 本地检查

```bash
make eval-check
python3 scripts/check-eval-catalog.py --base-ref <base-commit-sha>
```

`make eval-check` 与直接执行 `python3 scripts/check-eval-catalog.py` 使用同一个检查器。Aone backend Docker builder 在完整源码目录中执行同一检查，失败会中止该构建。检查器仅依赖 Python 标准库并兼容 Python 3.6；它核对结构、稳定 ID、仓内来源、隐私和引用归属，不调用模型、DWS 或执行真实评测。`--base-ref` 通过只读 Git 查询保护既有 ID，允许正常修订验证条件与持续增加场景，不允许默认删除或移动旧用例。

页面改动另运行仓库相应的 Go/渲染检查。本机呈现入口：`cd server && go run ./cmd/evalcatalog -listen 127.0.0.1:8092`；WorkBench 入口使用既有认证门禁。

## 持续迭代

修改本地定义 → 执行 `make eval-check` → 提交并发布对应版本。无需生成页面快照或手工复制文案。

失败先沉淀最小反例，再归入稳定场景用例；新场景关联对应 SPEC，达到组合回归条件时更新 P0 引用。定义、fixture 准备、runner 可执行性和实际证据分别记录。贡献步骤及提交/PR 清单见 [CONTRIBUTING.md](CONTRIBUTING.md)。

独立工作流 `.github/workflows/eval-catalog.yml` 对相关 PR/push 运行定义检查。仓库管理员需把 `Eval catalog / definitions` 配为 required status check，才能成为合并门禁；工作流文件本身不设置 branch protection。

## 本地结果上报

[上报合同](reporting-contract.md) 定义请求、身份、快照、幂等和汇总规则。机器合同由 `GET /api/evals/report-contract` 返回，与接收验证使用同一 JSON schema。

本地 runner 写运行信息、`selected_case_ids` 与逐例 `results`，先冻结定义，再提交同一文件：

```bash
python3 scripts/submit-eval-report.py prepare --input runner-results.json --output frozen-report.json
python3 scripts/submit-eval-report.py submit --input frozen-report.json --endpoint https://pre-fde-workbench.dingtalk.com --workspace-id <workspace-uuid> --token-env MULTICA_TOKEN
```

准备步骤不联网、不自动生成通过结论、不覆盖已有冻结文件；提交使用显式人类 PAT，不回显令牌。重试复用文件与运行 ID，不能重新从变更后的定义生成同一次报告。报告保持本地上报属性，定义检查和模拟结果不计入真实 E2E 通过。
