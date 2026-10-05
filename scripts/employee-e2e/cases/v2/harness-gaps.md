# harness-gaps：cases-v2 对 employee-e2e harness 的需求

> 给 harness 负责人（`scripts/employee-e2e/`）。2026-10-03。
> 每条需求后面的「用例」列表由脚本从 `G/M/C/P/T.json` 统计，和用例文件保持一致。
> 现状依据：`el2e/driver.py`（只认 text/at/match_key/wait/observe/conversation；变量只有 `gen_vars` 的固定键）、`el2e/grader.py`（未知检查键静默跳过；每会话只读最新 100 条；sentinel 从用例开始扫到转录末尾）、`registry.json`（DEAP 演员只登记了 daiyu，员工 open_id 只有 zhujue/director 两个视角）。
> CLI 名称已用本机 `dws chat --help` 核对；DEAP 与跨组织的限制见 registry 与 README。

## 0. v2 用例格式（字段说明）

**文件**：`{schema: "el2e.cases.v2", suite, source, category, notes, defaults, cases}`。`defaults` 新增：
- `wait.none.pause_s`；`human_send.ai_tag=false`（人类发送一律带 `--ai-tag=false`）。
- `pattern_sets`：命名的排除正则（EN 英文漂移、SERVICE 客服腔、RESEND 让人重发、INTERNAL 内部工具名、CONTACT 手机邮箱、MDTABLE Markdown 表格）。检查里用 `exclude_sets: ["EN", ...]` 引用。
- `aliases`：人名别名正则（DONGXIANG、DZONG、FENGJIE、DAIYU、BAOCHAI）。字符串里写 `{=DONGXIANG}`。
- `templating`：**先**替换选中变量行的 `{VAR}`，**再**展开 `{=ALIAS}`（变量值里可以含别名，如 M-07 的 `OWNER_RE`）。
- `grading`：tier / requires / only_if / observe / sentinel / unknown_keys 的语义（见 2.3）。

**用例**：`id`（`G-01` 格式）、`title`、`focus`（F1–F5）、`maps_to`（GoldenCase 与 08 §5 验收 ID）、`scene`、`conversation`、`roles`（剧中名 → registry 演员键）、`var_sets`（变量行，可选）、`fixtures`（可选）、`x_run_window`（可选，`{from,to,why}`，窗口外跳过记 `skipped_window`）、`segments` + `x_long_running`（可选，分段续跑）、`requires`（`harness/platform/release/ops` 四类能力代码）、`status`（`ready / ready_partial / blocked` + 原因）、`known_gap`（可选）、`steps`、`judge{criteria, checks, semantic}`、`pending_checks`（等 P1 证据实现后转正的检查，先不计分）、`doc`（卡片文字，harness 忽略）。

**步骤**：
- 发言：`{id, actor, text, conversation?, at?, at_all?, reply_to?, match_key?, burst?, segment?, wait}`。`at` 里 `"employee"` 指员工，其余是 roles 里的剧中名。
- `reply_to`：`{step}` 引用某一步发出的那条消息；`{employee_reply_of: step}` 引用员工对某一步的最后一条回复；`{observed: step}` 引用某个 observe 步骤匹配到的员工消息；`{employee_latest: true}` 引用员工在该会话的最近一条消息；任一种可加 `fallback: "employee_latest"`。
- 动作：`{id, kind, actor?, conversation?, wait?, ...}`，`kind ∈ file / recall / react / forward / combine_forward / setup / webhook / aitable_insert`，参数见第 3、4 节。
- 观察：`{id, conversation?, observe: {until_regex, timeout_s, since_step?, optional?, negative?, include_placeholders?}}`。
- `wait.mode ∈ reply / silence / optional / none`（none 用 `pause_s`）。

**检查**：`steps, replies, max_chars, include_all, include_any, include_regex, include_any_regex, exclude, exclude_sets, last_include_all, quotes_step, match_count{regex, range}, only_if{step, include_all}, requires, tier, note`；哨兵 `{sentinel, parts?, conversation, scope: "case_span"}`；证据 `{evidence: max_calls_per_wake | no_effect_for_step | same_task_runs(+if_dispatched) | tool_arg_present | task_count | effect_for_step(+if_dispatched_at), ...}`。

## 1. 优先级总表

| 能力 | 优先级 | 用例 |
| --- | --- | --- |
| 人类发送 `--ai-tag=false`、每用例立即评分、转录分页（见 2.3、2.5） | P0 | 全部 88 条 |
| `grader_v2` grader v2：include_regex / include_any_regex / exclude_sets / 别名 / quotes_step / tier / requires→vacuous / observe 计入硬检查 / 未知键报错 | P0 | 73 条：G-01、G-02、G-03、G-04、G-05、G-06、G-07、G-08、G-09、G-10、G-11、G-12、G-13、G-14、G-15、G-16、G-17、G-18、M-01、M-02、M-03、M-04、M-05、M-06、M-07、M-08、M-09、M-10、M-11、M-12、M-13、M-15、M-16、C-04、C-05、C-06、C-07、C-08、C-09、C-11、C-12、C-13、C-14、C-15、C-16、C-17、C-18、C-19、P-01、P-02、P-03、P-04、P-05、P-06、P-08、P-09、P-10、P-11、P-12、P-13、P-14、P-15、P-16、P-17、T-05、T-07、T-08、T-09、T-10、T-11、T-12、T-13、T-15 |
| `var_sets` var_sets 变量行（替代 gen_vars，按种子选行，重名报错） | P0 | 54 条：G-01、G-02、G-03、G-04、G-05、G-06、G-07、G-08、G-09、G-10、G-11、G-12、G-13、G-14、G-15、G-16、G-17、G-18、M-01、M-03、M-04、M-05、M-06、M-07、M-08、M-09、M-10、M-11、M-12、M-13、M-15、M-16、C-04、C-05、C-06、C-07、C-08、C-09、C-11、C-12、C-13、C-17、P-01、P-02、P-03、P-04、P-05、P-06、P-09、P-10、P-11、P-14、P-15、P-16 |
| `quote_reply` 引用回复 step（reply_to：step / employee_reply_of / observed / employee_latest，含 fallback） | P0 | 26 条：G-03、G-08、G-09、G-11、G-12、G-14、G-18、M-01、M-02、M-03、M-04、M-06、M-08、M-15、M-16、P-01、P-02、P-04、P-05、P-06、P-11、P-12、P-13、P-16、T-12、T-15 |
| `deap_multi` 多 DEAP 演员：登记、各视角 open_id、读回改用真人 reader、能力约束校验、租约板 | P0 | 22 条：G-03、G-04、G-08、G-09、G-10、G-11、G-12、G-14、G-15、M-02、M-03、M-04、M-06、M-12、P-01、P-02、P-04、P-11、P-12、P-13、P-16、T-12 |
| `memory_reset` 跑前后私有记忆与场域记忆快照、清理 | P1 | 5 条：G-15、C-13、C-14、P-08、P-10 |
| `pg_read` PG 只读证据：collection / invitation / occurrence / learning / WorkPacket ref | P1 | 5 条：C-16、P-05、T-09、T-10、T-12 |
| `evidence_v2` 证据检查扩展：tool_called / tool_arg_present / tool_arg_contains / task_count / effect_for_step / if_dispatched | P1 | 4 条：G-17、M-16、T-05、T-13 |
| `segments` 分段可续跑：状态落盘、跨段共享变量、not_before_hours 调度、每段各自判有效性 | P1 | 4 条：G-15、C-15、C-16、T-09 |
| `file_send` kind=file：发 fixture 文件（预渲染、相对路径） | P1 | 3 条：M-06、M-07、M-08 |
| `file_download` 下载员工发出的文件并核对编码、行数、指定行和 hash | P1 | 1 条：T-06 |
| `recall` kind=recall：撤回某一步的消息 | P1 | 1 条：M-10 |
| `react` kind=react：emoji / 文字表情 | P1 | 1 条：M-13 |
| `forward` kind=forward：单条转发 | P1 | 1 条：M-11 |
| `combine_forward` kind=combine_forward：合并转发 | P1 | 1 条：M-12 |
| `at_all` at_all：--at-all + 正文 <@all> | P1 | 1 条：M-05 |
| `setup_group` kind=setup：建群、加成员、记录员工入群介绍 | P1 | 1 条：P-13 |
| `burst` burst：连发后批量读回（间隔 1–3 秒） | P1 | 1 条：M-09 |
| `negative_observe` 负向观察：某时刻前不得出现某正则 | P1 | 1 条：T-09 |
| `webhook` kind=webhook：0600 文件读密钥，可控 delivery_id / body / 签名，记录 HTTP 状态 | P2 | 1 条：T-11 |
| `aitable_insert` kind=aitable_insert：以演员身份写 AI 表格 | P2 | 1 条：T-10 |
| `config_snapshot` 跑前 Diamond / agent 配置快照断言 | P2 | 1 条：T-07 |

**P0 补齐后**：今天可跑 51 条 + 可跑但目标半是 known gap 20 条。**不改代码也能跑**的只有 14 条：G-19、M-14、M-17、C-01、C-02、C-03、C-10、P-07、T-01、T-02、T-03、T-04、T-14、T-16。

## 2. P0（不补就跑不了大部分用例）

### 2.1 var_sets 变量行

- **现状**：`driver.gen_vars` 只生成 C1–C3、MISS、G1/G2、D1–D3、X、SECRET、MARK 等固定键，`fmt` 遇到 `{CUST}` 这类键会原样发进钉钉。
- **请求**：
  1. 用例有 `var_sets` 时，用种子 `run:case:attempt` 选一行，与 `gen_vars` 合并；**用例变量覆盖 gen_vars**；加载时若变量名与 gen_vars 键重名直接报错（v2 源已校验无重名）。
  2. 替换顺序：变量 → 别名（`{=NAME}`）。替换覆盖 text、match_key、observe 正则、所有检查字段、fixture 名与模板。
  3. 选中的行写进 driver 记录 `rec.vars`，grader 用同一行。
  4. 分段用例（见 3.8）跨段共享同一行。
- **用例**：54 条：G-01、G-02、G-03、G-04、G-05、G-06、G-07、G-08、G-09、G-10、G-11、G-12、G-13、G-14、G-15、G-16、G-17、G-18、M-01、M-03、M-04、M-05、M-06、M-07、M-08、M-09、M-10、M-11、M-12、M-13、M-15、M-16、C-04、C-05、C-06、C-07、C-08、C-09、C-11、C-12、C-13、C-17、P-01、P-02、P-03、P-04、P-05、P-06、P-09、P-10、P-11、P-14、P-15、P-16

### 2.2 引用回复发送（quote-reply send）

- **现状**：driver 只会 `+messages-send`；带 `reply_to` 的步骤会被当成普通文本发出，**DEAP 演员这样发出的消息员工根本收不到**（不带 @ 的 DEAP 发言不投递）。
- **CLI**：`dws chat +messages-reply --ref-msg-id <openMessageId> --content <正文> --uuid <幂等键> --ai-tag=false`；群里可加 `--group <cid>`，单聊用 `--open-dingtalk-id`；需要同时 @ 员工时加 `--at-open-dingtalk-ids <员工在发送者视角的 id>`（CLI 会自动补 `<@id>` 占位符）。
- **请求**：
  1. 解析四种引用目标：`step`（该步落地的 messageId）、`employee_reply_of`（grader 同款归属算法找员工对该步的**最后一条**回复）、`observed`（observe 步骤 `matched_message.messageId`）、`employee_latest`（读该会话最新一条员工消息）。前三种找不到、且 fallback 也找不到时，把这一步记为 `harness_error`。`employee_latest` 当时还没有员工消息时，记下 `quote_miss`，问题仍按普通消息发出，随后给出有名字的判定。
  2. **人类引用员工消息时不要再加 `--at`**：钉钉引用会自动 @ 被引用人，正文再放 `<@id>` 会双 @（已踩坑）。人类引用**别人的**消息又要叫员工时才加 `--at-open-dingtalk-ids`；此时员工看到的正文开头是原始 `<@openId>` 占位符，grader 不扣分。
  3. 读回定位与普通发送一致（`--uuid` + senderId + match_key）。
- **按引用目标统计**：employee_reply_of 16 条：G-03、G-08、G-11、G-12、G-14、M-01、M-02、M-03、M-04、M-06、M-15、P-01、P-04、P-06、P-11、P-16；step 5 条：G-18、M-02、M-03、M-06、M-08；observed 5 条：M-16、P-05、P-13、T-12、T-15；employee_latest 4 条：G-09、P-01、P-02、P-12。
- **其中 DEAP 演员靠引用点名员工的**：17 条：G-08、G-09、G-11、G-12、G-14、M-02、M-03、M-04、M-06、P-01、P-02、P-04、P-11、P-12、P-13、P-16、T-12

### 2.3 grader v2

- **现状**：未知检查键静默跳过（`include_regex` 等在 v1 里不生效，答错也能过）；`x_proposed` 这类没有 `steps` 的检查会抛 KeyError；空检被记成 pass；每会话只读最新 100 条，整轮跑完再评分时前面用例的消息早已掉出窗口；sentinel 扫到转录末尾，会撞上后面用例。
- **请求**：
  1. **未知键报错**（检查、步骤、用例三层都要），不能静默跳过。
  2. 新检查键：`include_regex`（每条都要匹配）、`include_any_regex`（任一匹配）、`include_any`（字面量任一）、`exclude_sets`、`quotes_step`（该步的每条员工回复都引用了该步源消息）、`match_count`、`only_if`（条件不满足记 n/a）。
  3. 判定分档：缺省是硬检查；`tier: "target"` 失败而硬检查全过 → `degraded`；`requires` 指向的能力没开 → 该检查记 `vacuous`、不计分（`!x` 表示只在 x 未开时适用，`a+b` 表示都要开）。能力开关来自运行参数（如 `--capabilities group_all_delivery=off`）。新增裁决 `degraded`，与 `pass/fail/needs_review/invalid_env/harness_error` 并列。
  4. `observe` 步骤 `matched=false` 计为硬检查失败（`optional: true` 除外）。
  5. **每个用例跑完立即评分**，或把 driver 的快照并入 final_transcripts；读转录按时间窗分页、按 messageId 去重（不要用 `--start`）；评分前校验转录覆盖了用例时间窗，不覆盖就 `harness_error`。
  6. sentinel 加 `scope: "case_span"`：只扫本用例首步到 ended_at。
  7. `known_gap` 字段同步到 `cases/known_gaps.json`（棘轮规则不变）。
  8. `pending_checks` 只记录、不计分，在 summary 里列出「待补证据」。
- **用到新检查键的用例**：include_regex 16 条：G-03、G-04、G-06、G-10、G-13、G-15、G-16、M-04、M-07、C-08、C-14、C-16、P-01、P-06、P-08、P-12；include_any_regex 7 条：M-01、M-02、M-08、C-07、C-08、P-03、P-17；exclude_sets 53 条：G-01、G-02、G-03、G-04、G-05、G-06、G-07、G-08、G-09、G-10、G-11、G-12、G-13、G-14、G-15、G-16、G-17、G-18、M-05、M-06、M-07、M-08、M-12、C-04、C-05、C-06、C-07、C-08、C-09、C-11、C-12、C-13、C-14、C-15、C-17、C-18、C-19、P-01、P-02、P-03、P-04、P-05、P-06、P-08、P-09、P-10、P-11、P-12、P-13、P-14、P-15、P-16、P-17；tier 23 条：G-01、G-02、G-03、G-05、G-10、G-14、G-15、G-17、G-18、M-05、M-07、M-10、M-12、C-13、C-15、C-16、P-02、P-08、P-12、P-15、P-17、T-05、T-13；requires 28 条：G-01、G-02、G-03、G-04、G-05、G-06、G-07、G-08、G-10、G-14、G-15、G-16、G-18、M-02、M-05、M-07、M-08、M-10、M-12、C-13、C-15、C-16、P-02、P-08、P-15、P-17、T-05、T-13；only_if 1 条：M-02；quotes_step 1 条：M-01；match_count 1 条：T-07。

### 2.4 多 DEAP 演员：登录、登记、读回（DEAP actor login/registry）

- **现状**：registry 只有 daiyu，而且只在 group_de_probe；员工 open_id 只登记了 zhujue/director 两个视角。DEAP 发言后的读回用 DEAP 自己的 profile，`exclude_sender_ids` 存的是员工在真人视角下的 id，排除不掉员工；「收到～」「+1 多肉葡萄 少冰」这类短句只要员工恰好说了同样的子串，就会被判 `send_duplicated`、用例中止。
- **请求**：
  1. registry 统一演员键：`daiyu`（红楼·林黛玉）、`wangxifeng`（红楼·王熙凤，**v1 的 fengjie 合并到这里**）、`baochai`（红楼·薛宝钗）。登录：`dws_env.py as 'Real Niubility/DingTalk-FDE Director' -- dingtalk-tag manage login --agent-uuid <uuid>`，再 `e2e.py gw prepare`；登录前查租约板（5598799a），不 connect、不改演员；号池里的「红楼·贾探春」（staffId 507523443）就是 Qwen-Real，禁止选用。
  2. 登记**员工在每个 DEAP 视角下的 open_id**（`employee.open_ids.daiyu/wangxifeng/baochai`），以及演员彼此、真人在各视角下的 id。
  3. DEAP 发言的读回改用会话的真人 reader，按 **senderId + messageId** 定位，不再只做子串匹配。
  4. 能力约束校验：DEAP 步骤带 `at` 或落在单聊直接报错；DEAP 只能用 `reply_to`。
  5. 入群自动发的介绍卡和「暂无可展示的最终产物」占位卡继续由 grader 过滤。
  6. 跑前登记租约、跑后释放。
- **用例**：黛玉 7 条：G-08、G-10、G-14、M-03、M-12、P-11、T-12；凤姐 15 条：G-03、G-04、G-09、G-11、G-12、G-14、G-15、M-02、M-04、M-06、P-01、P-02、P-04、P-12、P-16；宝钗 1 条：P-13。

### 2.5 发送与读回的小修

- 人类发送带 `--ai-tag=false`（`+messages-send` 和 `+messages-reply` 都有这个开关，默认 true）。**全部用例**都需要。
- 短句定位：以下用例有 ≤6 字的人类短句（「好嘞」「👌」「？」「可以」「在不？」），读回必须按 senderId 区分：G-06「好嘞 谢了」、G-15「收到～」、G-15「👌」、M-01「这个不动哈」、M-15「可以」、C-01「在不？」、C-08「好嘞 谢啦」、C-16「好 辛苦」、C-16「行 先这样」、C-17「哪个还没过？」、C-18「？」、P-05「{ANS}」、P-14「{PB}」、T-01「可以，谢了」、T-02「好嘞」、T-04「跑上了没」、T-04「行 辛苦」、T-05「好 我周一用」、T-06「收到👌」、T-07「好嘞 辛苦」、T-12「好 谢了」、T-13「好 辛苦」、T-14「那个先停了吧」、T-16「可以 谢谢」
- 多 @（multi-@）：现有 harness 已支持**句首**多 @（`at: ["冬翔", "employee"]`），无需新功能；请补一个读回字段校验——员工看到的正文是「@东翔 @Qwen-Real …」，mentions 只含员工。用到多 @ 的用例：2 条：G-02、M-04。句中 @（真人习惯「…；@小Q 你把…」）列为 P2，可选。

## 3. P1（解锁 M 类消息形态与长时用例）

### 3.1 发文件 / 发图（file/image send）与下载核对

- **CLI**：`dws chat +messages-send --msg-type file --file <工作目录内相对路径> --group <cid>|--open-dingtalk-id <id> --uuid <key> --ai-tag=false`。CLI 不能把本地图片换成 mediaId，**原生内联图片发不了**：png 只能当文件发，或用 `+messages-forward` 转发一条已有的原生图片消息。
- **请求**：`kind: "file"`，`file: <fixture 名>`；fixture 支持 `template`（按变量行渲染文本）与 `by_row`（每行一份预渲染内容）；发送前把文件写进工作目录的临时子目录，再用相对路径发送；读回按 senderId + 时间 + 消息类型定位。png fixture 用标准库做不了渲染，请按变量行预渲染 3 张入库（M-08）。
- **下载核对**（`file_download`）：`dws chat +messages-resource-download` 下载员工发出的文件，校验 UTF-8、行数、指定行和 hash，写入证据。
- **用例**：发文件 3 条：M-06、M-07、M-08；下载核对 1 条：T-06。

### 3.2 撤回（recall）

- **CLI**：`dws chat +messages-recall --msg-id <openMessageId> --conversation-id <cid>`；只能撤回自己 2 分钟内发的消息，所以撤回步骤要紧跟目标步骤。
- **请求**：`kind: "recall"`，`target: {step}`；记录撤回结果；grader 在转录里把被撤回的消息标成 recalled（员工那边收不到撤回事件，这是平台缺口 recall_event）。
- **用例**：1 条：M-10

### 3.3 表情（react）

- **CLI**：emoji 用 `+messages-add-emoji --conversation-id --msg-id --emoji 👍`；文字表情要先 `+messages-create-text-emotion` 拿 emotionId 和背景 id，再 `+messages-add-text-emotion --emotion-id --emotion-name --background-id --text --msg-id --conversation-id`。
- **请求**：`kind: "react"`，`target: {employee_reply_of | step}`，`emoji` 或 `text_emotion`；之后的静默窗口照常判。
- **用例**：1 条：M-13

### 3.4 转发与合并转发

- **CLI**：`+messages-forward --src-conversation-id --msg-id --dest-conversation-id --uuid`；`+messages-combine-forward --src-conversation-id --msg-ids a,b,c --dest-conversation-id --uuid`。
- **请求**：`kind: "forward" | "combine_forward"`，`source: {conversation, step | steps}`，目标是步骤所在会话；读回按 senderId + 消息类型定位。
- **用例**：转发 1 条：M-11；合并转发 1 条：M-12。

### 3.5 @所有人

- **CLI**：`+messages-send --at-all`，正文带 `<@all>`。
- **请求**：步骤键 `at_all: true`。先做探针（见第 5 节），确认 @所有人 会不会触发员工的 receive_at。
- **用例**：1 条：M-05

### 3.6 建群、拉人、入群介绍

- **CLI**：`+chat-create --name --users <openDingTalkId,...>`（冬翔不进新群可省掉跨组织 data-auth）；拉人用 `dws chat group members`（add 子命令，跑前 `--help` 核对）；新成员可见历史用 `+chat-set-history --option ALL`；群昵称用 `+chat-update-nick --nick D总`。
- **请求**：`kind: "setup"`，`create_group: {name, members}` 或 `add_members: [剧中名]`；新建的会话写进本次运行的临时 registry（不污染共享 registry）；observe 增加 `include_placeholders: true`，能匹配员工的入群介绍卡（现在会被 PLACEHOLDER_PATTERNS 归为占位卡），并把 messageId 交给后续 `reply_to.observed`。
- **用例**：1 条：P-13

### 3.7 burst 连发

- **现状**：每条发送都阻塞读回，真人 1–3 秒的连发被拉成 5–15 秒，测不出合并窗口。
- **请求**：`burst: true` 的连续步骤先依次发送（间隔用 `pause_s`），全部发完后一次性批量读回定位；读回失败时整段记 `harness_error`。
- **用例**：1 条：M-09

### 3.8 多小时等待（multi-hour waits）：分段续跑、运行窗口、部署

- **现状**：driver 一次跑完 `case.steps`，不认识 `segments`；同步等 25 小时必然撞上部署（预发每天约 28 次），整段判 `invalid_env`；冬翔的跨组织 data-auth 只有 24 小时。
- **请求**：
  1. `segments: [{id, not_before_hours}]` + 步骤 `segment`：第一段跑完把状态（变量行、各步 messageId、员工回复 id）落盘；`e2e.py run --resume <run-id> --segment seg2` 在 `not_before_hours` 之后续跑；变量跨段共享；`reply_to` 可以引用上一段的步骤。
  2. **每段、每个 observe 各自判有效性**：某段窗口内有部署只重跑这一段（能重跑的话），不要整条作废。
  3. 续跑前自动刷新 token、检查 data-auth 剩余时长（不足 2 小时就先续权）。
  4. `x_run_window`：不在窗口内的用例跳过记 `skipped_window`，不算失败。
  5. 长窗口（T-09 约 95 分钟、T-07 约 25 分钟）支持「只在部署冻结窗口跑」的调度开关（读 pipeline 66 状态）。
  6. 负向观察（`observe.negative: true`）：在给定时间段内**不得**出现某正则（T-09 暂停段）。
- **用例**：分段 4 条：G-15、C-15、C-16、T-09；运行窗口 4 条：G-04、G-12、T-01、T-09；长时 4 条：G-15、C-15、C-16、T-09；负向观察 1 条：T-09。

### 3.9 上下文栅栏（context fence）

EmployeeLoop 读当前场域最近 20 条 / 16 KiB 和 24 小时历史，还会注入私有记忆 brief；用例之间如果不隔开，前一条用例的材料会被当成本用例的（DS-20 基线就是这样失败的），偏好类用例写下的私有记忆还会改变后面用例的输出格式。请求：

1. **污染检测**：grader 从每次唤醒的第一个模型请求里取当前窗口和历史里出现的 openMsgId，若包含**其他用例**的步骤消息，给本用例打 `context_contaminated` 标记并列出来源；裁决不变，但 summary 要显示。
2. **记忆快照与清理**（`memory_reset`）：用例前后对员工在相关场域的私有记忆和场域记忆做快照，用例结束后按用例声明清理（管理 API；没有 API 前先人工清理）。偏好与记忆类排在单聊批次最后。用例：5 条：G-15、C-13、C-14、P-08、P-10
3. **会话占用锁**：同一会话一次只跑一个用例；跨会话用例（G-09、C-14、P-08、P-09、P-10、P-15、P-17、C-13）要锁住它涉及的全部会话。
4. **部署 fence**：每个用例开始前读一次 `GET /api/internal/deployment-fence`（`MULTICA_LOG_TAIL_TOKEN`）；状态不是 `normal`（`draining` / `frozen`）时等待，窗口内发生状态变化的用例判 `invalid_env`。
5. **例行任务干扰**：跑 dm_zhujue 批次前暂停「每小时对话汇报」（e02d1d7b），结束后恢复；暂停期间 grader 关闭 HH:00–HH:06 的粗过滤，改为按例行任务 run 记录精确排除。用例：21 条：M-14、M-15、M-17、C-04、C-07、C-08、C-11、C-14、C-17、C-18、P-05、P-08、P-09、P-10、P-15、P-17、T-02、T-04、T-05、T-13、T-14
6. **例行任务会话不登记为 routine**：group_t 里平台发的「开始执行例行任务…」是被测内容，不能被当成噪音过滤（T-08–T-11）。

### 3.10 证据扩展与 PG 只读

- `tool_called{steps, tool, count}`、`tool_arg_present{step, tool, arg}`、`tool_arg_contains{step, tool, arg, values}`、`task_count{min, max}`、`effect_for_step{step, tools, if_dispatched_at?}`、`same_task_runs.if_dispatched`（没派任务时记 n/a，现在会因 tasks=0 判 fail）。用例：evidence_v2 4 条：G-17、M-16、T-05、T-13；条件版 same_task_runs 1 条：M-16。
- PG 只读（依赖 P4 只读 API）：collection / invitation / input 计数、routine occurrence 的 planned_at 与 skipped 原因、verification 与 learning、WorkPacket 的 learning ref、process_exit_confirmed。用例：5 条：C-16、P-05、T-09、T-10、T-12
- 待补证据清单（现在在 `pending_checks` 里，不计分）：C-12（tool_called·memory_capture）；C-13（tool_called·memory_capture）；C-13（tool_called·memory_forget）；C-16（pg_learning）；C-16（workpacket_ref）；P-05（pg_collection）；P-06（at_includes）；P-07（tool_called·memory_capture）；P-07（tool_called·memory_capture）；T-02（effect_for_step）；T-03（send_within_s）；T-04（effect_for_step）；T-04（process_exit_confirmed）；T-06（file_download）；T-09（negative_observe）；T-09（pg_occurrence）；T-10（per_occurrence_calls）；T-11（match_once）；T-11（quiet_windows）；T-12（pg_collection）；T-14（effect_for_step）；T-16（tool_arg_contains·dispatch_task）

## 4. P2

- **Webhook 发送器**（`kind: "webhook"`）：从本地 0600 文件读地址和密钥；可控 `delivery_id`、`body`、签名（含故意错签）；记录 HTTP 状态并与 `expect_http` 比对；`quiet_window_s` 内员工消息必须为 0。用例：1 条：T-11
- **AI 表格写入**（`kind: "aitable_insert"`）：以演员身份往指定表写一行（`dws aitable` 记录新增）。用例：1 条：T-10
- **配置快照断言**（`config_snapshot`）：跑前读 Diamond / agent 配置（如 `runtime.employee_watchdog` 对 Qwen-Real 的 running 阈值），不符合用例前置条件就跳过。用例：1 条：T-07
- **句中 @**：真人常把 @ 放在句中，harness 现在只能放句首。暂无用例依赖。

## 5. 先做的探针（不是 harness 代码，但决定几条用例的写法）

用**真人号对照**探测，不要给 Qwen-Real 或 DEAP 演员加第二个事件消费者（每个账号只能有一个事件流）：Director 在 g_team 里发 @所有人、单条转发、合并转发、卡片，由冬翔用自己的 `dws event +listen-im` 观察 receive_at / o2o 的事件键与 content 形态，再推断员工那边看到的样子。结论写进用例前置条件。用例：2 条：M-05、M-11

## 6. 不是 harness 能修的阻塞（便于对齐）

| 类型 | 代码 | 说明 | 用例 |
| --- | --- | --- | --- |
| 平台 | `group_all_delivery` | 群内非 @ 消息投递给 EmployeeLoop（现在只订阅 receive_at） | 12 条：G-01、G-02、G-03、G-04、G-05、G-06、G-07、G-10、G-14、G-16、G-18、P-02 |
| 平台 | `event_trigger` | 主动参与（Qwen-Real event_trigger_enabled=false） | 2 条：G-07、G-14 |
| 平台 | `recall_event` | 撤回事件订阅并从历史剔除 | 1 条：M-10 |
| 平台 | `reaction_event` | reaction 事件订阅（合同 held） | 1 条：M-13 |
| 平台 | `forward_mapping` | 单条转发原作者 / 合并转发 ForwardMessages 映射进 DispatchMessage | 2 条：M-11、M-12 |
| 平台 | `member_event` | 群成员进出事件 + 不带 @ 的入群卡片 | 1 条：P-13 |
| 平台 | `vision` | EmployeeLoop 图片理解路径（RES-02） | 1 条：M-08 |
| 平台 | `third_party_at` | 员工在群回复里真 @ 第三人 | 1 条：P-06 |
| 平台 | `cross_scene_self` | 同一请求者跨场域使用自己的数据（G4） | 3 条：C-13、P-15、P-17 |
| 平台 | `person_memory` | 按人、跨场域的低敏工作偏好（memory-design 待定） | 1 条：P-08 |
| 平台 | `resource_followup` | 非引用追问回读同场域最近附件（wave2:06-resources） | 1 条：M-07 |
| 平台 | `at_all_probe` | @所有人 是否触发 receive_at（未验证） | 1 条：M-05 |
| 第二波发布 | `marker13` | marker 13（跨场域收集 A2、builds_on G5、follow_up_steps P3）未上预发 | 4 条：P-05、T-05、T-12、T-13 |
| 第二波发布 | `B3_decision` | 例行任务 decision 模式（B3）未交付 | 2 条：T-10、T-11 |
| 第二波发布 | `F2_webhook` | Webhook 例行任务改走 Employee（F2）未交付 | 1 条：T-11 |
| 第二波发布 | `G_memory` | 经验沉淀与召回（G2/G3）未交付 | 3 条：G-15、C-15、C-16 |
| 运维 | `watchdog_override` | 预发 Diamond 给 Qwen-Real 单独把 watchdog running 阈值调到 300s（需授权） | 1 条：T-07 |
| 运维 | `deploy_freeze` | 长窗口需在部署冻结窗口跑（预发每天约 28 次部署） | 2 条：T-07、T-09 |
| 运维 | `routine_pause` | dm_zhujue 批次期间暂停「每小时对话汇报」例行任务 | 21 条：M-14、M-15、M-17、C-04、C-07、C-08、C-11、C-14、C-17、C-18、P-05、P-08、P-09、P-10、P-15、P-17、T-02、T-04、T-05、T-13、T-14 |
| 运维 | `probe_first` | 先用真人号对照探测事件形态 | 2 条：M-05、M-11 |

## 7. 实现状态与 API 缺口（harness 侧，2026-10-03）

预发 PG 从本机连不上，`pg_read` 一律走预发 HTTP API（`pre-fde` profile），实现在 `el2e/preapi.py`、`el2e/api_facts.py`，由 `e2e.py collect` 写入 `evidence/<case>.a<N>.api.json`。下列事实**没有 API**，对应的待补证据记为 `api_gap`，不伪造：

| 事实 | 需要的用例 | 现有 API 能给的 | 缺口 |
| --- | --- | --- | --- |
| verification passed + verified learning（`pg_learning`） | C-16 | `GET /api/employee-tasks/{id}` 的 `latest_run.verification` gate | EmployeeLoop learning 行没有读 API |
| WorkPacket / ContextUsed 的 learning ref（`workpacket_ref`） | C-16 | `evidence_refs`、`links` | WorkPacket 内容与 ContextUsed 未暴露 |
| collection / invitation / input 计数（`pg_collection`） | P-05、T-12 | 任务详情 `collections[]`（expected / received / state）、`waiting_counts.open_collections` | invitation 与每个参与者的 input 没有 API |
| routine occurrence 的 planned_at 与 skipped 原因（`pg_occurrence`） | T-09 | routine runs 的 status / source / failure_reason / created_at / completed_at | planned_at 与跳过原因字段未暴露 |
| process_exit_confirmed | T-04 | 任务详情 `execution.exit_confirmed` / `stop_requested` / `state` | 无 |
| 员工私有记忆快照（`memory_reset` 前后） | G-15、C-13、C-14、P-08、P-10 | Coordinator `GET /api/agents/{id}/scene-memory/{sceneId}` | EmployeeLoop 共享/私有 learning 没有读 API；清理用 `/reset-memory` 场域指令，回执即证据 |
