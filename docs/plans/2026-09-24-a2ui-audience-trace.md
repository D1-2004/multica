# A2UI 姓名放量、选择观测与监听加固

## 目标与范围
- 保留数字员工 A2UI 总开关，增加手工姓名名单；可信入站提问人姓名去首尾空格后精确匹配，空名单不放量，未命中沿用正常 Coordinator。
- 卡片问题、备选项、实际选择与后续解释能在原 Coordinator Langfuse trace 内按同一 decision 关联。
- 审查并修复监听 ready、租约、重连和回调持久化中的实际缺口。
- 结合历史执行证据确认姓名来源；针对性验证后提交并部署 Aone 预发。

## 分工与步骤
1. 根任务：历史 Langfuse/SLS 只读调研、合同/证据同步、集成验收与部署。
2. audience：配置/API/存储/共享设置与 Host/collect 名单判断。
3. trace_listener：选择观测与事件监听加固。
4. 集成验证并经 GitHub Git Data API 提交，Aone 66 发布，检查部署及健康。

## 验收
- 命中、未命中、空名单、空姓名、多个姓名及混合 collect 有明确行为。
- 选项 ID/label/custom 与 decision/trace 关联；拒绝或重复回调不冒充用户首次选择。
- 回调 ID 身份校验、事务去重不退化；断流/失租退出并可恢复。
- 预发发布精确 revision 和健康有证据；本地测试与真实卡片测试分别报告。

## 进度与证据
- 用户确认首批名单仅「冬翔」。预发工作区已有两个开启 A2UI 的 Agent；发布前临时关闭总开关，部署所有副本后设置冬翔并恢复，避免旧副本在滚动期间全量发卡。

### 历史调研
- 2026-09-21 00:00 UTC 至查询时，预发 SLS `inbound_coordinator_llm_request` 共 48 条，未截断：冬翔 2 条、须莫🥥 33 条、须莫(须莫🥥) 2 条、Web 空姓名 11 条。姓名可用，但显示名变化需在名单中显式补别名。
- Langfuse trace `30d30a33d5394ee689cf647d81ef568c` 有原始 `propose_choices` / `review_choices`，无实际选择节点；PG 导出保留真实 custom 取消及 not_executed 状态。根因：提交解释使用新 trace ID。
- 现网预发健康回读：消费者 ready=true、租约 live=true；23 个历史决策中 send_unknown=3，duplicate_deliveries_total=17。历史未知发送保持不重发。
- 实卡验证准备使用 e2e-verification/dws-env/dingtalk-chat；主角查询指定 A2UI 测试群遇到 DWS CONTEXT_PROCESSING_ERROR（Trace ID `213d1ca017902194073495207e08f4`），未发送测试消息，不能声明实卡验收通过。

### 本地验证
- 姓名/collect/身份 Go 纯单元 10 个顶层通过；Core schema 45、共享设置 UI 15 个测试通过；core/views typecheck 通过；专用 sqlc --check 通过。
- server build、userdecision/dwsclient/langfuse vet 通过；policy 结构检查 PASS_STRUCTURAL_ONLY。
- 已有边界：locale parity 的 ja/ko 缺 dsh_plugins 与 asb_network 键（4 项失败，非本次新增）；inboundcoord 全包原有 schema 测试字符串比较出现 mutated shared input，相关实现/测试未改。本次选择/trace 相关用例单列验收。
- 新增数据库名单API与projection测试；真实数据库运行结果待补，未将 skip 计作通过。

### 补投审查修正
- 网络批次预算与数据库提交预算分开；成功版本不因尾条超时回滚。失败记录持久退避一分钟，避免最早20条坏快照阻塞全部新记录。新增三个 PostgreSQL 验证覆盖并发版本、尾部取消、公平重试。
- 预发开启 `MULTICA_USER_DECISION_VERIFY_ON_BOOT=true`；仅修改该 key，JSON 字符串序列化并回读验证，其他60个配置不变。发布入口自动迁移后执行隔离测试，未手工执行数据库迁移。
- 本地 langfuse/dwsclient/userdecision 全包及 race 检查通过，inboundcoord Decision/Trace 相关测试通过。

## 交付结果（2026-09-24）
- 功能提交 `956bc6b24bc8ceb5ddc6a50112f3a264702271bb`；预发合并提交 `a1a824bdbe`，仅合同文档与案例追加冲突，双方内容全部保留，合并后 build/相关Go测试/views typecheck/policy检查通过。
- CR [36306449](https://cd.aone.alibaba-inc.com/unite/micro/cr/app/342160/36306449)；run `3109734020` 构建/预发部署/集成测试均 SUCCESS；deploy `161891040` SUCCESS，allEnd=true。人工预发验证关卡保留，不影响本次已完成部署。
- 自动迁移日志确认 `up 9304_coordinator_user_decision_names`、`up 9305_coordinator_user_decision_trace`。预发启动检查通过，新增三个 TestPostgresDecisionTrace 用例分别 0.03s/0.01s/0.03s 通过。
- 真实 API 验证：去首尾空格、去重、清空、局部更新保留名单/不虚增revision均通过。原已启用的两个Agent `79ab6405-7947-46bb-bc2e-a280b28348ca`、`ada8943e-815e-44d9-9598-c26a5d0bb52b` 已恢复总开关，名单均为 `['冬翔']`，策略revision分别8、15。其他Agent默认空名单。
- 真实Langfuse回读：`2326de29f86d464d825c84d7f252e07e` 出现唯一choice `0a5c62dcafc2a8e9`，实际option_id=`reply-greeting`、label与PG一致，execution_state=completed；`30d30a33d5394ee689cf647d81ef568c` 出现唯一choice `09907c92a1f2f666`，无选择项、保留实际补充取消文本，state=not_executed。两条原根observation和trace input/output前后均相等。旧历史解释不会追溯迁移；新解释同trace已由本地调用链测试验证。
- 已移除临时启动验证配置（恢复原60项环境配置），DWS恢复任务前prod环境；不触发额外发布。

### 保留边界
- 本次原生新消息→发卡→点击→解释→回信闭环未完成：DWS查询指定群时报 CONTEXT_PROCESSING_ERROR，未伪造事件或发出测试消息。历史真实选择补投验证与本轮原生点击验收分开。
- 消费者的事务幂等、租约、断线取消和瞬时落库重试已加固，但DWS上游提前ACK后进程硬崩溃/长期数据库故障仍可能丢回调；没有把它宣称为exactly-once投递。
- 既有locale parity和schema字符串比较失败不归入本次全部通过。
