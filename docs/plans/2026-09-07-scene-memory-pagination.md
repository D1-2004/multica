# 场域记忆分页恢复与预发发布

## 目标与范围

修复 PoC/黑客松群暴露的历史续读死锁，保留已有记忆和未处理消息。完成本地验证后提交当前分支，通过 Aone 66 发布预发并检查代码、健康与分页行为。生产数据修复、正式发布和跨组织权限调整不在本次范围内。

根因与线上证据见 `docs/reports/2026-09-07-prod-scene-memory-investigation.md`。

## 实施方案

1. DWS 以待处理范围起点向新消息分页，消费返回的精确分页游标；每次读取一页（单聊 24 条、群聊 30 条）并限制 40 秒读取预算，不再把向旧消息的续读上界作为下一轮唯一输入。
2. 把已处理窗口进度与 claim 的 dirty revision 一起持久化到既有 `last_flush_meta`，同一次数据库写入同时提交记忆与游标。多实例/重启从数据库恢复；新入站使旧窗口进度失效，保守重读待处理起点以覆盖迟到消息。
3. 分批提交允许目标消息仍在后续窗口；只有完整处理到 claim 目标并校验目标证据，才完成该 claim。保留 source cursor 的单调性，窗口进度独立覆盖早于 source cursor 的迟到消息。
4. 旧的 `history_resume_before` 不再阻挡新读取，成功批次清除旧值，无需修改生产记录或新增数据库迁移。

## 验证

- 真实 DWS 只读核对 newer 方向、毫秒游标与边界语义。
- 本地回归：超过 8 页的大历史、超过 LLM batch 上限、旧续读位置、同秒消息、短页但 hasMore、非文本消息、目标暂不可见、重启、迟到消息和 claim 期间的新入站。
- 数据库验证提交原子性、lease/CAS 和新旧 dirty revision 隔离。
- 预发：部署源版本包含本次提交，构建/部署/集成测试成功，健康接口正常；使用可见场域只读历史与隔离数据验证分页进度。

## 进度

- [x] 线上根因与最新基线复现
- [x] DWS 边界验证、分页与进度实现
- [x] 本地回归和数据库验证
- [x] 提交与预发发布
- [x] 预发版本/健康验证、回填结果

## 本地结果

- 已确认原生 `newer` 返回最近的下一页，结果仍按倒序展示；`nextCursor` 必须转换为上海时间 `yyyy-MM-dd HH:mm:ss.SSS`。直接传数字字符串得到空结果，不能使用。实测精确续读的两页各 30 条，交集为 0。
- 新读取每次只提交一整页，不截断后保存页尾；超过预算的响应明确失败。旧 `history_resume_before` 被忽略，成功批次由现有 SQL 清除，无迁移。
- `go test ./internal/service/scenememory ./internal/dwsclient ./internal/service/inboundcoord -count=1` 通过；场域记忆的数据库测试在独立本地库运行，无跳过。
- 新回归覆盖 310 条/11 页、同秒跨页、进程实例重建、迟到消息、空文本页、目标缺失、过大页、原子回滚、新 lease 和并发 MarkDirty。完整 MemoryFlusher 两页测试保留已有文本，最后才变为 clean。
- 后端 `go build ./cmd/server` 通过；`go test -race ./internal/service/scenememory ./internal/dwsclient -count=1` 通过。
- 本地空库完整迁移遇到已有的 `271_task_completion_canceled_status` 排序问题（先于 9025 建表）；仅对隔离测试库补齐 9121–9127 场域记忆 schema 完成验证。未改动该历史迁移或共享数据库。
- 新旧实例滚动时共享表结构不变，旧实例忽略新增 JSON 元信息；旧实例写入会让新实例保守重读，不会凭旧进度跳过待处理消息。完成部署后所有新实例使用持久化正向游标。
- claim 期间出现新 dirty revision 时，后续从 pending 起点保守重读；这是对迟到消息完整性的选择，极繁忙场域可能额外重读，不能将 source cursor 单独视为当前窗口完成证明。

## 预发发布记录（已完成）

- 修复提交：`8c1bae600ab6873c65dd3b2001488581f4317212`。
- CR：`35998752`，运行：`3107046157`，mix flow：`240636806`。
- 发布分支：`releases/20260907122250542_r_release_342160_dt-fde-multica-code`。
- 合并提交：`b878e20ac00547fbb47fe4c85bf4608852cdb4e9`；已验证包含修复提交，场域记忆与 DWS 文件与修复提交一致。
- 预发默认 workspace 的可管理记忆 Agent 当前有 0 条记忆，无法直接观察已有卡住记录恢复；预发数据库从本机连接超时。预发验收据此调整为版本、流水线部署与 HTTP 健康核对；分页业务证据使用真实 DWS 两页只读核对及本地数据库/完整 Flusher 回归。未向钉钉发送测试消息、未修改生产记录。

- 15:03:45（UTC+8）：代码合并、构建、制品扫描、预发部署和预发集成测试均 SUCCESS。制品扫描提示存在非阻断检查项；本次没有执行 skip 或豁免。
- 流水线停在正常的人工「预发验证」节点（task `3444771731`），未推进正式。
- release get 确认 CR `35998752` 的 `revision` 和 `snapshotRevision` 均为 `8c1bae600ab6873c65dd3b2001488581f4317212`。
- 15:04:53（UTC+8）发布后回读：`/health` → HTTP 200 / `success`；`/api/config` → HTTP 200 / 有效 JSON；记忆列表 API → HTTP 200 / 0 条（与发布前一致）。
- [发布记录](https://cd.aone.alibaba-inc.com/unite/micro/publish/app/342160/flow/1005452/snapshots/240636806)
- [变更单](https://cd.aone.alibaba-inc.com/unite/micro/cr/app/342160/35998752)

## 遗留与边界

- 已完成分页修复和预发部署；生产两个卡住场域仍需后续正式发布才能由新 Worker 恢复。
- 预发没有可复用的卡住场域，本次未宣称完成真实预发场域恢复验收；已有真实 DWS 游标核对、本地完整 Flusher 和数据库回归作为业务验证证据。
- 跨组织权限错误独立于本次修复，未调整授权。
