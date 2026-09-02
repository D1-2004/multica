---
name: verify-scene-memory-pre
description: >
  预发验收绑定号数字员工的 Coordinator / Scene Memory：切 dws 预发并保持、给东翔测试号发单聊、查 sendStatus、
  再拉 Coordinator SLS 与 Router 观测。用户说「预发验证场域记忆」「给东翔测试号发」「触发数字员工干活」
  「scene memory 预发」时必须用。
compatibility: Requires dws CLI, write access to ~/.dws, logged-in normandy, logged-in a1.
metadata:
  version: "1.0.0"
  audience: "coding-agent"
---

# 预发验证 Scene Memory / Coordinator

预发发布之后，用绑定号数字员工真实入站验收。不要在本地假装发钉钉。

固定对象：

| 项 | 值 |
| --- | --- |
| Workbench | https://pre-fde-workbench.dingtalk.com/sombrero-galaxy-zleb/agents/e2293e9e-1e79-4926-b0e6-da4cb693add0 |
| workspace slug | `sombrero-galaxy-zleb` |
| agent-id | `e2293e9e-1e79-4926-b0e6-da4cb693add0` |
| 数字员工 | 东翔测试号（预发组织；通讯录搜不到） |

开关默认关。验收前先在该 Agent 详情打开 write/recall（需要 UI 的话再开 ui）。只对数字员工入站生效。

## 1. 切 dws 预发并保持

环境切换的机制、脚本、URL 以 `dws-env` skill 为准。本 skill 只规定验收时的策略：**切到预发后保持预发，不要自动切回线上。**

```bash
SCRIPT="$HOME/.agents/skills/dws-env/scripts/dws_env.py"
python3 "$SCRIPT" status
python3 "$SCRIPT" switch pre
```

确认 `environment=pre`，MCP 为 `https://pre-mcp.dingtalk.com`。

## 2. 发一条单聊，触发数字员工

企业通讯录搜不到「东翔测试号」时先：

```bash
dws chat data-auth cross-org --all --yes --format json
```

**不要** `dws chat +dm --to 东翔测试号`（预发通讯录没有这个名字，会 `resolution_not_found`）。也不要改发给冬翔/夏东翔。

从会话列表找标题精确为 `东翔测试号` 且 `singleChat=true` 的那条，取出 `openConversationId`：

```bash
dws chat +conversation-list --page-all --format json
dws chat +messages-send --as user --chat-id <openConversationId> --text "你好，请开始干活。" --yes --format json --timeout 60
```

用返回的 `openTaskId` 查 `dws chat +messages-query-send-status`。`sendStatus=SUCCESS` 才算出站成功。这条预发单聊会触发数字员工干活。

群场域：用同一个数字员工拉/建群，对群 `openConversationId` 发一条带 @ 的消息。群 A / 群 B 隔离验收必须用两个不同 cid。

## 3. 证明 Coordinator 跑了

SLS 查法以 `inspect-coordinator-sls` 为准。预发 tag：`acni_ag_dt-fde-multica_default_prehost`。先 unset HTTP 代理。

```bash
scripts/query-coordinator-sls.sh --env pre --name 东翔测试号
```

看：

1. `inbound_coordinator_llm_request` 是否出现；recall 打开后是否带 `scene_memory_revision` / 是否非空，**正文不得出现 Memory Text**。
2. `assoc_recall` 的 `conversation_id` 必须是刚才的 cid，issue_id 不得来自 Memory。
3. `inbound_coordinator_decided` 的 action。

## 4. 证明沙箱执行（需要时）

Router 观测以 `inspect-fde-llm-trace` 为准。预发 `environment=staging`。agentId 用上面的 `e2293e9e-1e79-4926-b0e6-da4cb693add0`。issue 自述和 assistant 正文不能当已送达。

## 5. Scene Memory 门槛（预发）

同一 Agent、两个 cid：

1. 群/单聊首次说话：本轮按旧短循环走；随后 `scene_memory` 行 `bootstrapped_at` 非空。
2. 纠正一句口径：本轮正常回复；Flush 后 Text 含纠正。
3. 再提同一术语：Coordinator 已用新口径；续 Issue 仍只信 `assoc_recall`。
4. 另一个 cid 看不到这份 Text。
5. `/reset-memory`：该 cid Text 空，下一轮不带回口径，也不从 reset 前历史重建。
6. 关 recall：立刻不读 Text。Flush Worker 挂掉时 IM 仍 ACK。

## 6. 已跑过的基线（2026-09-02 预发，scene memory 尚未上）

固定 cid：`cid+bEFv7ngm9n79Q1vL9HYJw==`（会话名「东翔测试号」，SLS `conversation_kind=p2p`）。Agent 显示名「预发测试智能体」。dws 保持预发。

| Case | 入站 | coord_trace_id | 裁决 | 结论 |
| --- | --- | --- | --- | --- |
| 问候 vs 活跃卡 | 「你好」 | `7bd5ee60-4e74-43de-a889-d2137a9f9e96` | `reply`，未带 issue | 通过。`assoc_recall` 有 waiting 卡仍只问候。 |
| 术语纠正 | 「GoalMate 是工具，不是数字员工。下次别搞错。」`sendStatus=SUCCESS` | `b4db44f2-9db0-4bc0-801c-db112dff3ec2` | `issue_comment_add` 到 `a2f6f860` / WS-31（须莫明早会议） | **失败。** 纠正被续接到无关 Issue。这就是 Scene Memory 要接的缺口：纠正不该进 assoc。 |
| 能力请求必须进 Issue | 「帮我约冬翔明天下午开半小时会对一下上海行程」`sendStatus=SUCCESS` | `49133eb5-7aaf-41f1-8e09-d4fb6eb2aaa3` | `action=issue` 无 issue_id；`text`「我去约冬翔明天下午半小时」 | 通过。未续接被污染的 WS-31。prompt 里 `busy: true` 是上一条误续接留下的。 |
| 同人不同交付物 | 须莫侧「开个新issue…问须莫v6明早有没有会议」 | `7f5655d7-a806-4dd6-8589-7e0e60b1a139` | `assoc_bind` 新 purpose + `action=issue` | 通过。未并进「今晚会议」旧卡。 |

Scene Memory 上线后再跑同一 cid：GoalMate 纠正应进该 Scene Text、不得 `issue_comment_add` 到 WS-31；下一轮同 cid 提到 GoalMate 应直接用「工具」口径。
