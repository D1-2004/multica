# 项目评测目录与只读 HTML 展示

## 当前交付范围（最终澄清）

- FD Workbench 原网站上的独立评测页面，三个 Tab：评测集、评测工具、评测运行时。
- 评测集同时包含 20 条 P0 Golden 组合回归和 100+ 条分场景办公用例，重点为群聊与通用办公协作。
- 用例呈现固定为“需要什么角色 / 测试验证的是什么 / 怎么验证”，不呈现 Why。
- 评测工具仅说明造场景、造数据、编写断言与失败 case 扩充方法；运行时说明拉群、主持、演员号池、环境、模拟执行及验证工具，不提供执行按钮。
- 仓库评测相关 Markdown 以明确清单同步生成展示资产；页面可阅读原文，保留当前规范 / 历史资料标识和源码路径。
- 新分支 `feat/evaluation-hub`；仅本 session 展示相关改动提交，Aone 预发独立 CR 发布。停止外部 Sites/Vercel 发布，不替代 FD Workbench。

## 当前执行板

- [x] 确定最新范围并创建分支。
- [x] 20 P0 组合回归与办公分场景用例。
- [x] Markdown 资产清单、同步检查与安全渲染。
- [x] 三 Tab 页面、工具和运行时说明。
- [x] 本地内容 / HTTP / 页面验证。
- [x] 远端提交、独立 CR、预发发布及实际 URL 验证。

## 最终交付记录

- FD Workbench 预发入口：`https://pre-fde-workbench.dingtalk.com/evals`。
- 分支 `feat/evaluation-hub`，部署代码 `3512f2543fd36c36d5202c3d6e87cb310773ab99`；在当前预发 release 上仅新增展示相关变更。
- 独立 CR `36361979`，pipeline 66 Run `3110373660`。代码合并、构建、预发部署成功；预发集成节点耗时为零，不作为实际测试证据。
- 20 条 P0、16 个办公场景、123 条分场景定义、197 份 Markdown 资产。每例仅呈现角色、验证目标、验证方法；78 条有既有定义映射，45 条为新增定义，未宣称新 runner 或运行通过。
- 三 Tab、场景详情、Markdown 原文/hash、网站 `/evals` 入口均实际返回 200。无认证 `/api/evals` 返回 401；响应为 private/no-cache。
- 本次部署窗口 09:27:15–09:29:41；两 live pod 后端分别于 09:28:01、09:29:11 启动。release ancestry 包含部署代码。
- 部署门禁回读：`normal`、revision 15，两 live replicas 均在线。
- Go 只读/安全/定义测试和后端编译通过；Web 两文件 65 条代理测试与类型检查通过；197 份快照与引用检查通过；1440px 桌面、390px 手机呈现无横向溢出。
- 本次未运行真实办公评测、未启动群或演员、未改业务配置/数据库、未提交正式 pipeline 67。页面是定义与资产阅读入口。
- 本机闭环证据：`.context/evaluation-hub/manifest.json` 及同目录发布/HTTP/启动/门禁读数；不含凭据。

## 当前交付范围（用户修订）

- 首页与正式呈现改为严格 20 个黄金测试，按场景复杂度从简单到复杂排列，四组各 5 条。
- 只显示定义、前置环境、身份授权、步骤、通过条件、证据、工具和清理；不显示 Why、方法论、历史通过率或 131 条目录入口。
- 原始 131 条目录作为来源材料保留，不充当黄金测试的当前结果。
- 保留 `GET /api/evals`，以同一 Go 模板导出静态站点并部署，提供已成功发布的实际 URL。

## 修订执行板

- [x] 20 条黄金定义与复杂度顺序。
- [x] 首页按场景呈现，移除 Why。
- [x] 目录检查、只读 HTTP 与静态导出验证。
- [ ] 页面部署成功并返回 URL。

发布进度：Sites 项目 `appgprj_6ac1940f700481918cb7c50cc7d2e042` 已注册，仍未发布；源码上传未通过，保持原 private audience。静态导出转由 Vercel preview 发布，包内仅 HTML/CSS。

## 目标与范围

把本仓已存在的端到端测试和评测按场景整理为一个目录，每条说明被测模块、为什么测、测试方法、验证条件、最低环境、身份与工具、来源及证据边界。提供 `GET /api/evals` 与单个评测集的 HTML 页面，只做呈现，不运行测试、不修改环境、不读取当前凭证或业务数据。

范围包括 `e2e/*.spec.ts`、`docs/evals/` 的评测定义、独立 MCP E2E 脚本，以及 Employee 近期真实验收和已声明待验收场景。历史结果不当作当前版本通过；违反当前 `scene_id` 合同的旧评测明确标为需迁移。平台单元测试不冒称端到端。

## 工作方案

1. 先盘点 spec、评测、现有环境与授权合同，建立逐例来源映射。
2. 更新 `docs/evals/README.md`，定义目录字段、spec→eval 关系、环境与身份授权边界。
3. 将脱敏评测目录放在 `server/internal/evalcatalog/`，由 Go embed 与 `html/template` 生成 HTML。与既有 Go router 接入，不增加数据库、执行器或前端状态。
4. 按项目/场景展示入口、逐例卡片、环境与角色授权表、搭建/触发/观测/清理工具。使用本地 CSS 和字体，无 JS 或外部资源。
5. 验证定义覆盖与引用完整性、HTTP 只读/未知路径/转义、浏览器桌面与手机呈现。真实 E2E 本次不执行，目录整理不产生新的验收结论。

## 验收

- 每条有模块、方法、通过条件、环境、来源、证据面与不能证明的内容。
- 环境展示本仓 L0/L1/L2 定义；业务预发与 DWS 网关是独立轴。身份区分业务调用方、真人演员、员工执行主体、参与人和观测者，说明权限由谁授予及其 scope。
- 当前账号、token、uid/cid、runtime/revision 等不从历史文件复制到公开页面；实际可运行性依赖当波 manifest。
- HTTP GET 只返回编译进二进制的目录；不依赖 PG、Redis、模型或真实账号。
- 旧合同、已声明待验收项与历史证据各自明确；不虚构 pass。

## 进度

- [x] 阅读仓库合同与评测/交付技能，完成首轮盘点。
- [x] 外部模式调研与目录 spec。
- [x] 逐例目录及环境/身份/工具元数据。
- [x] HTML endpoint 与页面。
- [x] 覆盖/HTTP/视觉验证与结果回填。

## 已知限制

本 checkout 缺 `docs/employee-delivery-workflow.md`，本次按交付技能读取既有交付树中的同名文件，页面来源使用本 checkout 已存在的 Step 0、delivery standard 与各专项 spec。真实账号与发布状态只认执行时 manifest。

## 呈现与参考

采用冷灰画布、蓝色导航与低饱和青色验证区；字体使用本机 Avenir Next / PingFang SC 与等宽来源文本，字号沿用项目角色尺度。入口以 Spec→Case→Environment→Evidence→Verdict 的真实依赖链作为主要视觉结构，评测集用清晰列表，测试方法和验证条件并列，手机顺序展开。

已阅读 OpenAI Evals 的 eval templates 与 Inspect AI 的 hello_world Task 示例。参考它们的输入 / 方法 / 判定分离模式，元数据不接管 runner，展示层不引入执行引擎。引用与采用理由已写入 `docs/evals/README.md`。

## 完成结果

- 19 个评测集、131 条场景：浏览器 36、历史关联 8、collect 5、progressive smoke 4、policy replay 14、proactive replay 26、Employee 验收 33、远端 MCP 报告段 5。MCP 五段仍是同一个顺序脚本，不改写为五个独立 runner。
- 定义状态：defined 41、historical 54、needs-migration 8、planned 28。Employee 33 条沿用原稳定编号，5 条带 2026-10-03 历史范围说明，28 条按计划待验。不存在的 employee-e2e runner 不伪装成已可运行。
- 5 种 profile、8 类身份、13 个工具、13 条门禁、5 个 Spec→Eval 阶段均展示；suite 共用条件与 case 条件同时可读。
- `GET/HEAD /api/evals`、`/api/evals/{suite-id}`、`/api/evals/style.css` 已接入后端。静态预览入口 `server/cmd/evalcatalog` 默认仅监听 `127.0.0.1:8092`，无需业务数据库或账号。
- 校准产品术语为“任务 / 工作区 / 智能体”；浏览器证据面按实际断言区分 DOM、几何尺寸、截图、实际响应和 mock 请求，不把 setup API 冒称效果证据。

## 已执行验证

| 验证 | 实际结果与边界 |
| --- | --- |
| `python3 scripts/check-eval-catalog.py` | 19 suites / 131 cases / 5 environments 通过；每条字段、全局 ID、环境/身份/工具引用与源文件存在性有效；36 Playwright、17 原 suite、40 replay、33 Employee、5 MCP 报告段完整映射 |
| `go test ./internal/evalcatalog ./cmd/evalcatalog` | HTTP 只读 HTML、所有 detail、未知路径、路径越界、原 JSON 不暴露、HEAD、写方法拒绝和查询参数不影响内容通过 |
| `go test ./cmd/server -run '^$'` | 后端测试包编译与新增接线检查通过；不声称执行了业务集成测试 |
| Go 最终受影响检查 | 目录 HTTP 两条测试与后端编译再次通过，精确命令见下方 |
| Ego 浏览器实际预览 | 20 页共 131 case；所有本地页/锚点引用无断链；390px 手机与 1440px 桌面 document width 等于 viewport；0 scripts、0 buttons/inputs/forms/iframes；已查看截图 |
| `git diff --check` | 通过 |

```bash
go test ./internal/evalcatalog ./cmd/evalcatalog ./cmd/server -run 'TestReviewedPagesAreReadOnlyHTML|TestCatalogMethodsAndPathAllowlist|^$'
```

未运行真实钉钉/模型评测、共享预发发布或故障注入。本次签收的是目录、来源覆盖和呈现，不签发任何新业务 case 的 pass。页面后端接线随后续版本部署生效；本地预览已启动。
