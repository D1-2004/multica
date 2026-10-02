# EmployeeLoop 执行面与场域对齐验收

用户在 2026-10-03 明确补充：Direct 提示词保持纯执行，默认通过钉钉交付文件；场域能力必须与 Coordinator 对齐；EmployeeLoop 前台接入 Langfuse。Task 与 EmployeeLoop 验收后，再推进记忆进化、Execution Event、Cron 和 Webhook。

沿用既有 GawkBot `71e82a1809565281cbd0bf8185d3c125b715d934` 的 Loop、任务账本和工作包设计。范围管理复用本仓 `scene.Ref`、contextcap 和事件 admission，不另造场域键、授权来源或执行框架。

## 本轮边界

- Direct 模型只看到岗位、目标、材料、实际能力和必要执行约束；去掉系统自动附加的 Multica Issue/Chat/Autopilot/CLI 通用引导、平台 builtin 技能和通用 MCP。不得删除用户原始指令、组织/场域/个人约束和场域配置 MCP。
- Token、任务身份、租约、取消、结果捕获与发送回执由 Host 保持，不能为了“提示词干净”删除控制和审计。
- 文件上传与钉钉文件消息不同。shortcuts 使用真实钉钉文件发送链路，保留目标、身份、幂等键、发送状态和回执；Multica 附件链接不能冒充钉钉文件送达。
- Employee 前台补齐有效场域提示和能力目录，以及 Host 生成的配置链接；自我场域管理继续使用现有受限 MCP。配置链接不让模型猜测，bearer 内容不进入模型或普通 trace。
- 前台 Langfuse 独立命名 `employee_loop`，记录实际请求的模型、消息、工具 schema、响应、耗时与工具效果。缓存/journal 重放不伪装成新增模型调用；后台仍为 `agent_task`，用真实 job/Task/Run/queue ID 关联。
- 先修复“用 dispatch 的 ACK 回答已有信息、同时多派任务”的真实问题：提供无任务副作用的 reply 终态，混合回复/副作用批次先验证再执行，保持最多三次前台模型调用。

## 验收门槛

| 项目 | 必须取得的证据 | 当前状态 |
| --- | --- | --- |
| 只回答不派工 | 同一随机记忆问题返回正确值，job 为 reply、无新 Task/Run、没有第二条矛盾结果 | 修复已提交，待新部署 E2E |
| 纯 Direct | Langfuse 实际 system 无通用平台操作段；岗位和场域 sentinel 保留；工具面仍有场域 MCP | 开发中 |
| 场域能力对齐 | 单聊/群的能力介绍给当前 scene 的有效配置链接；有效能力目录准确；执行器自管理 MCP 可用 | 待接线 |
| 钉钉文件交付 | 新任务产生原生文件消息，发送回执与真实消息匹配，文件内容可读回 | shortcuts 开发中 |
| 前台 Langfuse | reply、真实 dispatch、拒绝及 journal 恢复均有准确 trace，敏感字段脱敏，无额外 LLM | 开发中 |
| 控制与隔离 | 后台工作期间前台可回复；不同 scene 私有资料不混用；任务/产物权限不扩大 | 已有部分真实证据，集成后复验 |

既有真实证据：单聊直接回复约 5 秒、群聊 @ 回复约 9 秒；运行中的 Python 任务期间前台约 7 秒回复；真实计算结果 333833500 已回传。文件已成功存储且授权下载内容/SHA 匹配，但这不等于钉钉原生文件交付。当前记忆查询曾多派任务，不能将整条用例标为通过。

## 交付顺序

每个独立批次：有意义的回归与独立审查 → 提交 → 子代理同步最新 `feat/tag-multitenant` → 预发或专用 Runtime PUSH 自动构建 → 真实 IM 复验。镜像不在本地构建。保留远端并行提交，不制造目标分支 merge 节点。

上述 Task/Loop 门槛通过后，再接通尚未落地的记忆人工纠正与演化，以及 Execution Event、Cron、Webhook。届时继续对照 GawkBot 的现有实现，补相应来源映射、持久状态与端到端证据；不把当前私有 inferred 结果捕获称作完整记忆进化闭环。
