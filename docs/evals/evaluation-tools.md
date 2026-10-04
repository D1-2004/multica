# 评测工具

## 用例模板

每条用例固定三个呈现字段：

| 字段 | 定义 |
| --- | --- |
| 需要什么角色 | 主持人、请求者、参与者、数字员工、独立验证者及授权范围 |
| 测试验证的是什么 | 当前用例的准确结果、状态和禁止副作用 |
| 怎么验证 | 场景构造、输入顺序、实际执行、独立回读、断言及清理 |

数据文件保留稳定 case ID、场景 ID、来源文件、已有定义引用及 `existing / defined`。`defined` 只表示新增定义，不表示已有 runner 或当前运行通过。

## 场景与数据构造

现有可执行 harness 为 `scripts/employee-e2e/e2e.py`。资产包括 `cases/golden20.json`、`cases/v2/` 的 88 条 G/M/C/P/T 用例及 memory suite。页面中的 20 条 P0 是组合定义；分场景引用与现有 runner 能力分别记录。

| harness 部件 | 定义 |
| --- | --- |
| `cases/v2/_build/build.py` | 从 story、roles 和判定源码生成场景 JSON 与 `SUITE.md` |
| `el2e/driver*.py` | 扮演已获准角色并记录原始事实，不负责评分 |
| `el2e/evidence.py` | 按精确来源采集 LF、SLS 和业务事实 |
| `el2e/grader*.py` | 对最终 transcript、声明断言和证据评分，生成逐例结果与 baseline diff |
| `known_gaps.json` | 记录当前缺口；已转为通过的项同轮退出缺口表 |

源说明：[harness README](../../scripts/employee-e2e/README.md)、[现有场景集](../../scripts/employee-e2e/cases/v2/SUITE.md)。

| 工具 | 输入 | 产物 |
| --- | --- | --- |
| `office-scenarios.json` | 群聊办公场景、可信角色、结果条件 | 分场景用例及稳定 ID |
| `p0-golden.json` | 多个场景及分场景 case 引用 | 20 条 P0 组合回归 |
| DWS 演员工具 | 明确身份、测试会话、合成台词、幂等键 | 实际消息来源与发送回执 |
| fixture / TestApiClient | 隔离账号、工作区、任务、文件 | 可清理的前置对象及创建清单 |
| fake / httptest | 确定性事件、渠道及工具响应 | 本地协议、权限、状态和故障反例 |
| 随机文件夹具 | 不出现在问句中的随机码、CSV/JSON 预期 | 文件字节、标准答案、SHA-256 |

群聊剧本登记消息发送者、可信 @ 对象、引用消息、场域、输入顺序与每轮完成条件。自然语言重复与同 source 技术重投分别定义。标准答案与模型输入分开保存。

## 验证工具

| 工具 | 验证内容 | 保存结果 |
| --- | --- | --- |
| DWS 独立回读 / 下载 | 目标群实际消息、发送者、引用、次数、文件字节 | 消息 ID、完整窗口、分页状态、字节/hash |
| 业务只读 API | Receipt、Job、Task、Run、输入、等待、action | 精确对象关联、状态、修订号、数量 |
| SLS / Normandy | 摄入、门禁、重复、恢复、副本启动和真实退出 | 精确范围、分页与截断、事件时间线 |
| Langfuse | 实际模型输入、原生工具、结果与调用账本 | 精确 trace、generation、截断说明 |
| Host checker | 本 Run 的产物内容、JSON 预期值、投递回执 | 规格版本、目标修订号、证据 hash、结论 |
| 人工 / LLM-as-judge | 语义正确、指代、自然度和未预期表达 | 评分规则、证据引用、判定结果 |

命令返回、模型表达、业务状态、实际送达分别记录。安全、权限、隔离、幂等和证据完整性作为硬条件，质量评分不能覆盖硬条件失败。

## 失败 case 扩充

1. 保留原始失败来源、版本、角色、场域与证据。
2. 提取最小反例，保留导致失败的输入和状态。
3. 在对应场景增加稳定 case ID，写清三个字段。
4. 关联适用的 P0 Golden；新增情况未覆盖时更新组合剧本。
5. 修复后复验原反例，再执行受影响回归。
6. 记录 `pass / fail / incomplete / blocked / invalid_env`，不删除原失败记录。

## 仓库联动

`asset-index.json` 声明可呈现 Markdown；`scripts/sync-eval-assets.py` 同步文本快照与来源 hash；`--check` 检查来源是否变化。`scripts/check-eval-catalog.py` 检查稳定 ID、引用和数量。
