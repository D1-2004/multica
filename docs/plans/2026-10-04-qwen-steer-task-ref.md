# Qwen-DWS 纠正受理修复与 steer 执行审查

## 目标与边界

修复 2026-10-04 10:58「不想吃海鲜」补充受理失败，并审查取消、退出屏障、沙箱续期和原 session 恢复。只修改本轮相关文件，保护现有 WIP；不顺带更换 Runtime 或修改其他人的发布配置。先完成实现及本地验证；预发发布和真实 canary 分别记录，未执行不得签收。

原失败证据：预发 agent `23cbd386-9498-4848-a112-a6953b4aaef5`，job `aab15dd8-b4f6-410b-8ac8-603c99a9d34c`。`read_task(task_ref=t1)` 成功；`steer_task(task_id=t1)` 报 `invalid employee task input`，没有 receipt。10:58:52 原任务仍返回海鲜推荐。

## 设计与依据

复用 `employeeCurrentTaskBinding`：短引用是本 wake 内按 source/requester 绑定的定位符，Host 解析并再次校验权限。`steer_task` 增加 `task_ref`，旧 `task_id` UUID 和省略目标的唯一候选路径保留；冻结的旧调用若把短引用放进 `task_id`，只能通过相同 source 绑定解析，禁止猜测或全局匹配。两种目标字段不能同时提供。重放解析仍校验当前来源，但保持原工具参数与幂等 key。

参考 [MCP 工具规范](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2025-11-25/server/tools.mdx)：工具输入 schema 与服务端结果应一致、输入失败需明确表达。本轮采用现有 Host 定位符模式修正契约，不改变通用写工具的失败/重试语义，避免未知写入被自动重复。

## 验收与验证环境

- 原行为回归：当前消息候选 `t1` 可纠正同一任务，取消已 claim 的旧 Run、创建一个带非海鲜约束的新 Run，ACK 不落兜底。
- 权限反例：不存在、其他消息/requester 的短引用，或同时提供两种标识，必须在副作用前拒绝。
- 恢复反例：相同工具调用重放保持一个后继 Run；变更 payload 被拒绝。
- 本地：现有 Go handler 数据库夹具，定向运行 steer / current task / claim 测试；数据库不可用须明确报告，不能把 skip 当 pass。
- 真实 canary 判据：IM 补充与 ACK、原/后继 Task/Run、退出证明、同 sandbox ID、原/后继 provider session、纠正结果；缺少运行证据只说明源码行为。

## 进度

- 根因与线上证据：已确认。
- 文档/schema/Host 修复：已实现。新增 `task_ref`、兼容旧短引用，拒绝双目标；重放按相同 source/requester 再校验。副本标记升到 13，混版期间不得生成由旧 worker 解释的新工具快照。
- 定向验证：36 项 handler 测试与 6 项 service 合同测试通过（按顶层测试去重），独立 LLM 代码审查无新增阻断项。
- steer 生命周期审查：已完成，结论及限制见下文。
- 预发发布/真实 canary：未执行。

## 验证结果与环境清理

首次默认本地库在夹具创建时报缺少 `agent.coordination_mode`；不是修复行为失败，也没有当作通过。随后新建隔离本地库 `qwen_steer_20261004`，通过本仓 `cmd/migrate up` 应用至 9976，执行下面的定向测试；结束后已删除该隔离库，未修改原开发库的 schema。

在 `server/` 执行，数据库测试使用上述隔离库的 `DATABASE_URL`：

```sh
go test ./internal/handler -run 'TestEmployee(LoopSteerTask|SteerReference|SteerTarget|CurrentTask|TaskCombined|InheritedNotice|RunClaim|SteerEmployeeTask)' -count=1 -v
go test ./internal/handler -run 'TestEmployeeSteerTargetRequiresSingleCandidate|TestSteerEmployeeTaskHumanAPIResumesPredecessorSession|TestEmployeeRunNoticeSuppressedForSteeredRun' -count=1 -v
go test ./internal/service -run 'TestConnectionReuseScope|TestSandboxTaskTimeoutFloorsAt4800|TestResolveSandboxRenewsEveryWarmAcquisition|TestResolveSandboxReplacesFailedRenewal|TestSteerStopProofSeparatesUnreadableServicesFromMatchingRunners|TestSteerExitProofUsesCLIWhenSDKRolloutIsDisabled' -count=1 -v
```

handler 使用真实本地 PostgreSQL、模拟模型及执行状态；service 的 sandbox API 使用受控 HTTP/命令夹具。它们证明 Host 引用修复、拒绝/重放、退出屏障和恢复指针合同，不证明真实模型选择、远端沙箱存活或 IM 投递。`git diff --check` 通过。

## 生命周期审查结论

1. `EmployeeTaskControl.Steer` 取消 claimed queue/Run，设置 `process_stop_pending` 并创建同一 EmployeeTask 的后继。等待进程退出证明；逻辑 cancelled 或经过一段时间不解除屏障。未 claim 的 Run 合并纠正，不启动第二个 writer。
2. `applyEmployeeSteerResume` 在同 Task/runtime 下传递 predecessor session/workdir，最多跨过 5 个未启动 predecessor。daemon 再检查工作目录及 runtime context，不兼容或缺失时冷启动；完整纠正工作包始终保留。
3. 普通 FC Direct 开启 `sandbox_connection_reuse` 时按原 scene/person bucket 复用，取得沙箱时 POST timeout + GET endAt，普通沙箱默认至少 4800 秒。没有运行中的周期续期；健康、到期、容量及配置仍限制复用。等待退出屏障期间保留沙箱，idle 留存有上限。
4. 员工文件系统 Direct 分支跳过 scene reuse，无 Issue/Chat 时按 queue UUID 建执行 scope，因此不保证原沙箱连续。Qwen adapter 尚无 process-group proof，本机取消可能保留屏障，FC 依赖服务端 stop scan；这些是审查出的现有边界，本轮没有更换 scope 或 Runtime。

显示名不代表执行 provider。原推荐任务的 [agent_task](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/3da7c8a49d624030910412437968b25a) 实际是 cloud / Pi、模型 `bailian/deepseek-v4.1-flash`，已记录 Pi session 路径；不能把 Qwen adapter 的限制归因到本次 Qwen-DWS。纠正 [employee_loop](https://unify-aipilot.dingtalk.com/project/cmbio3oju0007l23kgk4f9li8/traces/aab15dd8b4f6410b8ac8603c99a9d34c) 当时在引用解析即失败，未创建后继，所以这次原始线上记录本身无法证明成功的同沙箱/session steer。

## 交付与剩余验收

源码修复和本地合同验证完成；没有提交、部署、修改远端配置或发送新 IM。已有其他人的 WIP 保持。发布需只选本轮 diff（`router.go` 仅副本错误文案这一行属于本轮），部署到原失败预发环境后确认所有副本 marker 13，再做上述同场域真实 canary。不能把本地通过写成预发已恢复，也不能在沙箱/session 证据缺失时签连续性验收。


## 预发发布波次（用户追加授权）

2026-10-04 用户要求发布预发。核对权威 Code 交付分支后发现本地旧 checkout 与其不同；发布在隔离 worktree 以 `aone/feat/tag-multitenant@79645b5b7b176ab36aa73a38bf9493ce4c8dd105` 为基线移植本轮修复。该基线已到 marker 17，发布版本采用 marker 18，保留其集合、quote control、模型路由及隐私处理。旧文中的 marker 13 只描述旧 checkout 的开发验证，不作为本波发布门禁。

本波只提交 steer Host/schema、回归测试、marker 18 与对应文档；不带主 checkout 的其他 WIP。当前流水线 66 / run 3110376103 在代码合并等待，先查明冻结的 source/release 和占用，禁止重复触发或覆盖他人发布。部署成功后验证 live backend marker、健康及原场域的真实纠正路径。

最新交付基线上的重新验证：server 二进制编译通过，38 项 handler 定向测试通过（本地隔离 PostgreSQL + 模拟模型/执行状态），包含引用纠正、quote/current-task 兼容、并发、重放、退出屏障、原 session/workdir claim 指针。该波隔离库迁移至 9999。发布代码不更换 Runtime。

发布门禁再核对：当前 release/live backend 的 marker 18 已由持久场域参与占用，因此本波最终采用 marker 19。source branch 的初次 marker 18 提交未触发新部署；以追加提交升至 19，release 语义合并保留既有参与工具与 send guard。

release 合并冲突按语义处理：保留 release 的 marker 18 持久场域参与代码和文档，追加 marker 19 纠正引用；交付合同采用 source 最新统一入口，release 既有用户纠正记录保留为历史证据。合并是流水线固定 release 的 source ancestry 要求，不是修改开发分支的合并策略。

release 候选编译通过；迁移独立本地库至 10001（已部署的场域参与所需），40 项 handler 定向测试通过，含持久参与暂停/恢复与纠正引用兼容。预发 migration 仍只由 Aone packaged migrator 执行。

当前发布检查点：source `4674169c3d`（含功能提交 `bc747806c3`），release `3af0ba7a63ffdc04574d9d0d5e862e3fdcce80dc`。pipeline 66 / run `3110376103` 的代码合并已 SUCCESS，构建 RUNNING；没有重新创建 CR 或另启 run。现有 CR `36355253` 与固定 release ancestry 保留。预发当前两台 live backend 为 `dt-fde-multica033060149134.pre.na620`、`dt-fde-multica033008056137.pre.na620`，fence normal/revision15，旧 marker18；这些是发布前读数。`/health` HTTP200 success。仅 scope 相关本地隔离数据库已清理；不修改远端配置、Runtime 或发送测试 IM。

## 预发交付结果

- 发布完成：run `3110376103`，代码合并/构建/预发部署/预发集成测试均 SUCCESS，仅人工预发验证门 WAITING。
- 构建 Job `174190350` 的真实 commit 为 `6a86b3101b54ec82144c691f8fe646c683f3e181`（平台再合入既有 eval CR）。Git ancestry 验证含 `3af0ba7a63` / `4674169c3d` / `bc747806c3`；与已验证候选的 steer Host/schema/marker 文件无差异。
- 制品 `20261004120128074043_prepub`，digest `sha256:7a1b12561faa19971c99c91ed026889eb7cfcf02f124d160a726ae30919639a6`。
- 两台 live backend 的 SLS 新启动分别为北京时间 12:07:39、12:08:50；均 `[employee-loop:19]`，fence normal/revision15，健康 HTTP200 success。没有使用 fence 历史 started_at 代替新启动日志。
- 运行验收边界：只完成本次服务端发布、健康及内置集成检查；未发送新 IM 或触发真实 canary，真实模型选择、纠正 ACK/结果和同沙箱/session 续接仍未签收。
- 清理：独立本地测试库已删除；没有改 Runtime、远端配置、例行任务或主 checkout 他人的 WIP。
- 脱敏 manifest：[2026-10-04-qwen-steer-pre-release.json](2026-10-04-qwen-steer-pre-release.json)。人工预发验证门保持原状态。
