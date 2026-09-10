# Coordinator 冻结回放与预发验收

用于用户要求把真实badcase重放、修复后验证时；普通只读巡检不自动触发模型采样、发消息或部署。

## 防止夹具制造结论

- 同时保存原环境、Agent真实配置、policy/assembly、源码SHA和实际模型请求；指定测试Agent原配置与生产岗位叠加组分开。
- 工具返回按完整查询语义匹配：q、since、limit、issue/CID。省略参数只按Host默认值匹配，不能让q-only规则抢先截获7d请求。未捕获组合返回unavailable，不当empty；修正夹具另起版本，原结果不覆盖。
- 先检查读取如何注入。`context_read(kind=coordination_state)`走Host Queries reader，不走普通ToolExecutor；Queries=nil只能证明未加载分支。只有真实持久化job快照才能验证成功计数路径。
- “工具可返回某限制”和“限制已进入模型请求”不同。visible/hidden对照必须逐份核验实际request；对照之外保持输入一致。明确恢复失败任务与催促运行中任务分开，不强制模糊“继续”必定派工。
- Go退出0、finish被接受、reviewer allow都不是语义PASS。逐项核对目标UUID、当前引用、basis、约束交接和独立交付数量；未知项保留INCONCLUSIVE。

## 新读取证据的界限

技能目录Managed占位可由有界frontmatter用途修复，不能把正文流程当作协调指令。记录实际模型看见的目录，fixture直接传Turn.Skills不会测试FillSkills数据库路径。

Issue.status、work_state.latest_execution、外部送达是三种事实。最近执行completed不代表已发消息；not_loaded/unavailable不代表没有任务。coordination_state最多前3个同场域窗口，计划数和持久确认数可为null，不能当整个会话总量。只拿assoc_recall的3项候选不能证明“刚才派了3项”。

混合actions逐项查真实审查职责：既含工作又含状态/澄清时，不能让合法工作把错误非工作回复一并带过。看generation实际选中的模块、mode和work_checks，不只看注册表。

## 发布与实际验证

按用户授权fetch最新远端并语义集成，记录实际run/构建/部署与运行版本。用已核验dws-env身份触发指定预发对象；发送回执、任务入库/开始/完成、DWS实际结果分开计时和判定。

运行中的共享Langfuse trace可能尚未包含全部agent_task generations。先补task-trace，完成后再读精确trace；不能将早期空快照当作未执行。答案内容正确但执行器绕过岗位规定的工具，仍不能认证整条E2E通过。

原始数据和模型请求保留仓库外私有目录，提交聚合报告、脚本和脱敏合同，不提交凭据或生产正文。
