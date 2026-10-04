# 评测定义

页面 `/evals` 只展示两部分：20 条 P0 Golden 组合回归，以及分场景用例。场景和用例在同一页内展开；页面不运行测试或修改环境。

## 权威定义

- `server/internal/evalcatalog/p0-golden.json`：固定 `G01`–`G20`，每条 `priority=P0`，引用具体场景及用例。
- `server/internal/evalcatalog/office-scenarios.json`：按办公场景持续增长，至少 100 条。每例展示需要角色、验证目标、验证方法。

Go 页面仅嵌入这两套定义。`suites.json`、`golden.json` 等旧资料保留来源引用用途，不决定当前页面或当前通过结论。Markdown 不作为页面快照，不需同步生成。

`origin=existing` 表示能追溯已有定义，`origin=defined` 表示按当前合同新增定义。两者都不表示 runner 已具备、已部署或已通过。

## 本地检查

```bash
make eval-check
python3 scripts/check-eval-catalog.py --base-ref <base-commit-sha>
```

`make eval-check` 与直接执行 `python3 scripts/check-eval-catalog.py` 使用同一个检查器。Aone backend Docker builder 在完整源码目录中执行同一检查，失败会中止该构建。检查器仅依赖 Python 标准库并兼容 Python 3.6；它核对结构、稳定 ID、仓内来源、隐私和引用归属，不调用模型、DWS 或执行真实评测。`--base-ref` 通过只读 Git 查询保护既有 ID，允许正常修订验证条件与持续增加场景，不允许默认删除或移动旧用例。

页面改动另运行仓库相应的 Go/渲染检查。本机呈现入口：`cd server && go run ./cmd/evalcatalog -listen 127.0.0.1:8092`；WorkBench 入口使用既有认证门禁。

## 持续迭代

失败先沉淀最小反例，再归入稳定场景用例；达到组合回归条件时更新 P0 引用。定义、fixture 准备、runner 可执行性和实际证据分别记录。贡献步骤及提交/PR 清单见 [CONTRIBUTING.md](CONTRIBUTING.md)。

独立工作流 `.github/workflows/eval-catalog.yml` 对相关 PR/push 运行定义检查。仓库管理员需把 `Eval catalog / definitions` 配为 required status check，才能成为合并门禁；工作流文件本身不设置 branch protection。
