# FD Workbench 评测中心

入口：`/evals`。页面采用三个只读 Tab，不执行测试或修改环境。

| Tab | 内容 |
| --- | --- |
| 评测集 | 20 条 P0 Golden 组合回归、16 个办公场景及 123 条分场景定义 |
| 评测工具 | 造场景、造数据、写断言、采证与失败 case 扩充 |
| 评测运行时 | 拉群、主持、号池、角色授权、环境准备、模拟运行与验证工具 |

每条用例固定三个呈现字段：需要什么角色、测试验证的是什么、怎么验证。Golden 明确引用具体分场景 case；一个组合不表示覆盖该场景全部反例。定义、runner 能力与实际运行结果分别记录。

## 权威资产

- `server/internal/evalcatalog/p0-golden.json` 与 `docs/evals/p0-golden.md`：20 条 P0。
- `server/internal/evalcatalog/office-scenarios.json` 与 `docs/evals/office-scenarios.md`：办公场景用例。
- `docs/evals/evaluation-tools.md`：构造与验证工具定义。
- `docs/evals/evaluation-runtime.md`：运行角色、授权及环境定义。
- `server/internal/evalcatalog/asset-index.json`：可阅读 Markdown 清单及分类/历史标记。
- `suites.json / environment.json / golden.json`：原始来源目录，保留溯源，不作为当前通过结论。

## 仓库联动

```bash
python3 scripts/sync-eval-assets.py
python3 scripts/sync-eval-assets.py --check
python3 scripts/check-eval-catalog.py
cd server
go test ./internal/evalcatalog ./cmd/evalcatalog
```

同步脚本生成文本快照及原文 SHA-256。Go embed 编译进版本；页面不临时读取任意仓库路径。新增评测 MD 先加入清单；`docs/evals` 中未登记的 MD 会使检查失败。新增失败用例加入对应场景，再更新适用 P0 引用。

## 路由与访问

`/evals` 经网站代理到 `GET/HEAD /api/evals`，使用现有登录 cookie 与人类访问门禁；`tab=evals|tools|runtime` 选择内容，`scenario=<id>` 阅读场景，`doc=<登记路径>` 阅读 Markdown。未知源路径返回 404。所有内容均为读取，写方法不提供入口。

本地呈现预览：`cd server && go run ./cmd/evalcatalog -listen 127.0.0.1:8092`。此入口仅监听本机；预发使用正常 Workbench 认证。
