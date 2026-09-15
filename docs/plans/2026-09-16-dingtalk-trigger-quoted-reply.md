# 钉钉触发消息引用回复修复

## 问题

数字员工收到消息后，Coordinator虽能正常接单或回答，但托管出站回复显示为会话中的普通新消息，没有引用触发它的那条消息。现场消息 `msgWFPeGzLu+nBHLEMCYH0YxQ==` 的工作接单可复现该现象。

## 根因与修复

底层 DWS client 和托管响应 provider 已支持 `replyToOpenMsgID`，任务上下文也保留动作来源；缺口在入站 `response_route`：它冻结了CID、发送人、身份与回调，却遗漏触发消息 `openMsgId`。无task的Coordinator即时回复和部分同步接单无法从后续task/Issue补回，于是退化成普通发送。

在入站事务注册托管路由时，使用现有 `dispatchOriginOpenMsgID` 冻结触发消息ID。该函数优先动作已经选择的 `WindowEvidenceID`，否则取本次事件的可见文本消息，不从昵称、正文或历史猜测。后续普通回复、接单、最终结果和错误兜底复用已有 provider 引用发送能力。缺失ID时保持原普通发送退路；旧版本已冻结路由不改写。

## 验收

- Host测试：路由保存当前触发消息ID；多消息窗口选择明确的 `WindowEvidenceID`；回调重放保持同一冻结输入。
- Provider测试：携带ID时实际构造 `dws chat +messages-reply --group <cid> --message-id <openMsgId>`。
- 预发真实E2E：在已订阅的冬翔→东翔测试号单聊发送普通对话和一条工作请求，分别回读回复；`quotedMessage.messageId`必须等于各自触发消息，正文与任务结果仍正确且不重复。
- 发布必须确认新分支源码进入本次Aone快照、预发部署和集成测试成功，健康检查正常。

## 状态

实现及本地Host/provider测试完成：响应路由保存默认触发消息和明确选择的 `WindowEvidenceID`，回调生成的托管action继续持有同一消息ID；reaction/空消息不被猜成触发消息；provider和DWS client使用 `+messages-reply --message-id`。policy结构检查通过。最新develop的全新本地库存在既有271/9025迁移顺序问题，本次测试在隔离worktree库中按仓库现有SQL补齐所需fork表后运行，不修改预发数据库。预发部署与真实引用回读待执行。
