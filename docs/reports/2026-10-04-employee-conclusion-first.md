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


## 2026-10-05 真实验收与补修

Run `3110403969` 的冻结源码为 `c219847b966aa16016c303e568b02a38403bcf2d`，release `cd37c4638e8f81f5e37e8806ed340f96d43adbb6`；此前通知的 `a56bb522` 已由发布方纠正。00:42:45 部署 SUCCESS，两副本00:41:09/00:42:17启动，Fence normal，markers employee-loop:23/employee-human:2。版本准入证据为发布方对应 manifest。

用户在本 Session 明确授权最多3条只读消息；00:53:59–00:57:48以主角在原 Qwen-DWS 单聊发送，未改 GitHub 资源。Agent 快照为 Tag·钉钉 `23cbd386-9498-4848-a112-a6953b4aaef5`，model `bailian/deepseek-v4.1-flash`，runtime `461aabb2-2da0-472c-9565-132042c36e25`。实际后台 task trace `7a2bd19ae21043119c849d8d866bc87a` 的 generation 输入包含新表达指导，五条实际错误原文均保留，故并非修复未生效。

- 默认失败报告：**fail**。终态消息 `msgwyyYSD7pB2+s4dCZLmTImw==` 结论在前但仍有多段诊断过程，自己的解释过长，IM证据列表粘连。不能因证据完整就把表达判为通过。
- 只给原文：**表达项通过，严格格式未完整证明**。消息 `msgsT+eZSZzS6YLPzNvAu+gtg==` 去掉开场和解释；trace `08aa55025d4c4425955ae9038a4c9b3a` 仅 find_tasks/read_task/reply，没有重跑GitHub，但保留工具标签和列表，不是字节级纯原文。
- 一句话短答：**partial**。消息 `msgXKRYSUCw5Vp2s5aFWoSF9g==` 明显简短，冬翔在钉钉回读反馈 `msguj00j53wCAsilOD6mZs9PA==` 为“这个版本还ok”；但实际两句且同窗有真人纠正，不冒称独立的一句话硬格式通过。

整体未通过，不关闭人工验收门。证据保存于 `/Users/yuanzhan/d1/employee-e2e-evidence/CR30323168-STYLE-ACCEPTANCE-20261005`，包含三条发送/独立回读、精确trace、最小manifest与assessment，原始用户数据不提交。后续仅补默认解释长度和排查过程边界：一两句自然结论与必要下一步；要求完整工具原文不等于要求排查报告，只有原文证据段按需展开。详细/固定格式/只要原文等例外、脱敏和结果协议保留。补修本地定向检查通过；尚未部署或真实复测，另交CR等待发布通知及新的IM授权，不追加第4条消息。
