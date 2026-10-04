# Employee 本轮唯一执行表

当前摘要唯一权威；最后更新：2026-10-04 10:30，Asia/Shanghai。上轮范围08:14冻结并在08:43前收口；09:00后的四项修复为用户单独授权的新批次。历史材料保留，不再作为当前状态入口。

## 当前交付版本

| 对象 | 当前事实 |
| --- | --- |
| Multica源码 | 已部署source11d6eb5061，含四项修复/epoch18；最后报告提交仅文档 |
| Release / 预发 | 74802f18290a36adce1e348870cc31b8d6a7fc5f；pipeline66 / run3110373861，10:00:29部署成功，验证门未关闭 |
| Live副本 | pod149134 09:58:52.009、pod56137 10:00:02.429本次启动；normal，两epoch18/webhook1/memory1–3 |
| 测试Agent | workspace5f8b5b73 / agent33af235e；RealNiubility rev11；runtime461aabb2，原Template4osx6sfmkew1ysmdck4n未切 |
| Runtime交付候选 | source55ac122f（实现2b463197）；内网CLI32feedf1；CI314792/run77295044 SUCCESS；Template53zkmuykn69wy7nhidpv；private Pi Runtime14835d95 |
| Runtime应用边界 | 独立cold/warm真实Task与11generation完整可读通过；未晋级到Employee461；candidate未宣称employee-direct-v1兼容 |
| 总签收 | 不能整体签收。交付/发布及若干机制成立；P1权限上下文遗漏和显式新Task错误仍阻断完整验收 |

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


## 10:30 最终部署与短E2E结论（当前摘要）
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
