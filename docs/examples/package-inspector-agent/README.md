# Agent 包验收助手

这是可直接从本地导入或放入 Git 仓库创建的完整测试智能体。

- 选择“从本地导入”，上传此目录内容的 ZIP；agent.json 必须位于 ZIP 根目录。
- 预览后选择工作区内的运行时，确认身份/电脑绑定留待配置，再确认创建。
- 创建后检查：人格、回复风格、并发数 3、两个 skills（一个启用、一个停用）、四个附属文件、OKR 与停用的 A2A 客户端策略。
- 同一工作区再次导入前，请修改 OKR 的目标和关键结果文本；平台不允许两个 Agent 占用同名 OKR 标签。
- 测试消息：“请按你的验收模板说明你如何检查一个 Agent 包。”
- 导出会将环境值换为 secret_ref，再导入时填写目标配置值。本示例无真实凭据、外部账号绑定或网络服务。

压缩命令（在本目录中执行）：`zip -r ../package-inspector-agent.zip agent.json agent.schema.json AGENTS.md skills README.md`
