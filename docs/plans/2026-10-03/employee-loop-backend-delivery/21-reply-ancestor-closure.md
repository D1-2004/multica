# 回复撤销来源的跨窗祖先闭合

2026-10-04，独立 review 指出 c7d49cc02a 的明确隐私边界缺口：窗内 B 引用了窗外 A，但 A 的 job/action 未被读取；A 曾读取被撤销 record 时，B 仍可能复活旧值。这个已识别的来源链不能以 Plan20 的局限声明代替修复。

## 设计与 Why

投影窗口限定候选，不限定候选的精确来源证明。只从 B 的冻结 assistant history action/message ID（或明确 self transcript 引用）出发，读取同 workspace/agent/tenant/scene、同目标会话、实际 delivered 的 A；由其现有 notice/callback/Run/Host 事实关联源 job，追加该 job 的冻结 manifest/tool 结果及下一层精确依赖。复用现有撤销传播，不扩大时间全扫、不按正文 value 推断，不改旧快照或审计原件。

集合仍受 2000 上限，总深度最多 64；无法找到精确 delivered 祖先、action/message 配对冲突、无法核对源 job、未知祖先快照或超限时，返回 unavailable。独立来源的同值 C 保留。来源仍为 GawkBot 固定 ordered SessionStore 与本仓 Host provenance reader，模型不参与这一步。

## 原反例与验收

真实 DB：A 的 job/实际 delivered action 都在 24 小时窗外，B 的 frozen history 精确引用 A 且 delivered 在窗内，C 同值但独立；record 经实际作者授权 ForgetSceneTx 退休。c7 基线应 FAIL、修复应隐藏 B 并保留 C；原件/旧 snapshot 不变。另验证 message-only 引用、来源缺失/跨 scope/未 delivered 不变成成功空图，深度/集合超限 fail closed。无需外部 IM、模型、schema 或 marker；主代理部署后再裁定真实 M5。

## 状态

实现已完成：原图追加精确祖先读取，未扩大日期全扫。真实 DB 的 action+message 与仅 message ID 跨窗反例均基线 FAIL、候选 PASS；原记录作者仍通过实际 ForgetSceneTx 提交授权遗忘。C 的独立同值回复和 A 审计原文保持原状。

独立本地 `multica_foreground_ancestors_20261004` 复验：旧 20 项与新跨窗/无法闭合/深度上限三组共 23 项 top-level 检查全部通过，无 skip。祖先缺失、外会话、未 delivered、未知 job、未知快照、action/message 配对冲突六种情况均 unavailable；65 层旧链触发深度上限，不能以部分图放行。gofmt、受影响编译与 diff check 通过，没有 Mac 编译或 DB 环境阻断。证据 `~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-ANCESTOR-CLOSURE/` 的 baseline.log/candidate.log 保留全部结果。没有新增模型、IM、schema、marker，旧冻结 job 与审计原件不改。提交 SHA 由输出记录，主代理独立审查/集成/发布及原 M5 真实质量仍 pending。
