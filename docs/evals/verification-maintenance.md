# SPEC / EVALS 验证组织与维护

定义权威为三份canonical JSON，贡献约束见[CONTRIBUTING](CONTRIBUTING.md)，不可变结果见[上报合同](reporting-contract.md)。本次接管/发布版本、责任人与执行结论统一记录在[当前执行表](../employee-delivery-execution.md)；分支、真实账号与环境从当波manifest解析。

## 每次发布

1. 冻结行为变更与验收范围，映射 `SPEC要求 → 场景/稳定case ID → 受影响P0`。先保留原失败条件，再确定新增边界。没有适用case时补定义，不拿相近标题冒充覆盖。
2. 每例写清角色/读取范围、可观察目标/标准答案、验证方法/错误反例。列明fixture、runner、权限、真实资源和时间窗口是否就绪。
3. 定义变更执行 `make eval-check`，与贡献规范一起检查稳定ID、来源、分类及P0引用。当前20个P0保持稳定；场景与用例可随实际风险增长，不用换人物或数字制造新例。
4. 在获准环境先跑原失败及受影响真实关键路径；其他有效且未受改动影响的证据可以复用。跨日、长等待、多真人等无法实际完成时分别报告审查结论及未证明项，不能自动签真实E2E。
5. 每例记录用户效果、来源/Task/Run、实际工具、上下文来源、权限与副作用。IM/文件、业务API、SLS、LF按依赖分别取证，缺一面只限制依赖该面的结论。
6. 冻结本地版本、定义commit/dirty/hash、选例、逐例结果及证据；`prepare`后用同一文件/同一run重试`submit`。real_e2e、mock、definition_check分开，blocked/incomplete/skipped留在分母；历史报告不随新定义改写。
7. 明确已实现、已发布、真实通过、review通过、开放问题及配置恢复。只有本波范围被证明时才推进验收门；未测和失败不因发布成功消失。

报告接口及页面协议验收有独立清单：人类身份先于workspace解析、201/同ID200/异内容409、列表meta/详情snapshot、跨workspace/服务身份拒绝、全live marker门、迁移INVALID恢复与workspace删除清理。这些接口测试通过不代表办公行为E2E通过。

## 接管后的首批验证排序

本表组织下一批范围，没有实际执行即不记通过；当前批发布与产品事实见执行表。

| 优先级 | SPEC / 场景 / case / P0 | 已有事实与下一步 |
| --- | --- | --- |
| 1 | spec-communication/spec-delivery → work-planning → office-new-goal-same-person、office-same-marker-different-goal → 依据caseRefs选择受影响P0 | 原“新蓝杉”复述旧结果FAIL。保留原Director/场域/历史/Python与sleep要求复验，不用本批新Task成功洗掉失败 |
| 1 | spec-delivery → progress-wait、work-planning → office-python-real、office-accept-ack-state / G06 | 真实接单ACK、Python、幂等工具上报及文件已证；阶段展示quiet未通过。office-running-progress是主动查询，不能冒充后台主动推送：先为新风险补稳定case与明确展示约定，再调整G06准确引用 |
| 1 | spec-learning-reliability/spec-trust → office-memory → office-memory-capture-private、office-memory-forget-specific、office-memory-withdrawn-history / G17按实际caseRefs范围 | 已有TaskWake过滤限定证据，记忆lookup与确认交互仍FAIL。分别复验事实独立召回、本人撤销范围及确认，不把History答案当长期记忆 |
| 2 | spec-collaboration/spec-learning-reliability → reminder-follow-up → office-waiting-target-reminder、office-watchdog-no-progress / G14 | 过程事实与已证短路径复用；真实提醒/长期等待不冒称通过，先检查演员和时间窗口 |
| 2 | SPEC / EVALS / 评测报告接口 | 代码已发布、定义检查通过；预发页面、协议201/200/409及权限真实验收待单独窗口，不自动撤销真实成员权限 |

已知迁移数字前缀10020–10023冲突记录在执行表。兼容已应用迁移记录的修复另定范围，不为接管而重命名已应用文件或手动操作预发数据库。

## 收件、发布与增量验证

本轮用户授权统一发布负责人持续接收本项目交付session的候选和验证请求。收到明确SHA/分支及范围后核对代码、合同、迁移/marker和受影响用例，推进语义集成与预发发布，不重复要求用户确认已经授权的步骤。存在已证关键运行缺陷时登记阻断并让交付者修好，不把不完整候选直接当可发布版本。

每个新发布、行为变更或失败反馈都对应SPEC→场景/case→受影响P0。有匹配用例且前置就绪就运行；没有匹配时向该交付session索要稳定用例、角色、判据、方法和必要fixture，独立代码核对可继续。不要因为缺用例自动声称通过，也不要用相近编号冒充覆盖。

发布与测试共享一份版本时间线。保留整轮run、case/segment检查点、原场域/历史、已发消息及证据；不终止整个runner或重建全部测试环境，不每次发布全量从头跑。发布后仅刷新变化的manifest字段，复用未受影响的有效检查。

- 在途用例继续观察和收证；发布不成为抹去真实失败的理由。
- 新步骤需要稳定reader/协议时，在该步骤准入门等条件成立，保留其他独立用例的进展。
- 发布/重启穿过关键步骤或改变了前置时，只将该例/段记invalid_env或incomplete；保留原attempt，从安全检查点恢复或只重跑受影响例，不把两个版本拼成单版本通过。
- 已经发生的外部副作用先核幂等回执，不重复发送、点击、建对象或覆盖原证据。
- 发布成功与当版验收分别报告；旧版通过不能自动升级成新版通过。

空闲时按当前环境复核尚未执行/失败/缺证的用例。优先补能运行的fixture、driver、独立读回和归属检查，再做一个有界切片；无新版本、输入、环境或证据变化时，不反复灌水重测已有通过项。缺真人、长窗口、权限、协议、工具或外部资源时写清具体env gap和解锁条件，标blocked/incomplete；可以源码review的部分单列，不能冒称真实通过。

每次有状态变化更新唯一执行表和对应证据。每次后台跟进只推进一个可恢复里程碑，记录正在运行的进程、对象和恢复责任；同一场域只允许一个当前写入者，多个交付者的独立测试分配不同场域。来源session补件和进度协调在本轮用户授权内，消息写给明确的交付session，不扩展到其他联系人或项目。
