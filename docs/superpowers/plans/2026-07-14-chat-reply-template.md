# Chat Session 回复模板实施计划

**Goal:** 实现 SessionKey、逐 Turn 可靠查询，以及沙箱侧可安装的最终回复后处理模板。

**Architecture:** Server 持久化 SessionKey 和模板配置，并把模板快照写入 task；Turn 查询以 task + assistant outcome 为事实源。CLI 轮询 terminal Turn 后直接执行沙箱内模板文件，忽略全部中间流式消息。

- [x] 增加 SessionKey、Session 模板与 Turn 快照迁移/查询。
- [x] 增加幂等 by-key Session API、按 key 投递与 Turn 查询 API。
- [x] 增加 CLI SessionKey、wait、turn 查询和模板安装/执行。
- [x] 增加前端类型/API兼容字段，为固定模板 UI 暴露契约。
- [x] 更新文档并完成定向验证。
- [x] 回填结果并完成提交前检查。

## 执行结果

- `session_key` 在工作区/创建者内唯一；重复 create 返回同一 Session，不同智能体占用同一 key 返回冲突。
- Session 的 `reply_template/reply_config` 在创建 Turn 时快照到 task；逐 Turn 与最近 100 个 Turn 均可查询。
- CLI 支持模板安装、SessionKey 创建/投递、terminal wait、断点恢复 delivery；Server 不执行任何脚本。
- DWS 模板只消费最终 `reply.content`，中间 Streaming 消息不会触发；Turn ID 作为 DWS `--uuid`，恢复重试具备 24 小时幂等去重。
- 前端 core 类型/API 已暴露固定模板、Turn 与 delivery 字段；本次不增加通用脚本编辑 UI，避免把沙箱本地脚本误建模为服务端资源。
- 验证：相关 CLI/Handler/Service Go 测试通过；`pnpm typecheck` 全部通过；DWS reply/send help 与脚本语法已核对；migration 164 已在隔离 worktree 数据库成功执行。全量 `cmd/server` 仍有既有 runtime sweeper 用例失败，该路径与本次改动无文件交集，单独复跑 3 次结果一致。
