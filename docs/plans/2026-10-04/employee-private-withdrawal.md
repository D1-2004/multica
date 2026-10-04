# DM 本人跨源私有记录的精确撤销投影

基线3128eda17b，独立employee/codex-private-withdraw。只改历史撤销/provenance和必要epoch门控；不改Persona、lookup/me、Task/COL/Runtime、数据库schema或旧快照/journal。主代理唯一集成与发布。

## Why 与真实证据范围

真实group私人capture0dcd…active→DM person_view/m1 originGroup和value入模→group精确forget0dcd…成功→DMafter m1消失，但看过已退休record的旧DM派生reply msgTro仍进History。该reply实际只说未知而非饮品，因此值复活完整e2e仍not_verified；接口来源链漏已证。新增真实DB带旧value的reply原反例必须基线RED/候选GREEN，不造真实IM pass。

## 最小合同

当前可信DM、Host确认唯一请求者且org-qualified exact owner时，复用RecentConversationRequest.MemoryPrincipal：sameWorkspace/Agent/Tenant、scope_kind=private、principal_id exact的退休记录可以来自所有原scene。只读取记录ID+真实origin scene元数据，不读insight，不读取别人私人/外源public；源scene目录已删不能让已导入旧值逃过withdraw，此查询是删除投影，不是新Scene.Resolve/新读取授权。

manifest按精确recordID+实际originScene核对；实际memory工具recordID继续用所选退休ID集核对。被关联DM已送达assistant及后继按已有有界算法剔除。外源group人话evidenceID不得进入DM Sources，避免跨conversation擦独立同值人话。原CurrentScene撤销保持；group没有跨私人view。lookup/me依然current-scene，当前窄lookup[]覆盖已有m1的模型质量问题单列，不借此扩权限。

epoch16→17：旧reader不会过滤DM跨源退休private，旧16/new17并行会暴露不同隐私语义；canonical EmployeeLoopReplicaMarker升17，使新受理/恢复/发送的现有门控混版暂停，全部副本17后恢复。build_id已引用canonical常量，不另造字符串/开关，无schema。既存快照不热改，新的读取投影使用当前隐私资格。

## 验证

DB原反例：本人group private实际capture、DM冻结manifest+实际tool ID/已送达含value旧答、本人group授权forget→DM新历史无旧答，原件/旧snapshot不变。对照另一owner/tenant/agent、外源public、group调用、未知owner、same-value独立源均不抹；原源目录删除仍撤销；PG/ctx/硬cap保持fail closed。只跑受影响窄历史/祖先检查及marker装配，无真实模型/IM/预发写入。原live完整值复活前置仍由主代理补，局部绿色不改e2e_verified。

状态：合同先写，代码和RED/GREEN进行中。证据~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-PRIVATE-WITHDRAW/。

## 本地完成结果

基线3128只读Go overlay：manifest/实际tool ID/源目录删除三组含旧value原反例全部RED；候选全部GREEN。真实RecordPrivateObservationTx先写本人group私有偏好，ForegroundBrief确实PersonView+value，DM已送达old/derived reply带value；actual ForgetPrivateTx作者授权后新历史不含old/derived，原审计/已冻结child不改，同actor同group同值另一active记录、独立DM同值人话/回复保留。

9种隔离对照全部PASS：other owner、空owner、staff-only、外源public、other tenant/agent/workspace、group-view、manifest origin错误。受影响entry7项PASS；race5项PASS（含SQL/ctx读失败、2000 tombstone上限和64祖先上限），这些重叠测试不增加分母；entry独立合计9项。canonical门控真实PG：old16/new17拒绝、all17恢复1项PASS，cancel工具历史最低epoch≥16纯unit1项PASS。总独立11项无FAIL/SKIP。build/vet覆盖entry/handler/server，gofmt/diff通过。

无新schema、lookup/me或其他生产域变化，build_id仍直接引用canonical常量。真实跨源实验只能证明record来源链和漏过滤：旧DM答当时未知、没有value，完整值复活e2e仍not_verified；本地带value RED/GREEN不伪造live pass。主代理当版全17后需再建立DM明确引用旧private value的真实答、group作者forget、下一轮DM核整generation无旧值，补其他Actor/同值独立记录的negative。
