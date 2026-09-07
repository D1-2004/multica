# 统一响应开启后未回钉钉：HTTP 入站修复

状态：根因已复现，修复已部署预发并恢复员工开关；等待新入站验证实际发送回执。

## 现象与证据

测试员工 `e2293e9e-1e79-4926-b0e6-da4cb693add0` 于 2026-09-07 23:55、23:56 收到两条采用统一响应策略的消息。Router 发送的原始 HTTP `requestPayload` 同时包含 `responsePolicy.mode=multica_coordinator`、revision7 和 `completionCallback.responseUrl`。

- Dispatch `90d3ac36-a9d7-4259-94b5-cafc6afa6d6f`，Trace `bdd90ff4fa5a27d928e3cb909368a690af87121536b6a9226948dd73226d3f21`。
- Dispatch `5438b50e-d601-41f5-a926-eb926557e34e`，Trace `4951a1fa0b7b469ddd422877b0ac48b4e457b94368c11d6cac3e9958911005c5`。
- Coordinator SLS 分别显示 reply 裁决；执行回调有正文，但 `shouldReply/replyReason` 没有 managed 标志，Router 没收到响应回执，两条接待记录均未请求清理。

根因是 `AgentDispatchV2Request` 漏声明 `responsePolicy`，其 `DispatchCommand()` 转换也没有传递该字段。HTTP 解码把 Router 已冻结的策略丢弃，Multica 按 legacy 处理；Router 则按 managed 停止代发，形成回复归属断层。此前测试直接构造内部 DispatchCommand，漏掉了这一真实 HTTP 边界。

## 修复与回归

修复提交 `b8fecbac07473310721264604382378e9a8c26a6` 仅增加请求字段和转换赋值，保留原协议、权限、幂等与 legacy 行为，不改 Router/Runtime 代码或数据库结构。

新增回归从真实 HTTP JSON 开始，验证解码、202 接单、Coordinator command 持久化、response_route 注册、reply outbox 与唯一 message.send 动作。旧代码可复现失败，修复后通过；错误策略返回400，缺失或显式legacy继续原链路，202不调用模型或DWS。

- 16项相关 Handler 测试及 CompletionWorker/response service 整包 race 通过。
- 与最新在役 release 合并后，15项响应测试及另一方2项合并回归通过，服务构建通过，无跳过。
- 日志：`/tmp/response-http-regression-before.log`、`/tmp/response-http-handler-race.log`、`/tmp/response-http-completion-race.log`。

## 处置与发布

1. 处理期间临时关闭该员工统一响应，Router已同步legacy；未改用户选择的 NewDSH Runtime。
2. 两条故障消息通过受认证的 Router 响应回执接口人工结束接待：state=failed、errorCode=http_response_policy_dropped，action/request前缀 `incident-20260908-policy-drop:`。这是人工故障清理，不代表调用过DWS或已送达；未补发旧正文。两条记录均确认 clear_requested=true、clear_done=true。
3. 当前预发另有渐进上下文改动的冲突。先等待其作者更新release到786231f9，再仅合入本次修复，形成1bcec3536；未覆盖对方合并或引入重复临时解决。
4. CR36002276 / Run3107156896 的快照包含b8fecbac；构建、预发部署、集成测试全部SUCCESS，部署于2026-09-08 00:47:33 +08:00完成。未发布生产服务。
5. 已恢复员工统一响应为true，员工revision10；Router同步回managed模式。实际消息送达需下一条新入站单独验证，不能用部署或执行完成替代。

临时处置记录 `/tmp/response-http-incident-state.json` 不含凭据。诊断读取的临时Aone配置文件已删除，未输出或保留Secret到本报告。
