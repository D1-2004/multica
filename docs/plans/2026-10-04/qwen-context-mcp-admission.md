# Qwen-DWS 近期对话、个人 MCP 与受理失败

## 目标、环境与边界

分析冬翔与 Qwen-DWS 2026-10-04 的真实单聊，修复短确认未承接最近提议、个人 MCP 查询不一致和仍存在的受理失败。基线为本轮抓取的 aone/feat/tag-multitenant；DWS身份为主角/夏东翔，网关prod；员工后端预发。只读取已有业务消息和执行轨迹，不发送业务测试、不修改远端能力、不自行部署。原始证据存于仓库外权限700目录 `QWEN-DWS-20261004-context`。

## 验收与流程

1. 用同一时间窗口的 IM、Langfuse 与 SLS 定位每种失败，区分配置、装配、模型决策、执行与投递。
2. 先更新行为合同，再做最小完整修复；保留当前来源授权，最近助手提议只能作为解释当前确认的材料，不能自动执行历史提议。
3. 本地验证聚焦个人配置读取、短确认上下文传递、受理失败具体原因及权限反例。测试应覆盖真实行为风险，不补无关矩阵。
4. 提交 Aone CR 到 feat/tag-multitenant 并交接给「发布协调」。未部署与未做真实模型/IM验收单独列出。

真实发布后需复测：具体只读 MCP 探测提议→本人「好」应实际调用；单纯致谢、多个提议、他人确认不得扩大动作；个人 MCP 展示与执行一致；取消当前后台任务不落通用兜底，原执行停止/副作用状态有证据。实现、本地通过与真实通过分别签收。

## 进度

- 已对齐当前交付分支；旧 checkout缺现行合同，未用于判断运行行为。
- 单聊15:00–22:00读取97条，DWS返回complete=true；LF窗口与原始证据正在核对。
- 已核实源码缺口：scene_config_get原字段仅返回场域层；新dispatch WorkPacket的History固定unavailable。
- 实现与本地验证完成；CR待交接；部署及真实回归未执行，部署归发布协调。

## 已确认的根因与证据

### 1. 「好」没有承接最近助手提议

21:10:35 助手提出用 dx-grok 跑一次探测；21:10:46 冬翔答「好」，21:10:49 只回「好」。[对应 trace](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/975efe12fee04f3f9e533a80fed4da1c) 的 employee_model 使用 deepseek-v4.1-flash、employee-fast-v1、thinking disabled，completion只有1 token，没有 dispatch。

该 generation input 的messages部分**确实含完整提议且为最后一条历史assistant**，之后是当前「好」。因此此例不是历史排序颠倒或上一句话漏传。输入80501字节、Langfuse尾部被64KiB属性上限截断；可见的message片段仍足以确认上述事实，但不声称完整工具表可见。现行RecentConversation保留最近20条、24小时且按时间正序，当前窗在最后；问题是当前意图规则强调先独立判断当前请求，未把接受最近具体提议作为请求的一种形式。

修复应增加一般性的当前意图解析规则：本人当前确认承接最近相关且尚未执行/取消的单一提议，继承具体目标与约束；不要只回同一个确认字。不按关键词自动派发，不把旧助手提议变成授权；歧义、他人确认、致谢或缺历史不得扩张动作。保持当前source_ref。另有独立Host缺口：dispatch工作包固定HistoryUnavailable，后台只得当前原话与模型改写，必须改为传已冻结的有界对话并保留四态；不在工具事务内重新读历史。

### 2. 个人 MCP 查询视图和实际执行不一致

[21:07:19 trace](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/5b89de4b06814242bdaa3d97ee45251c) 中scene_config_get给`mcp_servers=[]`，模型据此说没有远程MCP。21:18群聊[配置查询](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/e70d71c5809e4e86a8d30433cc9f151c)及[个人能力追问](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/c60cfbddc46248cd91f0979f90de7d90)也发生相同混淆。

代码有两种视图：启动目录与执行器使用global/org/scene/person层叠；scene_config_get原字段只读scene。仅给sceneConfigTarget补PersonKey不足以修复，因为sceneConfigGet本身也只读scene层。修复保留可编辑场域视图，再增加明确标记来源层的effective_context个人继承MCP投影，不能回显连接串/header/token，也不能让空scene列表覆盖个人层事实。

[21:11执行 trace](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/00405ddb493943028b9a4894fd1a8fb3) toolset含dx-grok、dx-workspace与dongxiang_personal_workspace；实际调用dx-workspace.issue_list得到真实工作区事项。这证明至少该轮个人/场域MCP已装配且工作区读取成功，不能总结成“个人MCP全部未生效”。

dx-grok.describe_agent确实调用失败，模型裸协议探测报告Agent profile is unavailable。代码错误是简介复用GetPublishedAgentA2AEndpointByPublicID，其SQL附带A2A runtime adapter资格；MCP元信息读取并不要求Agent能执行A2A。修复使用当前已认证endpoint的绑定查询，仍校验endpoint/Agent/workspace/owner/归档/成员；不放宽delegate任务执行资格。真实目标runtime元数据未独立回读，不能断言本例一定由某个runtime种类触发。

远端回复中的“HTTP200所以不需要授权”“所有业务工具都不能用”证据不足：MCP工具业务/权限失败可通过isError=true返回；不存在的probe task或另一连接的Issue报not found本属正常隔离行为，不能作为整个服务坏了的证据。[MCP工具规范](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)明确区分协议错误与工具执行错误。当前修复保留权限检查，不把网络成功解释成完整业务授权。

### 3. 同一句兜底来自不同故障

| 时间（北京时间） | Trace | 实际失败 | 当前处理 |
| --- | --- | --- | --- |
| 17:35 | 见既有employee-canonical-result调查 | 合法round-result JSON被展示解码破坏，任务没有收口、continue拒绝ready | 最新目标分支已有修复，不重复移植 |
| 21:14:01 | a87c19ae132f459fa715082cebfc62bb | quote已有q1定位；模型find→read→stop选中同一Task，但find生成的binding没有quote Origin，Host正确的quote闸门拒绝它 | 只给同源/同requester/同Task且已冻结验证的q候选继承Origin，重放再次复验，保留跨任务拒绝 |
| 21:14:39 | 17cd6a96924647388d34089a9cd48da2 | find把“PRI-104 e2e GitHub”当一个literal substring，无命中→无query重查→read用完3次模型预算；没有调用stop | 工具明确引用已定位则直接read，查询用连续goal片段；不增加预算掩盖查找问题 |

第一取消引用的是接单消息msgVLptuiv5JmbcZ3TzJLRABQ==，并非进度消息；第一轮Host已经提供正确q1。修复不要删除quote来源约束，或直接让任何discovered Task可控制。前台兜底本身不是网络异常的分类，判断必须读tool错误与模型轮次。

## 覆盖与证据限制

- DWS 15:00–22:00单聊97条，complete=true、hasMore=false、failures=[]，没有发送新消息。
- LF agent维度17:00–22:00查询71条employee_loop，跨单聊与测试群；raw/unique均71、无分页截断；重要trace另取详情。不能把71当单聊轮数。
- LF外部CID查询21:00–22:00只返回4条agent_task，因为employee_loop的session为内部scene UUID；这是索引语义差异，不是前台trace丢失。
- SLS预发21:00–21:25 employee_scene_job_completed 33条，包含场域/worker/轮数；不使用旧Coordinator事件查询结果为空证明没受理。
- assistant_provenance_omitted、记忆证据撤销及历史裁剪在部分轮次存在；本例关键提议可见，不用解除隔离来增加上下文。
- 原始证据及MCP连接串只存仓库外；报告仅保留定位键。未进行真实模型回放、远端新探测或IM回归；发布与真实效果由后续责任方分别确认。

## 本轮实现与本地验证

- 已实现：marker 23；新输入冻结短确认语义及recent_conversation_v1投射，旧输入不升级；新派发携带前台已有历史；个人MCP有效配置投影；Hosted简介读取与执行资格分离；已验证quote来源保留与重放复验；停止失败保持诚实且保留原状态变化分支。
- 追加已证实缺陷：场域MCP配置展示原mask只遮query，却留mca2a_/wmcp_/sct_凭证路径。本轮仅修展示脱敏，不改变保存值、访问URL或既有凭证；不回显原秘密、不修改远端配置。
- 定向本地验证：独立PostgreSQL数据库qwen_context_20261004，迁移至10121；单进程handler **47个顶层测试通过，0失败、0跳过**。覆盖冻结history工作包与effect重放、旧generation/journal、个人/混身份配置、Hosted endpoint归属/归档/成员反例、同任务quote取消及无验证拒绝、停止失败和路径脱敏。
- 首轮测试39个通过、2个夹具失败；真实用户FK夹具和旧标题断言已修正，最终上述47项复跑通过，不将首次失败隐藏为成功。
- employeetask/compiler与employeeloop/history相关窄测试通过；server构建通过；git diff --check通过。mock/scripted模型与本地SQL仅证明组装、权限与状态合同，尚未证明真实deepseek对短确认的正确选择或远端Grok执行恢复。
- 不增加三轮模型预算，不改变低延迟模型配置或Runtime；不重放原业务请求、不清历史、不修改账号/能力或发测试消息。
- 提交范围仅本session代码、合同、源映射与调查记录。合并/部署交「发布协调」，本轮不部署；所有live replicas须到marker23后再验证原场景。新旧协议混版受现有fence保护。
- 独立LLM源码审查完成：未发现阻断项；确认旧快照/journal、quote来源、MCP权限和混请求者投影边界。独立审查没有运行真实模型。
- 本地隔离数据库已删除；原开发库、DWS环境及远端场域配置未改动。
