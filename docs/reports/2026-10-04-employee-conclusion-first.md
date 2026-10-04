# Employee 回复短句与原文证据交付

- 目标：先给短句、已验证的结论；用户要求的完整工具原文保留在单独段落，不以缩短为由删节。只要原文或指定格式时不加开场。
- 原反例：Qwen-DWS 单聊 `cidMF9FOe1ACHthSFNyLtfy5uHGOC8F8fjtAQpiGLe8r1I=`；失败报告 `msgLopc/gX0kVXWv+e4EOujEw==`，反馈 `msgjNPdUQlpYkgnI2jwh3Ewig==`，原请求 `msgqIZG6a9TWY4rmPR+TgjaDg==` 明确要求失败时完整贴工具原文。此处仅采用派单提供的消息证据，未读取真实 trace 或重发消息。
- 实现：`employeeloop.BuildPrompt` 的现有 conversation voice 与 `employeetask` 的现有工作包增加表达指导。先更新 `docs/employee-loop.md` 行为合同，再改代码。复用固定 GawkBot `71e82a1809565281cbd0bf8185d3c125b715d934` 的 direct-session voice / execution packet，来源映射在各包 SOURCE_MAP.md；不添加独立 composer、后处理截断或额外模型调用。
- 分支：基于内部 Code `feat/tag-multitenant@1803f2ecf9`。核对另一 Session `codex/tag-round-result-delivery-20261004@2006c1df62`，两个本次代码文件均不在其改动内；不修改 JSON 解析、机器协议、claim Output、完整结果合同或发送所有权。
- 本地检查：已有 `TestPrompt`、`TestConfiguredInstructions`、`TestCompile` 定向 Go 检查通过，`git diff --check` 通过。这些证明既有装配、来源材料保留及权限边界，不能证明真实模型表达效果。没有增加仅检查提示词字面内容的低价值测试。
- 发布与真实验收：未部署、未合并、未发钉钉消息。CR 交发布协调，收到通知后按当波 manifest 固定实际 server/Runtime、身份、场域及版本，再执行下列案例。工作包指导对新编译任务生效，已持久化工作包和模型 journal 不重写；旧在途任务不能证明新表达效果。

## 待通知后的真实验收

| 案例 | 方法与判据 | 状态 |
| --- | --- | --- |
| 原失败反例 | 使用可控失败的连接器，要求失败完整贴原文。第一句明确未完成；下一步由工具事实支持；末尾独立证据段逐字对照真实工具返回，除已规定凭据脱敏外不得删节；不能伪造成功。 | 待通知 |
| 普通失败 | 未要求完整原文；用短句交代失败与可行下一步，不重复请求/排查过程。 | 待通知 |
| 只要原文/固定格式 | 明确只要原文或固定业务格式；不能擅自加结论、标题或破坏格式，仍遵守现有机器输出合同。 | 待通知 |
| 明确详细解释 | 用户要求完整排查解释时可展开，不用硬字数限制删掉验收信息。 | 待通知 |
| 工具截断/敏感数据 | 工具返回标记 truncated 或含凭据；明确说明截断，不称完整原文，现有脱敏和权限限制仍成立。 | 待通知 |

真实取证分别收集工具 I/O、Run/task 标识和 IM 已投递正文；按当前真实合同查询 trace，不以模型自述或入队 ACK 代替投递。表达质量使用人工/LLM-as-judge 按上述判据评审，不以 token 或速度指标签收。完整原文很长时整体消息仍可能长，缩短的是解释层；不得为压长度悄悄丢证据。
