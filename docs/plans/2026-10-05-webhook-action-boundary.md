# Webhook 创建漏派与工具协议外露

## 目标与边界

处理同一创建流程的两条打叉反馈，止于可审查 CR；不发送钉钉消息、不部署、不发布、不合并。按 `development-delivery.md`、`employee-delivery-workflow.md` 和 `employee-delivery-operations.md` 执行。实现和本地验证由本 chat 负责，集成/发布/真实IM验收待后续授权。

主checkout为 `feat/tag-multitenant@582cba1f73`，含其他chat的WIP。隔离工作树基于核实的Aone目标 `1803f2ecf9c8a7a8ec8bb8379b68fa8d645f92cc`，分支 `codex/webhook-action-boundary`。CR30323103/30323140的实际diff仅处理routine播报、UI和创建回执，不覆盖前台终结选择或native协议边界。

## 事故证据与Why

时间均为2026-10-04北京时间。IM已核实两处打叉及引用。LF按内部scene查23:52–次日00:00，6条独立前台trace，无列表截断；generation输入超过64KiB且 `input_truncated=true`，不能将前缀当完整提示词重放。输出、实际工具span及终态完整。

| 事实 | 真实证据 | 判定 |
| --- | --- | --- |
| 23:53:45请求创建，23:53:49承诺后台处理 | trace `ca566a00b15d40cfbb82e097bb2c8ac3`，单次generation仅native `describe_capabilities`；Host返回无receipt的terminal reply | 模型误选no-task终结工具，Host结束；没有调用dispatch，不是队列提交后丢失 |
| 23:57:35追问 | `a640bc43f4aa43f097fc0c1657b9761e`，find_tasks(query=Webhook)空且不截断 | 本请求者/场域的查询未找到创建Task；不依模型自述断言全库 |
| 23:57:52继续后真正受理 | `4d5970c3c99f428380b878aeeb07f45d`，第三次generation才dispatch；Task `1c4d9c3f-e49f-468e-97ec-9602f771584f`，queue/agent_task `f8776e53-94a3-473a-be50-f5ad60b925fb` | 后继真实Task，不抵消首次漏派 |
| 23:58:41创建 | 后台scene_routine_create真实回执，routine `73b37047-1522-4090-82ee-d04d0ad42e0c` | 创建成功；只返回masked URL及管理员配置页获取方法 |
| 23:58:28外露 | `0ca128b4c47b413b8540234456e0fe7e`，stop、tool_calls=null，正文含reply/find_tasks XML及source_ref；IM `msglvsdbc660UpIlk5lWtY1OQ==`实收 | 泄漏的是未执行机器指令，不是真实find_tasks列表结果；23:58:55才有真正find/read |

环境：LF pre，workspace `5f8b5b73-f912-4879-9a29-b763d103fedf`，agent `23cbd386-9498-4848-a112-a6953b4aaef5`，scene `f6daee25-f5fe-4bfe-a50a-e6ff5707f65b`。外部cid仅用于IM取证。前台/后台实际模型 `bailian/deepseek-v4.1-flash`，后台Pi；Qwen显示名不代表Qwen适配器。事故trace的release为dev，精确部署源码血缘未独立证实。原始证据存私有 `WEBHOOK-ACTION-20261005` 目录。

## 方案与参考

先更新employee-loop合同与SOURCE_MAP，再改代码：

1. 当前创建/变更动作先受理，配置入口不能替代动作。describe_capabilities/reply是no-task终态，不承诺后台执行；仅请求配置入口仍首轮直接返回，缺关键要素仍可澄清。语义判断沿用现有模型决策，不加Webhook关键词分类、额外审核模型或派发器。
2. 普通stop及native调用的用户回复字段不得包含注册工具XML调用标签或原始内部source_ref。拒绝非法协议输出，沿现有至多3次格式修复预算；不将XML解析为授权，不删标签后发送伪承诺。Host拒绝/预算耗尽救援不能重送该文字。正常业务HTML、数学比较和JSON不受影响。
3. 原生dispatch/receipt保留；正确受理后才ACK，已接受效果不能因随后非法文字被清除或重复派发。完整Webhook URL不进入会话的既有合同不变；只准确说明取得方式，不提前承诺拿到完整地址。

研究固定 [GawkBot loop](https://github.com/najmuzzaman-mohammad/gawkbot/blob/71e82a1809565281cbd0bf8185d3c125b715d934/internal/bot/loop.go) 的pending tool执行/session结果记录，以及 [兼容适配器](https://github.com/najmuzzaman-mohammad/gawkbot/blob/71e82a1809565281cbd0bf8185d3c125b715d934/internal/team/headless_openai_compat.go) 的native与专用prompt-encoded协议区分。采用回复/调用分离；本仓provider支持native，不移植文本工具执行路径。

## 验证及停止条件

- 有价值的本地自动检查：事故XML stop拒绝并修复成native read；native reply/dispatch ACK不绕过；预算耗尽无泄漏；没有第四次调用或XML副作用；保留HTML/JSON及真实receipt。PG检查Host/outbox唯一Task/Run/queue及投递正文，不将模型/DWS替身当真实效果。
- 无Host副作用真实模型检查原请求、仅配置入口、创建+入口、缺要素与漏派追问；judge评必要动作/澄清、receipt事实及协议边界。若完整事故input不可读，只能标合成检查，不能冒称重放。
- 真实E2E最小路径：原场域请求→Task/Run/queue→scene_routine_create→自然结果；追问不外露协议，URL获取准确。本轮禁止IM写入与部署，因此真实验收pending，交付CR后停止。

## 当前状态

- 取证：关键模型选择、Host终态、后继Task/后台创建及实际IM外露链完成。
- 实现：完成新快照动作合同、no-task终结工具说明、普通stop/native回复/stream首句协议校验、message恢复投递出口校验。冻结输入工具表用于恢复，拒绝裸消息定位符时保留业务UUID/msgdocs路径；旧Outcome作为审计数据保留，实际投递正文以response_action.input.text为准。
- 本地自动验证：Loop整个package通过，go vet及server构建通过；隔离本地PG `webhook_action_1005` 的20项顶层检查0fail/0skip，包括事故stop修复、动态工具恢复出口、能力只读/权限、唯一派发与journal恢复、拒绝混合终结批次及流式首句边界。模型/DWS为替身，不称真实IM。独立LLM源码审查指出的动态工具恢复遗漏及裸locator误拦业务路径已修正并补反例；这是grounded源码/边界审查，不是模型输出judge。
- 无副作用真实模型实验：blocked。LF工具schema截断；预发PG只读连接timeout，未取得完整原始请求；本机已配置Bailian模型入口唯一请求403 `Access denied by API-Key restrictions` 后停止。五组baseline/candidate精确构造准备完成，成功generation/candidate/judge均0；模型语义质量未证。私有 `model-quality/REPORT.md` 含原始失败与恢复路径，不换模型或凭据绕过。
- CR：已提交 [30323257](https://code.alibaba-inc.com/dingtalk-ai-lab/dt-fde-multica/codereview/30323257)，source codex/webhook-action-boundary → feat/tag-multitenant，opened/can_be_merged，评审门禁尚未满足。提交前语义rebase目标 c219847b966aa16016c303e568b02a38403bcf2d，无冲突；保留并行交付的结论先行及连接器发现修复，并完成受影响复验。创建CR后仅回填本状态，代码检查绑定64ceaa966ca04a27c206a3d7ad84244666e60a26，最终仅Plan状态改动。隔离PG已删除，无共享配置改动。交付边界完成，后续集成/发布/模型与IM验收须另行授权。
- 集成/发布：本轮不适用（止于可审查CR）；真实IM/runtime验收：pending，需修复版本部署及后续授权。
