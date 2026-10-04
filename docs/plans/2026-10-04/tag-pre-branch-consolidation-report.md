# 预发分支归并发布报告

本轮只做用户要求的分支归并与预发部署，原 E2E 暂停并保留检查点。

| 来源 | 冻结版本 | 归并方式 |
| --- | --- | --- |
| feat/tag-multitenant | c5c13339c6 | 目标基线，保留提示词与 GitHub 权限修复 |
| employee/backend-delivery | 11d6eb5061 | 正常 merge |
| employee/progress-integration | e5ef5adbea | 正常 merge |
| feat/evaluation-hub | 34dc79f7ea | 净变更归并；历史含被排除提交 |
| employee/progress-release | 36a18c03c2 | 净变更归并；保留累计 reader22/Human/SSE/once |
| codex/public-forwarding-ingress | 5ecfedf548 | 不合入目标；不是目标祖先，独有材料未带入 |

目标本身已有共享 forwarding 实现，本轮保留，不删除。该独立 CR 在共享预发流水线仍保留，此次用户要求的排除针对目标分支归并，没有擅自退出他人 CR。

源码已推送并远端回读：`5a7b6fa291a20ade5c17b803a66ca6812e7592b7`。源自精确 Run3110387322 的预发六 CR 清单；不把未提交修复或未关联的新候选加入此次范围。

本地全 Go 编译、145 定义门禁、14 受影响测试（零 fail/skip）、handler/server vet 均通过。reader22/human1/eval1 和完整迁移保持此前真实 PG 组合检查一致；未改动部分复用原134顶层测试证据。编译与这些本地检查不是办公 E2E。

复用目标 CR36355253，单次提交 Run3110388243。初始代码合并 WAITING，按该 task3459377989 提供的精确 source/target 解决四处冲突。release `46c5b21ffb835a11be1c2e1583c45df9595a0455` 已非force推送，发布树与源码仅差共享流水线原有 ingress 测试与文档，生产代码相等。release 编译通过；原 Run 恢复后代码合并、构建、制品扫描、预发部署与集成测试均 SUCCESS，18:36:41部署完成，人工预发验证 WAITING未关闭。

本轮不发新的业务消息，不清 History，不改 Runtime461/Tag配置/routine/全局DWS。heartbeat multica-e2e 已按用户暂停；主checkout及其他session WIP未带入。Golden20 的9完整通过/11失败、Human H01失败、once迟到与时间锚诊断均保留，不因归并升级成通过。

实际live两副本normal/loop22/human1/eval1，SLS分别证明18:35:03.320、18:36:13.651 backend启动，健康HTTP200。滚动过程曾一次502/一live，部署后回读已恢复，保留观察轨迹，不声称滚动零中断。

并发后到目标211d2c5174为Outlook/AgentMail能力，在本次源码冻结后提交，保留其代码但未包含在已部署release；不追加部署轮次或把它冒称本波生效。发布后仅回填文档，文档提交不触发新发布。证据：`/Users/yuanzhan/d1/employee-e2e-evidence/TAG-CONSOLIDATION-20261004/`。
