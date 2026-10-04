# Employee Direct 主动进展合同

一个运行中的非automation Employee Direct Run可通过task-token范围的专用MCP提报 `report_progress(report_id, summary)`。工具只能提交候选，不能改Task、指定收件人或发钉钉；返回accepted不表示已展示。summary是执行器报告，来源绑定不将其中的业务声明升级为独立验证完成。

Host从可信queue→Run→Task→原receipt解析workspace/agent/tenant/scene.Ref/requester与当前授权。重复Run/report_id同正文返回原记录，冲突拒绝；相同内容已报告可持久Quiet，不产生重复推理。每Run最多16条新report_id，summary最多4096 UTF-8字节、report_id最多128字节；同ID重放不占新容量。每Run有硬容量边界，超量明确拒绝，不能丢数据却返回成功。原始thinking/tool/text不自动变候选。

新 `execution.progress` TaskWake经既有Host内部admission与PG journal进入EmployeeLoop；原人类请求沿统一eventrouter来源恢复。只允许reply/stay_quiet，使用当前任务状态、原请求、候选及此前真正送达的进度；无新意、只是意图、过期或不能披露时Quiet。它不消耗执行推进的governor、不写Task目标账本或长期记忆、不新增执行、不CompleteGoal。

展示决定和现有HostNotice/outbox同事务保存，Quiet也持久。发送前核同Run仍active/running、goal revision、原tenant/grant/输出scene和取消；Task完成、C/D换Run/版本之后的旧pending进度失效。progress不覆盖final；已有unknown提交仅对账，不承诺撤回已发生发送。真实delivered才进入近期Host历史和已展示上下文。

工具只挂在已有managed MCP兼容的非automation Direct claim，名为employee-progress，服务端只暴露report_progress。旧能力/旧reader不伪装支持；新producer等所有live reader支持新的canonical loop marker才受理。旧冻结快照不热改。场域安静控制仍按其foreground/background合同，不借进度修改授权或停止Task；用户原工作对中间消息的明确约定进入展示判断。

后续真实E2E必须证明Runtime实际调用producer、Loop输入和决定、Task/Run不变、原会话一次真实投递、重投/版本/终态反例。局部fake模型/PG/HTTP与构建不替代语义或提供方真实效果。

本轮local验证与真实待验入口：[implementation.md](plans/2026-10-04/employee-loop-gap-plan/implementation.md)。候选通用抽取/批量合并、首轮前台反馈、任意后台HumanQuestion均不在这次切片。源候选 reader 为 loop19；本次 release 集成保留已发布 loop19 的 source-bound steer_task / legacy alias 实现，将后台进展与纠正组合为唯一 canonical reader loop20。混版旧 loop19 不具备完整组合能力，新 producer、恢复和发送仍经既有 all-live reader 门控；不能沿用较低 marker 冒称组合兼容。迁移 10020–10023 与已发布场域参与、纠正迁移并存，不改变旧冻结快照。
