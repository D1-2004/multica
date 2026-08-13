# 消息监听范围双维度改造 —— Multica / Router / Gateway 配合设计

**日期**：2026-08-11
**上游输入**：dm-bind《消息监听范围双维度改造配合方案》（v2 契约 `{schemaVersion:2, directCids, groupCids}`、版本标记共存、存量零迁移）
**状态**：待评审（Multica 侧已按 §4 落地，2026-08-11）

---

## 0. 一句话说明

dm-bind 的 LWP `bindDingTalkAccount` 实际落在 **Router**（`/r/Adaptor/AgentMessageRouter/bindDingTalkAccount`），所以"绑定接口解析 v2"的主体改造在 **Router**；Router 现有 Gateway 简化接口表达不了"单聊/群聊独立通配"，需 **Gateway 新增一个分桶订阅接口**；Multica 只动回调消费、存储双读、查询双视图。v1 全链路不动，存量零迁移。

## 1. 现状事实（代码核对结论）

- v1 契约：dm-bind 传 `subscriptionConfig: { cids: [...] }`（仅我聊=`[uid:uid]`，语义为"我聊"即自己给自己发消息；自定义=cid 列表；所有消息=`["*"]`），Router `DingTalkImSubscriptionConfig.parse` 要求单 key 对象，Gateway `subscribeByCidAndUid` 按冒号分流单/群聊并注册事件中心规则。
- Router 将 `subscriptionConfig` 原样持久化在 `agent_data_source.subscription_config`（jsonb），版本标记随记录走，天然双读，无需刷库。
- Router `SubscriptionResponse` 不回显 `subscriptionConfig`；dm-bind 回调已计划携带 `message_scope_detail`，Multica 不需要 Router 回显。
- Multica 现状不按 scope 做消息二次过滤，过滤事实在事件中心规则层完成。
- Multica MCP 直绑路径写死 `{"upstreamMode":"HTTP_CALLBACK"}`（机器人 HTTP 回调模式），不注册事件中心规则，与本次 cids 体系无关。

## 2. Router 改造（主体）

| # | 位置 | 改动 |
|---|---|---|
| 1 | `DingTalkImSubscriptionConfig` | 新增 v2 解析：先判 `schemaVersion==2` → 按 `{directCids, groupCids}` 解析；无版本标记 → 现有 v1 单 key `cids` 逻辑原样保留。注意现有 `parse` 的 `size()!=1` 前置检查要先按版本分流。 |
| 2 | 同上 | v2 校验（与 dm-bind 页面校验对齐）：两字段必传（允许空数组）、不允许同时为空、`"*"` 桶内独占（含 `"*"` 则该桶长度=1）、cid 非空且 ≤512 无空白字符；失败返回 `invalid_im_subscription`。 |
| 3 | `DingTalkImSubscriptionClient.subscribe/unsubscribe` | 按版本分流：v1/HTTP_CALLBACK 不动；v2 调 Gateway 新增分桶接口（见 §3）。**禁止**拼平后复用 `subscribeByCidAndUid`——`directCids=["*"]+groupCids=[]` 拼平为 `["*"]` 会触发 Gateway 全量模式同时建单聊+群聊两条规则，语义错误。 |
| 4 | 持久化 | 无改动：`subscription_config` jsonb 原样存 v2 对象，存量 v1 记录缺省视为 v1，不刷库；读取按记录自身版本解释。 |
| 5 | 不受影响项 | LWP wire（`SubscriptionRequest` 原样透传，`subscriptionConfig` 本就是 `Map<String,Object>`）；`check`/`unbind`/`source-identities`（account key 维度，与 config 内容无关）；解绑仍 `deleteByCidAndUid(uid)` 按两个固定 bizId 删除；surface PATCH；`SubscriptionResponse` 不加回显。 |

## 3. Gateway 改造（新增一个接口）

`MsgSubscribeServiceI`（gateway-client，Router 依赖随之升级）新增分桶简化订阅：

```text
subscribeByScopeBuckets(uid, directCids, groupCids)
```

分桶 → 规则映射（bizId 沿用 `agent_message_gateway_{single|group}_filter_{uid}`，解绑 `deleteByCidAndUid(uid)` 零改动）：

| 桶 | 取值 | 事件中心规则 |
|---|---|---|
| directCids | `["*"]` | `im_msg_23`（single_by_receiver：`convType=1, otoReceiverId=uid`） |
| directCids | 指定 `["peerUid:uid", ...]` | `im_msg_4`（`convType=1, senderId=对端uid列表, otoReceiverId=uid`） |
| directCids | `[]` | 不建；重绑时按单聊 bizId 删除存量 |
| groupCids | `["*"]` | `im_msg_38`（group_by_receiver：`convType=2, receiverId=uid`） |
| groupCids | 指定 `["群cid", ...]` | `im_msg_36`（`convType=2, cid=cid列表, receiverId=uid`） |
| groupCids | `[]` | 不建；重绑时按群聊 bizId 删除存量 |

实现要点：复用 `subscribeOrUpdate`（DB 中间态 → 事件中心 → 生效态）；空桶侧按固定 bizId 删除，保证"所有+所有"改成"仅单聊"这类重绑能收敛。退订 `deleteByCidAndUid(uid)` 删两条固定 bizId，already-absent 容忍，天然兼容 v2。

**为什么不让 Router 直接调现有通用接口 `subscribe(SubscribeRequestDTO)`**：该 DTO 只有单个 `senderUid`、无 `senderUidList`，指定多单聊 peer 表达不了；且 bizId 生命周期（固定前缀+uid）归简化接口管，通用接口需调用方自带 bizId，会打乱规则管理与解绑对称性。

## 4. Multica 改造

| # | 位置 | 改动 |
|---|---|---|
| 1 | `completion.go` / `dingtalk_account_config.go` | `MessageBindingResult` 增 `message_scope_version`、`message_scope_detail`（双维度明细快照），校验后持久化进 `DingTalkAccountConfig` 新增字段；存量记录缺省 v1，双读，不刷库。实现备注：v2 放宽“custom 必须带会话列表”校验（一维通配+另一维不接收的组合没有任何指定会话）；失败回执缺明细按 v1 落库。 |
| 2 | 绑定查询/列表接口（`PublicDingTalkAccountBinding`） | 按 dm-bind 方案 §2.4：每条记录返回 `schemaVersion` + 双视图（`subscription` 新视图 + `legacyView` 老视图）；v1 升格、v2 降级映射按 dm-bind 方案 §3 原样实现。 |
| 3 | 消息分发 | **不改动**。过滤发生在"绑定时"而不是"分发时"：绑定请求到达后，Router→Gateway 把用户选择的范围翻译成事件中心规则（如指定群A → `im_msg_36(cid=群A, receiverId=我)`），事件中心只把命中规则的消息推给 Gateway→Router→Multica——群B的消息、群A里未@我的消息根本到不了 Multica。Multica 收到的每条消息天然已符合订阅范围，无需在分发时再做桶内匹配。dm-bind 方案 §2.3 的"分发桶内匹配"工作项**取消**；若未来要防御规则漂移，再单独立项做校验。 |
| 4 | MCP 直绑路径 | 决策点：当前写死 HTTP_CALLBACK、不注册规则。本次最小方案不动；若要求直绑也支持范围订阅，再把 v2 入参加到 MCP 工具并复用 §2 的 v2 通道。 |

## 5. 确认项结论（2026-08-11 已与业务方确认）

1. **`me:me` 语义**：`["uid:uid"]` = "我聊"（自己给自己发消息），且事件中心目前**不具备**该场景的投递能力——v1 选"仅我聊"的绑定实际收不到消息。两个推论（需回传 dm-bind 修正其方案）：
   - dm-bind 方案 §1 "仅我聊正名为所有单聊、行为不变"的假设**不成立**：v2 "所有单聊"（`directCids=["*"]` → `im_msg_23`，所有发给我的单聊消息）是真正开始收消息，属于行为变化，不是改名；
   - dm-bind 方案 §3 升格映射 `["me:me"] → directCids=["*"]` **语义错误**：me:me 的真实行为≈收不到单聊消息，最接近如实的升格是 `directCids=[]`（单聊不接收）。建议按"行为等效"口径修正该映射，待 dm-bind 确认。
2. **指定群聊语义**：产品口径 = 群内**@我**的消息（非群内全部消息），§3 映射（指定群=`im_msg_36`、所有群=`im_msg_38`）无需调整。
3. **分发侧桶内匹配**：结论为不做，完整理由见 §4 第 3 项。

## 6. 上线顺序

1. Gateway 发布（新增接口，纯增量，老接口不动）；
2. Router 发布（v2 解析 + 调新接口；v1 流量完全无感）；
3. Multica 发布（回调双字段、存储双读、查询双视图）；
4. dm-bind 切换 v2 提交 + 回调双轨；
5. 渲染方按 `schemaVersion` 各自升级，无排期耦合。

存量 v1 记录随用户重新绑定自然更替为 v2，不做批量迁移。
