# DSH 标准会话服务预发验收

CR：36224658。仅预发，不构成正式发布批准。

## 固定对象与版本

- 用户已有 Agent：`ada8943e-815e-44d9-9598-c26a5d0bb52b`，Runtime：`387d4e89-a0a3-486d-a04b-fd1349b24c0f`。
- DSH `0.1.6-alpha.1`，官方 IM `4.23.0` 保持原包，酒馆 `2.3.9-multica.3`。
- Runtime 候选：`56759bb18fc4b347d4af9d99298e0ac40709942f`；模板 `da79llbqop7krnf0ekqm`；镜像摘要 `sha256:33d8761a626dac96a8f86c0a625578d3094ea42d1d9b06ca972d25c5bf6173ba`。
- Runtime CI `74234961` 成功，云上 smoke 成功、临时沙箱清理完成。
- 应用功能提交：`dc400b173`；领取请求号修复：`f1e8a3107`；旧 Host 探测保护：`ea9c0740f`；独立后台恢复：`cc79d5593`。
- 没有新增数据库迁移，没有转换 JSONL/Zstd 或搬迁历史会话文件。

## 实测发现并修复

1. 官方 Gateway receipt 增加 `managed_sessions` 后，宿主启动校验仍比较旧 receipt，导致健康进程被误判并替换。候选 `56759bb` 修复，真实 receipt 回归测试通过。当前 Host generation 12、Profile 151 已应用。
2. 插件原始 requestId 不是 UUID。接入已派生数据库 UUID，但领取阶段仍直接比较原始 ID，任务 `b79e2ce7-2a3f-4cf2-baa5-432045b8b7b2` 在领取前失败。统一派生 ID 校验，并验证原始请求号仍保留在原生事件和轨迹中。
3. 关闭网页后的后台提交两次超时，而打开入口建立连接后可提交。原恢复机制依赖串行配置队列，无法独立保证连接恢复；改为各副本独立、有并发上限的恢复线程。最终两台应用更新后，未创建浏览器入口的提交已完成；原生重启后关闭测试入口再提交也已完成。

## 已有端到端证据

| 场景 | 结果与锚点 |
| --- | --- |
| 网页标准会话入口 | `4b65e485-cb38-4b3f-a4d4-ce010cd8459b` completed；返回角色“雙葉莉緒”和 `DSH_STANDARD_WEB_385dc36d` |
| 非 UUID 宿主插件请求 | `7eafb5c6-948d-4e6b-8b87-8d4700a431ec` completed；返回角色和 `DSH_STANDARD_PLUGIN_b902e776` |
| 请求去重 | 同一个 `weixin-acceptance-881e3e46a0d54d56a606f651dcd111cd` 首次 201、重试 200；同 task/message，replayed=true |
| 原生历史 | Session `session-75dcf06f-1d7a-426c-a9ab-cdf50750b15f` 的 page 读到原始 plugin requestId、输入、回复和 completed turn/end |
| 平台轨迹 | 插件任务轨迹 GET 200，保留原始 requestId 和验收标记 |
| 发布中的运行任务 | `e5874983-6f05-4c6f-b58a-65ea1e62e203` 于 17:47:41 开始、17:50:52 completed；真实工具执行 sleep(180)，工具起止 17:47:50–17:50:50，跨过预发发布单 `161603344` 于 17:50:04 完成两台实例更新 |
| 应用发布后的后台恢复 | 不创建浏览器入口，任务 `f46cdb01-8e29-46f6-9421-6d9011263262` 于 18:01:24 completed，角色和 `DSH_BACKGROUND_c77a9e8d` 正确 |
| 原生重启及持久化 | `/dsh-market/restart` 返回 202；boot 变化，Host generation 12、原 sandbox 不变，Profile current=true/state=applied；微信 config/workspaces 文件哈希不变，账号仍为 1 个 |
| 原生重启后的后台输入 | 关闭测试入口后，任务 `1a38c3e7-63ad-4df7-9626-6c9ce01ebecf` 于 18:04:30 completed，角色和 `DSH_BACKGROUND_44543148` 正确 |
| 连续消息排队 | `df1803cf-1042-423d-beb9-87bcbee4c5bf` 执行中，第二条 `27804a82-3dda-40c9-b782-1f264d805217` receipt.queued=true；第一条 17:55:51 完成，第二条 17:55:57 开始、17:56:00 完成，标记分别正确 |

以上宿主提交走真实后台输入通道，不等同于已通过官方微信收件和回包验收。发布中任务连续性是一次预发样本，不证明所有故障窗口都零失败。

## 本机与发布检查

- Go DSH 协议、daemon、handler、service 定向测试通过；新增请求身份及旧 Host 重复探测回归通过。需要数据库的集成用例未在本机运行。
- `go vet ./internal/handler ./internal/service ./cmd/server` 通过。
- Runtime 正式 Cordis isolate、官方 Gateway/schema、标准 unary/stream 分发测试通过；不修改官方 IM 类或原型。
- 同一预发流水线中的文件页翻译类型问题阻塞过构建；最终保留 develop 的 `73981ac39` 修复，合并后 views 类型检查通过。
- 发布单 `161603344`：2/2 成功、失败 0、allEnd=true；最终独立后台恢复补丁流水线 `3109109779`、发布单 `161604695`：2026-09-20 17:58:48 完成，2/2 成功、失败 0、allEnd=true。预发集成测试阶段成功；停留在人工预发验证门禁，未提交正式发布。

## 尚未放行的门禁

- 已绑定微信账号真实入站、原生历史、平台任务、微信回包的对应证据。已请用户通过已绑定微信发送 `DSH_IM_ACCEPT_20260920`；当前无可代发微信的入口，未把宿主模拟输入记为此项通过。
- 多 Host 下相同 IM 账号的单消费者约束；当前 facade 没有实现跨 Host 的 IM 控制和交互转发。
- IM 直接使用 `ctx.agents` 的停止、插话、维护、审批路径及超过官方 replyTimeoutMs 的长排队，不能用标准 prompt 成功替代验收。
- 附件、取消、reset、既有机器人/定时入口的完整受影响回归。

当前结论仅为已列场景通过。未放行所有插件兼容，也未批准生产部署。
