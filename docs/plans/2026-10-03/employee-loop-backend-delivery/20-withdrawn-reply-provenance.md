# M5 遗忘后的已送达回复来源过滤

2026-10-04，接续 19 输出合同，所有权仍为 employee/codex-foreground-output。只改 Employee 历史投影与撤销证据读取，不改 Persona 已提交合同、digest、主 router 接线或 e2e harness；主代理集成与预发原场域复验。

## Why 与真实反例

证据目录 `~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-M5/`。授权 forget 后原人类证据与 memory 已不在新输入，但后续正常问答的 assistant 历史仍带旧值。`trace-a750e74f0052463b9ab1eb3007709df4.json` 保留“周二 17 点”；`trace-e8ad6c4c5f1a4b11a13a596fa7112495.json` 保留早期“白鹭厅”。这些回复有独立 job，现行原 capture job 的过滤不能覆盖它们。无关成员 forget 的拒绝被模型误表述由 19 Persona 合同负责，不扩张本任务。

## 设计、边界与来源

延续固定 GawkBot `71e82a18` 的 ordered SessionStore、bounded broker context 与本仓 Host provenance 投影（SOURCE_MAP 的 Bounded recent conversation snapshots）：不改原文，只在新快照里按可信结构化来源删去不可再用的上下文。

按同 workspace/agent/tenant/scene 读取 tombstone 记录 ID，关联 job 冻结的 memory_manifest 和已持久 memory tool 结果中的明确 record ID，再关联有实际 provider 消息 ID、同目标会话的 delivered response_action。回复的最小过滤单位为整条已送达消息：既有 job 可能看过多个材料，Host 不猜哪几个词来自哪条记录。随后沿旧快照 RecentConversation 的 action/message ID 和 transcript_refs 的 message ID 有界传播，防止后续回复借较早 assistant 文本把撤销值带回来。只读取结构化引用，不扫描 insight/text 或按同值擦除；另一成员的同值人类原话与独立来源回复保留。

RecentConversation 与群 transcript 引文共用撤销回复 ID：不能从 provider 历史的引用正文重新读回已撤销的自述。原审计记录、response_action、已有冻结快照/journal 不改；当前授权审计仍可通过独立授权路径读原件。没有结构化来源的独立复述无法被归因，不宣称全域擦除。读取超出证据上限或失败时让历史显式 unavailable，不能用部分撤销集合冒充完整结果。不新增 schema、模型调用或权限。

## 验证

先建立真实 PostgreSQL 原反例：active record→新问答 job 的冻结 manifest/实际 memory lookup 结果→已送达 assistant→授权 tombstone→新历史；必须原实现失败。加一跳真实 delivered reply 的冻结历史引用、同值另一成员来源、外场域撤销、引用读回和审计原件保留。先执行窄 Employee entry 回归，必要时 handler 新快照集成回归；scripted模型的答案不作为质量证明。预发 M5 原例由主代理裁定。

## 状态

代码与 spec 已完成。证据目录 `~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-WITHDRAWN-REPLIES/` 保存真实 DB baseline/candidate 日志与只读源码 overlay：基线 `27a6f9f05b` 在 manifest/lookup 两条原反例均 FAIL，候选均 PASS。最终反例调用实际 `employeememory.ForgetSceneTx`，由记录作者成功提交授权遗忘；此前/此后的 delivered reply、冻结历史一跳关联、群引用读回、同值另一成员来源、原件/已冻结 snapshot 保留均实际检验。没有用 scripted 模型正文作为通过条件。

独立本地 DB `multica_foreground_withdrawal_20261004`：19 项 RecentConversation/DMReset/Transcript 定向检查无 skip 通过；最终收口额外验证 Host notice 的 scene/principal 归属，并复查新过滤与证据上限（20 项独立 top-level 检查合计，重复运行不加分母）。读取的 record/job/source binding/delivered reply 集合分别最多 2000，超限返回不可用；拒绝的 memory tool 结果、正文里的 UUID、外 org tombstone 都不能制造来源关联。gofmt、受影响编译、diff check 通过。

保守单位是整个依赖 job 的已送达回复，可能连带省略同条回复的其他内容；没有结构化依赖的独立复述不能按值猜测归因。跨出本次有界投影范围的旧 job 不扫描，不宣称全局擦除。Persona、在线模板、旧快照、journal、审计原件、schema 与 marker 未改，无外部 IM、部署或线上写入。本地原子提交供主代理集成；SHA 由提交输出记录。修后预发 M5 原场域与真实模型表现尚未验收，部署/e2e_verified 保持 pending。

后续独立 review 识别到精确依赖跨窗仍会复活旧值；上述跨窗局限不能作为隐私边界免责。该项由 [21 跨窗来源闭合](21-reply-ancestor-closure.md) 补上，原 c7 反例与测试通过范围保持原记录，不冒称覆盖此新边界。
