# Coordinator Finish 展示实际任务去向

## 问题与目标

当前聊天中的 Finish 仅包含模型输入，创建任务在模型返回后执行，真实任务 ID 没有进入聊天记录；向已有任务追加评论的终结工具也没有统一的结果入口。排查时无法从决策直接跳到实际任务。

目标：在 Finish 展示服务端确认的最终动作及可点击的任务入口，覆盖新建任务、向已有任务投递、同窗口多个任务，刷新后仍保留。旧记录没有执行证据时明确显示未记录，不把模型意图当作成功。

## 实现

1. 为 Coordinator trace 增加可选的结构化任务结果，记录动作、任务 ID、编号、标题、评论和执行 ID。仅在实际操作成功后填充，沿现有 source_payload 和 WebSocket 持久化/传输，无数据库迁移。
2. 补齐 Web 创建、渠道创建、多事项窗口以及评论投递的结果记录。
3. API schema 防御解析；共享 Web/Desktop 聊天组件在 Finish 展示结果并通过 AppLink 导航，原始输入保留折叠查看。
4. 针对数据传递、异常数据与界面链接运行必要的局部验证。
5. 失败收尾保留本轮已经确认的任务结果，同时显示失败状态，避免部分创建成功后因后续步骤失败而丢失去向；不改变重试/回调行为。

## 范围与风险

- 保持 Coordinator 决策、任务派发、重试与回调语义不变。
- 任务创建和执行入队分开展示，不把已创建误写成业务已完成。
- 不回填无法可靠关联的历史记录；缺少新字段的后端或旧数据正常展示。
- 本次不发布环境，不更改 Runtime/Daemon 协议。

## 结果

已完成。

- Trace 新增 `issue_results`，持久化及 WebSocket 保留已确认的任务、评论、执行 ID。Web 新建、渠道新建/投递、终结评论工具、多事项窗口与幂等恢复均接入。
- Finish 独立于工具步骤折叠区默认展示；任务编号/标题使用 AppLink 按稳定 UUID 打开任务详情，参数可以展开。
- 终结失败保留本轮捕获的成功结果，并显示失败原因。旧记录无证据时不推断创建成功。
- API 和 WebSocket 对新字段做相同解析，异常结果条目不影响同一轮其他正常结果。

验证：

- `pnpm --filter @multica/core typecheck`、`pnpm --filter @multica/views typecheck`、`pnpm --filter @multica/web typecheck` 通过。
- core 的 schema 与 realtime 局部测试通过；views 的 Finish 与 ChatMessageList 测试 27 项通过。
- Go 的 inboundcoord、handler、channel/engine 受影响 Coordinator 局部测试通过（`Test(Decision|Coordinator|WriteAgentChatCoordinator|RecoveredCoordinator)`）。
- ego-browser 使用真实组件和示例数据验证桌面及 390px 窄窗口布局、参数折叠、任务路由跳转，未发现横向溢出。
- `git diff --check` 通过。

历史缺少执行结果的记录不回填。浏览器验证使用本地组件示例，不代表线上任务运行验收。

## 预发部署

2026-09-07 用户已授权完成预发部署。提交当前会话改动并推送专用分支，通过 Aone 应用 342160 的预发流水线 66 发布；核对构建版本、部署和集成检查结果。部署进行中，完成后回填运行记录。正式环境不在本次范围内。
