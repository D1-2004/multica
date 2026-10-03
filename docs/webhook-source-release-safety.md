# Webhook 冻结来源的发布与回滚边界

## 受理与持久隔离

来源由PG认证绑定冻结。当前binding schema是JSON数值`v:1`，不是任意非空JSON。新通用Webhook入口（Autopilot及Employee）在验签后、创建任何receipt/run前检查所有live副本`[webhook-source:1]`；缺就绪查询或混版返回503，不改成legacy路径。拒签审计仍可写rejected且不调度。

新来源使用内部`queued_frozen`，新worker和lease mutation支持queued与queued_frozen；对外Deliveries API仍显示queued，保持客户端enum和Webhook accepted/duplicate等响应契约。DB BEFORE trigger把`status=queued && source_binding @> {v:1}`转换为queued_frozen，事务内覆盖旧producer先INSERT queued再UPDATE binding的窗口；新writer同样受该约束。新worker读取frozen binding、digest与使用时权限，保留既有H1/M3规则。

旧worker领取谓词只有queued，因此隔离后的冻结来源不会被其claim。旧producer没有新marker gate，仍可能返回accepted并同步分配AutopilotRun，**不能说混版时旧入口也503**；这些记录持久等待兼容reader，旧lease状态变更碰不到queued_frozen。重试/延期保持queued_frozen；数据库拒绝降级到queued。未知/legacy JSON不冒充v1，仍按其已接受语义处理。

## 升级前提与不可证明的窗口

状态CHECK先扩展，安装guard并隔离已有v1 queued，再启动兼容reader，全部live reader支持才新入口受理。迁移前已经被旧worker领取并开始dispatch的效果无法由新trigger撤销。发布负责人必须核对旧consumer停机、旧lease/任务运行事实与来源绑定；需要draining时按现行deployment fence操作，不能强行中断其他CR的共享任务。数据库迁移保护安装后的新claim，不声称历史已claim执行已被自动隔离。

## 回滚

先停producer并检查pending frozen sources。可保留兼容reader直到排空，或暂停兼容consumer，把queued_frozen安全留在PG等前进恢复；不能将其改回queued给旧worker消费。新API将queued_frozen显示为queued；旧binary的API可能直接暴露queued_frozen，不承诺旧客户端显示兼容。旧consumer可以处理legacy queued但不能处理queued_frozen，因而回退consumer会安全暂停这些行；回滚期间保留兼容API reader，或明确旧API的未知状态边界。撤销status schema的down migration在存在queued_frozen时失败，绝不自动降级来源。已接受的新Employee/Task其他协议回退仍按各自合同，不能因Webhook安全暂停就推导整个应用可降版本。

## 验证

真实PG中模拟旧producer写v1绑定、新状态guard与旧claim SQL，证明零旧领取/零旧lease变更；新reader可领取并保持延期状态。冻结来源入库后、run admission前崩溃，绑定变化时新reader必须拒绝binding_changed，无另一个目标Task。真实live replica marker混版门、空数据库全迁移、full-stem alias和down拒绝分别验证。局部测试不等于已执行预发draining或真实部署。
