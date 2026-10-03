# EmployeeLoop 前台与后台的边界：自管理、派发与工具 MCP 化

状态：紧急修复 `a9dae78977` 已上预发并通过真实 IM 验收（第 6 节）。当前合同写在 `docs/employee-loop.md`「前台与后台的边界」一段；本文记录现象、根因、GawkBot 的对照和后续方向。

## 1. 现象（2026-10-03，冬翔 ↔ Qwen-DWS 单聊，预发 Tag·钉钉）

| 时间 | 用户 | 前台回复（摘要） |
| --- | --- | --- |
| 16:11 | 你走沙箱 dws 查询一下看看 / 你去运行下 dws 看看通讯录 | 「我自己不查通讯录……这类信息我这边确实拿不到」 |
| 16:12 | 开一个后台任务，使用 dws 去查询下你的主管是谁 | 派发成功，后台真跑 `dws contact user get` 查到结果 |
| 16:17–16:20 | 你每隔15分钟给我讲个笑话 / 你开个后台任务…… / 后台任务里面有能力去创建定时任务的 / 不要拒绝我 直接后台去干 / 创建例行任务 | 连续 6 次拒绝：「后台任务里没有创建定时任务的工具」「配置那边本身是只读的」「得在会话/平台的定时设置里配（平台或管理员那边设）」 |

同一天的预发日志显示，这个单聊的每个后台 Direct 任务都挂着 `config-qwen-tag-scene` MCP，并且实际调用过它（13:26、15:14、15:51、16:12 四次 `scene config MCP: tool call tool=scene_config_get`，scene `f6daee25…`）。后台有 `scene_routine_create` 等全部写工具，是前台不知道、也不敢派。旧 Coordinator 没有这个问题：它把实际工作一律 `start_work` 交执行器，并明确「Coordinator 技能目录不是执行器工具集」。

## 2. 根因

1. **能力目录里的禁令。** `c476ba23fe`（10-03 03:18）在前台能力目录写入：`Existing Cron/Webhook settings are configuration only: Employee-triggered execution is unverified; do not advertise or promise it.` 原意是「Cron 唤醒 EmployeeLoop 前台模型尚未接通」（见 `employee-cron-task-service.md`），但模型读成「例行任务这件事不能做」。
2. **`read_only: true`。** 前台 `scene_config_get` 的结果被强制写上 `read_only: true`、`runtime_availability: unverified`。在执行器里 `read_only` 表示「这是一次例行任务运行，不准改配置」；到了前台，模型把它读成「这个场域的配置不可修改」，于是回答「配置那边本身是只读的」。
3. **没有「默认派发」的边界合同。** 目录只说技能/DWS「在后台运行，不能声称前台可调用」，没说「前台没有工具时应派发」。模型把「前台没有」推成「我做不到」，DWS 通讯录查询也因此被拒。

## 3. GawkBot 怎么划这条线（固定提交 `71e82a18`）

GawkBot 没有「快前台 + 不同工具集的执行器」这种拆分。说话的 bot 就是干活的 bot：每一轮都是一次完整的 headless 运行（claude / codex / openai-compat），都挂同一个按角色过滤的 MCP server `wuphf mcp-team`。

- **能力从工具本身来，不靠手写目录。** 每轮 Claude 用 `--mcp-config <per-bot json> --strict-mcp-config` 启动（`internal/team/headless_claude.go:42-52`），Codex 用 `-c mcp_servers.wuphf-office…`（`headless_codex_runner.go:655-687`）。bot 知道自己能做什么，靠的是 MCP 工具列表和工具描述、提示词里按角色列的工具段（`prompt_builder.go:216-231`、`:348-362`），以及 `team_runtime_state` 返回的 CapabilityRegistry（`capability_registry.go:127-144`）。
- **一个实现，多种传输。** 进程内的 openai-compat 循环不另注册原生工具，而是把 `wuphf mcp-team` 当子进程启动，列出它的工具，再把每个包装成 `bot.BotTool`，执行时走 `session.CallTool`（`headless_openai_compat_mcp.go:31-69`、`:88-118`）。
- **例行任务是每个 bot 都有的工具。** `team_routine`（`internal/teammcp/routine_tools.go:49-54`）在 1:1、DM、App Builder、office 四种模式下都注册（`server.go:171/230/277/391`），不限 lead。同目的、同 schedule 重复注册时更新原任务，并且「does NOT flip Enabled」（`broker_scheduler_routines.go:214-216`）。暂停、编辑、立即运行只在人用的 UI 路由里；工具结果会告诉 bot「the human can pause, edit, or re-cadence it there」。
- **人的请求就是授权。** `prompt_builder.go:677`：「THE HUMAN ASKED YOU, here, for this. Then file it yourself … No permission round trip: their ask IS the authorization」。例行任务没有审批步骤。
- **高风险确认放在工具和 broker 里，不交给模型。** 外部动作的写操作由 `requireTeamActionApproval` 弹阻塞审批卡（`actions.go:83-163`）；缺连接时 `/integrations/resolve` 弹 Connect 卡，并告诉 bot「Do NOT retry until connected」（`action_resolve_gate.go:110-120`）。bot 没有 connect、disconnect、grant 工具，连接器授权只在人的 UI 完成。
- **派发判据。** `prompt_builder.go:655`：「Work the human asks for gets an Issue. A question the human asks does not.」`:919` 的检验法：「does this require any tool call beyond team_broadcast/human_message? If yes, it needs an Issue.」另有 `:304`：没做对应调用之前，不许声称状态已改变。

## 4. 本仓的边界合同（本次修复）

我们保留「快前台 + 沙箱执行器」的拆分（三轮预算、首轮直答是 EmployeeLoop 的核心价值），所以按报告的第二条路走：**给前台一份真实的后台能力边界，外加一条「默认派发、不拒绝」的规则**。

- **前台只做三件事**：回复；读当前窗口、记忆、任务和本场域配置（`scene_config_get`）；给配置链接（`describe_capabilities`）。
- **其余一律 `dispatch_task`**：DWS 查询（通讯录、主管、组织、日程、文档、窗口外消息）、技能、连接器、MCP、脚本、文件，以及场域自管理。只有任务结果能证明做不到；前台不回答「做不到 / 没有记录 / 没有工具」，也不把人支到管理员或平台设置。
- **场域自管理清单**（后台 `config-qwen-tag-scene` 实际挂载的工具）：例行任务 / 定时任务的新建、修改、暂停、恢复、删除、立即运行（cron 最短 15 分钟，默认 Asia/Shanghai，或 webhook）；场域提示词；开关已公开的技能和连接器；增删远程 MCP。账号连接和连接器授权在配置页完成，前台给链接（对应 GawkBot「连接只在人的 UI 里」）。
- **确认**：对齐 GawkBot「请求即授权」。后台技能把请求人自己明确、完整的请求当作确认，直接执行。缺要素、多项无关变更、新增或改址远程 MCP 时，才先复述并等确认（远程 MCP 每次运行都会被调用，属于高风险变更）。Direct 是一次性运行，没法等回复；如果仍要求每次都等确认，前台会把「确认」当新任务再派一次，沙箱再问一次，形成循环。
- **何时追问**：只有缺「做什么 / 什么时候」时，前台才在派发前简短追问。

## 5. 往后：EmployeeLoop 外围工具 MCP 化

冬翔的要求：EmployeeLoop 将来可能不在了，但它外围的工具和逻辑都应该能变成 MCP，放进沙箱里用。GawkBot 的 `headless_openai_compat_mcp.go` 已经给出了做法：**同一份工具实现，进程内循环作为 MCP 客户端调用，沙箱 agent 通过 `--mcp-config` 调用**。今天这次事故的本质也是手写的前台目录和执行器真实工具清单发生了漂移。工具只有一份定义时，这种漂移不会出现。

| 前台工具 | 现状 | MCP 化方向 |
| --- | --- | --- |
| `scene_config_get` | 前台原生，内部调用与执行器同一个 `sceneConfigGet` | 直接作为 `config-qwen-tag-scene` 的只读子集，前台用一个只读的 scene token 走同一个 MCP handler |
| 例行任务、提示词、开关、远程 MCP 的写操作 | 只在沙箱 MCP 里（`scene_routine_create` 等） | 已是 MCP；前台的「后台能力目录」改由 `sceneConfigToolDefinitions` 生成，不再手写 |
| `dispatch_task` / `read_task` / `read_task_history` / `continue_task` / `steer_task` / `stop_task` | 前台原生，底层是 EmployeeTask Service（已有 `POST /api/employee-tasks/{id}/steer`） | 抽成 `employee-task` MCP：调用者身份取自 task token / scene token，requester 取自任务的 principal，`source_ref` 换成可信的来源引用，不允许模型填身份 |
| `memory_lookup` / `memory_capture` / `memory_forget` | 前台原生 | 抽成按 scene 和 principal 隔离的 `employee-memory` MCP |
| `describe_capabilities`（附链接）、`reply`、`stay_quiet` | 前台循环的终态语义 | 不 MCP 化。沙箱里的对应物是最终回复文本和 `scene_connect_link` / `chat_send_message` |

落地顺序建议：

1. 在 handler 里抽出 `sceneToolset`：一份 `{name, schema, readOnly, call(ctx, target, args)}`，同时注册到 MCP JSON-RPC 和 `employeeloop.Tool` 适配器。
2. 前台能力目录从实际会挂载的 toolset 生成。
3. 再迁 EmployeeTask 和 memory。

身份、租户栅栏和收据（journal / receipt）始终留在 Host，传输层只负责搬运。

## 6. 预发验收（2026-10-03，提交 `a9dae78977`，pipeline 66 run 3110344239，17:03/17:04 两副本上线）

冬翔 ↔ Qwen-DWS 单聊（预发 Tag·钉钉，scene `f6daee25…`），以主角身份经线上 dws 网关发送。每条都是前台首轮（1 次模型调用）决定：

| 发送 | 前台 | 后台与结果 |
| --- | --- | --- |
| 17:07:40「每隔15分钟给我讲个笑话」（16:18–16:20 同义请求被拒 6 次） | 17:07:45 dispatched，「我来在当前会话里建这个例行任务」 | 沙箱 `scene_routine_list` → `scene_routine_create`（routine `c25e73ac…`，autopilot `f5d55c39…`，task `88cd0021…`），未反问确认；17:08:01 平台变更通知，17:08:05「创建成功…下次运行 17:15」 |
| 到点 | — | 17:15:05「开始执行例行任务」，17:15:20「已完成，用时 5 秒」+ 笑话：Employee 模式 Agent 的场域例行任务按点自触发、开始/结束通知齐全（走现有 Autopilot run_only 路径） |
| 17:16:42「讲笑话的那个例行任务删掉吧」 | 17:16:47 dispatched | 17:16:58 平台通知删除，17:17:05「已删掉…例行任务剩 0 个」 |
| 17:17:4x「你去运行下 dws 看看通讯录里面是不是可以查到你的主管信息」（16:12 同义请求被拒） | 17:17:50 dispatched，「我去后台真跑一遍 dws」 | 17:18:56 真实 `dws contact user get-self` / `aisearch person` 结果 |
| 17:19:43「你能帮我设置定时任务吗？」 | 17:19:46 reply「能…说清楚多久跑一次、每次干什么」 | 不派发 |
| 17:20:28「好的，谢谢」 | quiet | 不派发 |

测试对象已清理（例行任务已删除）。已知小瑕疵：后台回报里的 cron `*/15 * * * *` 被钉钉 Markdown 渲染成粗体（`***/15 * * * ***`），属展示问题，后续可让技能把 cron 放进代码样式或只写自然语言。
