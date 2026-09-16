# 钉钉触发消息引用回复修复

## 问题

数字员工收到消息后，Coordinator虽能正常接单或回答，但托管出站回复显示为会话中的普通新消息，没有引用触发它的那条消息。现场消息 `msgWFPeGzLu+nBHLEMCYH0YxQ==` 的工作接单可复现该现象。

## 根因与修复

底层 DWS client 和托管响应 provider 已支持 `replyToOpenMsgID`，任务上下文也保留动作来源。第一个缺口在入站 `response_route`：它冻结了CID、发送人、身份与回调，却遗漏触发消息 `openMsgId`。无task的Coordinator即时回复和部分同步接单无法从后续task/Issue补回，于是退化成普通发送。

首次预发E2E进一步证明还有第二个所有权分支：当前测试订阅没有下发 `responsePolicy`，实际由 Router callback 返回 `dwsDelivery`。该对象已经精确携带 `sourceOpenMessageId`，兼容发送器却只用它解析收件人，没有设置 `ReplyToOpenMsgID`。所以正文成功送达但 `quotedMessage` 为空。Router trace `8d8af7e8357a864d3bd24e7a0648702495916e22143f22461c2c089237cbc359` 与钉钉消息 `msgT9YWsWf5gNuhJcAwoy9EQw==` 保留这次失败。

在入站事务注册托管路由时，使用现有 `dispatchOriginOpenMsgID` 冻结触发消息ID。该函数优先动作已经选择的 `WindowEvidenceID`，否则取本次事件的可见文本消息，不从昵称、正文或历史猜测。后续普通回复、接单、最终结果和错误兜底复用已有 provider 引用发送能力。Router callback兼容发送器则把可信 `sourceOpenMessageId` 同时作为目标解析来源和引用消息ID，继续使用同一CID与幂等键。缺失ID时保持原普通发送退路；旧版本已冻结路由不改写。

## 验收

- Host测试：路由保存当前触发消息ID；多消息窗口选择明确的 `WindowEvidenceID`；回调重放保持同一冻结输入。
- Provider测试：携带ID时实际构造 `dws chat +messages-reply --group <cid> --message-id <openMsgId>`。
- 预发真实E2E：在已订阅的冬翔→东翔测试号单聊发送普通对话和一条工作请求，分别回读回复；`quotedMessage.messageId`必须等于各自触发消息，正文与任务结果仍正确且不重复。
- 发布必须确认新分支源码进入本次Aone快照、预发部署和集成测试成功，健康检查正常。

## 状态

完成。响应路由保存默认触发消息和明确选择的 `WindowEvidenceID`，回调生成的托管action继续持有同一消息ID；reaction/空消息不被猜成触发消息；托管provider和Router callback兼容发送器都使用 `+messages-reply --message-id`。缺失source消息的旧回调仍走原单聊退路。Host/provider/Router恢复与后端构建通过，policy结构检查通过。

最新develop的全新本地库存在既有271/9025迁移顺序问题，本次测试在隔离worktree库中按仓库现有SQL补齐所需fork表后运行，不修改预发数据库。首次预发run3108400116部署成功，但普通对话回读没有引用，暴露并定位第二条兼容路径；该失败保留。修订源码516c06293在run3108401487重新部署，部署/集成测试及health成功。冬翔→东翔测试号真实单聊验证：普通回复、工作接单、工作最终正文的 `quotedMessage.messageId` 均精确等于各自触发消息；工作终态后二次回读没有重复正文。只有该单聊存在真实订阅，群聊由Host选择和DWS参数测试覆盖，不冒充群IM实测。详见 `docs/reports/2026-09-16-dingtalk-trigger-quoted-reply-e2e.md`。

## 群聊双 @ 回归（2026-09-16）

上面的验收只覆盖单聊，群聊路径留下一个未测事实：**引用回复自带被引用人的 @**。正式群「客户交付-数字员工小群」里 交付小助理 引用回复 笑曳 后正文出现 `@笑曳 @笑曳`。

预发实测确认平台行为（群 `cidVaO557dsSgYcgnvRNbwY4g==`）：`dws chat +messages-reply` 不接受任何 at 参数，但渲染结果总是以被引用消息发送人的 @ 开头。正文不带占位符时读回 `@东翔测试号  实验A：正文里没有任何 at 占位符`；正文再带 `<@openDingTalkId>` 时读回 `@东翔测试号  <@DIBwz...> 实验B：…`，钉钉界面把这两处都渲染成 @。群聊托管回复本来就为 `--at-open-dingtalk-ids` 准备占位符，引用回复改造后占位符原样留在正文，于是每条群引用回复都稳定多出一个 @。

修复按发送口径分三处，都只去掉“寻址前缀”，句中刻意的 @ 保留：

- `dwsclient.Send`：`ReplyToOpenMsgID` 非空时剥掉正文开头指向 `AtOpenDingTalkID` 的占位符并清空该字段（引用回复本就不传 at 列表）。托管 provider 与 Router 兼容发送器都经此收口。
- `dingtalkresponse` provider：引用回复分支不再补写占位符。
- `execenv.RewriteDWSOriginReply`：把执行器的普通群发改写成引用回复时，按新冻结的 `DingTalkMessagePolicy.ReplyToSenderOpenDingTalkID` 剥掉同一个人的前缀；一旦该发送还 @ 了别人（`--at-all` / 其它 openDingTalkId / 手机号 / userId），或被引用发送人未知，就保持普通发送，不让引用回复吞掉别人的 @。

`ReplyToSenderOpenDingTalkID` 只从已冻结事件中与该 `openMsgId` 精确匹配的消息取，取不到保持未知。旧任务冻结的策略没有该字段，退化成“带 at 的发送不改写成引用回复”，不会产生双 @。

验收：`dwsclient`、`execenv`、`agentmessagerouter`、`dingtalkresponse` 与 handler 的相关用例通过，`internal/handler` 失败集合与 `bcc139f68` 基线逐条相同（本地库既有问题）。执行器 shim 分支随沙箱镜像里的 `multica` 生效，本次服务端发布不覆盖它，本轮未实测。

CR 36159468 随 run 3108452697 部署预发成功（代码合并/构建/预发部署/预发集成测试全 SUCCESS，`/health` 正常）；发布分支 `releases/20260916101607562_r_release_342160_dt-fde-multica-code` 包含 `64e31751b`，且本次涉及文件与本地逐字节一致。预发群 `cidVaO557dsSgYcgnvRNbwY4g==` 回读四条托管出站（对话回复×2、工作接单回执、任务结果回报），`@` 均只出现一次且正文无 `<@...>` 占位符；修复前的平台行为对照实验一并保留。详见 `docs/reports/2026-09-16-dingtalk-trigger-quoted-reply-e2e.md` 的「群聊双@回归」。

## 执行器自建引用回复的漏网路径（2026-09-16 补）

上一节的三处收口都在「Host 发送」或「把执行器的普通群发改写成引用回复」上。预发容量回归（15:19:53，群 `cidVaO557dsSgYcgnvRNbwY4g==`）暴露出第四条路径：**执行器自己直接调 `dws chat +messages-reply`**。

证据：Langfuse trace `e4ceb69efa654ddabb4b7a2cc88fb2b6` 里，沙箱先 `write ./reply.txt`（首行 `@冬翔  群里文档"统一隐藏封面"我实测排查完了…`），再执行
`dws chat +messages-reply --group ... --message-id msgQ1z8qjL4/ZTqMfSZwHoUpw== --content "$(cat ./reply.txt)"`。
群里读回是 `@冬翔  @冬翔 群里文档…`：一个来自钉钉给引用回复自动加的 @，一个来自正文首行。同群其它六条（接单、等待说明、另两条结果回报）都走 Host 发送，只有一个 @。

两个原因叠加：

- `RewriteDWSOriginReply` 开头即 `if parsed.values["message-id"] != "" { return args }`，命令本来就是引用回复时直接放行，剥前缀那步不执行；
- `StripLeadingMention` 只认 `<@openDingTalkId>` 占位符，而执行器写的是显示名形式 `@冬翔`。

这条提示本身也是 Host 给的：`dingTalkOriginReplyHint` 直接把 `dws chat +messages-reply …` 交给执行器，却没说“别再 @ 一次”。

本次修复：

- `DingTalkMessagePolicy` 增加 `ReplyToSenderDisplayName`，与 `ReplyToSenderOpenDingTalkID` 在同一条已冻结消息上一起取，取不到保持未知。
- `dwsclient.StripLeadingAddressing(content, openID, displayName)` 同时剥占位符与显示名形式的开头寻址，要求名字后面是空白或标点边界（`@冬翔翔` 不会被当成 `@冬翔`），句中的 @、指向别人的 @、以及“整条只有一个 @”的情况都保留。
- `RewriteDWSOriginReply`：命令已是引用回复且 `--message-id` 正是本任务冻结的触发消息时，**只替换 `--content` 的值**，其余参数逐字保留；引用的是别的消息则完全不碰。原有“普通群发改写成引用回复”的分支改用同一个剥离函数。
- `dingTalkOriginReplyHint` 补一句 `the quote already @s the sender; do not open <text> with @them`。

发布口径要分开看：**提示词那一句随服务端发布立即生效；shim 的剥离随沙箱镜像里的 `multica` 生效**（FC 走 `multica-fc-hermes-runtime` 候选镜像，正式走 ASB 发布口径），服务端发布不覆盖它。冻结的显示名字段随服务端发布先落库，旧任务没有该字段时退化成只剥占位符，不会产生新的双 @。

验收：`execenv`、`dwsclient`、`handler` 的相关用例通过（新增显示名剥离、已是引用回复被剥、引用别的消息不动、只剩 @ 不清空等用例）；`internal/handler` 失败集合与本分支基线 `5fa1416ba` 逐条相同（104 条，本地库既有问题）。真实群聊验收要等带新 `multica` 的沙箱镜像，未跑不记为通过。
