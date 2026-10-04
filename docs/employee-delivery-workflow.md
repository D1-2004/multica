# Employee / Tag 领域交付入口

先执行全项目 [开发与有界交付合同](development-delivery.md)：明确验收标准、E2E场景、测试环境和本轮流程后再实现。这里仅补充Employee/Tag跨IM、Host、Task和Runtime的行为边界，不要求个人skill、固定分支或特定checkout。

## 领域验收标准

- 用户效果：实际答复/文件/动作、次数、格式与时间事实；介绍卡、ACK、模型自述不代替交付。
- 工作归属：可信来源receipt/requester、Task/Run、scene.Ref和当前输入。新事项、续接、停止及等待答案都需正确归属。
- 执行继承：原请求要求的工具/方法、约束与已做副作用延续到后续Run；答案正确不证明实际执行。
- 记忆来源：长期记忆、近期History、原话和工具事实分开。若验长期记忆独立使用，近期历史仍含答案不能签完整通过。
- 权限和状态：当前tenant/principal、取消/迟答、幂等、混版恢复和隐私撤销；已存在真实失败不能被普通回答质量抵消。

每项只取证明上述相关条件所需的证据；不用无关的完整矩阵。环境依赖缺失时记录未证明项与检查点，并提供交接，不擅自将真实效果要求改成代码审查通过。

## 环境与执行

涉及真实消息、发布或Runtime时读取 [领域操作合同](employee-delivery-operations.md)。固定本波版本、对象、能力、身份与场域；具体账号/群、分支/Runtime、profile及发布run属于当次manifest，不写入永久规范。

操作按需读取本仓Aone、FC、LF或Coordinator skill；DWS身份与产品命令使用环境中可用的对应工具/技能。缺少个人安装的skill不阻断只读审查和文档交付；需要真实执行但缺工具/身份时明确环境缺口。

共享发布/Apply和真实测试窗口有明确负责人；实现、独立审查和只读取证按各自边界推进。产品行为、观测完整性与Runtime状态分别签收。长等待及新发现按通用合同形成检查点和接手包。


对应已完成批次的实际轨迹见[收口效率沿革](plans/2026-10-04/employee-closeout-efficiency-history.md)，当前交付事实见[执行表](employee-delivery-execution.md)。这些是批次记录，不作为固定账号、路径或截止的项目政策。

SPEC / EVALS相关交付按[验证组织与维护](evals/verification-maintenance.md)映射需求、稳定用例与受影响P0；定义、代码发布和真实验收分别报告，当前责任与结果仍以唯一执行表为准。

持续收件与发布采用[增量验证规则](evals/verification-maintenance.md#收件发布与增量验证)：测试不中断整轮、不全量重启，版本穿窗只处理受影响用例；缺环境明确标注，缺对应case向交付者补件。
