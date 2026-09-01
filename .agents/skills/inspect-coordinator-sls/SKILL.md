---
name: inspect-coordinator-sls
description: >
  查 Coordinator 快循环在预发/正式 SLS 上的推理：user prompt、assoc_recall 参数和返回、finish 裁决。
  用户说「Coordinator 怎么决策」「SLS 推理」「inbound_coordinator」「为什么接到旧事项」、给了会话名/cid/coord_trace_id 时必须用。
  走 Normandy `log list --source sls`（或 scripts/query-coordinator-sls.sh），不要用 Router LLM trace 顶 Coordinator 上下文。
compatibility: Requires logged-in normandy (AIT). Unset HTTP proxy. Project dt-fde-multica-sls / logstore application-log.
metadata:
  version: "1.0.0"
  audience: "coding-agent"
---

# 查 Coordinator 推理（SLS）

Coordinator 日志在 **SLS `dt-fde-multica-sls` / `application-log`**，不是 Router observability。slog 是 **文本**，整行在 `content` 里。SLS 按整词切 token：`inbound_coordinator` **匹配不到** `inbound_coordinator_decided`，必须搜完整 event 名。不要写 `event: xxx` 这种 JSON 字段语法。

代理会挡住内网。每条命令先：

```bash
unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy
```

## 入口（优先脚本）

仓库根：

```bash
scripts/query-coordinator-sls.sh --env pre --name 冬翔
scripts/query-coordinator-sls.sh --env pre --cid 'cid+bEFv7ngm9n79Q1vL9HYJw=='
scripts/query-coordinator-sls.sh --env pre --trace '<coord_trace_id>'
scripts/query-coordinator-sls.sh --env pre --event inbound_coordinator_llm_request --message 上海
```

脚本自己调 `normandy log list`。加 `--raw` 看原始 JSON。

## 直接 Normandy

预发主机 tag 是 `acni_ag_dt-fde-multica_default_prehost`：

```bash
normandy log list --source sls \
  --project dt-fde-multica-sls --logstore application-log \
  --query '__tag__:__user_defined_id__: acni_ag_dt-fde-multica_default_prehost and inbound_coordinator_llm_request and conversation_name=冬翔' \
  --from 2026-09-01T12:00:00Z --size 20 --reverse -o json
```

正式把 tag 换成 `acni_ag_dt-fde-multica_default_host`。场景文件：`scripts/normandy-coordinator-sls.yaml`（`--query` 会覆盖场景默认 query，自己把 tag 写进 query）。

不要用 `aliyun sls` 除非 Normandy 不可用；Normandy 会自己拿 STS。

## 索引字段（content 里的 slog key=value）

| 字段 | 用途 |
|---|---|
| `coord_trace_id` | 一次 `Decide()` 的全部 request/tool/finish |
| `conversation_name` | 群名；单聊空标题时用发送人花名 |
| `conversation_id` | `openConversationId` |
| `sender_name` / `person_id` | 本轮说话人 |
| `agent_name` / `agent_id` | 智能体 |
| `current_message` | 入站正文 clip |
| `event` | `inbound_coordinator_llm_request` / `_llm` / `_llm_finish` / `_decided` |

先按 `conversation_name` 或 cid 找 `inbound_coordinator_llm_request`，抄 `coord_trace_id`，再按 trace 拉齐 tool 和 finish。

## 读什么

1. `inbound_coordinator_llm_request` 的 `user_prompt`：人设、cid、钉钉历史、current_message。
2. `inbound_coordinator_llm` 的 `arguments` / `result`：`assoc_recall` 当时看见的图。
3. `inbound_coordinator_llm_finish` 或 `issue_comment_add` terminal：`action` / `issue_id` / `text`。
4. `inbound_coordinator_decided`：最终裁决。

没有 `inbound_coordinator_llm_request` 的旧日志只有 `inbound_coordinator_decided`，看不出 prompt。

## 错法

- 用 Router `get_observability_llm_trace` 当 Coordinator 上下文。那是沙箱模型。
- 查询 `inbound_coordinator` 或 `event: inbound_coordinator*`。前者匹配不到带后缀的 event 名；后者当 JSON 字段，这一路不是 JSON。
- 不带预发 tag，把正式和预发混在一起。
- 不 unset HTTP 代理。
