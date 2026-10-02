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
| 纯 Direct | Langfuse 实际 system 无通用平台操作段；岗位和场域 sentinel 保留；工具面仍有场域 MCP | 新镜像真实 system 无平台段；64 个工具名完整，12 个场域 MCP 保留，真实执行通过 |
| 场域能力对齐 | 单聊/群的能力介绍给当前 scene 的有效配置链接；有效能力目录准确；执行器自管理 MCP 可用 | 单聊/群链接已送达；新镜像场域读取、测试项创建/禁用/清理均通过，H5 页面仍未验 |
| 钉钉文件交付 | 新任务产生原生文件消息，发送回执与真实消息匹配，文件内容可读回 | 身份衔接修复后原生文件已送达且内容读回匹配；多余总结仍修复中 |
| 前台 Langfuse | reply、真实 dispatch、拒绝及 journal 恢复均有准确 trace，敏感字段脱敏，无额外 LLM | 真实 reply/dispatch 均首轮完成并有 job→Task/Run/queue 关联；journal/拒绝由回归覆盖 |
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

## 第二轮实测（03:30 起）

服务器 `c476ba23fe29eeae663f7500ef6ab307d24de9de` 已经预发 `3110324009` 构建、部署、集成通过。Runtime `6edbded38990c06408c4007e7c3fef4ca411797d` 的自动 PUSH 构建 `77188406` 成功，模板 `eyq6tw0p1jcjayewwtxr` 已切换并读回；Daemon pin 不变。

- 普通能力介绍：单聊 job `b056094e-8f47-45b5-9364-07a62a3acd7f`，群聊 job `0d3f422a-ba47-4cbd-b6d1-a38594db4c92`，均首轮 `describe_capabilities` 终态、无 Task/Run。实际 IM 分别 03:30:15→03:30:20、03:30:29→03:30:39，回复精简且附各自场域链接；Langfuse 各一个真实 generation。
- 文件 queue `f9e5fb90-601d-4c48-a1cf-9aee752ca6d7`，前台 job `6e845715-3cc7-4d1e-a167-dcc0235f1647`、Run `addf6e08-fd38-45cd-9ad8-64ce1cd36553`。03:31:59 原生文件消息 `msgleiPa1N1ruPZ5MndM6FTHA==` 送达；provider 回执 SUCCESS，冬翔账号定向读取并下载 `employee-e2e.txt` 成功，26 字节内容精确等于 `EL5-DM-NATIVE-FILE-031511` 加换行，SHA256 `3c411ffa31dce9fa7ca13d28c24c32436bc5223445597e6c3360b078681c2244`。
- 新后台 trace 记录 tools=64、recorded=64、complete=true、truncated=false，包含 12 个 `config_qwen_tag_scene` 工具，无通用 `mcp_multica_`；完整 schema 仍明确为未导出。
- 新缺陷：文件用户明确要求不再发总结，Host 在 03:32:11 仍追加长摘要（消息 `msgYDdpolk4VhRn4Hh/LMNmhw==`）。原生文件运输已通过，完整交付体验仍未通过；需修 Run 终态通知策略后继续真实 IM 回归。

新纯执行模板的专项验收：task `b1cdd611-6a5a-41ea-b0f3-00bf95755ff1` 真实执行 Python 得到 `338350`，并调用场域 MCP 返回“冬翔 / dws-shortcuts”，03:35:42 Host 仅回传一行结果。task `44f0bf1d-1e3e-4f7e-b17c-ac58f69acaad` 实际完成 config_get → prompt_upsert(enabled=false) → config_get → 删除自己新建的测试提示词 → config_get；管理 API 独立比较前后 prompts、skills、connectors、mcp_config 完全一致。测试项未启用、无残留。此证据说明自我场域管理执行链已通，不只是在工具列表里出现。
