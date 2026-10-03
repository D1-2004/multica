# G 包：证据验证、持久提炼、真实复用与受控晋级

> **验收与实现方针：**先读 [Step 0](00-step-0-environment.md) 与 [交付标准](10-delivery-standard.md)。本包的内部API/表结构/阈值/步骤是参考路线，可由主代理协调优化；业务结果、权限、幂等、恢复、真实证据及已合入公共合同必须保持。

> **执行方式：**G1证据与治理可立即开发；生产激活必须等Q确认本批Task/Loop通过。G2/G3/G4逐批发布，不改前台capture/correct/forget的已通过行为。

**目标：**符合GawkBot verified-execution机制的经验进入适用记忆，下一次工作实际引用；经独立共享授权才能晋级，撤回后停止召回，崩溃/重放不复活。

## 1. 现状与所有权

已有`service/employeememory/{distill,store,workflow,workflow_store,recovery}.go`、`employeelearning/`、`handler/employee_learning_capture.go`。当前普通Run结果只生成requester-private inferred confidence3；账户明确记忆为observed confidence4、Trusted=false。两者都不是VerifiedExecution。

`Store.Distill`是确定性函数，但需要Host VerifiedRun。`RecordWorkflow(...Promote...)`只记录信息，不实施晋级。不能写一条promotion artifact就称进化完成。

G拟拥有`service/employee_task_verification.go`、`employeememory/promotion.go`、`handler/employee_memory_evolution.go`及对应测试；修改现有memory模块须保留source map/许可证。Task terminal共用入口/周期worker/router由I接，G不自起scheduler。

Gawk固定源：`task_distill.go`、`memory_workflow.go`、`memory_workflow_reconciler.go`、`learnings.go`、`scoped_memory.go`。复用passing machine proof和知识来源机制，替换goroutine、进程single-flight与local-file检查为PG事实/租约/OSS可验证对象。

## 2. G1：可验证结果合同

持久verification record绑定Task/Run/queue/goal revision、检查种类、真实evidence引用/内容hash、checker version、Host完成时间、passed/failed/unknown。检查规则由已受理Task contract或可信业务配置选择，不接模型的Verified=true。

首批检查种类：

- artifact_contents：授权对象字节、schema/行数/明确success criteria与hash；文件确实发送不是文件内容正确的证明。
- execution_output：Task明确的确定性计算/校验，由Host可信checker对结构化证据核验；沙箱自报exit0或assistant说PASS不够。
- delivery_receipt：确切provider message/cid/resource实证，证明交付条件，不能单独证明业务答案正确。

验收使用预先已知算例和文件schema。signed来源仅证明归属，不证明内容准确。未知规则保留inferred候选，不能自动升verified；失败验证保留审计不正向提炼。

- [ ] 写`TestVerificationRejectsAssistantClaimAndMismatchedEvidence`、`TestVerificationCurrentGoalAndArtifactContents`、`TestVerificationDoesNotTreatDeliveredAsCorrect`。
- [ ] 写两worker同evidence唯一record、checker版本/同ID内容冲突、tenant/requester/Task取消、reset时间fence。
- [ ] 在真实PG和对象testfixture观察RED后实现；不执行用户已安装agent CLI，真实provider smoke须独立授权开关。

## 3. G2：VerifiedRun → durable Distill

在verification提交后，同事务保存“待提炼”意图或现有consumer可发现的明确watermark；现有Employee reconciliation领取。不能仅go routine调用Distill；commit-before-notify、两副本、export失败均可恢复。

VerifiedRun的TaskID/ExecutionID/EvidenceID/Passed与当前goal绑定由Host从record重建，Actor是可信checker；Store.Distill仍确定性，不加总结模型。source为稳定verification ID或对应execution/verification revision。若现有Record不能与消费者事务同commit，提供DistillTx并使用原RecordTx原子路径。

- 默认scope保留原Task requester-private；自动化的routine/webhook source不是人类私有namespace，按其明确Agent/scene治理scope选择或不capture，不借最早发起人的身份。
- 只提炼可复用经验及适用边界，不将其他参与者原始答复自动复制到共享playbook。verification覆盖的是哪些条件，insight明确表达哪些条件。
- 工作结束时间/evidence reset fence不按重试时间刷新；旧候选、旧verified记录重放不能绕过忘记/撤回。
- 已有confidence decay、trust保护、source identity与supersedes机制继续使用。不为更多候选再建独立memory库。

- [ ] 写`TestVerifiedDistillCommitRecoveryIsExactlyOne`、普通成功/取消/failed zero verified、pre-reset evidence no revival、automation scope no human private。
- [ ] `EMPLOYEE_MEMORY_TEST_DATABASE_URL`指向独立PG，跑`go test -race ./internal/service/employeememory ./internal/employeelearning -count=1`，handler capture集成另用隔离DATABASE_URL。

## 4. G3：召回与实际工作复用

先复用现有有界Search/Brief和Compiler.ContextUsed；不增加隐藏embedding/总结服务。DM/group/private/shared仍按现有授权筛选；group不自动灌privatebrief。

候选作为低可信资料，verified学习作为有来源且有限适用范围的知识，都不是权限或system指令。新Task注入实际匹配引用和manifest，记录学习ID、verification/evidence、适用范围和最新状态；不能仅写“used”标志。

- [ ] 写真实新Task WorkPacket包含合适学习ref且不含不适用/已忘/已撤记录；不同scene/principal、同key冲突仍隔离。
- [ ] 已冻结旧请求审计保留；发送/新effect前根据政策重验撤权/撤回，不能让缓存知识扩大权限。
- [ ] 后续真实Task设置一个经验能影响行为的已知案例，核实际工具执行与输出改进，同时Langfuse/ContextUsed可追溯，不用同一次结果自证复用。

## 5. G4：受控共享晋级、冲突与撤回

private→scene-shared/Agent-shared需要独立授权记录：来源owner允许共享哪些内容、目标scope、当前管理权限、脱敏范围、有效期和撤回入口。用户明确授权且规则允许时不机械追加确认；缺这份授权不得自动晋级。管理者权限本身不能读取任意他人private学习。

先支持同tenant/scene-shared，Agent-shared只有明确配置的更广授权才开放。晋级创建实际active知识对象/记录、引用原learning与verification和grant，不只workflow artifact。promotion receipt与实际对象写入同事务；RecordWorkflow引用真实已提交ID。

冲突：相同key/type且语义不同保留候选conflict关系，可信知识不被inferred覆盖；Host没有确定性裁决时由授权人明确更正，不加一个全局“自动裁决正确性”的LLM。源learning被忘记、source revoked或promotion撤回后，derived共享投影必须inactive并从Search/Brief排除；审计保留。不能把旧metadata fallback当active知识。

- [ ] 写`TestPromotionRequiresOwnerGrantAndActualActiveArtifact`、private leak/不同tenant/管理者越权、source tombstone、旧evidence重投、两worker幂等。
- [ ] 写promotion提交成功而通知失败恢复、artifact缺失repair不伪成功、source撤回与召回/packet编译竞争。
- [ ] 开放授权的list/read/promote/revoke管理服务与最小HTTP入口；路由由I注册，scope/expected revision不取模型自由字段。

## 6. 真实验收与完成条件

MEM-01：已知计算/文件Task→可信Host verification passing→一条verified learning；假成功和wronggoal不晋级。MEM-02：另一真实Task在相同允许scope召回并实际使用该经验，manifest/trace/结果共同证明。MEM-03：明确owner grant后共享目标可用、未授权参与者不可读；同人其他private记录不泄露。MEM-04：撤回后新Task不召回，旧source重投不复活；reset/forget既有单聊与群剧本通过。

“workflow.promote recorded”“Record返回200”“新prompt含learning”都不单独证明进化闭环。G完成要有证据验证、持久提炼、真实下一Task使用、受控晋级和撤回四段；无需强制所有工作转Wiki或自动修改岗位指令。
