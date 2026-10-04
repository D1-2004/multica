# 钉钉触发消息引用回复预发验收

## 结论

修复通过预发真实单聊验收。最终源码 `516c062937be688d0d003a52d6f1f8057789ee20`，CR `36153265`，预发 run `3108401487`。代码合并、构建、预发部署和预发集成测试均为 SUCCESS，`/health` 返回 `success`。制品扫描节点SUCCESS，但提示存在不强制阻断项且未返回详情，不写作零风险。

真实订阅只有冬翔→东翔测试号单聊。普通Coordinator回复、工作接单和执行器最终正文都引用各自的触发消息；最终回读无重复正文。群聊与合窗的精确消息选择由Host及DWS参数测试覆盖，不冒充真实群IM验收。

## 失败与定位

第一版源码 `5e22e7b9e`、run `3108400116` 已部署，但不能通过验收：

- 触发消息：`msgQ9WdxE6LgWPx2ZLktlyK6Q==`
- 回复消息：`msgT9YWsWf5gNuhJcAwoy9EQw==`
- 回复正文正常，但钉钉回读没有 `quotedMessage`。
- Coordinator trace：`8f45727d-1aed-433e-a846-474b4eee7e8a`。
- Router trace：`8d8af7e8357a864d3bd24e7a0648702495916e22143f22461c2c089237cbc359`。

Router详情证明当前订阅未下发 `responsePolicy`，实际发送由callback `dwsDelivery`兼容路径负责。它已携带正确的 `sourceOpenMessageId=msgQ9...`，旧实现只用该ID解析收件人，随后仍普通发送。第二版让该路径也用同一可信ID设置 `ReplyToOpenMsgID`，同时保留第一版的托管response route修复。

## 最终实时验证

### 普通对话

- 触发：`msgGLsu5XWy2yht3xVYFikdUQ==`，02:12:26，“第二轮引用回复测试：请只回应你看到了这句话。”
- 回复：`msgMl4OEKV7LDYlZyAS/DQGjQ==`，02:12:42，“看到了，这句话我已收到。”
- 回复的 `quotedMessage.messageId`：`msgGLsu5XWy2yht3xVYFikdUQ==`，与触发消息精确一致。
- Coordinator trace：`e8128950-7486-4346-ab00-04a4256c1157`。

### 工作接单与结果

- 触发：`msgM5ZZ3Nw0uaO48cTAwb7Y3g==`，02:14:17，请求写一句周三读书会提醒。
- 接单：`msg5WKJptmXOh1leuBSYhrBmA==`，02:14:30，“收到，我来处理。”；`quotedMessage.messageId` 精确等于触发消息。
- 最终正文：`msglyNZbnN1U49t295kiBUmfQ==`，02:15:22；`quotedMessage.messageId` 同样精确等于触发消息。
- Coordinator trace：`09c7bb5d-c3e2-4c43-b3ac-5c1356b3dbab`。
- 02:17后二次完整回读仍只有原请求、一条接单、一条最终正文，没有重复交付。

## 本地验证

- response route事务与重放、明确 `WindowEvidenceID` 选择、普通/失败/静默callback action测试通过。
- 托管provider和DWS client的 `+messages-reply --message-id` 参数测试通过。
- Router callback持久恢复、worker重启、缺失source消息旧退路、重复请求不重发测试通过。
- 集成发布分支后端构建和Coordinator policy结构检查通过。结构检查只验证注册表、来源映射和引用完整性，不代替上述真实钉钉回读。

## 群聊双@回归（2026-09-16 13:00-13:15，预发）

上面的验收只有单聊，群聊留下未测事实：引用回复自带被引用人的 @。正式群「客户交付-数字员工小群」因此出现 `@笑曳 @笑曳`。

### 平台行为对照（先于修复，证明根因）

群 `cidVaO557dsSgYcgnvRNbwY4g==`（Multica 预发群测试）。冬翔以 `+messages-reply` 引用东翔测试号的 `msgBLQ9RytCsozoueTKRvJuNA==`：

| 发送 | 消息ID | 回读正文 |
| --- | --- | --- |
| 正文不带占位符 | `msgBKPx9i06FdnyR1SoO0/6pQ==` | `@东翔测试号  实验A：正文里没有任何 at 占位符` |
| 正文带 `<@DIBwz3Bm4ugAGaaIaZvSXyAiEiE>` | `msgWB23xfuog6KmOplMk25axw==` | `@东翔测试号  <@DIBwz3Bm4ugAGaaIaZvSXyAiEiE> 实验B：正文里带了 at 占位符` |

`+messages-reply` 没有任何 at 参数，第一处 @ 完全由平台加。第二处是我们群聊出站为 `--at-open-dingtalk-ids` 准备的占位符，钉钉界面同样渲染成 @。

### 修复后回读（CR 36159468，run 3108452697 预发部署成功）

冬翔在同一群 @ 东翔测试号（预发数字员工），四条托管出站逐条回读，`@` 出现次数均为 1，正文均不含 `<@...>` 占位符：

| 时间 | 形态 | 正文开头 |
| --- | --- | --- |
| 13:04:31 | 对话回复 | `@冬翔  收到。这条回复里我只写一次对你的称呼…` |
| 13:06:00 | 对话回复 | `@冬翔  明白，多出来的那个 @ 确实让引用回复看着乱…` |
| 13:08:55 | 工作接单回执 | `@冬翔  收到，我来处理。` |
| 13:14:10 | 任务结果回报 | `@冬翔  预发工作台登录转圈的事我查了一轮…` |

四条的 `quotedMessage.sender` 均为冬翔，引用关系保持。对话、接单、结果三种托管出站形态都覆盖到了。

### purpose 只复述被请求的结果（同轮观察）

同一批消息触发的派工，purpose 停在被请求的结果上，没有自造处理流程：

- WS-271 ← 「你去查一下到底什么原因」→ `冬翔委托：排查预发工作台登录转圈超时问题，定位具体原因`
- WS-272 ← 纯抱怨「切工作区每次都要整个重新加载，慢…再不解决我天天跟你念叨」→ `冬翔委托：排查工作台切换工作区时全量重新加载导致的性能问题，定位具体原因`

WS-272 与璟琦那条同形（纯抱怨、未说方法），没有出现「整理成 VOC / 提交产品团队 / 待审批」这类被发明的步骤。这是**正样本观察，不是对照实验**：没有对同一窗口跑修复前提示词做 A/B，`f04_requested_outcome_vs_invented_method` 仍为 `not_run`。

### 未覆盖

执行器 shim 分支（`execenv.RewriteDWSOriginReply`）跑在沙箱镜像里的 `multica` 二进制上，不随本次服务端发布生效，本轮未实测。
