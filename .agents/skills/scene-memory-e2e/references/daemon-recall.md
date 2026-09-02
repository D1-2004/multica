# 可选 E：Daemon 按场域召回

Coordinator 短循环已经精确读 Scene Text、用 `assoc_recall` 选事项。Daemon（沙箱 Run）默认 **不** 依赖 Memory 做续接。本切片可选，不挡 A–D。

## 事项召回 — 已有，要验

沙箱已有 `multica assoc recall --conversation <openConversationId> --since 48h` 和 MCP `assoc_recall`。builtin skill：`server/internal/service/builtin_skills/multica-assoc/SKILL.md`。

过线（e2e 文档 P2 / P3 对 dxxh 建出 Issue 之后）：

1. 该 Issue 的 Run 在沙箱里对 **入站 cid** 做 `assoc recall --conversation <cid>`
2. 能看到刚绑定的 issue / purpose
3. 换一个无关 cid，召回不到这份事项
4. Daemon **不得**用 Memory Text 里的只言片语当 issue_id

若 dispatch 事件没把 `openConversationId` 交给 Run，这是接线问题，先补 brief/env，不要让模型猜 cid。

## 场域记忆召回 — 未做

今天只有 Coordinator prefetch。Daemon 没有读 `scene_memory` 的 API。

若做，约束：

- 只读。MCP 建议 `scene_memory_get`，按当前 workspace + agent + cid
- 门控 `scene_memory_recall_enabled`；关则空
- 返回 Text + revision + 状态，**永不**返回 issue_id
- 不写 Text、不 MarkDirty、不 Flush
- 不接入 people-group-memory
- 落点：`server/internal/handler` Scene GET（与 D 的 API 共用）+ `builtin_skills` 里一条只读 skill（改 CLI/API 时同步 `SKILL.md` 和 `references/*-source-map.md`）

过线：P1.3 已写入「GoalMate=工具」之后，在该单聊开的 Issue Run 里问「这个会话里 GoalMate 是什么」。Daemon 应读到同一份 Text；P3 测试号↔配角 那条单聊的 Run 读不到。

## 不做

Daemon 不负责纠正合并、不负责 `/reset-memory`、不负责选 Issue。那些仍是 Coordinator + assoc。
