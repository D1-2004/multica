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
| 只回答不派工 | 同一随机记忆问题返回正确值，job 为 reply、无新 Task/Run、没有第二条矛盾结果 | 已通过 EL3-RECALL-011456：正确返回已有验收码，无新 Task/Run 和第二条矛盾回复 |
| 纯 Direct | Langfuse 实际 system 无通用平台操作段；岗位和场域 sentinel 保留；工具面仍有场域 MCP | 新镜像真实 system 无平台段；场域 MCP 执行仍待专项验证 |
| 场域能力对齐 | 单聊/群的能力介绍给当前 scene 的有效配置链接；有效能力目录准确；执行器自管理 MCP 可用 | 单聊/群链接已送达，场域只读已通过；自管理执行待验收 |
| 钉钉文件交付 | 新任务产生原生文件消息，发送回执与真实消息匹配，文件内容可读回 | 原生文件发送在身份资料读取处失败，任务已取消；修复中 |
| 前台 Langfuse | reply、真实 dispatch、拒绝及 journal 恢复均有准确 trace，敏感字段脱敏，无额外 LLM | 真实 reply trace 已验证两次 generation、模型/schema/usage/工具齐全；dispatch 关联继续核验 |
| 控制与隔离 | 后台工作期间前台可回复；不同 scene 私有资料不混用；任务/产物权限不扩大 | 已有部分真实证据，集成后复验 |

既有真实证据：单聊直接回复约 5 秒、群聊 @ 回复约 9 秒；运行中的 Python 任务期间前台约 7 秒回复；真实计算结果 333833500 已回传。文件已成功存储且授权下载内容/SHA 匹配，但这不等于钉钉原生文件交付。记忆查询曾多派任务，EL3 回归已修复并通过；这只证明已有结果的私有召回，不是完整记忆演化验收。

## 交付顺序

每个独立批次：有意义的回归与独立审查 → 提交 → 子代理同步最新 `feat/tag-multitenant` → 预发或专用 Runtime PUSH 自动构建 → 真实 IM 复验。镜像不在本地构建。保留远端并行提交，不制造目标分支 merge 节点。

上述 Task/Loop 门槛通过后，再接通尚未落地的记忆人工纠正与演化，以及 Execution Event、Cron、Webhook。届时继续对照 GawkBot 的现有实现，补相应来源映射、持久状态与端到端证据；不把当前私有 inferred 结果捕获称作完整记忆进化闭环。

## 本轮预发与真实验收（02:50–02:57）

- 目标提交 `4c9cf8b19aef6e9621f7c2ea524b4cb0e30d5dd7`，预发流水线 `3110322950` 的构建、扫描、部署与集成阶段均通过；两个在线副本均为 `employee-loop:3`。
- Runtime `b706c1f6-186d-476a-847a-8b36b4aba05c` 切至模板 `4yzq6as7ub31jm8nu1af`；不可变来源为 Runtime `d315c39b8da6268d5c7524bc1b43c4fd8610e3d3`、Daemon `1f57769da2331189f98cf769b67e0027180b144a`，CI `77180754` 成功。
- 单聊能力介绍 job `615b8387-4122-4e59-b4aa-9bbca65acd37`，群聊 job `a7301f59-ebc1-45db-bcf3-bb1fd0c1d8ce`，均有真实 IM 回复和独立场域链接，无后台 Run。对应 Langfuse `employee_loop` 均为 `scene_config_get → describe_capabilities`，两个未截断的 generation，模型 `qwen3.8-max`；usage 分别 9723、9840 tokens。
- 这两条能力介绍耗时约 21、18 秒且正文暴露内部术语，不能称为体验验收通过。下一批让普通介绍直接使用已有目录，明确查询开关时才额外读取；保持三轮硬上限。
- 文件任务 queue `73165a18-0bd1-47d1-a142-b07b54c9d3a1` 的真实 system 为 7463 字符，无通用 Multica Runtime / Issue / Chat / Autopilot / Mika 段。真实文件已生成，但原生 DWS 与 Python shortcuts 的身份资料 RPC 都返回 `success:false`，仅 corpId 无用户身份，文件尚未上传发送。02:57 发出取消并读回 queue `cancelled`，Daemon 在 02:57:13 回传 cancel-ack 200，02:57:43 sandbox idle_trimmed。候选 Runtime 随后回退至此前模板 `vdz20bmfcc37s16lbh9i`，等待修复后重新 canary。
- 当前普通 PAT 调用 H5 链接兑换返回 403（要求钉钉用户认证），未绕过该鉴权；已送达链接与配置只读不是 H5 交互验收的替代证据。文档和聊天不保存 bearer 链接或凭据。

后台工具观测发现既有 `tool_names` 静默只保存前 40 项，而该请求实际 64 项；不能据截断列表断言场域 MCP 缺失。将补齐工具名及截断状态，装配代码与真实场域工具执行另行核验。

03:09 旧模板对照任务 `25ddc355-bdf9-4162-a519-4d298113f5c2` 实际调用 `mcp_config_qwen_tag_scene_scene_config_get`，返回正确单聊 scene_id 与常开技能；场域 MCP 不是缺失。随后钉钉身份查询仍失败，且旧执行面诱导了多余的 final/reply 尝试，均被上游拒绝；最终 Host 通道成功回传结果。因此须以新纯执行模板再次核验。

身份故障根因是 Host/Runtime 版本衔接：Host 已通过 `MULTICA_DWS_AUTH_CODE` / `MULTICA_DWS_AUTH_CLIENT_ID` 提供原生数字员工凭据，当前 Runtime 脚本却忽略这两个变量并使用另一来源的 Agent Identity code。修复须显式消费 Host 的成对注入，不通过忽略 `success:false`、伪造 userId 或改用别人的账号恢复文件发送。
