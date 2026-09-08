---
name: inspect-langfuse
description: >
  用 Langfuse 查预发 Coordinator / 沙箱 LLM 轨迹：inbound_coordinator、
  agent_task、generation I/O、coord_trace_id。用户说「Langfuse」「LLM 轨迹」
  「沙箱里模型看到了什么」「generation」或要补 SLS 闭环时必须用。
  凭证在 ~/.grok/langfuse.env，走 scripts/query-langfuse.sh；不要用
  langfuse-cli observations（v2，对本机 3.x 会 404）。
compatibility: Requires ~/.grok/langfuse.env (pk/sk + unify-aipilot host).
---

# 查 Langfuse（Coordinator / 沙箱闭环）

预发 Langfuse 是 **self-hosted 3.123**，`https://unify-aipilot.dingtalk.com`。
服务端 scope 名 `github.com/multica-ai/multica/server/internal/langfuse`。
Trace `name=inbound_coordinator`，metadata 里有 `loop`（`inbound_coordinator` 或 `agent_task`）、`coord_trace_id`、`issue_id`、`agent_id`。

凭证只读 `~/.grok/langfuse.env`（mode 600）。不要把 key 写进仓库、SKILL、聊天。

代理会挡内网。每条命令先：

```bash
unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy
```

## 入口

仓库根：

```bash
scripts/query-langfuse.sh --name inbound_coordinator
scripts/query-langfuse.sh --trace '<coord_trace_id>'
scripts/query-langfuse.sh --issue '<issue-uuid>' --observations
scripts/query-langfuse.sh --agent 3141dfdb-d567-46ca-93d4-a754292fc16e --message 练货
scripts/query-langfuse.sh --agent 金龙 --from 7d
scripts/query-langfuse.sh --loop agent_task --limit 20
```

`--agent` 是 UUID 时走 `tags=agent-<uuid>`（不要对全项目 recent 20 条做 metadata 过滤）。`--message` 会 hydrate 列表里 `input` 为空的 trace 再按子串匹配。带 `--agent` / `--message` 时默认窗口 7 天。更完整的 search 走 `inspect-langfuse-trace` 的 `langfuse_lookup.py search`。

`--raw` 打 JSON。`--observations` 拉第一条命中的 spans/events。

不要用 `npx langfuse-cli api observations list`：CLI 打 `/api/public/v2/observations`，本机 404。脚本走 v3 `/api/public/traces` 和 `/api/public/observations`。

通用 Langfuse 文档/其它资源：本机 `~/.agents/skills/langfuse/SKILL.md`。

## 和 SLS 怎么分工

| 要证什么 | 认谁 |
| --- | --- |
| 下一轮 Host 有没有 Scene Text / 探针 | **SLS** `inbound_coordinator_llm_request.user_prompt`（skill `inspect-coordinator-sls`） |
| Coordinator 这一轮 generation 输入输出、token、耗时 | **Langfuse** trace `inbound_coordinator` + observations |
| 沙箱 Run 里模型看见的事项简报 / 工具 | **Langfuse** `loop=agent_task`（metadata.issue_id / task_id） |
| Router 钉钉投递 | **不是** Langfuse，走 `inspect-fde-llm-trace` |

召回过线仍以 SLS 切开 `current_message:` 为准。Langfuse 用来看沙箱和 generation，补 SLS 乱码。

## 读什么

1. trace.metadata：`coord_trace_id`、`loop`、`issue_id`、`agent_name`、`conversation_id`。
2. observations：`GENERATION` 的 input/output；`TOOL` 是沙箱工具。
3. `loop=agent_task` 才是沙箱。不要把它当 Coordinator Host。

## 错法

- 把 Langfuse `agent_task` 当成 Scene Text 召回证据。
- 把 key 写进 git / SKILL / 命令回显。
- 用 Cloud v2 observations CLI 打 unify-aipilot。
- 不 unset HTTP 代理。
