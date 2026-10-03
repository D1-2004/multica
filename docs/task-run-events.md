# 长任务运行事件合同

PRI-105 的下层合同。基准是 `feat/tag-multitenant`，场域只有 `scene.Ref.scene_id`。

## 边界与参考

沙箱 adapter → daemon → `/api/daemon/tasks/{taskId}/messages` → PostgreSQL
`task_message` → 按游标读取。复用现有轨迹，不另建事件总线或正文副本。

PRI-105（Codex）负责原生事件归一、顺序、合并、重试、原子入库、恢复读取和本文。
Employee Loop 工作流负责消费游标、当前目标判断、对人发送时机、Tone、人设、通知
outbox 和钉钉呈现。本单不改 Employee Loop、steer 或 A2UI。接上层消费前，由该
工作流负责方核对本文；下层落库本身不产生模型请求、新任务或消息发送。

参考 PRI-94 的冻结链路调查：500ms 正文与工具重排、失败清空缓冲、批次部分写入、
缺少幂等。参考 PRI-97 对 Kinto `uploadTaskContext` / `pullTaskContext` 的源码结论：
运行中增量离开原设备，恢复读沙箱外的稳定记录；不搬运 provider 私有会话文件。
沿用本仓 PostgreSQL 事务与现有任务读取权限，避免进程内状态成为平台事实来源。

## 记录与身份

单批上限为 2000 条、HTTP body 上限 8MiB；新 daemon 每批最多 256 条且约 4MiB。
超过单条上限或最终重试仍失败时显式报错，不把未确认提交的正文算成已落库。

一条记录仍是一条 `task_message`，正文、工具参数和输出沿用现有脱敏字段。
新增 `event` JSONB 只存元数据：版本、workspace/agent、canonical scene、Employee
task/run/goal revision（存在时）、runtime/provider、session、turn/message/call ID、
phase/status/level、daemon 观察时间。不存在的原生 ID 留空，不生成假的关联。

workspace、agent、scene、Employee 关联与 provider 从平台已受理任务和 runtime 取得。
daemon 只能报告原生 session/turn/message/call、phase、时间等观测；它不能指定
workspace、scene、principal 或通知接收人。未受理 scene 的任务保留 task 范围，
不猜场域。读取场域事件要核对当前目录和 tenant fence，历史绑定不授权跨租户读取。

事件稳定键为 `(queue_task_id, seq)`；首次执行的 seq 从 1 开始，恢复同一个 queue 时
StartTask 返回已提交的 `message_seq`，新 daemon 从该游标续写。重试保持原 seq。
重复相同记录返回成功，不重复入库或发布；相同 seq 改内容返回 409。每批全提交或
全回滚，先提交再实时通知。旧 daemon 可继续使用旧 messages 请求；旧记录的
原生事件时间、阶段和 ID 均标为未知，不能补造历史。

## 对上层的视图

`GET /api/tasks/{taskId}/events?since=0&limit=500` 和 daemon 对应读取路径返回
`{events, next_seq, has_more}`。since 是该 task 的排他 seq 游标。包括不可播报行，
游标仍推进，避免只过滤可播报行造成无限重读。UI 轨迹与原 messages API 保持原格式。
同一读取支持 `scene_id` 和 `session_id` 过滤，sqlc 查询可供上层在原任务授权域内消费；
索引分别覆盖 task+seq、workspace+scene 和 workspace+session。

| kind | 原始 type | reportability | 含义 |
| --- | --- | --- | --- |
| assistant_text | text | progress | assistant 正文或正文增量，是未核实的叙述 |
| tool_started | tool_use | progress | 只证明工具开始，不证明成功 |
| tool_finished | tool_result | progress | 只证明收到结果，不证明任务完成 |
| status | status | progress/none | 仅已知 running / turn_complete / step_complete 可候选播报；未知状态不播报 |
| error | error | attention | 运行错误，不自动授权重跑 |
| diagnostic | thinking / log / unknown | none | 不作为对人正文，视图不返回内部思考和诊断正文 |

事件视图不包含工具 input/output 或 thinking 正文；详情仍在受限轨迹接口。
`phase` 区分 delta、message、commentary、final 和未知。final 只说明 provider 标注了
最终正文，不能代替平台终态；真实终态继续由现有 `employee.execution` 合同负责。
工具输出不是已送达文件，stdout 也不是用户已收到结果的证据。

Tone 使用服务器标注的事实种类、reportability、phase、tool、status 和关联 ID，
并由上层读取当前 `Persona.Tone` / `reply_tone`。模型产生的正文是数据，不是发送
指令；事件不接受模型指定的 Tone 或 audience，不增加一次润色模型调用。

## 合并、失败与恢复

500ms 是传输批次窗口，不是对人发送频率。只合并相邻、同 type、同原生消息/
阶段的正文碎片；跨工具、status、message ID 或 phase 不合并。每个合并片段的 seq 在首次观察时分配，
按真实到达顺序上报。session status、工具开始/结束、turn_complete / step_complete 作为独立事件。

网络失败后保留原批次，后续 flush 重试同一批，后来的事件排其后。最终 flush
失败明确记录缺口，不把“任务 completed”写成“所有事件已送达”。这不是 provider
原生输出的 exactly-once 保证：adapter 通道溢出、进程崩溃时尚未上报的片段、销毁
沙箱时尚未提交的片段不能从平台恢复，必须与已提交记录区分。

已确认提交的记录保留在平台 PostgreSQL，断线、滚动发布或换沙箱不改变事件键。
消费者保存 task+seq 游标，恢复后按页补读。新沙箱可读取公开正文恢复任务事实；
这不等价于恢复 Claude/Codex/OpenCode/Pi 私有 session 文件，也不承诺 native resume。
记录与任务同寿命，当前没有自动 TTL；删除任务/工作区按既有清理路径移除轨迹。
不在本次发布自动删除历史正文。以后若改 TTL，必须同时定义游标过期和显式 gap。

## 原生能力与验收

新增元数据写入和事件读取由 `MULTICA_TASK_RUN_EVENTS_ENABLED=1` 开启；缺省关闭。
原子入库/重试去重是既有 messages 接口的正确性修复，不随新视图开关关闭。迁移仅
增加一个 nullable JSONB 列与两个 concurrent 索引，不修改历史正文。旧记录视图
`version=0` 表示没有冻结事件元数据，不能冒充 version=1 的原生观测。
服务端部署不升级沙箱里的 daemon；FC candidate 必须另以同一 Multica SHA 构建。
旧 daemon 的合并顺序无法由服务端修正，只有新 daemon 的观测时间与阶段才是本次
上报协议证据。沙箱失败/旧镜像只能证明兼容读取，不能记为新协议云端验收通过。

CLI：`multica issue run-events <queue-task-uuid> --since 0 --limit 500 --output json`；
`--session <provider-session-id>` 过滤某个 session。请求完整 UUID 时不依赖 Issue。


Pi 支持 text_delta；OpenCode 支持 text part 和 step finish；Claude 支持完整 assistant
block；Codex 支持 agentMessage 和 phase。原生未知字段不猜，不把 reasoning 映成正文。
各 provider 没有公开阶段事件时不捏造阶段；运行终态由平台生命周期读取。

验证真实风险：同窗 text→tool→text 顺序、跨消息合并边界、重复/变更 seq、批次
失败回滚、并发重投、网络失败后重试、取消尾部 flush、诊断不播报、Direct/A2A
读取权限、canonical scene、session 过滤。预发另用实际长任务，在 running 时至少
两次读到持续增加的已提交事件，记录发布 SHA、sandbox daemon 版本、task/scene/
session 和读取游标，再请 Claude 只读 S4。源码和 fixture 不能替代云端实跑。
