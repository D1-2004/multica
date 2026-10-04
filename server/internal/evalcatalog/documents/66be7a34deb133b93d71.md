# 造 Scene

Scene 的 key 是 `openConversationId`（`cid` 前缀）。群名、花名、userId 都不是 key。每个要隔离的证明都用 **新 cid**，不要把两个探针打进同一个旧群。

`SCRIPT="$HOME/.agents/skills/dws-env/scripts/dws_env.py"`

## 单聊（冬翔 ↔ 测试号）

跨组织，通讯录搜不到。用 e2e 文档里的固定 cid，`as 主角`：

```bash
python3 "$SCRIPT" as 主角 -- chat +messages-send --as user --chat-id '<cid>' --text 'R7-ALPHA-4821 GoalMate is a tool not an employee' --yes --format json
```

禁止 `+dm --to 东翔测试号`。

## 从零建两个隔离群

群主必须是 **配角（dxxh）**，成员只拉测试号。两人都在 Think测试组织，`--type INTERNAL`。

```bash
python3 "$SCRIPT" as 配角 -- chat +chat-create --name "场域回归-R7A-<unix>" --member-query 东翔测试 --yes --format json
python3 "$SCRIPT" as 配角 -- chat +chat-create --name "场域回归-R7B-<unix>" --member-query 东翔测试 --yes --format json
```

从返回里抄每个群的 `openConversationId`。名字必须带 unix/探针，避免和旧「场域隔离A-GAMMA」撞车。

## 群里 @ 测试号（员工才能入站）

群消息不 @ 绑定号，Coordinator 不会当数字员工入站。必须同时做两件事：

1. `--at-open-dingtalk-ids` 用 **这个群** `+chat-members-list` 返回的测试号 `openDingTalkId`
2. 正文里写 `<@那个openDingTalkId>`

不要用冬翔侧的 id，不要用另一群的 id。每次建群都重新 list。

```bash
python3 "$SCRIPT" as 配角 -- chat +chat-members-list --group '<group-cid>' --format json
python3 "$SCRIPT" as 配角 -- chat +messages-send --as user --chat-id '<group-cid>' \
  --at-open-dingtalk-ids '<member-openDingTalkId>' \
  --text '<@member-openDingTalkId> R7-A-7749 in this group GoalMate is a report tool' \
  --yes --format json
```

`atOpenDingTalkId解析失败` = 用错了 id。重新 list 这个群。

## 探针

- 每条纠正一个 ASCII id：`R7-A-7749`、`R7-B-8830`。A 群的 id 不得出现在 B 的 Host。
- 不要用「报表工具」这类 CJK 当唯一过线条件。SLS 会 latin-1 乱码。
- 问句里不要带探针 id，否则 `current_message:` 段会假阳性。

## last-N 灌水

Coordinator 钉钉历史默认 last-10。要证「Host 有、history 没有」：

1. 先把口径/探针发出去并等 Flush
2. 再在 **同一 cid** 发 12+ 条不含探针的短消息（`flood-1` …）
3. 再发下一轮问句（问句正文不含探针 id）
4. 切开 `user_prompt`：探针在 Host，不在 `recent_dingtalk_history`

## 等 Flush

冷启动约 4s，增量约 30s，最长 2min。隔离剧本必须等 A 的 `memory_revision>=1` / `scene_memory_flush_commit` 后再打 B。revision=0 时打 B 不算隔离失败。
