# Employee 记忆写入、查询与遗忘（现行合同）

本文是 EmployeeLoop 前台记忆工具与场域共享记忆的现行合同。设计依据见 `docs/plans/2026-10-03/employee-loop-backend-delivery/` 的记忆设计 §3.2、§4 W2/W3/W10、§6；首版私有记忆的实施记录见 `docs/plans/2026-10-03/employee-memory-capture.md`。代码与本文不一致时以代码为准，并同步修订本文。

## 1. 分层与权限

| 层 | 存储 | 谁能写 | 谁能读 | 谁能忘 |
| --- | --- | --- | --- | --- |
| 场域共享（scene） | `employee_learning`，`scope_kind='scene'` | 场域成员请员工记录（`audience=scene`）；Host 后台写入器（flush）；已验证经验 | 本场域全部成员，包括以后加入的人 | 记录人或被引原话的发言人；负责人在管理页 |
| 个人（private） | `employee_learning`，`scope_kind='private'`，按场域分区 | 本人请员工记录（`audience=me`）；本人发起任务的已验证经验 | 只有本人 | 只有本人 |

- 所有写入、遗忘只认 Host 冻结的来源：被选中的 `source_ref` 的发言人。模型参数里没有 actor、scope、trust、confidence；`audience`、`scope` 只是请求，由 Host 按可信目录里的场域 kind 裁定。
- 共享记忆只存在于 group 和 dm 场域；enterprise 与未知 kind 一律拒绝。
- 群里永远不返回或注入任何人的私人记忆。私聊里可以忘掉本人在其他场域（如群里）记下的私人记录，Host 会先对原场域重新做租户 fence。

## 2. 工具 v2 与滚动门控

新 chat 唤醒只有在所有在线副本都声明 `[employee-memory:1]`（`deploymentfence.AllLiveReplicasSupport`）时才冻结 v2 工具，否则冻结 v1。已冻结的 job 保持原工具 schema，重放不新增模型调用。没有 Diamond 开关：对 employee 模式的 Agent 全部生效。

| 工具 | 参数 | 说明 |
| --- | --- | --- |
| `memory_capture` | `source_ref, audience(me|scene), type, subject, quote, transcript_ref?` | `key` 由 Host 从 subject 生成：`<type 首字母>-<sha256(NFKC、小写、去空白标点的 subject)>[:20]`。同 subject 即为更正。 |
| `memory_lookup` | `source_ref, scope(me|scene), query` | `me`：选中发言人在本场域的私人记录；`scene`：本场域共享记录。最多 8 条，带记录引用、归属和日期。不返回 Run 推断候选（`inferred`）。 |
| `memory_forget` | `source_ref, record_ref` | `record_ref` 为简报标签（如 `m3`，从冻结的 `memory_manifest` 解析）或 lookup 返回的记录引用。 |

v1 工具（`key`/`record_id`）语义不变，只是 lookup 同样不再返回 `inferred` 记录，type 枚举冻结为原有六种。

Host 在工具事务里拒绝的调用（原话对不上、不是人发的行、无权遗忘等）以 `ErrToolRefused` 返回，模型可以在三次调用预算内纠正或向用户说明，不会整轮失败。所有写入都在 savepoint 中执行，失败只留下 journal 记录。

## 3. 写入规则

**`audience=me`**
- quote 必须是选中消息外层正文的子串；不接受 `transcript_ref`、引用消息、reaction、工具输出。
- `type=preference` 时记为本人自述（`user-stated`、受信、不衰减，拍板 D4）；其他类型为 `observed`、置信度 4、不受信。

**`audience=scene`**
- quote 必须是选中消息外层正文的子串，或 `transcript_ref` 指向的冻结转录行（`input_snapshot.transcript_refs.gN.text`）的子串；转录行只接受 `sender_class=human` 且发言人 ref 属于本租户。
- 记录为 `observed`、置信度 4、不受信；flush 写入为 `synthesis`、置信度 3、标为候选。
- 归因字段由 Host 从冻结证据填写：`subject`、`speaker_ref`（`dingtalk:<租户 org>:uid|open_id|staff_id:<v>`）、`speaker_name`、`said_at`、`capture_origin`（`window|transcript|flush`）、`capture_source_id`（发起记录的消息 `employee-message:<receipt_id>`）；`created_by` 是发起记录的人。
- replay 身份为 `("dingtalk-message:"+scene_id, 原消息 id)`：一条原消息最多产生一条共享记录，窗口路径、转录路径和 flush 共用；第二次记录返回第一条（`already_recorded`），被遗忘的记录不会经由同一条消息复活。

**冲突与更正**（只作用于不受信的共享记录）
- 同一作者更正：作者指记录人，或者（人请求的记录）同一原话发言人。新记录 supersede 该作者当前所有同 key 记录。
- 不同作者同 key：两条都保留为 active，新记录写 `conflicts_with`，简报和管理页展示「说法不一」。
- 不受信记录永远不能覆盖或并列于受信记录（已验证经验），返回 `ErrUntrustedCorrection`。
- 按原消息时间排序：较晚到达的更早原话写成 inactive 墓碑，不会替换较新的更正；早于 reset 的证据被拒绝。

## 4. 遗忘与重置

- 共享记录：只有 `created_by` 或 `speaker_ref` 等于选中发言人才能遗忘；其他人被拒绝，提示由负责人在管理页处理。精确 ID、保留墓碑（`ForgetSceneTx`）。
- 私人记录：只有本人；跨场域（仅私聊）先重新 fence 原场域再遗忘。
- `/reset-memory`（拍板 D3）：
  - 私聊：清空本场域共享记忆和发令者的私人记忆（不变）。
  - 群及其他非私聊场域：只清发令者在这里的私人记忆，以及 `created_by` 等于发令者的共享记录；不移动场域 reset 下界。整个群的共享记忆只能由负责人在管理页清空。
- 遗忘只按记录精确撤回；别人在其他消息里重复同一个值，那条消息照常可见。

## 5. Host API（供后台写入器使用）

`employeememory.Store` 上的事务方法都在调用方事务内加 workspace `FOR KEY SHARE` 与 namespace 状态行 `FOR UPDATE` 锁：

- `UpsertSceneFactTx(ctx, tx, scope, SceneFactInput)` → `SceneEntry{Record, State, Changed, Replayed, Conflicts}`
- `RetractSceneFactTx(ctx, tx, scope, id, actor)`：只撤回 `created_by==actor` 的自己产出
- `ForgetSceneTx(ctx, tx, scope, id, requester)`、`ForgetSceneByAuthorTx(ctx, tx, scope, author)`
- `SceneEntryTx`、`ActiveSceneFactsTx`（最多 200 条，新到旧）、`HostKey`
- 展示辅助：`SceneAttribution(rec)`、`ConflictPeers(records)`

## 6. 管理页

管理页只列共享层：主题、来源、证据、「谁 何日 说」、记录人（与发言人不同时）、「说法不一」。管理者看不到任何人的私人记录。
