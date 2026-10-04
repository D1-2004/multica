# Human + SSE 联合集成预检

本轮仅在独立 worktree 联合检查 A2UI 人工接续与 SSE 首轮反馈，不发布，不访问预发或 DWS。
基线为 `a2c706bac0`；SSE 完整差异为 `eb62d0f06e..46cc61872c`，不能单独摘取最后提交。

验收：人工工具和按需事项查找并存；reader21 与独立 human:1 就绪门均接线；pending questions 在新 TaskBrief 后追加；人工最终通知、file-only Goal、quiet、工具 LF 归属保持；A2UI 卡和 first feedback outbox 不混用。

验证：独立 PostgreSQL `tag-hitl-handler-pg` / 15434，限定 Human、FirstFeedback、Stream、TaskDiscovery、受影响 Notice/Participation；传输、模型路由、Loop/SDK定向回归；`cmd/server` 仅编译。真实模型、钉钉点击和跨实例生效留给统一发布窗口。

依赖顺序：f9f5bf17e7（合同）→ cde7a086ee（流式 frame）→ 74d6cdffd3（反馈 Host/outbox）→ bfcc2c38d2（事项发现）→ 46cc61872c（接线/恢复门）。实际采用完整 diff 后处理五处冲突，保留人工与 SSE 双方意图。

当前状态：本地联合预检通过，尚未发布或真实验收。

- handler 72 个顶层 / 189 个命名用例通过，无失败或跳过；独立联合入口 2 例通过。
- 传输、Loop、SDK 18 个顶层 / 53 个命名用例通过。模型路由 1 例初次缺专用 DB 变量跳过，补显式独立 DB 后通过。
- cmd/server 编译通过；Coordinator policy 为 PASS_STRUCTURAL_ONLY；Eval catalog 结构检查通过。
- 10040/10041 独立数据库迁移通过；本轮 PG 已停止，资源责任解除。
- 唯一联合修复：A2UI card 与 FirstFeedback 路由字段互斥；各专用 enqueue 清除另一类字段，冻结输入拒绝混合通知。用例覆盖两类混合输入拒绝。
- 未证明：两 live 实例 reader21 + human:1 生效、真实模型按提示产生 choices/first_feedback、原生钉钉点击及普通文字答案、Pi round-end continuation 的实际 Task/Run 和最终 IM 送达。需统一发布窗口按既定剧本取 IM/API/LF/SLS 独立证据。

原始日志与结果保存在本 worktree 的 `.precheck-evidence/`；该目录为本地证据，不属于业务提交。
