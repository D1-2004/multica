---
name: inspect-daily-qa
description: >
  巡检数字员工一天的实际问答，将 dws-env as 的真实消息、Coordinator SLS、Langfuse 和场域记忆串成问题报告。
  用户说「巡检今天」「菲迪问答质量」「SLS + Langfuse 巡检」「记忆为什么没更新」「快速查到问题」时使用。
  这是开发面只读巡检，不是数字员工岗位技能；不自动发消息、重置记忆或做 E2E。
metadata:
  audience: coding-agent
---

# 日常问答巡检

交付的是有证据的问题及覆盖范围。默认不改业务状态；用户另外要求修复、提交或发布时，按其授权继续。不要把本 Skill 放进员工的 agent/skills 或 dingtalk-agent.json。

## 固定范围和身份

1. 读仓库 `CLAUDE.md`。写下日期、时区、起止时刻和取数截止；跨零点先说明日历日假设，必要时问一次。各数据源用同一窗口，不用滚动“24h”冒充昨天全天。
2. 使用 `dws-env` 的 `status`、`actors`，再 `as 教练 -- contact user get-self`（其他员工用已解析角色）。记录 profile、userId、openDingTalkId，业务命令一律 `as`。`expired` 标签不等于读取失败，以实际结果为准。
3. DWS 网关 pre/prod 与 Multica 环境是两个维度。真实员工线上问答优先用 prod；保存原 status，临时切换后恢复。SLS 明确 pre/prod tag，Langfuse 明确 pre/production。CID 必须由该身份/环境真实返回或对应 SLS 定位。
4. 创建仓库外权限 700 的证据目录；原始消息、prompt 和工具返回仅存其中，不进 Git。凭证只从既有本机配置读取，不回显。

## 先取实际问答，再查模型

按 `dingtalk-chat` 的读取契约操作。按日全会话读取需要原始分页信封，用以下精确入口；时间是机器本地时区，先核对为目标时区：

```bash
python3 "$DWS_ENV" as 教练 -- chat message list-all \
  --start '2026-09-08 00:00:00' --end '2026-09-09 00:00:00' \
  --limit 100 --page-all --page-limit 100 --format json > "$EVIDENCE/dws.json"
```

检查 `success`、`paging.hasMore/truncated/pages/total` 及 `result.conversationMessagesList`。不要只查员工发出的消息：它会漏掉没回的问题。定点补读用 `+chat-messages --conversation-id … --start … --end … --page-all`，按消息 ID 核验真实送达。

即使全量接口自报完整，也要把 SLS 入站 evidence_id 与消息账本做差集；9月8日实测有两条入站未出现在完整日列表，但 `+messages-mget --msg-ids …` 能读到。按ID补读后保留独立ledger，索引脚本可重复传 `--dws`；没有读取之前不能把差集直接判成漏接或无消息。

- `+search-msg` 至少要一个过滤条件，不能只传时间。默认富化可能失败；`complete=false` 不表示零消息，保留失败 ledger。需要搜索投影时可用 `--no-enrich`，但不能声称取得完整消息详情。
- 若 pre 列会话/详情报 System is error 而搜索能返回，检查网关环境；不能解释成账号无权限或没有回复。prod 读取成功只证明 prod 可读。
- 卡片搜索投影可能含摘要+正文重复。用消息 ID 与详情判断是否真的重复发送，不按 `[s]` 文本重复次数统计。
- 区分真人请求、日志/周期任务通知、员工出站和群友讨论。总消息数不能直接当请求数；消息中的员工日报统计也不是巡检事实。

## 从 SLS 一跳到 Langfuse

读 `inspect-coordinator-sls` 和 `inspect-langfuse-trace`。先按员工 ID 拉 `inbound_coordinator_decided` 与 `inbound_coordinator_llm_request`；再查异常 trace 的全部工具/finish。SLS 的 failed/deferred 也可能用 decided event，必须读 action/error。

```bash
scripts/query-coordinator-sls.sh --env prod --agent-id "$AGENT_ID" \
  --event inbound_coordinator_decided --from "$FROM_UTC" --to "$TO_UTC" \
  --size 100 --offset 0 --raw > "$EVIDENCE/sls.json"
scripts/query-langfuse.sh --env production --trace "$COORD_TRACE_ID" --raw
```

SLS stderr `possibly_truncated=true` 时保持窗口和 query，按 `next_offset` 翻页，合并原数组；取满一页不能称全量。中文关键词先按 agent/时间取数后本地匹配。环境 tag 不能省。

Langfuse 先用 `agent-UUID`、session/CID、trace ID 或 idx 事件缩小范围，不扫全项目后猜用户。脚本 `--to` 固定结束时间；需要覆盖统计用 `--stats`，分页/扫描/水合达到预算时必须披露。列表有同 ID 多版本：保留 raw_count、unique_count、duplicate_rows；以 ID 去重后的统计才是 trace 数。一个 trace 内可能有多次 Coordinator 重试，重试数再数根 observation。

**共享 trace 顶层 input/output/timestamp 可能由后续 agent_task 覆盖。** 原始问答读 `observations[name=inbound_coordinator]`，逐次尝试看根输入和 `coordinator.round.N`；沙箱读 `agent_task` 和 `llm.call.N`。模型参数、显式 reasoning 字段和工具参数才是推理证据；没有的推理不要补造。

## 判断问题的顺序

1. **资格与覆盖**：谁在问谁，哪些原文构成当前窗；静默是否合理，是否有未完成的明确请求。
2. **提示词与路由**：记下实际 policy_version/modules/hash；当前请求是否被旧事项/记忆误带走，短确认是否继承了员工自加的加工步骤，链接参数是否被误判成 CID。
3. **数据与回答**：召回是否覆盖对象和时间；重发名单是否忠于原数据；未加载/截断是否被说成空；“谁和她聊”是否回答成“她是谁”。Host 校验拒绝与供应商失败分开统计。
4. **执行与送达**：finish 是意图，任务状态是执行证据，同 CID 的 DWS 消息才是实际回话。查 outbox 或 Router 时按 `inspect-fde-llm-trace`，不以 assistant 自述证明外部已发送。
5. **场域记忆**：看 scene_key、attempt、dirty/flushed revision、输入的已提交水位、history page、commit拒绝、最后失败；再找后续同场景 SLS user_prompt 中 `current_message` 之前的 scene_memory。当前原文里的重复不能充当召回证据。

记忆工具 accepted 仅表示 draft 通过校验；根 `committed=true`、新 revision 才证明数据库提交。旧 trace 没有 committed 时读最终状态；`planned_cursor_at` 不是 `cursor_at`。旧版 oldest/newest 可能来自倒序首尾，应回看事件。`INCOMPLETE` 且 claimed evidence 不可见不能跳过水位，更不能用 reset-memory 掩盖问题。统计持续重试热点（如数百次），区分超长、超时、DWS错误、证据不可见。

## 可复用索引与报告

`scripts/build_index.py` 只读已下载 JSON，归一化实际消息和 SLS 关联键，汇总去重 Langfuse 及覆盖缺口；它不自动裁决问答正确性，不猜出站匹配。

```bash
python3 .agents/skills/inspect-daily-qa/scripts/build_index.py \
  --dws "$EVIDENCE/dws.json" --sls "$EVIDENCE/sls.json" \
  --langfuse "$EVIDENCE/langfuse.json" --self-id "$DWS_OPEN_ID" \
  --from '2026-09-08T00:00:00+08:00' --to '2026-09-09T00:00:00+08:00' \
  --output "$EVIDENCE/index.json"
```

报告包含：窗口/身份/环境和覆盖；按影响排序的问题；每项时间、消息/trace ID、实际行为、原因、历史版本和当前代码状态、已修/待办；查询耗时与缺口；变更分支/提交和部署结果（若用户要求）。有限观测不能写“全无漏回”。旧数据不会被查询/提示词修复回填。没跑 E2E 就明确未验收真实模型与投递。

若修复 Coordinator/记忆/trace，更新现行合同、policy 来源/案例及真实验证等级，运行结构检查和必要窄验证。用户只要巡检时，修复和发布作为建议；明确授权时执行，部署以目标 SHA、部署成功和健康回读为准。
