# Codex 接续核对与执行计划

核对窗口：2026-10-03 23:55 至 2026-10-04 凌晨，Asia/Shanghai。用户要求接手限额会话，结合计划、评审、提交和文档确定怎么继续、还差多少。本轮完成接续审计、实时读取和证据归档，没有发送新 IM、修改模板/租户/例行任务、触发部署或关闭验证门。

## 2026-10-04 继续推进更新

- 用户确认按明确开发/发布/环境/IM/API/SLS/LF 分面方法推进；长期流程已写 `docs/employee-delivery-workflow.md`，个人 skill 与个人 AGENTS 指针已落盘。
- SLS 有限补查成功，22:00 至 00:17 窗口共六行未触顶：两台 backend 最后启动 22:08:15.266 / 22:09:23.974，23:00 任务窗口不相交。CRON-04 的缺证已补齐，进度改 pass。原始证据在 `CODEX-RESUME-20261004-PREFLIGHT/`；CLI 无 total/scan-complete 字段，读取边界保留在 summary。
- group_all optional probe 明确 allowed，stream 同时保留 @ 与单聊；该窗实际 group_all 摄入零命中，不能当摄入 pass。Director/dxxh 正确 Real Niubility 身份与认证已验。group_p_hx 只有介绍卡，服务端 scene 尚未建立，正在按真实 @ 入站准备夹具。
- 四条 service 在独立库实际复验均 FAIL，无 skip：ASB 两项 fixture 误用 user UUID，FC identity 项期待旧 trace sink，retry/reconciler 项确为并发重复 child。按 [16 修复切片](16-service-retry-repair.md) 在独立 worktree 处理；不再仅列“历史待归因”。
- 下文保留初次核对事实作为审计沿革，实时状态以本节和进度板为准。

## 1. 判断与范围

第五批生产代码已经合入、部署，主要剩余是当版验收、harness 收尾、失败归因与滚动升级保护。不能据此宣布 R5 整体完成：Capsule、通用中间通知、资源冲突和共享晋级仍有独立结果缺口。

原 Grok 计划已吸收 `07-plan-critique.md` 中多数流程修正，但两者都含过期事实。以下核对更新它们的事实前提；产品标准仍是 [10-delivery-standard.md](10-delivery-standard.md)，开放承诺见 [13-plan-gap-audit.md](13-plan-gap-audit.md)。

本轮两名子代理只读核对代码缺口与验收证据，主代理负责线上读数和本文件。后续开发在各自 worktree，主代理唯一负责集成、部署和 Apply。每个用例完成或阻断立即落盘，禁止等整波结束才更新进度。

## 2. 实时事实

| 项目 | 本轮证据与结论 |
| --- | --- |
| 交付 HEAD | `44566d0bf7`；比 `a4c3aa4dab` 多两笔差距审查资料文档，无生产代码变化。交付工作树已安全 fast-forward |
| 主 checkout | 保留另一会话的 context-config 等 WIP，不 stash、不提交、不合入 |
| 发布血缘 | 当前 release HEAD `0ecd9a7b30` 包含 `a4c3aa4dab`，已用 ancestry 核对 |
| 流水线 | run `3110352822`：合并、构建、部署、集成阶段 SUCCESS；预发验证 WAITING，未关闭 |
| 当前副本 | 两个 live 副本均有 `[employee-loop:15]` 和累积 `[employee-memory:1] [employee-memory:2] [employee-memory:3]`，fence normal |
| Qwen-Real | 租户 `cc357586` applied revision 11，runtime `461aabb2-2da0-472c-9565-132042c36e25`，模板 `4osx6sfmkew1ysmdck4n` ready |
| 另一租户 | Tag·钉钉 applied revision 10。本轮没有 Apply 或切换任何 runtime |
| 例行任务 | `e02d1d7b-adb8-4a7f-bdb0-0e940e8837ba` 当前 enabled=true，不需要再恢复 |
| 新镜像真实执行 | 23:00 自动运行完成，Task/Run、Langfuse runtime_id 和真实 IM 一致；旧计划的“尚无真实任务”已过期 |
| 工具 | a1、dws、multica、normandy 已安装；预发 API、Langfuse、冬翔 DWS 消息读取成功。Director/dxxh 仅确认存在正确 Real Niubility profile，认证和真人作答档期尚未核实 |

流水线复查：[预发发布页面](https://cd.aone.alibaba-inc.com/unite/micro/publish/app/342160?flowId=1005452)。本轮返回 runId 为 `3110352822`，页面 URL 本身不区分精确运行，复查时保留 runId。

23:00 运行的关联证据：

- Autopilot Run：`8bd8dd12-d729-494d-9b6b-82671e2b0d5c`。
- Task：`18063f0c-7627-447f-9fd7-463fe5812731`，EmployeeTask：`84a97c1b-5abf-4adf-9c28-52844bd5e17d`。
- Langfuse：`18063f0c7627447f9fd7463fe5812731`，runtime 匹配新镜像；29 observations，含 7 generation 和 7 tool。这是后台 Task，不适用前台每 wake 三请求的预算。
- 真实 IM：23:00:15 开始、23:00:50 完成，最终消息 `msgU5IfQLz8ZYw03Q7kSIYmRw==`；内容正确识别冬翔 22:26 的消息。

该证据满足“新 runtime 至少真实跑通一个任务”的进入门槛，不需要为了首个真实任务再造一个。CRON-04 的严格窗口结论暂留部分取证：SLS 查询 45 秒超时，三次 log tail 命中同一副本，只有该副本最后启动 22:09:23 的日志。fence 的 started_at 是历史字段，不能替代两副本本次启动日志。下一轮补齐另一副本窗口后再正式签收，不把当前 marker 倒推为全窗口无重启。

## 3. 原计划不应再做的代码工作

`git cherry +` 说明补丁标识不一致，不能直接认定业务内容缺失。本轮源码对照确认：

| 原计划待拣 | 已有交付提交 | 判断 |
| --- | --- | --- |
| `46f64869fc` 简报说法不一、发言人归属 | `0df1b126e0` | 已在，不重拣 |
| `c646b98986` 工作区删除邀请提醒 | `74fb717c3b` | 两表清理已有；测试写法不同造成 patch-id 不同 |
| `ae5ecf5207` 档案事实 | `eede2f531c` | employeedirectory 内容一致，不重拣 |
| 邀请提醒再升 marker | marker 14 已覆盖 BeforeSend，15 累积覆盖 | 没有单独升 marker 的必要 |

旧 Codex 审查 2 High + 4 Medium 的当前状态：

| 项 | 现状 |
| --- | --- |
| H1 已受理 webhook 改派 | 已修 `4d9e39bfb8`，当前 pending run 校验 frozen binding |
| H2 旧 worker 消费 frozen source | 仍需明确滚动升级/回滚保护；当前全新副本不等于已观察到线上错误 |
| M3 failed delivery 后换 event 内容 | 已修 `1c24e70aba`，failed 记录也保留身份占位 |
| M4 Task SHARE→UPDATE 死锁 | 已修 `181d6ded64`，统一 FOR UPDATE |
| M5 partial 后不能 terminal close | 已修 `e93c710513` |
| M6 9900 CHECK 验证长锁 | 历史迁移/回放风险，后续迁移遵守安全约束；不为已部署旧迁移造重复 DDL |

H2 源码：`autopilot_webhook.go:474` 普通 queued；`employee_webhook_origin.go:483` 同事务写 frozen source；`webhook_delivery_worker.go:112` 通用 claim；`server/pkg/db/queries/webhook_delivery.sql:65` 无来源版本过滤。Direct attachment 的 EmployeeRoutineReady gate 不保护所有通用来源。

下一次生产行为改动前，先更新合同说明 reader-first 门槛、旧 consumer 无法领取新来源、停 producer/处理队列后的回滚条件。高价值验证是提交 delivery 后、run admission 前崩溃再变更绑定，以及旧 consumer 与新来源的混版领取；不能只测新 worker 自己会拒绝。

## 4. 真正未收口的 harness 与测试

Q 分支尚未合入的五笔：`bcb38f580f` memory_reset、`cfdd4c5ab6` pg_read、`c633f2ceed` segments、`7e2f45802c` evidence_v2、`eb1764ef3e` file_send。另有六个脏文件，`+386/-26`；已保留，未提交或 cherry-pick。

脏 diff 比交接的“file action”更宽，还含 download、recall、react、forward、combine_forward、@all、setup_group、burst、negative_observe，且九项能力默认改为 true。收尾要分清“driver 支持操作”和“平台摄入/效果已验证”；撤回和原生表情继续按本轮排除项处理，不能靠 capability=true 假通过。

本轮分别在已交付 HEAD 和 Q 脏树运行只读 dry-run，均退出 0、suite_errors=[]：

| 口径 | 数量 |
| --- | --- |
| 作者静态标记 | 88 = ready 51 + ready_partial 20 + blocked 17 |
| 已交付 harness 默认 | 88 = runnable 30 + runnable_partial 15 + blocked_harness 28 + waiting_ops 15 |
| Q 脏树默认 | 88 = runnable 35 + runnable_partial 21 + waiting_release 7 + waiting_ops 21 + blocked_resource 1 + blocked_harness 3 |

所以评审对“默认约 30”没有名单依据的批评只部分成立：30 有真实 dry-run 依据，51 是静态编写状态，35 又是未提交能力声明下的结果。后续用明确 capabilities 和 run manifest 给出可跑名单；14 条 R0 是子集，不能加成增量。

原 `13 NEW` 是 rel-b4 历史日志的基线匹配误报。按每行提取 Test 名重算：domain 1 fail/0 未解释，handler 88 fail/3 未解释，service 4 fail/4 未解释。总计 7 个未解释历史失败，不等于当前新增回归。

- 历史来源：`_shared/logs/rel-b4/b5-{domain,handler,service}.log`，不是 int-b5 日志。
- 四个 service：`TestResolveASBSandboxAttachesFreshBUCTokensBeforeProbe`、`TestResolveASBSandboxReplacesUnavailableWarmSession`、`TestFCE2BChatIdentityComesOnlyFromAgentBinding`、`TestFailedTaskFinalizationSerializesRetryAndReconciler`。
- 三个 handler：`TestCatalogAPIAdminGalleryGitHubConnectAndTools`、`TestGitHubInstallCallbackWithoutCodeContinuesToUserAuthorization`、`TestGitHubUpdateWithoutCodeRefreshesWhenCredentialExists`。与其他 connector 会话边界核对后归因，不直接修进本批。
- `b_fix_fail` 四个 handler 已在基线集合，登记历史待确认，不能算新增。
- int-b5 `service.sum` 是定向 256 pass/0 fail，其 regex 不覆盖以上四个 service 名称，不能用它销掉四项。

后续先修基线比对脚本，再在专用库定向重跑这四项，与基线逐名比较。当前没有运行 Go 测试，也没有改生产实现。

## 5. 还差多少

第一波是八个工作包，按交接粒度共至少 13 条行为验收和 1 个摄入探针；MEMX-G3 若拆 G3a/G3b 则再多一条，G3b 需第三位真人。

| 工作包 | 本轮状态 / 下一步 |
| --- | --- |
| CRON-04 | 新 runtime 真实任务与送达已补证；补两副本窗口后签收 |
| COL 歧义 + COL-03 | 未开始，Director 与 dxxh 各自单聊按迟到/更正剧本 |
| REF-01a | 未开始，EL-RES-1003 |
| G5-BUILDS-ON | 未开始，EL-E2E-1003；已过双上游不重跑 |
| 单聊创建例行任务 | 未开始，e961aa28；创建、回读、删除测试任务 |
| M8 探针 + MEMX-L1 + DS-09 | 未开始，group_p_hx；先证未 @ 摄入，不拿 quiet 代替未收到 |
| M5 G2/G3/X1/R1 | 未开始，独占 g_team；R1 必须专门混版/旧 job replay，当前全新副本不足以证明 |
| MF MEMX-D1 | 未开始，独占 group_t；14 条真人未 @ 人话，flush 后 ≥30 分钟再召回 |

广播之外至少五项义务：MEMX-N1、BASE-MEMORY、两人采集独立提醒账本、WD-04、BASE-TASK 同 Task 新 Run。M1/M5 的 MEMX-X1 属同一合同读写两侧，协作存证，不重复计算。

Golden20 权威基线是 10 pass/10 fail，单聊 9/11、群聊 1/9。harness 历史 22 项 scoreboard 还含 BASE-TASK 与 BASE-TASK-G，不能混算。wave2 的 88 条通过 maps_to 覆盖基础聊天和办事合同，不能加成“108 条待验”。原失败场景的复测窗口单列，已过且未再改到的案例不重跑。

已落地的 DS-01 证据回答、DS-12 source repair、persona/语言修复应标“代码已修，原场景待验”。DS-03 矛盾表达仍待质量判断。wave2 有 ≥25 小时案例，完整收口至少跨日；现在不能给可信完成百分比或承诺一晚全部完成。

R5 开放项单独切片：Capsule/完整执行现场恢复、通用 SceneNotice/HumanQuestion、跨 Task 资源冲突与跨 principal 隔离、受控共享晋级/撤回/新 Task 复用、广义 DWS 多源及 PRI-78 原验收收尾。本批实现与验收完成不销掉这些承诺。

## 6. 接续顺序与完成条件

1. 当前审计已完成。下一轮先补 CRON-04 副本窗口与三个真人身份前置，再开展独立场域验收；无须重新 Apply 或恢复例行任务。
2. M8/MF 先证群观察、账本与 flush；M5 与 MF 不串群。MF 等待期间可并行收 Q harness、基线脚本及定向失败归因。
3. harness 逐能力核验后按补丁收进交付树；H2 先补 spec 和 reader-first/回滚设计，再实施。已有等价代码不重拣。
4. 一批修复集成后定向验证、部署流水线 66，核对发布血缘、两副本新启动、累积 marker，再通知验证。Known baseline 不自动豁免本轮真实风险。
5. 刷新 wave2 manifest/capabilities，优先原群聊失败与记忆反例，随后跑有效 runnable 子集；partial/vacuous、外部依赖、混版或跨日项分别保留结论。
6. 八包有效结论满足产品标准后才关闭验证门。补证索引、执行板、清理和延期承诺；R5 未完成项持续显式登记。

进度写 `_shared/resume-progress.md`。证据目录 `~/d1/employee-e2e-evidence/CODEX-RESUME-20261003/` 已保存 Tag/employee/runtime/routine API、Run、IM、完整 Langfuse trace、两种 dry-run、启动 log tail、summary 和九份交接/计划 Markdown；不依赖 `/tmp` 长期存活。私有 DWS 配置含认证元数据，禁止入库或分享。
