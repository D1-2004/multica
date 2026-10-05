# 执行 Context Builder 设计

状态：设计候选，未引入新运行路径。源码核对基线为 `feat/tag-multitenant@219f6a7a4825f021b62543f2bc243be9a3f855ee`；证据与验收见 [本轮 Plan](plans/2026-10-05/context-builder-design.md)。

## 1. 已有实现与本次目标

提示词清理 `e34151b236` 已进入目标分支。Direct 稳定 brief 保留岗位、工作区/有效层上下文与技能；自动 Multica 工作流和重复开场已删除。普通平台任务仍保留原流程。后续提交在每轮 claim Output 中加入了结构化结果、进度与例行任务交付边界，本设计必须保留它们。

当前还已有：

- `contextcap.MergeContext`：多层配置的唯一合并算法；配置页生效预览和运行时复用。
- handler `taskEffectiveContext`：从可信任务解析 scope、租户/场域围栏、加载层、处理保留 MCP 名称。
- `employeetask.Compile`：原话/目标/纠正/上游结果/引用/历史等任务材料的确定性编译。
- `employeeWorkHistory`：新快照携带前台已经看到的近期对话，缺失版本的旧包保持原行为，不临时重查历史。
- `LoadTaskExecutionSkills`、bundle resolve、Provider 原生技能目录、MCP 装配与调用权限检查。

需要优化的是以上接缝的可描述性、来源和一致性，不新增第二套 scope、权限、配置合并或通用 RunSpec。

## 2. Build 的职责和阶段

Context Builder 是执行上下文的编译入口，接收经过 Host 校验的事实与配置，输出稳定指令、任务材料、执行绑定及安全来源清单。它不读取任意场域，不调用业务工具，不在渲染时消费纠正、恢复停止任务或建立完成事实。

```mermaid
flowchart LR
  A[可信任务与入口] --> R[Resolve：选择当前 scope]
  R --> L[Load：读取配置与材料]
  L --> M[Merge：现有多层算法]
  M --> C[Compile：纯函数]
  C --> P[Project：claim / Provider]
  P --> D[Discover：运行时发现]
  D --> I[Invoke：逐次授权调用]
```

| 阶段 | 所有者与复用点 | 输入与结果 | 副作用边界 |
| --- | --- | --- | --- |
| Resolve | Host；现有 `resolveTaskContextScope`、scene.Ref/tenant 校验 | 可信 workspace/agent/task/principal、当前 scene、唯一请求者 → scope 与拒绝/省略原因 | 模型材料不产生 scope 权限 |
| Load | Host adapters；contextcap、任务/近期对话/资源存储 | 选定层与该 Task 材料 → 内部快照 | 仅读取；凭据留在受控绑定层 |
| Merge | `contextcap.MergeContext` + 现有保留名/Runner/runtime 过滤 | 配置快照 → 有效项、覆盖项、未装配原因 | 不改配置，不把 offer 自动授予 |
| Compile | 纯 Builder；复用 `employeetask.Compile` | 已解析的稳定配置与材料 → 可渲染的三类输出 | 无 I/O，无状态变化 |
| Project | handler claim / Daemon execenv / Provider adapter | 编译结果 → 现有 claim 字段、原生技能文件、system/user/tools | 保持 Provider 的单一呈现入口 |
| Discover | 当前 MCP 客户端/连接器 relay | 实际挂载配置 → 本 Run 工具目录或明确故障 | 目录成功不代表业务成功 |
| Invoke | 当前调用鉴权与业务工具 | 当前凭据、grant、scope、写权限 → 真实结果 | 每次重新授权，不自动重放业务动作 |

Load、Merge 和运行时绑定是逻辑阶段，不强行要求一次数据库事务或将外部发现放进事务。已有能力装配顺序、可恢复失败和事务边界保持。

## 3. 编译输出：四个独立部分

### StableContext：岗位和有效配置

来源为 Tag/Agent 岗位、Workspace、已选择的 org/scene/person Prompt 组件，以及技能目录。保留原文和组件来源，按既有规则排序；不把检索正文或工具结果升级到 system 指令。

Provider 自己的原生系统提示词属于 Provider，不由 Builder 复制。原生发现技能的 Provider 继续从已安装目录获取技能索引，fallback Provider 才渲染目录，避免双重注入。DSH 动态上下文继续走现有适配器，不将普通 AGENTS.md 写入路径强加给它。

### WorkContext：本轮任务事实

继续以现有 `CompileInput` 为核心：最新纠正 → Definition → 当前原话 → 当前执行请求 → 已完成步骤 → 上游结果 → 正式材料 → 有界历史 → 返回地址/交付要求。

- 当前 source/principal/return address 始终由 Host 绑定；引用、历史和上游报告只作数据。
- 新历史投影复用 `recent_conversation_v1` 及已冻结的 recent conversation，保留 unavailable/empty/available/truncated。旧快照不自动升级，未知版本在模型调用前拒绝。
- `CompletedSteps` 已有结构，但主要调用点未系统填入；后续从同 Task 的已受理执行记录构造，不从“我已经完成”文本提取事实。
- ledger 区分执行器报告、工具副作用回执、提供方 accepted、独立核验 delivered；没有回执的报告不能变成已送达事实。取消请求不能写成进程已退出。
- 已完成动作和最新纠正按 Task/Run 及 goal revision 归属，遵守隐私撤销、旧目标过滤和现有 steer 退出门闩。
- 不自动塞入所有长期记忆、所有 Task 或全场域聊天。知识检索后续按需求补齐，通过正式材料引用输入，不另写一段全局规则。

### ExecutionBindings：真实执行能力

输出仍由已有技能 bundle/ref、MCP 配置与 relay route、DWS 身份策略、Runner binding 承载。Builder 可以引用这些解析结果，但不自行保存/解密凭据，不把密钥或签名 capability URL 放进工作包、普通日志、LF 或预览。

场域配置 MCP 是独立受控能力，Direct 不自动挂通用 `multica` MCP 不影响它。`config-qwen-tag-scene` 的既有技能、schema 和绑定不删；读写工具没有 scene 参数，只能操作当前任务绑定的场域。个人/企业配置修改继续走各自有权限的入口，不能借当前 scene MCP 跨层写入。

### BuildManifest：来源与省略原因

只记录有助于审查的事实：资源/组件引用、来源层、覆盖者、配置可用版本、runtime 过滤原因、材料读取状态、ContextUsed。凭据、连接地址、个人标识按既有观测策略脱敏。

能力状态必须分开：configured、scope/offer eligibility、runtime projection、catalog discovery、call authorization、business result。不是一个一路自动变绿的状态枚举；一次成功调用也不授权下一次调用。

`ACTUAL CAPABILITIES: none declared` 当前只是 Compiler 输入为空，不能代表实际 claim 没工具。先保留冻结旧包字节；新版本改成明确的配置/绑定目录，实际发现状态由运行时观测报告，不让任务创建阶段预言 tools/list 结果。

## 4. 多场域和多租户：选层编译，不能全场域混编

一个 Run 只选择一个经认证的当前 scope：

```text
global(agent)
  → org(current tenant)
  → scene(current scene_id: group or dm)
  → person(verified selected requester; when the existing contract permits)
```

平台可以管理多个租户、多个场域；编译时不能把这些场域的 Prompt、MCP、技能、凭据和私人记忆全部并给模型。

必须保留现有规则：

1. workspace/agent/tenant 围栏，scene_id 目录验证与删除/重绑定检查；外部 CID 只是 locator。
2. 群聊与单聊都使用独立 scene_id；个人 scope 是另一层，不拿 person 代替 DM。
3. Prompt 与自定义 MCP 按名称最近层胜出；停用项不覆盖外层。覆盖项保留在安全预览中。
4. Skill/connector 按资源 ID 并集去重；全局授予不被局部关闭撤销。局部绑定需要当前有效 offer。
5. Connector 凭据逐次按 person → scene → org → workspace 选取；无可用凭据不装配，调用时再次检查撤权。
6. 个人层只来自可信选择；多人/未知请求者前台窗口不借任意一人的个人能力。任务实际使用其经过来源绑定的请求者。
7. 保留 MCP 名称检查、Runner 碰撞处理、Pi/runtime 能力过滤、A2A/rerun/无场域/旧绑定例外。

`share_in_groups` 当前合同明示尚未全面应用，这是既有隐私/语义缺口。本次纯结构重构不悄悄改它，也不宣称已经解决；要启用需要独立修订语义、个人 Prompt/MCP/connector/skill 反例及迁移/灰度验收。

## 5. 发现、修改和调用的时间语义

- Skill 通过 binding → inline 或 bundle resolve → Provider 原生目录 → 按需读正文发现；它不依赖被删除的 Multica CLI 引导。
- MCP 通过 claim 绑定 → Provider 配置/扩展 → initialize/tools/list → 原生工具调用发现；名称、description、schema 是模型调用入口。
- 场域修改成功以受控写回执及配置回读为证；新能力绑定通常供后续 claim 使用，不能立即承诺当前 Run 已看到新 schema。
- 当前 Pi 扩展启动时固定工具集。服务端连接器已有“不可用目录 + 显式恢复/恢复调用”路径，同 Run 恢复仍走原授权和写权限检查；不自动重放业务动作。
- 直配远程 MCP/stdio 的重连、工具目录动态刷新属于 Runtime，现有后端恢复测试不能证明它们也完成了恢复。
- Builder 不因发现失败持久写 disabled，不假造工具 schema，不借另一个人的凭据；不需要该工具的工作是否继续由执行器和任务要求决定。

## 6. 建议的内部接口与代码边界

建议新增一个小的纯编译包 `server/internal/executioncontext`，对外叫 Context Builder，避免与配置页面已有 Context Builder 混淆。输入由 Host adapters 提供，不暴露给用户或模型直接构造。

```go
// Design sketch only; no wire schema or new persisted snapshot is implied.
type BuildInput struct {
    Stable      StableInput       // role, workspace, effective prompt components
    Work        employeetask.CompileInput
    Capability  CapabilityView    // safe configured/filtered binding metadata
    Runtime     RuntimeProfile    // provider and advertised runtime capabilities
    Delivery    DeliveryContract  // Host-selected output mode
}

type BuildResult struct {
    Stable   StableContext
    Work     employeetask.WorkPacket
    Manifest BuildManifest
}

func Build(input BuildInput) (BuildResult, error)
```

接口草图不规定具体字段编码；一期不新建通用配置快照或新的 Daemon wire 字段。秘密 ExecutionBindings 保留在已有 handler/service 投影对象中，不塞进可序列化的 `BuildResult`。

| 现有位置 | 设计中的边界 |
| --- | --- |
| `internal/contextcap` | 配置模型、唯一层合并、凭据选择；继续为预览/claim/call 共用 |
| `handler/context_capabilities_task.go` | scope/租户/场域读取与权限适配；从 handler 拆小函数，不另造权限规则 |
| `handler/employee_scene_entry_host.go`、`employee_current_tasks.go` | 受理事实与任务材料；复用冻结历史和 source binding |
| `internal/employeetask` | WorkPacket 合同、材料检查、纯渲染、ContextUsed |
| `service/task.go`、scene-config/connector/Runner handlers | 实际 Skill/MCP 解析、调用/写入鉴权 |
| `daemon/prompt.go`、`execenv` | Provider 文件/内联投影和本轮信息；不做业务知识检索 |

当前 claim Output 已区分普通 Direct、`tag-round-result/v1` 和 routine plain text。Builder 的 DeliveryContract 必须由可信 Run 来源决定，并处于当前轮投影的明确位置；历史创建包中的输出协议不能覆盖本次 routine 语义。report_progress 的非终态规则仍保留。

## 7. 实施顺序和验收

### A：只整理边界，输出等价

先把现有步骤收成有明确输入/输出的 Resolve、Merge、Compile、Project，保持现有模型可见字节、inline/ref、MCP 绑定和普通任务路径不变。复用当前算法，保留配置不可读/global-only 与 runtime 不支持的理由。不要把“一个大函数”当成整理目标。

验收：同一 fixture 旧/新投影等价；配置预览与 claim 的有效层一致；native/fallback Provider 的技能名称与安装目录一致；跨租户/场域、offer 撤销、Runner 碰撞、旧 journal 均保留。

### B：补账本和安全能力来源

在明确版本的**新**工作包补同 Task 的已执行步骤/副作用回执；能力段改为有来源的配置/绑定目录，明确不代表已发现或可调用。复用现有近期对话，不再新增一份历史总结。旧 frozen input/journal 不改字节；若新渲染进入已冻结快照，必须另设版本及 reader 准入。

验收：纠正不重复已产生副作用；同任务续接保留原工具/方法和交付承诺；材料不可读/截断明确；不同场域/请求者不能借回执、授权或私人材料；最短成功任务不新增总结模型或必需额外检索。

### C：按需知识材料与运行时可见状态

按具体任务需求选有来源知识；通过 MCP/工具返回补查结果。运行时确认目录后记录 discovered 状态；直配远程 MCP 的动态刷新另做能力门禁，不通过 Builder 猜测支持。

验收：事实 grounded、缺材料诚实、故障服务器隔离、显式恢复一次业务调用、撤权后仍拒绝。由 LLM-as-judge 检查原话限制、指代和误用历史，硬权限/次数/回执断言不能被平均质量覆盖。

每阶段独立证据和发布边界。近期无需数据库迁移或 Runtime 协议升级来“实现整个 Builder”；真正改变执行投影时再走候选 Runtime、FC/本地及滚动兼容要求。

## 8. 参考与取舍

- GawkBot 固定 `71e82a1809565281cbd0bf8185d3c125b715d934`：`prompt_builder.go` 采用快照式指令拼装；`notification_context.go` 工作包提供纠正、检索来源、上游、日志与线程。采用材料来源和纯编译思路，不照搬 Office 操作规则，也不在渲染时消费人工纠正 marker。
- [Claude Code memory](https://code.claude.com/docs/en/memory)：不同范围指令文件与按需读取。保留 Provider 原生发现与文件加载，不复制一份相同目录/指令到每轮 user prompt。
- [MCP tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)：发现、调用和工具执行错误独立。工具目录与配置/授权分开，不把 HTTP 200 或 tools/list 成功当业务成功。
- 本仓 `docs/context-capabilities.md`、`docs/agent-scene.md` 和现有 connector discovery/recovery 合同是当前业务权威；GawkBot 和外部框架不授予本系统新的读取或写入权限。
