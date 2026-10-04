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


对应已完成批次的实际轨迹见[收口效率沿革](plans/2026-10-04/employee-closeout-efficiency-history.md)，当前交付事实见[执行表](employee-delivery-execution.md)。这些是批次记录，不作为固定账号、路径或截止的项目政策。
