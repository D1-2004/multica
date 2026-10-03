# EmployeeLoop 预发验收记录

环境：Qwen-DWS 对应预发 Tag；冬翔单聊及「各种 Tag」测试群。本记录只确认列出的真实场景，不代表跨场域收集、独立停止、Cron/Webhook 已验收。

## 发布与架构

- Task / 纠正 / 续接：目标提交 `f4aacecd1abc208ab29816dbdf8fac574f95c90c`，预发流水线 `3110336688` 的构建、部署和集成测试通过。
- 原生对话轮次：目标提交 `e2cda04a479fa4154ceb0fe92453963f197c010e`，流水线 `3110338376` 的构建、部署和集成测试通过。
- Coordinator 与 EmployeeLoop 使用同一配置；实测 revision 21 主模型为 `bailian/deepseek-v4.1-flash`。前台最多三次真实模型请求，无额外总结或润色模型。
- PostgreSQL 持久化独立 Task / Entry / Run；Issue 绑定校验留在适配器。Redis 复用既有跨节点唤醒和加速，不成为任务事实来源，不增加第二个执行器。
- 对照 GawkBot 固定版本 `71e82a1809565281cbd0bf8185d3c125b715d934`：保留工作定义、追加账本、纠正优先和 ContextUsed；近期对话使用现有 SessionEntry 的真实 user/assistant 轮次。PG 历史不固定条数删除，只限制每轮注入量。

## 真实 IM 结果

| 场景 | 核验结果 |
| --- | --- |
| 旧紫色 → 重设蓝绿 → 后者 → 只改小周紫色 | 正确确认蓝绿、回答小周绿色、保留小林蓝色并改小周紫色。每轮一个模型请求，仅回复，无 Task 派发或 memory_capture。 |
| 只输出 JSON | 收到合法 JSON 对象，小林蓝色、小周紫色；无前缀或 Markdown 围栏，一个模型请求。 |
| Python 等待 12 秒算平方 → 查进度 → 继续求合计 → 致谢 | 同一 Task 两个 Run，无 Issue。实际工具输出等待 12.00 秒、平方数组及合计 55；进度使用 read_task，致谢没有新执行。前台调用次数 1 / 2 / 2 / 1。 |
| 群里运行中将苹果 3 / 香蕉 2 改为苹果 5 / 无香蕉 | 同一 Task，旧 Run 取消、后继 Run 成功。FC 两次扫描确认进程退出后才领取后继；旧通知以 steered 原因抑制。只交付最新清单，不重复 60 秒等待。 |
| CSV 仅文件交付 → 同 Task 增加一行并继承交付要求 | 两份文件实际收到并下载核验，第二份增加 pear,2。两个 Run 都有 suppressed / native_file_delivered 回执，未追加完成总结。 |

### 历史理解与 JSON

剧本保留旧历史，没有清空或换人物规避冲突。历史 assistant 仅承载 Host 已验证的送达文本，不携带工具调用或授予权限；原 JSON 快照仍供审计，旧快照保留旧请求字节。

- [蓝绿重设](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/54459cc5a7c34acc8443017408b95341)
- [后者指代](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/f8d317a4dfc9447392e7dda1bc372d16)
- [单项更正](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/7eb23f7d777245debf67f7a2b0970a1b)
- [仅 JSON](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/32d60f52b679417bacbc6a68c8b6aab4)

### 独立 Task 续接

Task：`653ff1af-37af-46ef-b3a2-4345fc5716b5`。

| 执行 | Run | Queue | 终态事件回执 |
| --- | --- | --- | --- |
| 首轮 | `83be6c83-fb98-4d97-9e33-afe9f5f8f100` | `21edcb9e-41a7-497a-aafd-c960f2674518` | `66528887-838a-4322-a864-56cc91554ddd` |
| 续接 | `43c727e1-901b-425d-9377-10d45a8cd015` | `874bf7da-5ba9-4922-8c75-cfc23a60c1b5` | `f1480bb7-12fa-4dc5-bff9-6b7d5f07f294` |

每个 Run 只有一条终态 receipt 和一条 Langfuse ExecutionEvent。两个 queue 的 issue_id 均为空；实际 transcript 包含 Python bash 调用和工具结果，不能用模型自述替代。

### 群内纠正与退出屏障

Task：`8cf50385-33a4-4b18-a763-161f7f5093a3`。旧 queue `f9ac6ff7-5134-467b-9641-77248f768e1e`，后继 `2c9d04fc-1c33-4ef3-ba2b-d3161c8c9e89`。

- 13:15:30：取消旧执行并创建后继；后继被停止屏障暂缓。
- 13:15:36.636：第一轮扫描终止 5 个进程并杀死 1 个，remaining=0。
- 13:15:41.700：第二轮扫描 runners=0、remaining=0、outcome=ok。
- 13:15:47.672：后继实际领取，晚于退出证明。
- 13:16:07：群里收到苹果 5、无香蕉的新结果；旧通知 suppressed / steered。

退出证明来自 FC 两次进程扫描，不把逻辑 cancelled 或超时当作退出，也不冒称为 Daemon cancel-ack。

### 文件交付策略继承

Task：`66be2a13-6c61-4d88-ab33-2e78979443fb`。首轮 Run `aab9aa29-94f4-4c55-9627-a04cfef1f5cb`，续接 Run `d10a2409-47dc-4e12-a97d-0fa46a84c288`。

接收者账号从实际消息下载两份 CSV，解码 UTF-8 BOM、规范换行后，首轮为 name,qty 和 apple,5，续接新增 pear,2。

原始下载字节 SHA-256：

- 首轮：`5119f1b581eeb4f683fe892e91d9015d8b55ce29356b0297cd71efb38c2b9dcd`
- 续接：`bbb30127b40e86a86df6d6528d1ee8cd86e4829042a118baaf2264db833163b1`

## 失败记录与修复边界

1. EmployeeLoop 最初绕过 Coordinator 配置链。Trace `59cfce9e475c4aadaca8630615180cf2` 的首轮模型耗尽 45 秒，无工具或 Task。已接入同一链、单次 20 秒边界和根 Trace ERROR 标记。
2. v13 指代轮三个候选按 20 / 20 / 5 秒超时，历史证据完整。补齐非 thinking 请求和 4096 输出预算；这是确定参数缺口，不认定为所有超时的唯一原因。
3. v14、v16 已无超时，但单 JSON 历史加时序提示仍取旧紫色。改成逐轮角色后，保留旧紫色种子的 v18 才通过。保留失败证据，不把本地桩测试当作语义验收。
4. 回归发现并修复了旧记忆借历史复活、callback URL 错误关联、registry 故障阻断缓存恢复、纠正后交付源错配及仅文件策略丢失，并做了独立 PostgreSQL race 与真实 IM 对照。

## 尚未验收或未完成

- 独立 stop_task：单独停止后不派发后继 Run，与运行中纠正分开验收。
- 三个真实测试对象的跨场域收集、乱序归集及同一人参与两个 Task 的隔离；测试对象待指定。
- Cron / Webhook 的 Task Service 接入，以及定时、暂停、重投幂等真实验收。
- 纯附件问答与轻反馈需独立剧本；本记录的文件验收是产物交付及策略继承。
