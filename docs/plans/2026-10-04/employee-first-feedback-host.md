# 首轮公开反馈 Host / outbox 切片

参考 GawkBot 71e82a1809565281cbd0bf8185d3c125b715d934 headless_live_chat_relay：公开正文先于工具结果，过滤未解析工具内容；PRI-101 是反馈交互证据，不是底层取消证明。本切片使用来源绑定的完整 native first_feedback 帧，不透传 thinking / 半参数，不增加展示模型。

资格：仅新构建的首个模型请求、单一真人来源，公开一句≤80 Unicode字符，lookup意图；自动化、reaction、多来源、安静、失权及终态拒绝。资格取持久化 job/model_journal 和 admission，不由模型自行宣称。稳定键 job+receipt，独立通知及outbox和tool journal同事务。正文冲突不覆盖。反馈无terminal、Task/Run或Router callback。

发送：专用 running gate 反向校验冻结 action / source / scene / principal；普通最终回复 completed-only gate 保留。发送提交前pending由最终Complete或quiet抑制；unknown/accepted沿原查询不重发，不阻塞final。已提交与第三方到达顺序不能绝对保证。滚动期间新producer必须所有reader21就绪，旧worker不能消费新sourceKind。

验证：独立 PostgreSQL employee_first_feedback_1004，非真实provider/IM；测试早帧、重放/正文冲突、来源/权限/quiet、多来源、final取消、unknown不重发与callback不关闭。Root集成SSE与Loop，发布方真实模型/IM验收。本切片不部署、不push、不改Task查找。

## 实现与交付结果

- 已实现：独立 first_feedback Host schema/资格，ExecuteTool和notice/outbox同事务savepoint；稳定键job+receipt拒绝内容/native ID变化；独立非终态ActionInput及专用running发送门；Complete事务取消pending；fresh provider submission的CAS避免guard后final取消再复活。最终消息沿原通道，不依赖反馈投递。optional notice故障回滚后作为ErrToolRefused，仍可执行业务read；lease/journal/tenant权威错误保留硬门。
- 迁移10040新表/10041唯一并发index均已在独立本地库实跑；无FK，workspaces显式清理。
- 本地验证：6个Feedback顶层合同测试（含8个资格反例、3个pending/unknown/accepted子例），2个既有Participation回归通过；4个outbox顶层测试（含unknown/delivered子例）通过；完整dingtalkresponse包回归通过。非空日志在`/Users/yuanzhan/d1/employee-e2e-evidence/EMPLOYEE-FIRST-FEEDBACK-HOST-20261004/`，真实模型/IM/SLS/LF未跑。
- 环境恢复：employee_first_feedback_1004已drop，pg_database回读计数0；未动默认业务库、预发、账号、Runtime。无后台测试残留。
- 交接：Root负责SSE帧/完整工具batch、首轮工具移除、lookup只读计划和版本21整副本门禁；实际模型自然句、strict-format/直答/dispatch zero-feedback、真实IM早到及终态独立仍由发布方验收。已提交unknown第三方到达顺序与quiet/revocation刚发生于权限检查之后的跨系统窄竞态不承诺绝对撤回。
