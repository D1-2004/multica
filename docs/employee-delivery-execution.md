# Employee 本轮唯一执行表

当前摘要唯一权威；最后更新：2026-10-04 15:11，Asia/Shanghai。最新批次为后台进展预发交付，以下旧批次段落保留历史。上轮范围08:14冻结并在08:43前收口；09:00后的四项修复为用户单独授权的新批次。历史材料保留，不再作为当前状态入口。

## 当前交付版本

| 对象 | 当前事实 |
| --- | --- |
| 预发release | f9a6d687a346c5175f65a88a43a1e35f7da5c6ca；app342160 / pipeline66 / CR36362274 / Run3110378343，13:16:09部署成功 |
| 本批源/组合 | 冻结e5ef5adbea6f628efba623cf8c73d38f37d71381；组合f84f629789564108e9a29281d68bd0bc0f12cf35；保留steer，平台并入eval78fffd467 |
| Live | 两副本employee-loop:20 / eval-report:1 / normal；13:14:27与13:15:38实际启动SLS已证 |
| Runtime / Agent | 原461aabb2未切；33af235e idle；新Task a9af04a2 succeeded、唯一Run31f720c9、0等待 |
| 本批验收 | 首轮ACK约6秒、真实Python/sleep60、report_progress同ID两次幂等、最终文件650通过；阶段展示未通过（Loop quiet），整体不签收 |
| 恢复/清理 | 本批无routine暂停/Runtime修改；3隔离DB已drop；hourly原autopilot12:02:30归档属本批前变更，未擅自恢复 |
| 当前报告 | [后台进展预发报告](plans/2026-10-04/employee-progress-pre-report.md)；人工预发验收门保留 |
| 开放项 | 阶段quiet展示P2、迁移10020–10023数字前缀碰撞P2；历史新蓝杉/记忆交互FAIL保留；不追加部署/镜像/长等待 |

## 13:30 本批窄验收清单

| 项 | 当前结论 | 证据与下一步 |
| --- | --- | --- |
| 发布生效 | PASS | 精确Run及两live20；manifest/pipeline-watch/startups-sls |
| 接单反馈与最终执行 | 真实PASS | PRG-LIVE-01 IM/API/沙箱TOOL；ACK约6秒、真实60秒、文件650 |
| 原生进展上报幂等 | 真实PASS | 同report_ref、replayed false/true、一次wake，Task/Run不因上报推进 |
| 阶段展示 | 未通过 | f7fb76f2 quiet_committed，无阶段IM；先校正原请求展示约定并原例复验 |
| 取消/终态旧上报/禁中间消息 | 真实未跑 | 本地检查/review与真实验收分别记录，不升级 |
| LF/SLS | 部分可读/查询缺证 | 沙箱TOOL及Host generation可读，沙箱generation输出仍乱码；业务SLS精确ID查询空，不证明事件未发生 |
| SPEC/EVALS | 已随平台部署、整体验收未跑 | eval78ff已在构建祖先；不提交虚假全量PASS |

## 以下为旧批次冻结清单与沿革（不代表当前版本）

## 冻结验收清单

证据根：`~/d1/employee-e2e-evidence/`。pass指对应列证明，不跨列升级。

| 项 | 产品真实证据 | 代码review/当前结论 | 证据目录 | 阻断与下一步 |
| --- | --- | --- | --- | --- |
| CRON04 | pass，新镜像真实23:00自动任务、IM/API/LF/SLS | 已证且未受改动影响，复用 | CODEX-RESUME-20261003 | 无 |
| COL歧义 | pass，裸8澄清，具名8/2正确归属 | source/binding/idempotence复核 | ROOT/fourface-audit | 无 |
| COL03取消/迟答 | cancel真实、late无consume/无复活汇总；epoch17晚答仅4家 | 状态/授权/CAS/关闭事务可审；原假记下/汇总失败保留 | ROOT/col03-*、COL-MODES | 不升级为所有话术完全准确 |
| REF01a | pass，引用正确Task、退出确认、旧notice suppressed | 受影响取消链复核 | ROOT/ref01a-fourface-result.json | 无 |
| G5 builds_on | effect pass，fresh stats→fresh retro、原数字正确、实际交付 | 产品链成立；旧461后台LF损坏另列 | TASK-G5-HOTFIX/g5-report.md | 观测partial不抹产品效果 |
| DM routine生命周期 | API create/read/delete/read pass | 关系/权限review | ROOT/dm-routine-* | 自然语言创建不冒称已验 |
| M8/L1/DS09 | 摄入/旧原话O召回/only BR3 pass | 当前scope/source已证 | M8-MF、ROOT/ds09-m16-result.json | L1不冒称长期学习 |
| M5同scope遗忘/权限 | BASE硬门pass，完整availableHistory、旧值不复活、真人保留；G3硬门pass/话术degraded | 精确ID/权限/混版约束review | M5-FINAL/summary.md | 历史“从未记过”仍失实 |
| 本人cross-source遗忘 | epoch17真实before含值、after不复活、derived reply剔除 pass | chat path通过；TaskWake遗漏MemoryPrincipal P1未通过 | MEMX-CROSS-FORGET-M17、CLOSEOUT-REVIEWS/privacy-and-recovery.md | 修TaskWake资格传递；旧冻结输入另定治理 |
| R1恢复 | f4c/epoch17实际16→17 gate、冻结v2零新增模型/hash保持、TaskWake幂等 pass | pass_with_known_limit | R1-M17/summary.md | 混版暂缓，不承诺零中断；旧v1另有c301→fa证明 |
| MF D1 | flush accepted7、33min后答对、检索命中已证；纯memory因History仍答案partial | 时间/来源代码review；不升级pure E2E | M8-MF/attempt2-loop16、attempt3-history-eviction | 独立因果证据保留限制，无新长等 |
| BASE-TASK | 原新Goal误continue旧e0 fail；另机制原合计句实际新Run/Python55 pass | 接线/权限/CAS成立，模型意图选择P1 fail | THIRD-TASK-REMINDER/base-third-report.md | 校正明确新工作优先级；机制不洗原失败 |
| 两人独立提醒 | 双真实1次、10/14→24与终态成立；叙事虚构fail；独立ledger缺证 | 复杂等待以源码review，不冒E2E账本通过 | THIRD-TASK-REMINDER/reminder-report.md | 补可审ledger；禁止无证时间/提醒断言 |
| WD04第三真人 | 未跑，缺第四真人 | code_review_accepted：资格/配额/已答抑制，真实未跑 | CLOSEOUT-REVIEWS/collection-schedule-harness.md | 不标真实2/3 E2E |
| G15跨日 | seg1真实，seg2最早10-05 05:18:38，未跑 | code_review_accepted：时间checkpoint/版本/隔离，真实未跑 | WAVE-PREP/G15-segment-checkpoint.json | 不继续等待/调度，不标跨日E2E |
| Wave/Golden重点 | 去重清单当前按execution-checkpoints保留；G08安静承诺后答机器人fail，C03第三未重验 | 新发现冻结登记；真实失败不被review绿替代 | WAVE-PREP/runs/WAVE-THIRD-GOLDEN-20261004 | quiet需要持续状态边界；SLA条件回答待实际证明 |
| Harness | 80离线通过、4已有窗口正常分页重放成立 | envgate、typedcursor、失败/cap/pagination fail-closed复核 | ROOT/harness-80-integrated.log、HARNESS | 不把runnable当pass；88不加Golden/R0 |
| Runtime观测修复 | 专有候选cold/warm/精确nonce/nativePython/11generation完整可读pass | codec/stream/secret/generation异常review，未切生产/461 | RUNTIME-CANARY/result.json、RUNTIME-TRACE | generic Pi非Employee direct能力等价；老乱码不可恢复 |

## 清理与恢复

| 对象 | 原状态/操作 | 当前结果 |
| --- | --- | --- |
| hourly e02d1d7b | 原enabled=true，03:52:41暂停 | 已PATCH恢复enabled=true；08:38 GET回读确认；cron/timezone/instructions保持。next_run_at仍04:00旧游标，实际下一次触发未验证 |
| 临时routine bae62ef5 | 本轮API验收创建 | 已DELETE204且回读不存在 |
| Canary Issue059079ca / Agent7e2c82e7 | 三Task均completed，Agentidle | Issue当前GET404（DELETE曾405/404，不能称本次DELETE成功）；Agent archive200，GET archived_at=08:38:05，active list无该Agent；审计行保留 |
| Candidate Runtime14835 / Template53zkm | 已交付可追溯候选 | 保留未晋级，461/Tag/另一租户未改 |
| 本地R1 DB/Redis | 隔离协议环境 | 已清理，不动共享DB |
| 测试记忆与actorleases | 仅新测试记录 | 已精确forgotten，群/DM lease释放；原历史保留 |

本轮已收口：review汇总、恢复/清理回读和最终报告已完成。后续按开放项另立批次。禁止新的真实测试/长等待/构建/部署。新问题按严重度登记后续，不重启本轮范围。


## 用户单独授权的四项修复（09:28 本地交付）

[本轮报告](plans/2026-10-04/employee-four-fixes-report.md)与[Plan](plans/2026-10-04/employee-four-fixes.md)。不修改上轮真实验收结论，不开启新部署/IM轮次。

| 项 | 当前结论 | 证据 | 限制/下一步 |
| --- | --- | --- | --- |
| 新任务归属 | 代码合同修复，独立新Task/redo/builds_on本地通过 | EMPLOYEE-FOUR-FIXES-20261004/integrated-handler-tests.log | 真实模型选择及Python未复验，原E2E FAIL保留 |
| 遗忘遗漏 | 新TaskWake历史沿用可信本人过滤，本地builder通过及旧实现对照失败 | EMPLOYEE-FOUR-FIXES-20261004/privacy | 旧冻结snapshot未治理；预发epoch17未切 |
| 提醒事实 | 有界持久过程事实、时间边界与unknown输入验证通过 | EMPLOYEE-FOUR-FIXES-20261004/task-facts | 真实表述未复验，不补长等 |
| 持续安静 | 持久控制、owner恢复、异账号0模型及native排队抑制通过 | EMPLOYEE-FOUR-FIXES-20261004/quiet-final-tests.log | Router callback提交前、旧多receipt哈希notice保留边界；后台通知不静音 |
| 本地清理 | 三独立DB已drop且回读0，配置未动 | EMPLOYEE-FOUR-FIXES-20261004/cleanup.json | 开发worktree保留审计，未新增远端对象 |

当前行动已结束；发布/真实四例复验需另批授权。本轮无新增全仓migration冲突，已有lint失败保留，不扩大修复。


## 09:58 本轮部署与短E2E进行中
用户已授权部署及简单真实E2E。独立CR36362030、run3110373861；source11d6eb5061，release74802f1829含本轮四项代码。代码合并成功，构建进行中；两个live仍17，不开始业务测试。Runtime461、Tag rev11、hourly未更改。演员Director/冬翔/dxxh已在私有prod DWS配置核对刷新；无新消息。当前证据EMPLOYEE-FOUR-FIXES-PRE-20261004/manifest.json，root是发布/测试/恢复唯一执行者。

10:07：发布成功10:00:29，release74802f1829，两live epoch18启动09:58:52.009/10:00:02.429（SLS+tail已证），normal。NEW-TASK原台词/原Director DM真实FAIL：直接复述旧结果，0新Task，旧版本未改变；trace699e4889。已保存new-task-result.json，不再次修复或发布。其他三个短例进行中。


## 10:30 四项修复部署与短E2E历史结论
[报告](plans/2026-10-04/employee-four-fixes-pre-report.md)。一轮发布成功，业务测试已结束，不再推进新范围。

| 项 | 当前结论 | 证据/边界 |
| --- | --- | --- |
| 新Task | 真实FAIL | 原台词/原场域直接复述旧结果，0新Task/Run；699e4889 |
| 遗忘TaskWake | 完整输入限定PASS，记忆交互仍FAIL | 71d89856可读未截断，private旧值/旧派生答复剔除，正常History保留；96aac456 lookup为空与背景m1矛盾；43145832 forget已提交但报失败 |
| 收集/事实 | 短无提醒路径PASS | 7/9/16、真实时间、process_facts及Task终态；真实催促没跑不签 |
| 安静恢复 | 实际另账号引用→Quiet→本人active→答16 PASS | SLS0模型/0action、LF/IM闭环，DEAP actor未使用 |
| 恢复清理 | 已完成 | 新memory精确forgotten、active rev2、collection0 open waits，hourly true/11:00、Runtime461不变 |
| 发布窗口routine | Runtime准备失败保留 | 10:00运行a5c3e376未重跑，不新增Runtime修复/镜像 |

之前的08:39/09:28/09:58摘要只作沿革，不是当前部署事实；功能整体不签收，未关闭人工预发验证。

## SPEC / EVALS接管回执（当前职责）

已收到并核对源session的人类移交授权：本session负责统一集成/预发发布、组织后续验证和持续维护SPEC+EVALS；源session停止独立发布。不需要再次确认；没有创建后台自动化或新的部署轮次。

| 项 | 核对事实 |
| --- | --- |
| fetch来源 | aone/feat/evaluation-hub精确HEAD34dc79f7eabb63153f63a83930bc9dd49a67b4e9；功能78fffd467179e5ed577317d8e834ad731643abaf |
| 完整序列 | 3512f2543f → e1d8f13495 → 3a2f721ad0 → 326e563cb8 → 8e7f4a447d → 78fffd4671均为已发布f9a6d687的祖先，不只取最后一笔 |
| 必需资产 | 交接、上报合同、贡献规范、三定义、evalreport模块、CLI及10020–10023 eval migrations共22文件齐全；source与集成blob逐项相等，除最后交接文档外与已发布release亦相等 |
| 统一分支 | employee/progress-release，接管代码核对HEAD6852c43343e44f6685b20898e0c56cb720191319；已rebase到平台f9a6d687并摘取34dc的交接文档。reader20/steer/progress及本session记录保留，主checkout其他WIP未动 |
| 当前检查 | make eval-check通过：5 SPEC、5分类、20 P0、16场景、123用例（78 existing / 45 defined）；evalcatalog/evalreport Go检查通过。只是定义/局部检查，不是办公E2E |
| 安全接缝 | human-before-workspace、全live eval-report:1、workspace事务清理、三个INVALID索引恢复hook均在统一代码中；不把代码存在升级为真实协议验收 |
| 已发布/待验 | 完整功能已在13:16发布的f9a6d687；本次新增交接/维护文档无需再部署。预发页面、真实报告201/200/409和权限反例未跑，整体评测仍不签收 |
| 单一入口 | 预发publisher为本session统一分支；原CR36361979历史证据保留，不重跑其旧阻塞实例。下一次行为/发布从统一来源推进 |
| 后续组织 | 按[维护流程](evals/verification-maintenance.md)依次组织原新任务FAIL、后台阶段展示、记忆交互及报告协议；每次映射SPEC→case→受影响P0，保留未执行分母 |

精确核对证据：EMPLOYEE-PROGRESS-INTEGRATION-20261004/evals-receipt.json、evals-definition-check.log。本次代码接管与收件回执完成；后续真实验收另有明确窗口/范围，不冒称已完成。

## 用户新授权：黄金20例真实E2E一轮

当前执行：[Plan](plans/2026-10-04/employee-golden20-round.md)。现成golden20.json20例，Director+冬翔两真人、Qwen-Real预发；仅测试，不发布/修复。证据GOLDEN20-20261004-1356，auth/self、会话与pipeline/fence准入已通过，正在运行DM/GROUP两路；逐例当前状态见本波`current-execution.json`，已发送测试消息。SPEC20 P0及v2/G19不混作同一覆盖率。所有20例保留分母，按实际结果回填。


## 黄金20例最终收口（当前最新批次）

20/20实际执行：完整原话约束9通过、11未通过（11DM/9群；Actor为Director+冬翔两真人）。release f9a6d687 / reader20 / Run3110378343未改，窗口13:59:43–14:23:05。主要FAIL为旧History误答、无证事实/时间、编负责人、恢复漏答、隐私表述，以及4格式/1语言；不是完整SPEC20 P0通过。

[最终报告](plans/2026-10-04/employee-golden20-report.md)；证据GOLDEN20-20261004-1356/summary-final.json为逐例执行表。5测试记忆精确forgotten已证，群active、agent idle/无未终态任务；hourly原archived未动，Runtime/全局网关未改，监控停止。没有修复、部署或将旧ID冒充canonical G01–G20上报。

下一独立批次候选收件：A2UI/HITL aone/codex/tag-a2ui-human-loop-20261004@1b476e502a；意图/SSE aone/feat/employee-intent-stream-feedback@b156cf1c72340c07857ea7f60ee29cbef996b1b4（运行46cc61872c792162f20881098da21e53c211df51、reader21、10040/10041）；架构状态图文档a62ce24d8d。均待统一审查/集成，不称已发布，不占本轮业务窗口。


## 当前持续工作授权与收件队列

用户最新授权：收到本项目发布候选即按合同核对、集成并预发；发布不中断整轮E2E，保留检查点、只处理受影响例。空闲复核前置并完善可执行测试；复杂缺环境标blocked/incomplete。有对应case就运行，没有则向交付者索要用例。规则已写verification-maintenance.md及operations，已授权步骤不反复确认。

| 收件 | 当前状态与下一步 |
| --- | --- |
| 意图/首轮SSE反馈 b156cf1c72340c07857ea7f60ee29cbef996b1b4（运行46cc61872c792162f20881098da21e53c211df51） | 已fetch核对完整SHA，待语义审查/集成；base eb62，声明reader21、10040/10041、6个新风险定义/129例。不能仅据源报告称已发布。优先原新Task及首轮反馈/格式/quiet反例 |
| A2UI/HITL 1b476e502a | 原候选撤销可发布状态；来源已交修复候选a67def98bd7ad3e2805819959c9f1def7abe8406（7308810d53→c5829f220d→a67def98bd），3项运行缺口声明本地反例修复通过；本发布方已fetch核对SHA，待复核，未发布。9977–9982与human1须组合核对，不整支带旧基线 |
| 状态图文档 a62ce24d8d | 仅文档接管参考，不授予新大能力实施；后续版本变化核对图中状态与真实证据 |
| 已跑黄金20 | 9完整通过/11失败，证据和环境已清理；作为原反例/不受影响证据继续复用，不因发布全部从头跑 |

定时跟进附着当前会话；每次从此入口接续，并先确认已有任务/进程状态，避免重复发布、重发消息或抢占其他session场域。


持续跟进已建立：当前会话heartbeat `multica-e2e`，ACTIVE，每30分钟接续一次，新增消息按本会话正常收件处理。不是独立新任务或另一个发布者。已有收件立即进入准备队列，定时补空闲检查，不承诺常驻后台实时监听所有应用。

新增收件：Direct/Tag提示词清理本地ca166bb92c6bb0b944e6d7f8a9b26b0ee07a82df，源码待集成；来源声明Tag模板已revision13并Apply全部现存租户，需实际GET核对生效时间/指令hash。它是环境变化，不把黄金窗口旧LF当revision13证据，不盲目重复Apply。Daemon/execenv改动需单独候选Runtime及相应兼容检查，服务端发布不冒称旧Runtime稳定提示词已清除。

一次性定时任务：来源尚未提交，不能发布。计划3m/5m、源上下文保留、取消/改期、并发到期及<1m边界；9977/9980/9981与A2UI冲突，要求提交前重新分配未占编号并更新恢复hook/合同。收到固定SHA和用例后推进，默认持久调度释放创建沙箱，不凭假时钟签真实到点。


本轮实际核对：SSE b156cf1c72340c07857ea7f60ee29cbef996b1b4、Human a67def98bd7ad3e2805819959c9f1def7abe8406、提示词ca166bb92c6bb0b944e6d7f8a9b26b0ee07a82df、once原769d406a8f585e13f0076b4be89c74e2c791278d对象可读取。once来源后报fresh/sqlc 10000+ lexical排序会先ALTER后CREATE，正补排序和重编号，旧769暂不发布。时间门/首波5例及稳定case补件要求已发源session。

两个Tag员工实际GET均updated_at=2026-10-04T15:07:57+08:00，instructions sha256=ed42f4a706d5024ebb80a83d88e45739eb6c44abc178234cb55c003bb8137e7a，Runtime461未变。晚于黄金20业务闭窗14:23:05，不污染该轮结果；下一例必须用此新配置准入。回执DELIVERY-CONTINUOUS-20261004/，未再Apply。


最新补件：Human源a2c706bac0（a67业务修复后增加1b8b2713c2验收合同及Host journal有限LF取证）待fetch；once修复源b2d5b2c1e80338a0ad1a42b045795d4c719adb4b（10060–10062净迁移及numeric排序）待核对。不再以原1b/769单笔作为当前可发候选。维护循环按完整候选净差分接手。
