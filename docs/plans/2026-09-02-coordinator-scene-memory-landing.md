# Coordinator 场域记忆落地说明

合同原文：`docs/plans/2026-09-02-coordinator-scene-memory-takeover.md`  
分支：`feat/agentic-memory-view`  
范围已拍板，不再做 Task Card / 关系轨 / 新一级「场域」Tab。

## 拍板

| 项 | 决定 |
| --- | --- |
| Task Card / Host 续接三态 | 不做。`issue_get` 保持现状。 |
| 观察面 | 扩现有 inbound Tab，不新增顶栏。 |
| `/reset-memory` | 清该 cid 的 assoc 边，并清该 Scene Text。cursor 推到 now，保留 `bootstrapped_at`，避免旧 14 天历史立刻把口径学回来。 |
| 开关 | **Agent 详情 UI 四开关，默认关闭。** 不是只靠 env/Diamond。 |
| 谁用 | **只有绑定号数字员工** Dispatch V2 入站。网页 Chat / Robot / 日历 / 审批不接。 |
| DWS | 后端 Identity + list 抽成独立模块，Coordinator last-10 与 MemoryFlush range reader 共用，不把凭证写入 `scene_memory`。 |

四开关（全部 `NOT NULL DEFAULT false`）：

```text
scene_memory_write_enabled
scene_memory_recall_enabled
scene_memory_ui_enabled
scene_memory_bootstrap_enabled
```

关 write：停 MarkDirty/Claim。关 recall：短循环立刻不读 Text。关 UI：inbound Tab 不展示 Memory。关 bootstrap：不预热，只靠第一条入站兜底。

## 切片

```text
A  scene_memory 表 + Store 状态机 + Worker 骨架 + fence + Reset
B  dwsclient 模块 + range reader + MemoryFlush prompt/tool
C  admission MarkDirty（仅数字员工）+ prefetch + prompt 修正 + /reset-memory
D  Scene API + inbound Tab + 四个 UI 开关（默认关）
```

A 的并发/lease/reset 测试不过，不准接 LLM。

## 预发验收

固定狗食用例：workspace `sombrero-galaxy-zleb`，agent `e2293e9e-1e79-4926-b0e6-da4cb693add0`（东翔测试号）。切 dws 预发后保持预发，按会话 ID 发单聊，不要搜通讯录名字。完整步骤见仓库 skill `verify-scene-memory-pre`。
