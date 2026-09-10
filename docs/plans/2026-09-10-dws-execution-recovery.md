# DWS单命令任务执行恢复

## 目标与来源

用户要求根据已定位根因修复预发“继续重新打印 dws auth status”长时间等待。原trace b28d4b671b4d4ca5b236ad1df34fdc6a：Coordinator约7秒，工作因容量及同Issue busy共park118次、等待约10分24秒；旧DSH任务shell无法满足Multica CLI的mat_任务级凭证门槛，进而花数分钟自修配置；命令成功后仍有大量身份/发送/收尾操作。

基于最新共享预发f6eb46d8b隔离分支codex/dws-auth-execution-recovery，不覆盖并行开发或已有未提交资料。

## 工作范围

1. 定位DSH shell凭证/proxy处理与Multica CLI前置校验的具体衔接缺陷，保留任务级认证与租户/任务隔离；选择最小兼容修复，不引入管理员凭证、关闭校验或信任任意loopback。
2. 忙工作等待要提供真实且去重的状态反馈，区别已接收等待、任务已入队、正在执行和完成；不提高并发或重复执行原工作。
3. 去掉执行描述对所有来源强加钉钉身份/外发的错误要求，按真实入口与已声明交付处理；对明确单一工作不扩成无关环境维修。不新增复杂通用执行框架，不靠命令字符串词表绕过权限。

## 验证与交付

- 源码级定位、适配/边界测试，覆盖无凭证、跨任务、普通语气/显式交付与重复等待。
- 受影响Go/UI检查，Coordinator改动同步来源/案例/合同；结构通过不等于模型/投递通过。
- 如修改Runtime镜像，按候选分支、不可变Multica+Runtime commit和Template构建，真实冷/热任务验证。共享Daemon代码改变时补对应本地/滚动兼容验证，权限不足不伪造通过。
- Server改动只部署预发，保留现有CR；候选Runtime与原测试对象隔离，不改生产/stable通道。
- 用原只读命令或无敏感输出的等价探针验证首次执行与续接；记录排队说明、实际命令和送达，不只看模型承诺。

## 进展

- [x] 冻结原始日志与定位直接错误
- [x] 确认Runtime根因与最小实现
- [x] 等待反馈与来源正确的执行描述
- [ ] 验证、提交、预发交付与报告

## Host合同登记与验证边界

本次执行交接/等待通知不改变Coordinator模型模块、工具或装配，保留policy2026-09-10.2/assembly14及所有模块正文/hash/预算；并行finish-hint实现不由本登记覆盖。新实现版本以代码SHA与本Plan识别。103条来源原文/hash保留，只补实现映射。

- IssueDescription按可信Decision.Source区分Web/钉钉/未知，保留显式外发与原始引用/历史，只精简平台重复目标；单项工作不擅自扩为环境维修。
- persistCoordinatorWait只为已保存且有未提交Items的合法lease停放job记一次真实等待；主动会话/task_finished跳过。标记、本地message及适用managed outbox原子提交，不能关闭原callback或假称执行开始。
- 发送前Coordinator job已终态/全项提交的未发notice取消；通知失败回滚且不阻断park。普通调用者不能伪造Host进度路径，反馈不放宽并发、授权或去重。

新增4组有限对照，测试引用在policy/cases.json；Host本地测试已通过，预发投递暂记not_run。结构checker只证明来源/预算/引用完整，不证明业务执行或真实送达。

## 等待反馈实现与验收（2026-09-10）

- 已保存且仍有未提交 work item 的普通 Coordinator job，在容量满/同 Issue 忙的 park 点追加一次等待说明；只说明请求已保存、尚未开始及等待原因，部分成功窗口只描述剩余部分。未裁决输入、已全部提交、proactive 和 task_finished 不走此通知。
- 复用 job.command 的 `_coordinator_wait`、普通 assistant 消息和既有 `response_action`：同一个数据库事务提交；稳定 job ID 去重，后续 5 秒 park 不重复通知，不覆盖原计划、不产生 task、不提前发送原接单或终结回调。普通消息类型不会占用终结 Coordinator 消息的位置。
- IM 仅在原冻结 response policy 为 managed 且 route 的 workspace/agent/CID/Router target 与当前 Host 一致、DWS 身份及目标完整时发送；缺目标或非 managed/Web 仅保留本地等待说明。等待说明入库失败仅记录告警，原 park/后续执行照常继续。
- 新增内部 `EnqueueCoordinatorWait` 入口，普通 Enqueue/Submit 拒绝 caller-supplied progress 字段。等待 outbox 清空执行 task/issue 与终结 callback，发送仍走既有身份/权限、供应商幂等及查询路径；新 worker 不发终结 receipt。发送前核该 action 确属此 job 的等待元数据且还有未提交项，已结束/已提交的未发送通知取消。
- 旧副本兼容证据：Go overlay 使用原 f6eb46d8b 的 service.go/worker.go 读取新等待记录；provider.Send 确实会发送一次通知，空 callback 仅阻止 terminal receipt 的 HTTP 提交。连续 3 次 receipt 重试仍仅 1 次 send、0 次可提交的终结 callback，测试 PASS。不能把该结果描述为旧 worker 阻止了所有 HTTP。
- 本地验证：dingtalkresponse 完整包 PASS；5 个 handler 顶层测试/6 个场景 PASS，覆盖跨重试去重、部分完成、无授权目标、发送开关、事务故障不阻塞及后续终结消息。Handler 在独立临时 schema 应用仓库已有 9144–9148 迁移，未修改 public 或复制用户数据。日志 `/tmp/dws-coordinator-wait-handler-tests.log`、`/tmp/dws-coordinator-wait-response-tests.log`；旧副本证据 `/tmp/dws-wait-legacy-check/result.log`。
- 本节只证明 Host 持久化/供应商协议与本地故障边界；真实预发等待通知送达仍需部署后验证，尚不记为 E2E 通过。无新表或迁移、无并发上限变化。
