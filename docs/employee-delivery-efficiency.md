# 提示词架构与副作用边界

## 共享架构

```text
AGENTS.md：统一入口与资源地图
CLAUDE.md：项目工程硬约束
development-delivery.md：验收/E2E、环境、流程、边界与交接
模块合同：领域规则，例如employee-delivery-workflow.md
专项SKILL.md → references：当前操作及按需细节
任务/Plan/执行表：本次范围、配置、当前状态和证据
```

个人记忆与上述共享结构分离：个人开发模式、工作目录、分支、profile、提交偏好和特定会话预算留在本机；不能成为其他开发者的必备依赖。历史事故和Plan提供依据，不自动作为现行规则。个人skill可帮助定位项目合同，但不是项目规则的唯一入口。

## 本次消除的副作用

| 副作用 | 共享规范的处理 |
| --- | --- |
| 个人四种模式被当成项目固定流程 | 移除模式表；直接以用户结果、E2E判据、环境和交付里程碑定义任务 |
| 到时间即降为review通过 | 时间只决定停止/交接；运行效果仍按E2E证据判定，未证明项保留 |
| 一套真实验证强加给所有改动 | 定义相关用户路径和风险；静态文档可以E2E不适用，业务链路仍需相应证据 |
| 当前表规则制造新文档负担 | 沿用已有任务/Plan作为状态入口，小改简短清单即可 |
| 无界交付或过早丢下任务 | 在约定边界给可审查成果、真实结论和明确接手/恢复责任；核心失败阻断签收 |
| 旧分支/profile或最新SUCCESS被误当目标 | 从本次环境解析，核对不可变来源和精确run，不绑定个人默认值 |
| 只修入口而下层参考仍触发写操作 | 参考命令也遵守授权范围、精确目标和有界重试；权限缺失不自动部署改权限 |

## 不能降低的条件

身份/场域绑定、使用时授权、事务和幂等、秘密保护、迁移与多副本兼容、真实运行canary、实际用户投递仍由项目/模块合同约束。复用有效证据不代表长期相信旧权限；code review和mock不证明模型选择或跨系统效果。人工接手也不等于验收通过。

性能与效率按质量约束评估：是否早产生可审查切片、是否及时更新当前状态、是否重复扫描/构建、是否明确控制范围与外部等待。没有实际对照不能声称提速百分比。

设计参考 [OpenAI skills与提示词分层](https://developers.openai.com/blog/rethinking-skills-and-prompts-for-gpt-6-astra)、[AGENTS.md](https://learn.chatgpt.com/docs/agent-configuration/agents-md) 及 [Claude Code按需skills](https://code.claude.com/docs/en/skills)。独立场景审查记录见 [delivery-instructions-review.md](evals/delivery-instructions-review.md)；它是开发面指令验证，不是产品运行验证。


## 历史收口效率纠正（2026-10-04）

# Employee 交付效率与收口纪律

2026-10-04用户基于本次实际轨迹提出以下五项纠正。本文件限定Multica接续交付，不替换架构/权限合同；当前状态唯一入口为[执行表](employee-delivery-execution.md)，历史证据保留。

## 1. 范围不断扩大

轨迹：第一次候选e442/run3110364914之后修history unavailable与错接Task；第二版fa8302/run3110367125之后发现口算绕实际Python、记忆历史归因；后来又加入SLA、跨源withdraw、Runtimegzip及cold/warm。第三版f4c/run3110369623/epoch17已部署，但“收尾”不断变成开发。

执行规则：开始列冻结能力与硬门、可接受限制、硬截止。新发现写severity/影响/具体file-function/后续动作，分阻断与可后续处理；非阻断登记后结束。阻断只有在截止内可验证的局部修复才处理，跨模块新能力建立后续任务而非隐式并入。新部署是里程碑，不能自动开启另一轮无界完整开发。完整R5开放项保持单列。

## 2. 一次检查完整行为

轨迹：BASE数字55正确，先错接旧f1，来源修后无新Run口算，方法继承修后明确开新又continue旧e0；MF33min答案正确却原句/旧assistant仍在History，42非@仅挤掉transcript。原失败与每代证据见THIRD-TASK-REMINDER、M8-MF。

执行前一次性列清同一验收清单：用户效果；来源receipt/requester/scene与Task/Run归属；是否要求实际工具、方法继承与真实stdout；模型输入来源Memory/History/O/工具是否独立；权限/拒绝；幂等/取消/迟答/恢复；所有副作用与无副作用窗口；版本/配置/演员/时间条件和清理。数字/ACK/Job completed不代替这些。缺任何关键前提记invalid/incomplete，禁止vacuous pass。新增症状先对照同一清单，避免一层一层改口径。

## 3. 唯一当前执行表及时更新

轨迹：23报告草稿停第一次版，共享resume停第二版，实际epoch17第三版。接续者需重新拼多目录。

执行规则：[employee-delivery-execution.md](employee-delivery-execution.md)是唯一当前表。每项写版本、状态、分面结论、证据、阻断和下一步；责任人状态变化立即更新，主代理里程碑批量核一致性。Plan/旧report/shared进度保留沿革但醒目指向唯一表；每次发布完成后同步源码/release/run/startup/配置，不能只追加历史而不改当前摘要。最终报告由表生成，不能独立漂移。

## 4. 按里程碑编排与批量读取

轨迹：用户统计本轮主代理约374命令、271次执行调用、130次子代理消息/追加任务（该数量是用户提供的近似轨迹，不宣称独立重算）。数量本身不证明浪费，反复读取同版本、碎片追加指令与等待窗口迟释放已造成实际成本；COL DM预计3分钟，释放迟滞使BASE执行者多次等待。还有取消regex日志PASS但[no tests to run]，最终不能作验证。

执行规则：独立读用批量并行，依赖/变更顺序处理。一次给执行者完整输入、所有权、当前版本、scope租约、全验收条件、交付物、截止和禁止项；只按明确里程碑收结果，避免逐条追加。只读取证不继续占业务场域，业务最后消息终态即显式释放。固定精确run，事件/有界后台watch，状态不变退避；禁止重复触发CI。已有通过且未受影响检查复用，新增测试先说真实风险；必须检查实际测试名/数/skip/exit，no-tests不算pass。等待Actor/跨日不循环口述“仍推进”。

## 5. 产品、观测与Runtime分面

轨迹：G5实际stats→retro/builds_on/IM已有产品链，后台LFgzip损坏却引出rt-dsh代理、独立CI314792/run77295044、候选Template53zkm及cold/warm，延长主交付。候选genericPi完成不自动认证Employee direct，current461没有切。

执行规则：分别写产品行为、观测完整性、Runtime修复门。只有缺证妨碍关键结论才阻断该项，其他列限制/后续；不拿可读assistant事件冒充完整generation，也不把I/O缺口反向否定已证的真实执行。Runtime source、CLI source、Template、Runtime ID独立固定；候选canary/兼容范围明确。修复产物可单独交付，不自动cutover、重测或并入主收口条件。

## 本次纠正的实际落地

08:14冻结范围；复杂跨日/长等/多真人改源码review验收，review不冒称E2E；当前表统一epoch17；剩余P1/P2登记位置与动作；停止新增真实测试/镜像/部署。08:38已回读hourly enabled=true、Canary Agent已归档且active list无该对象、Canary Issue GET404。DELETE未取得204，不冒称删除执行成功；hourly下一次实际触发未验。最终报告已落盘。其他session主checkout WIP保持不动；本文件只写实际交付worktree。
