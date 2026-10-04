# `/reset-memory`

这是 Dispatch V2 入站拦截，不是 Router `/reset`（ForceFresh 沙箱），也不是清所有 cid。

## 发什么

入站正文 **第一个 token** 是 `/reset-memory`（忽略大小写）。前面可以有一个 `@提及`。

单聊（冬翔 → 测试号）：

```bash
python3 "$SCRIPT" as 主角 -- chat +messages-send --as user --chat-id '<dm-cid>' --text '/reset-memory' --yes --format json
```

群（必须 @ 员工，否则不是数字员工入站）：

```bash
python3 "$SCRIPT" as 配角 -- chat +messages-send --as user --chat-id '<group-cid>' \
  --at-open-dingtalk-ids '<member-openDingTalkId>' \
  --text '<@member-openDingTalkId> /reset-memory' \
  --yes --format json
```

`/reset-memory 确认` 可以。`请 /reset-memory`、`reset-memory` 不会触发。

## 它做什么

只动 **这个 cid**：

- 清 Scene Text，`memory_revision+1`
- cursor 推到 **这条 reset 命令的时间/evidence**（不是随便 now）
- 保留 `bootstrapped_at`，避免立刻 14 天回填把旧口径学回来
- 关掉这个 cid 上未关闭的 assoc 边，Event 去 `task_id`
- **不** MarkDirty 这条 reset 本身
- 不进 Coordinator，不进沙箱。回一句已清理

reset **之后** 已经 MarkDirty 的更新消息：如果 trigger/dirty_through 仍新于 reset cutoff，dirty 保留，Flush 会收那条，不会被 reset 吞掉。

## 怎么验

本轮：`sendStatus=SUCCESS`；log `scene_memory_reset`；库里该 `scene_key` Text 空、`bootstrapped_at` 还在。本轮可以没有 LLM，不当召回证据。

下一轮（同一 cid 问句，问句里不要带旧探针）：

- Host **没有** 旧探针
- `assoc_recall` 不该再命中 reset 前的 waiting 卡

对照（必须做）：另一个没 reset 的 cid 再问一句，Host 旧探针还在。只 reset 一个群却去看单聊，或反过来，都不算对照。

reset 后立刻教新口径：等 Flush，再下一轮。Host 应有新探针、没有旧探针。这是 P9。
