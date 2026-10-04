# Employee / Tag 真实验收操作合同

用于Employee/Tag运行路径的端到端验收与相应环境操作。先按 [通用交付合同](development-delivery.md) 和 [领域验收标准](employee-delivery-workflow.md) 确定判据、场景、环境、范围与检查点，再读取本章相关操作；没有领域运行行为的改动说明E2E不适用即可。

## 发布与环境

遵守当前 Aone / FC skills，不沿用历史 CR、run、账号或 Runtime 作为现状。内网命令进程内去除代理，凭据不打印、不入库。schema 随 packaged migrator执行，不手动运行预发迁移。

当版真实验收准入需要源码/release血缘、各 live backend本次启动日志、fence normal与相应reader marker一致。确认一次保存当波manifest；新部署、重启、影响本例的配置变化或受影响版本改变后重查相关事实。无关配置变化不触发全量复验；read-only日志查询不触发部署。

模板变更后针对目标租户 Apply并GET回读。新reader先于producer；滚动恢复与混版边界按实际触及协议检查。Runtime候选保留固定两仓提交/Template，并按实际触及的FC/local生命周期取得真实canary证据；FC成功不代替持久设备验证。

manifest仅包含本用例需要的事实：server、workspace/agent/tenant、源码/release/run、Tag/runtime/template（若涉及）、live副本/窗口、scene.Ref与外部cid、演员profile、能力、routine与未终态干扰、恢复责任。复用无变化的manifest不等于跳过发送或读取时的权限检查。

后台预发与DWS网关是独立轴。本仓真实IM使用进程私有线上DWS配置；每次发送前核对，不切全局网关。`scene_id`为唯一内部场域键，cid仅外部发送/读取。不能换演员或用介绍卡充当真人答复。隔离必须两个明确独立scene/cid；续接与原失败复验保留原场域。

## 用例与取证

每例先定义用户效果、反例、全行为条件、所需证据面、等待截止和清理。自然台词，幂等发送；未知结果先回读，重试保留同一幂等键。

| 要证明 | 取证与边界 |
| --- | --- |
| 实际投递/文件 | 独立回读目标cid消息/附件，保留发送者、引用、内容、次数、时间、分页/失败；sendStatus与模型自述不代替 |
| 状态推进/归属 | Receipt→Job→Task/Run→queue→outbox；检查wait/input归属、授权、取消与退出；completed不自动证明退出 |
| 摄入/门禁/重启 | SLS固定时间窗、pod、source/receipt/job/scene；记录分页/截断，单pod不冒充全副本 |
| Employee输入与选择 | LF `employee_loop` / `employee_model`，冻结messages/tools、native调用、lease/replay与失败；不是旧Coordinator格式 |
| 沙箱实际模型/工具 | LF `agent_task` / `llm.call.N`，按精确Task/Run/runtime；根摘要不代替generation |
| Digest写入与来源 | LF `employee_scene_digest`、page_hash/游标/accepted/rejected；写入成功不代替下一轮独立使用 |

LF查已知ID先详情，不扫描全项目最近列表。列表定位时Employee使用 `--name employee_loop`，Task使用 `--name agent_task`，digest使用 `--name employee_scene_digest`。精确trace详情按返回JSON中的observation名选节点，不假定给详情命令附加 `--name` 会过滤生效。检查truncation，使用本仓公开API helper，不用不兼容的v2 observations。Coordinator专项才使用对应SLS技能。

记忆写入后按本例合同等待稳定，再独立询问；问题不含答案，Memory/History/原话分面。若需证明独立长期记忆使用，原答案仍在近期历史时不能判完整通过。证明零调用/零事件需要完整覆盖窗口，列表零命中不够。具体flush阈值从当前模块合同和配置确认，不把历史时刻写成永久规范。

## 判定与恢复

真实case状态为pass/fail/invalid_env/waiting_actor/blocked/incomplete/known_limit，partial或无实际断言不计完整pass。重启/部署穿窗按该例受影响程度判invalid_env；发布允许与持续测试并行时保留run/segment检查点，仅恢复或重跑受影响例，不重置整个测试轮次；保留原失败，不以清历史、换场域、改口径躲反例。

原失败复测只在该修复和部署属于本轮范围时执行；已过且未受影响例复用。LLM质量看事实、指代、连续性、必要执行及边界，不以速度/token或平均分抵消安全/归属失败。

暂停routine/改配置前登记原状态、恢复责任和时机。结束或中断后安全恢复并回读；若已明确交接且正在运行的测试/发布仍依赖该配置，可以保留状态并转交恢复责任与截止条件，不能为交接盲目强停或提前恢复。未落实的移交、恢复失败和未回读分别列明，不能声称已清理。证据持久保存，更新当前状态入口；报告产品结论、缺证限制、Runtime状态、恢复/转交结果与后续项。不要因某个观测面缺失自动扩大业务修复范围。
