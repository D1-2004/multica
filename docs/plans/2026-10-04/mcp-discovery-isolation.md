# MCP 发现故障隔离

状态：实现与本地检查通过；目标 `feat/tag-multitenant`；发布交接记录见对应 CR 和本波
证据 manifest，真实验收等发布协调通知。

## 问题与证据

2026-10-04 23:18，Qwen-DWS 的钉钉文档开通任务
`02569f3f-c885-4f5d-8735-501abf4a8947` 已成功启动并 claim，但场域 GitHub
连接器 `f9e39627-50dd-43fb-9aca-e1fb8accb3d7` 的 `tools/list` 在 45.025 秒后
记为 `upstream_error`。Pi 在发现全部服务器后才注册工具，因而零模型消息即退出。
底层错误类别被丢弃，不能将超时、代理或凭证原因当作已证明事实。
详细只读证据：`/Users/yuanzhan/d1/employee-e2e-evidence/QWEN-MCP-STARTUP-20261004/`。
CR30323004 和结果格式/前台流式反馈修复没有覆盖这个接缝。

## 参考与合同

GawkBot 固定 `71e82a1809565281cbd0bf8185d3c125b715d934` 的
`internal/team/mcp_config.go:buildMCPServerMap` 提供本机 `mcp-team` broker；
`internal/teammcp/server.go` 通过 `mcp.AddTool` 声明本地工具。
借鉴的是本地能力声明与外部调用连通分离，不声称它证明了 Pi 的失败跳过行为。
[MCP tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)
允许服务端定义具备 schema 的工具及结构化结果。

本次采用显式不可用诊断工具，不缓存/伪造业务 schema，不缩减授权配置。
先更新 `docs/internal-mcp-connectors.md`，再实现服务端响应和安全错误分类。
不改 Runtime、Daemon、claim/task wire，不重建或切换模板。

## 边界与验收

- 首页面外部可用性失败保留失败审计，返回只读诊断定义，Pi 可以注册其他连接器工具。
- 诊断不执行/重试上游请求，只报告本轮发现不可用，不能作为业务完成证据。
- 非法元数据、后续页失败、父请求取消、授权/配置/限流/审计失败继续拒绝。
- 未授权或已撤销的连接器连诊断都不可访问；保留每次调用的 task/grant/credential 检查。
- 故障依赖任务只能报告受阻，不回退本机令牌或工具，不报告业务成功。
- 错误日志只含类别、HTTP 状态、耗时和既有 ID，不含错误原文、URL、token 或上游正文。

## 步骤与验证价值

1. 文档与 Plan：明确保留/拒绝范围及保留名字，完成后回填。
2. 服务端：发现响应、只读诊断调用、安全错误分类与审计。
3. 高价值检查：故障首页面/分页中途/协议损坏/取消；诊断无副作用与授权撤销；
   原有业务调用不会重试；真实 SDK + 现有 Pi 扩展接受故障定义并注册健康工具。
   这些覆盖实际启动与权限风险，不添加全量重复测试。
4. 提交 CR 至目标分支并交发布协调。部署前只报告本地检查；收到通知后再跑原场域
   与隔离故障反例，证据分 IM/API/SLS/LF；没有实际业务证据不签验收。

待发布真实场景：健康 GitHub 工具调用；GitHub discovery 故障时无关工作继续；
必须依赖 GitHub 的任务明确受阻且零替代凭证/零副作用；授权撤销拒绝。
原文档开通操作不可盲目重放，先核对目标状态和当前授权。

## 当前结果与遗留

已完成根因调查、合同与实现。服务端增加明确的本地诊断工具及安全失败分类；
保留上游失败审计、原业务调用错误和权限撤销行为，没有修改 Runtime/Daemon。

本地结果（跨至 2026-10-05）：

- 独立数据库 `mcp_discovery_4054_20261004` 应用本分支迁移后，定向 handler 检查通过：
  discovery 可用性/协议/分页/取消、诊断入参、保留名字、真实 handler 经过 DB 的故障响应、
  诊断零上游请求、业务错误仍报错、授权撤销及失败/诊断审计；既有 InternalConnector、
  OAuth401刷新和授权拒绝检查通过。
- `go vet ./internal/handler` 通过。
- 实际 Go handler 的故障响应，经真实 MCP SDK 1.29.0 与 Runtime
  `931a1882ffaf28027c7840cfa890c4ffe57b7ab2` 的原样 Pi 扩展初始化后，故障诊断工具与
  健康文档工具都已注册，健康工具调用返回夹具标记。使用本地 HTTP 夹具与假 Pi 注册器，
  没有启动真实模型或云端任务，不等于业务验收。
- `git diff --check` 通过。本地证据保存于上述证据目录。

交接里程碑：CR 提交到目标分支并交发布协调。遗留：部署、原场域效果、故障依赖任务的模型行为与真实
IM/API/SLS/LF 验收未证明，收到发布通知后按本 Plan 的反例验收。Runtime 未改，
不要求重建候选镜像或本机 Daemon 滚动矩阵；复用原故障的固定 Template 验现有客户端。
