# Tag A2UI 人工答复闭环实施

2026-10-04。当前开发分支 `codex/tag-a2ui-human-loop-20261004`，基线 `aone/feat/tag-multitenant@f4c4790cbea1066857f96d4b6468c9710d77646b`。本轮为开发与独立本地验证；不自动发布共享预发或发送真实IM。

## 已确定产品合同

- Employee前台可发询问；Pi默认在Run结束后输出文案和可选问题，不阻塞等人、不恢复旧session。
- 单选、多选、人员歧义用当前A2UI投影；ChoicePicker可承载已核实候选，原生UserPicker不等于冻结候选。
- 按钮和普通文字均为正式输入，原话约束保留，复用同一个Employee Loop与模型预算。
- 每个问题保存Host绑定的owner/tenant/scene/source/requester及可选Task/Run/目标版本，事件只提供问题引用，不能指定工作归属。
- 答案保存与下一步消费意图同事务、重复事件不双派；多题歧义追问，新话题不关闭所有问题，停止/过时目标不复活。
- 任务轮末结构结果明确区分clarify与suggest；缺信息仅表示本轮结束，不宣布原目标已完成。没有选择不自动批准，不新增复杂过期状态机。
- 随机短反馈设计保留，源受理/重投冻结同一句；loading只在真实渠道支持时投影，不能凭示意图宣称已实现。

## 架构依据

GawkBot固定来源 `71e82a1809565281cbd0bf8185d3c125b715d934`：按钮与引用文字共用答案变更；人话优先并明确唤醒等待/完成事项。采用其统一输入与定向唤醒，不照搬30分钟工具poll或新消息取消同频道全部interview。比较证据见 `tag-human-text-gawkbot-comparison.md`。

当前规范：根CLAUDE.md、docs/employee-loop.md、docs/employee-delivery-workflow.md及对应模块合同。首批复用持久scene jobs、原工具journal、三请求预算、response outbox、Run事实消费；不用第二个推理Loop。

## 并行任务与范围

只读查看在线任务：Gap任务在开发B/C/D/进展；Eval任务在调整黄金场景呈现；原交付任务刚验epoch18及四项修复。这里独立开发A2UI/HITL；不改它们的测试身份、Runtime、场域、共享配置或未提交代码。集成前重新fetch并检查语义冲突。

## 当前执行表

| 切片 | 用户效果 | 本地验收 | 状态 |
| --- | --- | --- | --- |
| R：结果契约与提示约束 | Pi轮末明确输出summary/choice，普通文本兼容 | 严格schema、非法选项/引用、不把stdout/Markdown误判为控制 | in_progress |
| Q：问题与答复账本 | 每条答复明确原事项，按钮/文字统一去重 | 权限、场域、Task版本、竞态、事务重放 | design |
| F：前台工具装配 | Employee发询问后释放本轮 | 冻结工具schema、Host来源、幂等发卡意图 | design |
| C：回调与文字回流 | 两种输入进入对应Employee Loop | 两题隔离、无引用明确答复/歧义、不会伪造授权 | design |
| S：轮末卡片与下一轮 | Run结果投影选项，人答后fresh Run | 精确Run/Task、停止/旧卡、已有副作用不重做 | design |

## 验证策略与停止条件

先独立本地数据库/Redis和fake model/provider，禁止共享库与ambient真实agent CLI。新增测试只抓权限、关联、事务和竞态等真实风险；Golden E09–E12文字案例需要生产接线后在真实DWS路径验，真点击需客户端单独验，不把模拟写成真实E2E。每个切片记录implemented/integrated/deployed/e2e_verified和未证明条件；当前只有开发授权，发布与真实验收随集成阶段安排。

## 本地实现检查点

已完成R/Q/F/C/S核心接线；通过独立PG15434的32个顶层handler用例（含人工交互/原通知与交付约束回归），全部81个命名用例无fail/skip。协议、entry和transport切片的独立测试另见证据。这里是scripted model/provider，不是真实Pi/IM/模型语义验收。审批权限扩大、引用loading与即时随机反馈的生产接线不在此代码切片中。

独立审查发现并修复：cancel答复不能吞掉原stop_task输入；notice先scene后Task防锁序反转；BeforeSend检查旧目标/停止以抑制过时卡。工作区显式清理问题、响应及关联A2UI记录。

基线检查局限：migration lint已有23处历史重复数字前缀；workspace deletion manifest的旧schema分类大量缺失。新9977–9982与问题表已登记，不把这两项基线失败写成通过。一次cmd/server的go test -run空集仍触发TestMain，默认本地库上在本轮新加nil发送hook处panic；已修nil guard，并改用compile-only/显式独立DB，不能算该轮成功。子代理一次默认DB测试也因旧schema失败并清理，之后强制独立环境；均无真实IM或沙箱执行。

当前检查点：完成最新remote同步/rebase与受影响检查后交付可审查代码；预发发布、真实DWS/Pi端到端和客户端点击属于下一验收里程碑，保留E09–E12及新10条候选场景，不自动占用其他在线任务的环境。

规范更新：远端4674169c3d带入docs/development-delivery.md统一开发合同；“提示词入口”是开发指令AGENTS/CLAUDE入口，不新增产品提示词平台。本轮保持原kernel提示词与历史快照，只在Host冻结结果契约/工具。Eval候选按在线Eval分支8e7f4a447d的roles/verifies/method贡献规范编写，canonical集成待该分支落入集成基线。

环境恢复：cmd/server默认测试库panic留下的本次合成fixture，仅按确切UUID和04:10:49 UTC创建窗口清理（workspace/member/agent/runtime/user）；没有按全局slug清理他人数据。compile-only `go test -c ./cmd/server`通过，不再执行未显式指定DB的TestMain。
