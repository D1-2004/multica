# Coordinator 有限动作、协调视图与短合同

## 目标与授权

用户要求按1→2→3推进：收口通用reply(text)，让start_work等有限动作内聚LLM回复；提供专用协调事项视图；在Agent定义中维护短协调合同，并控制上下文。沿用本会话提交推送及预发部署授权；不做业务E2E、不发布正式、不修改线上Agent定义。

## 实现合同

1. 模型只调用finish(actions[])。入站动作固定为start_work、continue_work、clarify、report_status、acknowledge、describe_capabilities、report_memory、decline、ignore；task_finished只允许report_result或ignore。decline仅以scope/authorization/privacy解释边界，并逐字引用适用constraint_quote。开始/续接等动作拥有自己的reply，删除模型的action=reply逃逸口。Host验证动作字段、引用、来源覆盖和字数预算后，沿既有域状态提交/回执；旧持久化checkpoint保持幂等，不使运行中的旧任务重放。
2. 每项保留source_refs与对应回复；当前窗口完整有去向。多任务、混合澄清、短确认与状态查询不能因收口而丢失。任务开始/续接回复在对应工作已入库/排队后送达，审查输入明确该保证，合法受理回执可以通过，但不证明执行完成或外部送达。审查候选只给candidate.actions并标Host action_ref；必填work_checks为每个work action恰一条single/multiple/none，纯非工作为[]；Host不放行multiple/none的allow。
3. assoc_recall改为专用协调视图：目标、意图、真实状态、等待对象、更新时间和可读取状态引用；默认不返回聊天事件、评论正文或业务结果。提供work_state读取当前合法事项状态，保留unknown/partial和截断信息；旧task_finished读取当前结果的边界单独保留。
4. 主循环按Host保存的最新读状态重建上下文，不无限累积历史工具正文。保留完整可信当前窗口、原水位和最新修复反馈。托管读快照8000字符、最新反馈800字符与最近被拒提案6000字符分别计预算；提案保留精确JSON，超限或非法时显式omitted，不累积旧提案。Reason保留具体诊断，不能被边界摘录替换。限制候选数、字段和历史，不能静默截掉用户当前授权限制。
5. 增加agent.coordinator_contract JSONB，schema v1包含scope、must_delegate、constraints、clarify_when及Host绑定的instructions hash。合同总预算1600 Unicode code points，超限/未知字段拒绝，不做运行时摘要。合同只能收紧平台有限动作；有效短合同服务协调层，执行器继续持有完整SOP。缺失/过期/无效合同显式标记，保留旧完整岗位审查，不能解释成无限制。未配置短合同时，legacy完整岗位审查仍有全文上下文成本，本轮不增加截断或硬失败门槛。API/CLI/复制/Git与DTA来源/模板必须roundtrip；不自动替现有线上Agent编造合同。
6. 同步模型schema、Host、提示词模块、来源映射、案例、可观测字段与内建管理Skill。可选constraint_quote只有<=200字符且逐字真实才可作反馈；无效摘录丢弃并记录finish_check_boundary_quote_discarded，allow携带真实旁证不使整个裁决失败。必填request_quote_ref/candidate_quote_ref只能选本轮Host quote_options的qN/cN；Host绑定精确原文并保留RequestQuote/CandidateQuote记录，自由抄写引文不再是模型输入字段。verdict和missing_source_refs仍严格，decline自身的真实边界校验不变。有限动作和独立审查仍需语义验收，不宣称仅靠schema彻底杜绝误路由。

## 验证与交付

- Host验证旧reply输入拒绝、每种动作字段隔离、非法引用/混合窗口/新建续接/状态与结果回读、上下文预算、合同校验与roundtrip。
- 真实模型冻结回放覆盖原AI听记trace与问候、澄清、状态、起草限制；所有业务工具为假只读工具，不产生任务/消息写入。
- Agent API及共享schema做最小验证；policy结构检查不冒充模型行为证明。
- 提交推送工作分支，预发发布前检查占用，按精确run跟踪、语义解决冲突、核对构建SHA与健康；记录未配置合同的迁移边界和未做E2E的限制。

## 进展与结果

主体实现及模型验收完成；预发基线语义合并与新包接口兼容已完成，正在构建与发布验证。最终七例真实模型回放全部通过，详情见 `docs/reports/2026-09-09-coordinator-closed-actions.md`。

最新诊断修复：真实冻结回放暴露多意图窗口中的错误合并与修复漂移。工作审查改为先读整窗全部原文，短quote只作佐证，source_ref不等于意图数；Host另保留最近被拒提案和原始Reason，使下一轮能只修具体错误字段。新增metadata `repair_proposal_runes / repair_proposal_budget`，可选边界摘录丢弃单独观测。三例模型回放仍待验证结果，不把Host修复或文档同步记为模型已通过。

- 新字段迁移改为 `9164_agent_coordinator_contract`，避开预发已占用的9159–9163；不重编号既有迁移。
- 预发合并保留目的描述中DWS身份/MCP/Skills等合法对象及精确错误反馈，新输入仍仅有限actions；原故障和失败回放不改写为通过。
