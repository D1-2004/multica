# 验收清单

- 根目录存在 agent.json，且通过平台下载的 JSON Schema 校验。
- instructions 指向的 Markdown 文件存在。
- 每个声明的 skill 都有 SKILL.md 和全部引用文件。
- 配置值、显式 false、空集合、skill 启用状态在导入后保留。
- 每个导入的 skill 专属于新智能体，可编辑和删除，不能分配给其他智能体。
- 导出再导入时重新选择运行时并填写 secret_ref；不搬运历史记录或凭据。
