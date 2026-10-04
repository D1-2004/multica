# QwenTag SPEC & EVALS 发布与验收接手包

2026-10-04。用户已要求把本分支全部代码与改动交给「发布和验收」session，由该 session 统一集成、发布到预发并按本合同验收。预发只采用接手 session 维护的统一集成/发布分支；发送 session 停止独立发布。本交接不是上线或真实办公 E2E 通过证明。

## 代码与所有权

- 远端：`aone`，`dingtalk-ai-lab/dt-fde-multica`。
- 来源：`feat/evaluation-hub`，功能代码 head `78fffd467179e5ed577317d8e834ad731643abaf`；本接手包之后只有文档提交。
- 原始已发布评测页提交：`3512f2543fd36c36d5202c3d6e87cb310773ab99`。
- 后续完整序列：`e1d8f13495`（原交付记录）、`3a2f721ad0`（单页与贡献门禁）、`326e563cb8`（当时阻塞记录）、`8e7f4a447d`（SPEC 与分类）、`78fffd4671`（接口合同与报告）。不要只取最后一个提交，它依赖前面的 SPEC/页面/校验器。
- 原展示 CR：36361979，app342160 / pipeline66。旧 Run 3110376103 曾被其他 CR 文档冲突阻塞。
- 本次只读查询最新 Run 为 3110378343：代码合并 SUCCESS、构建 RUNNING；未证明该冻结构建包含本功能 head。接手 session 的组合 release `f84f629789` 正在推进，不能未核对就宣称它已包含本包。
- 接收 session：`01a10278-0dd8-7a83-be4c-72fc351bbecf`，显示名称「发布和验收」。保护其现有 worktree WIP，不 stash、覆盖或提交其他会话未完成内容。
- 当前 checkout 仅另有未跟踪 `artifacts/`（旧展示临时资产），不属于已推分支交付，不进入统一发布。

先 fetch 来源，在自己的统一集成分支核对 ancestry / tree 等价并语义集成；保留当前 reader20/steer 等既有交付。整支 merge 或逐个 cherry-pick 由接手者依据实际共同祖先选择，不把旧基线覆盖成当前代码。路由、server build markers、workspace 删除和迁移 hook 最可能与并行改动相交。

统一后按实际 CR/run 处理来源展示 CR 的 pipeline 参与关系，避免重复合并/双重发布。只统一预发发布来源，不删除历史分支、提交或 CR 审计证据。当前冻结发布如未含本包，先完成或协调其窗口，再安排一个明确包含全部改动的统一版本；不要把没进入构建的分支 head 当已发布。

## 产品与定义合同

标题：`QwenTag SPEC & EVALS`。三个同级 Tab：SPEC / EVALS / 评测报告。

- SPEC：员工应做到什么，5 类行为要求，每项关联对应办公场景。规范不表示已支持或已验收。
- EVALS：固定 20 个 P0 GoldenCases（G01–G20），以及 16 场景、123 条分场景用例；后续用例数允许增长，不用换编号/角色/数字凑数。
- 分类：沟通与任务理解、执行与交付、协作与例行工作、权限与隐私、记忆/学习/可靠性。
- 场景与用例在同一页展开。每例三个展示字段：需要什么角色、测试验证的是什么、怎么验证。
- 不恢复 Markdown 阅读器、资产/hash 清单、工具/运行时操作界面。页面只读，不在网页执行 E2E 或修改环境。

权威定义为 `server/internal/evalcatalog/{spec,p0-golden,office-scenarios}.json`。本地修订定义 → `make eval-check` → 提交/发布对应版本，Go embed 自动呈现。`docs/evals/CONTRIBUTING.md` 与模块 AGENTS 约束稳定 ID、来源、分类、SPEC 关联及 P0 引用；原 MD 只是维护上下文。

每次接到发布请求，先把该次行为变更对应到 SPEC 要求、原场景/case ID 与受影响 P0 组合。优先原失败和受影响链路；规范/编译/mock 不代替真实验收。原场景、历史、约束与演员应保留，不通过清历史、换群或降标准消除反例。

## 本地结果上报与报告

权威合同：`docs/evals/reporting-contract.md`；机器 OpenAPI 3.1 / v1 schema：`server/internal/evalreport/report-contract.json`，GET `/api/evals/report-contract`。

- POST `/api/workspaces/{workspace_id}/eval-reports`；列表 GET 同路径；详情 GET `/{report_id}`。
- 严格人类身份检查在 workspace 成员解析前，禁止任务/云节点/DTA service 凭据借 owner 上报。提交人和 workspace 不由 body 赋权，每次读回重新检查成员资格。
- 固定 run UUID、执行类型、环境、被测版本、runner 版本、时间、定义 commit/dirty/hash、计划用例的完整三字段快照及逐例结果。
- execution_kind：`real_e2e` / `mock` / `definition_check`。每个计划用例恰好一个 `pass/fail/blocked/incomplete/skipped`，pass/fail 需要证据引用。
- 服务端算汇总，未执行/阻塞/不完整仍在分母中；部分选例通过不等于全 P0 通过。mock/定义检查不计真实 E2E 通过。
- 相同 workspace/run ID、相同规范化内容首次201，重放200且同 ID/hash；不同内容409，不能覆盖历史。
- 保存当次快照，允许本地新增未部署定义；历史报告不套当前目录标题与标准。证据只存引用，不主动抓任意 URL，不将本地路径变成下载链接。
- 报告为“本地上报结果”，接收成功与引用存在均不代表平台复验通过。

本地工具：

```bash
python3 scripts/submit-eval-report.py prepare --input runner-results.json --output frozen-report.json
python3 scripts/submit-eval-report.py submit --input frozen-report.json --endpoint https://pre-fde-workbench.dingtalk.com --workspace-id <workspace-uuid> --token-env MULTICA_TOKEN
```

runner 输入提供 `selected_case_ids` 与各条结果，不推导或补造 pass。准备阶段不联网、不覆盖旧文件；重试只提交同一冻结文件和 run ID。PAT 从显式环境变量读取，不在文档或证据中落令牌；HTTP 重定向拒绝。

工作流中 `waiting_actor` 可映射 blocked 并保留原因，`invalid_env` 映射 incomplete 并记环境变化；partial/vacuous 不能计 pass。known_limit 按是否实际复现为 fail 或未运行为 blocked/skipped，保留原始判断在 summary/证据中，不吞掉未完成计划用例。

## 发布前关键检查

- 10020–10023 `eval_report` 表及三个独立 CONCURRENTLY 索引，无外键。DDL 可重放，三个 index full stems 已登记 `cleanupInvalidConcurrentIndexHook`。
- 核对统一分支是否占用了这些编号；未部署前可协调整批编号，随之更新 hook/test/合同，不能只改一个文件名。
- 迁移仅由既有 Aone packaged migrator 执行，不手工迁移预发。
- 独立 `[eval-report:1]` 表示上报与 workspace 清理兼容。新节点广播，POST 仅在所有 live 支持时放行；nil/不支持/查询失败返回503零写入。不得替换或降级接手版本已有 EmployeeLoop 等 marker。
- workspace 删除先锁父行，再在同一事务清理报告；提交锁父行与成员。回滚最低版本必须保留报告清理，具体流程见上报合同。

## 预发验收清单

1. 记录统一 source/release ancestry、准确 CR/run、所有 live 本次 server starting、fence normal 与报告/业务 reader 累积 markers，确认本包实际生效。
2. 人类登录 GET `/evals`、`?tab=spec`、`?tab=reports`、报告合同均200；简化标题与三 Tab 正确；20 P0、分类场景与三定义字段可展开；手机无横向溢出。报告存储不可用与空列表区分，不造 demo 数据。
3. 在获准测试 workspace、显式人类身份上报一份标记清楚的 mock/definition_check 协议测试报告，核对201/原ID200/变更409、列表meta、详情快照和未执行分母；这些只签接口/存储验收，不签办公 E2E。
4. 测其他 workspace/member/服务凭据的拒绝；按约定测试身份撤销窗口确认读回404，保护其他真实成员和数据，不随意撤销其他人的资格。
5. 按本次业务改动选择真实场景/P0，固定当前 manifest 和可运行前置；独立回读 IM/文件、业务 API、SLS、LF，与真实工具效果相连。执行后 `real_e2e` 上报真实结果，并核对报告页与证据一致。
6. 没跑的例明确 blocked/incomplete/skipped；平台本次未具备的跨日/演员/工具条件保留，不算通过。失败缩成最小反例补回稳定用例，再修订相关 P0，避免改历史报告。
7. 恢复测试配置、routine、身份/场域状态；保留审计证据。预发验收门只由统一发布负责人按本次范围推进，不转正式。

## 已有验证与边界

本地已完成定义门禁、Go呈现/校验/race/vet、独立子代理表达和安全评审；专用新建 PostgreSQL 验证并发幂等、成员撤销、workspace 删除排序。HTTP取得201/200/409、冻结快照读回、metadata列表、报告HTML及撤销404；DDL执行两次、INVALID index恢复 hook通过。CLI冻结/拒绝覆盖/不回显token/禁重定向通过。专用测试容器已停止自动移除，没有修改其他本地DB。

没有运行真实办公 E2E、没有上报真实项目报告；截至移交，未证明该功能已经预发上线。本地预览8095没有业务存储，报告Tab明确 unavailable。全量旧migration lint存在既有前缀冲突；一次过宽TestNormalize选择触发无关handler既有失败/缺DB panic，随后本次TestEvalReport通过。全量不签绿，不要求重跑无关测试来伪造验收。

当前状态与沿革：`docs/plans/2026-10-04-eval-catalog-one-page.md`。真实账号、workspace、Tag/Runtime、source/release、run 与测试窗口由接手 session 当前 manifest 核对，历史 ID 不成为永久默认值。
