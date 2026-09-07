# 旧 Daemon 的 Runner MCP 兼容修复

## 背景与范围

线上旧 FC Daemon 没有声明 `runner-mcp-mounts-v1`，服务端在所有任务的 MCP 注入入口提前拒绝，导致未开始执行即取消。本次从 `develop@7180b0607` 创建修复分支，只调整服务端能力校验与现有兼容路径，不改变镜像或数据库。

## 方案

1. 缺少动态挂载或托管中继能力时，使用现有 legacy 注入路径。
2. legacy 路径先计算有效动态挂载；没有挂载时保持 Agent 配置并允许执行，有挂载时才检查 `runner-mcp-mounts-v1`。
3. 新 Daemon 继续现有托管中继路径；真实动态挂载需求仍拒绝不兼容 Daemon。

## 验收与发布

- 覆盖旧 Daemon 无挂载、空清单、名称冲突、有挂载拦截与兼容 Daemon 注入。
- 编译并执行受影响 Go 测试。
- 提交分支与 CR，通过 Aone pipeline 66 部署预发；核对构建版本、部署与健康状态。
- 预发使用旧 FC 模板运行无副作用 canary，并记录执行证据；按 Daemon 协议兼容矩阵记录实际通过项与未验证项。
- 不推进正式发布。

## 结果

- 已完成服务端修复及协议说明更新。
- 在修复前的 `develop` 上运行“旧 Daemon 无挂载”用例，复现 `sandbox daemon does not support dynamic Runner MCP mounts`；修复后 8 个兼容场景及 6 个相关 MCP 用例通过。
- `go build ./cmd/server` 与 `git diff --check` 通过。
- 本地空库全量迁移在既有 `271_task_completion_canceled_status` 处失败（依赖晚于它的 `9025_task_completion_outbox`）；在独立测试库补齐所需 Runner schema 后完成针对性验证，未修改仓库迁移文件。
- 预发部署与线上 canary 待执行。
