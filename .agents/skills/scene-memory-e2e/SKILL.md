---
name: scene-memory-e2e
description: >
  Coordinator Scene Memory 的目标、切片、预发 e2e 剧本和发布后验证。绑定号数字员工、
  东翔测试号、场域记忆、记忆召回、事项续接、/reset-memory、inbound Tab、dws-env 多身份
  编排、每次预发部署后怎么验。用户说「场域记忆 e2e」「跑剧本」「验证 scene memory」
  「A B C D」「daemon 按场域召回」或 /scene-memory-e2e 时必须用。
compatibility: Requires dws-env, dws CLI on 预发, logged-in a1 and normandy.
---

# Scene Memory：目标、切片、e2e

本文件是 **目标 + 验证标准 + 预发剧本** 的唯一操作入口。设计原文在 `docs/plans/2026-09-02-coordinator-scene-memory-takeover.md`，落地约束在 `docs/plans/2026-09-02-coordinator-scene-memory-landing.md`。剧本细节在 [references/plays.md](references/plays.md)。Daemon 可选召回在 [references/daemon-recall.md](references/daemon-recall.md)。

身份与切环境走 `dws-env`。Coordinator SLS 走 `inspect-coordinator-sls`。沙箱 trace 走 `inspect-fde-llm-trace`。发布走 `aone-deploy`。不要在那些 skill 里复制演员表或剧本。

本机 dws **保持预发**，不要 `switch prod`，不要验收完 restore。

## 目标

绑定号数字员工对每个群/单聊各有一份 exact Scene Text。纠正下一轮可用；事项续接只信 `assoc`；操作者能开开关、在现有 inbound Tab 看见记忆。

完成后必须同时成立：

1. 同 cid 的明确纠正，下一轮 Coordinator 用新口径。
2. 不同 cid 的 Text 不串（群 A / 群 B / DM）。
3. 纠正不进 Issue；能力请求仍走 `assoc` 建/续事项。
4. MemoryFlush 失败不拖慢 IM ACK。
5. `/reset-memory` 清该 cid 的 assoc 边和 Scene Text；cursor=now；保留 `bootstrapped_at`；旧 14 天历史不立刻学回来。
6. owner 能在 inbound Tab 看见 Text / Flush 状态；关 UI 开关则不展示。

不做：Task Card / Host 三态、新一级「场域」Tab、改 `assoc` schema、改 `agent_task_queue`、Web Chat / Robot / 日历 / 审批、people-group-memory。

## 切片

| 切片 | 内容 | 预发现状 | 对应剧本 |
|---|---|---|---|
| A | 表、lease 状态机、Worker 骨架、fence、Reset SQL | 已部署 `d00a47a8b`。Flusher=nil，开关默认关 | L0 合约；e2e 还证不了召回 |
| B | DWS range reader + MemoryFlush LLM（唯一 tool `memory_flush_commit`） | 未做 | P0 脏写；Coordinator 仍不读 |
| C | 仅数字员工入站 MarkDirty；prefetch Text；`/reset-memory` 清 Text | 未做 | P1–P5 核心 e2e |
| D | Scene API；Agent 详情四开关；扩现有 inbound Tab | 未做 | P5 UI + L2 |
| E（可选） | Daemon 按场域读 Memory + 用已有 `assoc_recall` 读事项 | 事项召回已有；Memory 召回未做 | 见 daemon-recall.md |

顺序：`A → B → C → D`。E 可与 D 并行，不挡记忆上线。

四开关 `scene_memory_{write,recall,ui,bootstrap}_enabled`，全部 `NOT NULL DEFAULT false`。关 write 停 MarkDirty/Claim。关 recall 立刻不读 Text。关 UI 不展示。关 bootstrap 不预热。

固定狗粮：workspace `sombrero-galaxy-zleb`，agent `e2293e9e-1e79-4926-b0e6-da4cb693add0`，Workbench `https://pre-fde-workbench.dingtalk.com/sombrero-galaxy-zleb/agents/e2293e9e-1e79-4926-b0e6-da4cb693add0`。绑定账号是 **测试号（东翔测试号）**。

## 身份

`python3 "$HOME/.agents/skills/dws-env/scripts/dws_env.py"`。每条 dws 都 `as <角色>`。

| 角色 | 干什么 |
|---|---|
| 主角（冬翔） | 跨组织给测试号发单聊、读回复 |
| 测试号（东翔测试号） | **本 Agent 绑定的数字员工**。入站目标 |
| 配角（dxxh） | 与测试号同组织：建群、拉测试号、群里说话 |
| 教练 | 不用 |

禁止 `+dm --to 东翔测试号`。主角侧 DM cid 从会话列表取标题精确「东翔测试号」且 `singleChat=true`。已有 cid：`cid+bEFv7ngm9n79Q1vL9HYJw==`。

## 发布后验证环

每次推代码后自己盯流水线，**预发部署 SUCCESS** 即可开跑（不要等人工「预发验证」门）。

1. `unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy`
2. `make deploy` 或 `a1 app pipeline reenter --pipeline-id 66`
3. 盯实例直到阶段 **预发部署 = SUCCESS**（构建失败先看 bootstrap / 代码合并 CONFLICT，按 `aone-deploy` 解）
4. `python3 …/dws_env.py status` 确认仍是 `pre`
5. 按刚合入的切片打开必要开关（D 之前可用 SQL/API；D 之后用 UI）
6. 跑 [plays.md](references/plays.md) 里该切片的剧本
7. 每步：`sendStatus=SUCCESS` → SLS `coord_trace_id` → 对照过线标准
8. Coordinator 错了：用 `inspect-coordinator-sls` 看 prompt / `assoc_recall` / finish，改提示词，再发布再跑同一句
9. 需要沙箱时用 `inspect-fde-llm-trace`。Issue 自述和 assistant 正文 ≠ 已送达
10. 把 `coord_trace_id` 和结论追加到 plays.md 的「最近一次预发记录」

SLS 查询必须带预发 tag，搜完整 event 名（`inbound_coordinator_decided`），**不得**出现 Memory Text 正文。

## 分层

| 层 | 何时 | 怎么证 |
|---|---|---|
| L0 | 每切片合入前 | `go test` / `pnpm test`。A 的 store 合约要有 Postgres |
| L1 | C 上线后 | dws-env 真发钉钉 + SLS。P1–P5 |
| L2 | D 上线后 | 浏览器点 Workbench 开关和 inbound Tab |

不要本地 mock 钉钉入站。不要拿 Router trace 当 Coordinator 上下文。

## 切片完成才算过

- **A**：L0 lease/CAS/reset 绿；预发表在；Flusher 仍可 nil
- **B**：脏行能被 Flush 成 Text；SLS 仍无 Text 泄漏；IM ACK 不慢
- **C**：P1 纠正不再进 WS-31；P1 下一轮用新口径；P2 事项仍走 assoc；P3 群隔离；P4 reset
- **D**：P5 四开关 + inbound Tab 看见 Text
- **E（可选）**：daemon-recall.md 过线
