# Employee 按需事项查找

本切片只实现新事项判断的可信提示边界、按需查找及来源绑定；流式反馈、集成 marker 与发布由主代理负责。基线 `f9f5bf17e7`，现有 reader20 合同保持兼容。真实模型是否正确判断意图，交发布方在原群用原失败台词验收；本地脚本不签模型行为通过。

## 参考与选择

参考 GawkBot `71e82a1809565281cbd0bf8185d3c125b715d934` 的 `internal/team/prompt_builder.go::ruleZeroBlock`：在身份后、能力和人格之前首先区分问答与工作。移植的是有序可信边界，不移植强制所有外部读取创建 Issue 的办公室政策。明确独立新开优先于同主题复用；新工作不能复制旧报告充作执行。继续同一事项、重做继承原工具要求。Host 不以中文关键词代模型判断意图。

## 合同与边界

- 仅新冻结输入由 `TaskDiscoveryReady` 开启；旧 snapshot/config 工具表及请求字节保持原样。未开启时保留原 t1/q1 行为。
- 新普通输入不再默认注入最近五项、原始 prompt、历史 entries 或成功报告。精确 quote 仍定位本人的 q 引用，只给事项元数据，不给旧结果。
- `find_tasks(source_ref, query?)` 只在查找、续接、明确依赖旧成果时调用。query 为模型选择的目标短语，服务按 goal 的字面不区分大小写子串过滤，不自动把新请求改为查找。
- 排序层级是当前 workspace/agent/org/scene 下当前消息 requester 的匹配事项；只有本层零匹配，且已注册 scene 确认为 group 时，才查同场域其他 requester 的安全元数据。DM/enterprise 不跨 requester，scope 不跨场域和租户。每层最多五项；歧义须问人，不以最新项为默认答案。
- Shared 仅 goal/state/created/updated 与 `shared_read_only`，不得返回 prompt/entries/result。读取 Shared 也只回元数据，拒绝 history/continue/stop/steer/builds_on。
- 动态引用为本次原生调用 ID 加 `:tN`，保存在 find 的同事务 tool journal；后续引用解析检查原调用、当前 source/requester、scope 及 task 的真实权限。回放复用原结果而不重新选择候选，并重新检查授权。旧 t/q 引用继续按 snapshot 解析。

## 本地验收与交接

独立数据库 `employee_task_discovery_1004`：验证 Own 优先、Own 无关才 Shared、Shared 无材料、DM/cross-org 隔离、Shared 不可改、动态引用 source 绑定与回放、新输入不预置报告，以及新 dispatch/同 Task redo 的原机制。查找多一轮但不增加模型调用上限；find/read/continue 路径恰好最多三轮。完成后删除独立数据库并回读不存在。无预发写、部署、IM 或 Runtime 变更。

## 实现与验证结果（2026-10-04）

实现完成：事项查找读取 metadata 投影，动态引用与 result 在同一 ExecuteTool 事务 journal 内提交；Shared 消费点明确拒绝控制及材料使用；引用当前来源、调用 ID 与 scope 三重绑定。生产 builder 根据已选工具表固定 candidate 版本，避免 replica readiness 两次读取在切换瞬间把新工具和旧候选混入一个输入。`Persona.DecisionRules` 非空才前置，旧 JSON 配置省略该字段并保持 prompt 原字节。

本地 PostgreSQL 集成：五个新行为合同（独立 dispatch、不注入旧报告、Own refs/回放/续接、精确 quoted Own、Shared只读与层级隔离）及原引用、续接、builds_on 定向回归共 **16 个顶层 case 通过**。`internal/service/employeeloop` 包通过，服务构建通过。真实 LLM 与 DWS provider 由夹具替代，未跑真实群消息，也没有宣称原失败模型意图已经通过。证据位于 `employee-e2e-evidence/EMPLOYEE-TASK-DISCOVERY-20261004/`：`handler-16.log`、`loop.log`、`build.log`。独立库已删除，`cleanup-readback.log` 为 0；未留运行中测试或改共享配置。

集成方仍需：将 `TaskDiscoveryReady` 接在本批统一新 reader marker 的 readiness；未接字段默认为 legacy，因此不能仅 cherry-pick 就宣称新行为在线。合并 stream worker 对 `types.go`、Host/worker 的独立区段；聚合 SOURCE_MAP / EVALS 映射。查找是有界字面相关性，不是向量检索；未命中、多个候选都应明确/澄清。历史中的旧答案仍作为正常对话存在，不热改已有 snapshot 或清历史掩盖原失败。

真实验收待发布方：原群原话要求独立新建＋实际 Python＋新 Task/Run/结果归属；本人旧任务查询/redo（三轮内）；另一个人同群任务只显示metadata且不能控制；DM不跨人；quote只作用精确原事项；新旧 snapshot 恢复不追加模型调用、不改hash。
