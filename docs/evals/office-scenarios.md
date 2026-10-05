# 通用办公场景评测定义

评测集包含独立的 20 条 P0 Golden 组合回归与本文件索引的分场景定义。分场景数据以 `server/internal/evalcatalog/office-scenarios.json` 为准，Golden 定义以 `server/internal/evalcatalog/golden.json` 为准。

每条场景用例只处于一种核对方式：对话或收到的文件里就能判断；对话之外再看一条事实；或还缺一项运行条件。页面上分别标成可跑可验证、补一条事实可判断、还缺条件。

每条用例展示三个字段：

- `roles`：需要的角色与其测试读取范围。
- `verifies`：验证目标和具体通过条件。
- `method`：测试夹具、操作与核验步骤。

`id`、`title`、`sources`、`origin`、`sourceCases` 是索引元数据。`existing` 表示可追溯到来源库中的既有用例定义；`defined` 表示按当前合同补充的场景定义。两者都不表示已执行、已有 runner、已部署或通过。

场域只使用当前 `scene_id` / `scene.Ref`，角色名字均为合成别名。真实消息及文件由获准接收者独立读回。故障注入、缓存清理和重启仅限登记的专用隔离实例。

## 场景索引

| 场景 | 用例数 | existing | defined |
| --- | ---: | ---: | ---: |
| 群参与与消息对象 | 8 | 8 | 0 |
| 信息澄清与对话指代 | 8 | 6 | 2 |
| 群内授权与可信来源 | 8 | 2 | 6 |
| 任务分派与工作规划 | 8 | 7 | 1 |
| 进度查询与等待状态 | 7 | 5 | 2 |
| 多轮续接与纠正 | 8 | 5 | 3 |
| 停止、退出与控制边界 | 7 | 4 | 3 |
| 文件、文档与实际材料 | 8 | 6 | 2 |
| 多人收集与输入归属 | 8 | 5 | 3 |
| 跨群、单聊与组织边界 | 7 | 1 | 6 |
| 主动提醒与授权后续 | 8 | 7 | 1 |
| 定时办公事项 | 8 | 5 | 3 |
| 外部事件与 Webhook | 8 | 7 | 1 |
| 私有记忆、纠正与遗忘 | 7 | 4 | 3 |
| 验证经验、复用与治理 | 7 | 5 | 2 |
| 幂等、故障与持久恢复 | 8 | 1 | 7 |

共 16 个场景、123 条定义，其中 existing 78 条、defined 45 条。浏览器 UI 与远端 MCP 原始用例保留在来源库，不计入这 123 条办公场景。

## 定义索引

### 群参与与消息对象

测试群中接收对象、参与配置及消息来源决定员工处理资格。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-at-other` | 真实 @ 别人 | existing |
| `office-at-employee` | 真实 @ 员工 | existing |
| `office-bare-group-greeting` | 未指定对象的群问候 | existing |
| `office-mixed-recipients` | 同一窗口不同接收者 | existing |
| `office-old-task-addressing` | 旧任务不改变接收对象 | existing |
| `office-quoted-employee-call` | 引文中的呼叫 | existing |
| `office-outside-role-request` | 岗位外开放求助 | existing |
| `office-bound-name-vs-label` | 绑定身份与配置名区分 | existing |

### 信息澄清与对话指代

在材料不完整、短答和冲突历史中定位当前需求。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-missing-body` | 外发缺正文 | existing |
| `office-missing-material` | 指定材料不可用 | defined |
| `office-ambiguous-reference` | 多对象指代有歧义 | defined |
| `office-latest-reset` | 最新明确重设覆盖旧值 | existing |
| `office-one-field-correction` | 单项更正保留其他事实 | existing |
| `office-approval-vs-closing` | 短肯定按原对话解释 | existing |
| `office-json-only` | 指定 JSON 格式 | existing |
| `office-quoted-question-negation` | 疑问中的否定表述 | existing |
| `office-questions-stay-separate` | 明确不要合并的两问分开回报 | defined |

### 群内授权与可信来源

控制、读取和发送使用当前外层授权、受理主体与场域权限。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-control-other-owner` | 群内控制他人的私有任务 | defined |
| `office-unaddressed-control` | 未取得参与资格的群控制 | defined |
| `office-reference-no-authority` | 引用旧命令不授予新权限 | existing |
| `office-reaction-no-control` | 轻反馈不执行旧命令 | existing |
| `office-payload-actor-spoof` | payload 人物字段不授予身份 | defined |
| `office-revoke-before-send` | 发送前权限撤销 | defined |
| `office-scene-link-scope` | 场域配置入口权限 | defined |
| `office-unknown-scene-held` | 未知场域不猜测路由 | defined |

### 任务分派与工作规划

新工作、旧事项和同窗多交付物分别形成正确任务计划。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-python-real` | 实际 Python 计算 | existing |
| `office-new-goal-same-person` | 同人不同交付物 | existing |
| `office-mixed-old-new-work` | 同窗旧工作更改与新工作 | existing |
| `office-two-existing-targets` | 同窗两个旧目标 | existing |
| `office-direct-without-issue` | 独立 Task 不依赖 Issue | existing |
| `office-accept-ack-state` | 接单 ACK 与执行状态 | defined |
| `office-durable-capacity-wait` | 容量满时真实新工作等待 | existing |
| `office-same-marker-different-goal` | 诊断标记相同不合并目标 | existing |

### 进度查询与等待状态

前台答复反映实时工作、执行、输入和停止事实。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-running-progress` | 运行中依据实时进度回答 | existing |
| `office-presence-while-busy` | 后台忙碌时前台回复 | existing |
| `office-waiting-input-count` | 等待参与者准确计数 | existing |
| `office-scheduled-wait-honesty` | 等待时点诚实说明 | defined |
| `office-stopping-not-stopped` | 停止中不宣称已退出 | existing |
| `office-failed-status-facts` | 失败状态保留真实报告 | existing |
| `office-multiple-progress-targets` | 进度查询目标歧义 | defined |

### 多轮续接与纠正

同一目标延续、实质更改和技术重放分别处理。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-continue-success` | 成功任务同 Task 续接 | existing |
| `office-addition-same-work` | 同工作补充材料 | existing |
| `office-repeated-presence-no-resume` | 重复提醒不重新执行 | existing |
| `office-steer-exit-barrier` | 运行纠正等待旧执行退出 | existing |
| `office-steer-delivery-inheritance` | 纠正保留未变交付约束 | defined |
| `office-continue-own-file-receipt` | 续接使用新 Run 的文件回执 | existing |
| `office-continue-stale-read` | 过期读取引用拒绝写入 | defined |
| `office-continue-replay-commit` | 续接提交后重放不重复 | defined |

### 停止、退出与控制边界

停止须精确作用于原任务并具有外部进程退出证明。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-explicit-stop` | 明确停止原执行 | existing |
| `office-negative-stop` | 否定停止无副作用 | existing |
| `office-stop-old-output-suppressed` | 停止后不补发旧结果 | existing |
| `office-stop-steer-predecessor` | 停纠正后继仍确认前驱退出 | defined |
| `office-duplicate-stop` | 同源停止技术重放 | defined |
| `office-lease-expiry-not-exit` | 租约到期不等于进程退出 | defined |
| `office-thanks-after-stop` | 停止后查询及致谢 | existing |

### 文件、文档与实际材料

回答使用真实资源，交付由接收者独立读取字节验证。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-file-native-delivery` | 原生文件与内容读回 | existing |
| `office-file-no-extra-summary` | 仅文件不追加总结 | existing |
| `office-attachment-hidden-code` | 文档附件中的随机码 | existing |
| `office-image-pixels-relations` | 图片文字和图形关系 | existing |
| `office-resource-bad-large` | 坏文件和过大资源 | existing |
| `office-resource-permission` | 无权限文档不可读取 | existing |
| `office-file-upload-not-delivered` | 上传不当作已送达 | defined |
| `office-file-failure-report` | 文件失败保留原始错误 | defined |

### 多人收集与输入归属

邀请、答复、计数及汇总按获准对象和原发起场域绑定。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-collection-out-of-order` | 三人乱序归集 | existing |
| `office-collection-two-task-person` | 同人两 Task 答复 | existing |
| `office-collection-unreferenced-ambiguity` | 无引用多邀请歧义 | existing |
| `office-collection-replay-count` | 答复技术重投不加人数 | existing |
| `office-collection-late-after-close` | 取消或撤权后的迟答 | existing |
| `office-collection-pending-dm-scene` | 首次联系对象建立单聊场域 | defined |
| `office-collection-uninvited-answer` | 未受邀成员不计入人数 | defined |
| `office-collection-answer-not-learning` | 参与者答案不自动变共享记忆 | defined |
| `office-collection-cancel-no-recount` | 取消后不复述已作废人数 | defined |

### 跨群、单聊与组织边界

scene.Ref、当前租户和身份桥限定工作资料及效果范围。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-same-people-different-groups` | 相同成员的两个群 | defined |
| `office-dm-group-private-isolation` | 同人单聊与群私有隔离 | existing |
| `office-same-locator-two-tenants` | 相同外部 locator 的不同租户 | defined |
| `office-org-unbound-current-fence` | 组织解绑后读取与发送 | defined |
| `office-cross-org-read-not-contact` | 跨组织可读不等于可联系 | defined |
| `office-forwarded-invite-principal` | 转发邀请不转让答复身份 | defined |
| `office-shared-disk-private-workdir` | 共享资料与私有执行目录 | defined |

### 主动提醒与授权后续

停滞、等待及终态推进各自使用当前工作事实和单独授权。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-watchdog-no-progress` | 无可观测进展提醒 | existing |
| `office-watchdog-heartbeat` | heartbeat 不当工作进展 | existing |
| `office-watchdog-two-consumers` | 双消费者不重复提醒 | existing |
| `office-waiting-target-reminder` | 只提醒未答的获准对象 | existing |
| `office-authorized-terminal-followup` | 明确预授权多步推进 | existing |
| `office-terminal-without-plan` | 成功事实不自动继续工作 | defined |
| `office-followup-stale-revision` | 旧版本或取消阻止后续 | existing |
| `office-followup-budget-bound` | 多步计划达到预算收束 | existing |

### 定时办公事项

真实发生时点、冻结工作包和暂停状态约束例行任务。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-cron-real-due` | 真实到点交付 | existing |
| `office-cron-timezone` | 配置时区决定下一时点 | defined |
| `office-cron-occurrence-replay` | 同发生技术重投 | existing |
| `office-cron-instruction-freeze` | 受理后改指令 | existing |
| `office-cron-pause-resume` | 暂停跨点与恢复 | existing |
| `office-cron-decision` | 定时条件 quiet/dispatch | existing |
| `office-cron-automation-private` | 自动化不借人私有 namespace | defined |
| `office-cron-one-notice-owner` | 例行结果通知唯一归属 | defined |

### 外部事件与 Webhook

外部事件以可信绑定、原始验签和稳定 provider 身份受理。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-hook-signature` | 合法和错签事件 | existing |
| `office-hook-raw-byte-signature` | 验签使用原始字节 | defined |
| `office-hook-idempotent-replay` | 同事件 ID 重投 | existing |
| `office-hook-content-conflict` | 同 ID 不同内容冲突 | existing |
| `office-hook-new-event-identity` | 新 ID 正常独立受理 | existing |
| `office-hook-disabled` | endpoint 禁用后新事件 | existing |
| `office-hook-payload-scene` | payload 伪造场域拒绝 | existing |
| `office-hook-decision-target` | 条件决策仅作用指定目标 | existing |

### 私有记忆、纠正与遗忘

记忆使用当前 scene/requester-private、外层来源与精确墓碑。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-memory-capture-private` | 明确记住自己的事实 | existing |
| `office-memory-correct-supersedes` | 同 key 更正 | existing |
| `office-memory-forget-specific` | 精确遗忘不擦除他人资料 | existing |
| `office-memory-source-replay` | 改 key 的旧 source 不重复学习 | defined |
| `office-memory-source-outer-only` | 引文和工具结果不伪装记忆来源 | defined |
| `office-memory-withdrawn-history` | 遗忘内容不借近期历史复活 | existing |
| `office-memory-mixed-requesters` | 多人窗口不聚合私有记忆 | defined |
| `office-same-chat-drink-recall` | 同一对话里刚确认的饮品不被推翻 | defined |

### 验证经验、复用与治理

可信验证、实际复用和共享/撤回是独立事实。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-experience-host-proof` | 可信 Host 验证 | existing |
| `office-experience-fake-success` | 自报成功与错误目标不晋级 | existing |
| `office-experience-real-reuse` | 新真实 Task 使用经验 | existing |
| `office-experience-owner-grant` | 共享晋级必须有 owner grant | existing |
| `office-experience-revoke` | 撤回后新任务不召回 | existing |
| `office-experience-old-evidence-fence` | reset 前旧证据时间栅栏 | defined |
| `office-experience-commit-recovery` | 提炼或晋级提交后恢复 | defined |

### 幂等、故障与持久恢复

专用隔离实例验证同源、提交、缓存、重启及投递恢复的唯一效果。

| 用例 ID | 标题 | 来源类型 |
| --- | --- | --- |
| `office-source-replay-vs-message` | 技术重投与自然重复不同 | existing |
| `office-commit-before-notify` | 提交后丢唤醒通知 | defined |
| `office-cache-expiry-eviction` | 缓存到期与删除恢复 | defined |
| `office-redis-unavailable` | Redis 不可用回退 PG | defined |
| `office-consumer-restart` | 消费者重启恢复原回执 | defined |
| `office-delivery-unknown-reconcile` | 未知投递只查状态 | defined |
| `office-model-journal-replay` | 已保存模型结果零新增请求 | defined |
| `office-restart-window-invalid` | 共享环境变更使证据窗口失效 | defined |

## 运行入口与前置资源

本目录不提供运行按钮，也不新增测试执行器。选择用例时将其 `roles`、`verifies`、`method` 交给对应的现有测试入口或专用场域剧本。

- 群消息与控制：登记当前场域、可信接收身份和参与配置，核实际 receipt/job、工具效果与独立消息读回。
- 执行与文件：登记专用 Runtime 和产物读取权限，核真实工具输出、进程退出及原生文件自身资源。
- 收集与提醒：登记明确参与者、邀请与提醒授权；仅操作自己的测试场域。
- Cron 与 Webhook：提供可管理的测试 routine/endpoint、可信时钟及安全签名 provider。
- 记忆与经验：限定当前 scene/requester-private，使用精确记录与来源墓碑；verified 经验需要可信 Host checker 和实际 manifest 引用。
- 恢复与故障：提供独立 PostgreSQL、专用 Redis、消费者和 Runtime；观察实际 TTL、正常 poll 与全部配置重试窗口。

需要具体资源才能成立的定义保持其前置条件；缺少资源时不把替身结果填作真实用户效果。
