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
