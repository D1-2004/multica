# 场域要点 native 输出修复

2026-10-04，承接主交付 Plan 18；只负责 digest 输出合同，独立分支 employee/codex-digest-output，基线 2f4e6005b4。

## 真实失败与原因

预发 `814fc131-5191-4df0-a7da-63617a106896`（15 人话，threshold）两次调用都把长思考写进 subject，输出到 1024 token 时 JSON 截断，两次 invalid_arguments、零写入。第二轮将第一轮坏参数原样喂回，几乎重复。schema 未 strict、只要求 op、主题仅有描述；prompt/Host 的条数和主题长度不一致；合法 8 条 × 300 字引文也无法保证在 1024 token 容量中装下。解析失败 Proposed=0 又误判 no_change，重置无进展计数。

证据：`~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-M8-MF/mf-threshold-trace.json`。LF 没记录 finish_reason，所以「触及上限且 JSON 中断」是已证事实，不能凭 LF 断言原响应 finish_reason。

## 研究依据与合同

- GawkBot 固定 `71e82a1809565281cbd0bf8185d3c125b715d934` 的 `internal/team/entity_synthesizer.go`：专门的简洁产物 prompt、bounded output、坏输出拒绝；task_ledger 的零模型事实账本和阈值机制照旧。它输出完整 markdown，我们保留当前条目级引用与权限门，不照搬替换整篇。
- [OpenAI native function calling](https://developers.openai.com/api/docs/guides/function-calling)：strict 要求对象关闭额外字段且全部属性 required；原生函数参数与用户正文分开；Host 继续验证语义和来源。这里采用有限字段 strict 单函数，供应商是否执行 strict 必须用当版真实预发验证，不能把 schema 当质量保证。
- subject 只表示可召回主题，20 字；quote 4–300 字，是一条人话的连续原文字串，含内部空白原样保持。每次最多 8 操作，删除不参与写入的自由 reason；无关字段用空串。给短正确示例，禁止参数内解释/推理。输出上限 8192 token，容纳合同中合法多条中文引文；这不是性能优化，也不提高调用次数。
- 只解析唯一指定 native call，缺 ops、null ops、未知字段、JSON 中断、finish_reason=length 均拒绝；不补括号、不截主题、不从长思考中摘嵌套 JSON。
- 修复轮从原输入重建，只带同一 native call_id 的安全空 ops 参数与错误/已接受证据标签；损坏或长思考参数保留在 journal/LF 作为证据，但不再喂给模型。仍只允许一次 repair，已接受效果不重复。
- malformed/truncated 全拒绝记 rejected 与无进展；真实合法空 ops 才记 no_change。

次数、阈值、去抖、租约、24/场域/天、300/Agent/天、journal replay 零 generation、人话/租户/场域/撤回权限不变。

## 验证与验收

必要风险验证：真实隔离 PG + httptest native malformed→repair、两轮 malformed 无写入且 rejected/no_progress、完整 JSON 但 length 拒绝、原文空白/主题超限/来源权限；原 replay 与预算/时机测试不退化。定向 build/vet。

本地测试不代替产品验收。主代理负责集成/发布/原 MF 群真实复验，再核对实际候选、下一轮召回、SLS/LF 及环境窗口。子代理不发 IM、不改共享预发、不调用失效本机同名 provider key。

## 本地完成结果

- 全 digest：RUN=27 / PASS=27 / FAIL=0 / SKIP=0；隔离库 `multica_codex_digest_output_624`，每例独立 schema 并自动删除。日志 `_shared/logs/codex-digest-output/digest-output-final.{log,sum}`。
- 原生路由适配 unit：1 PASS / 0 SKIP；`digest-adapter.{log,sum}`。
- replay、双副本、malformed/length 的 race：11 PASS / 0 SKIP；`digest-output-race.{log,sum}`。malformed→repair 的两次响应在模拟提交崩溃后重放，provider 请求仍恰好两次。
- `go vet` 和 `go build` 覆盖 digest 与 handler，均退出 0；`vet.log` / `build.log`。
- 基线 2f4e6005b4 对同一「两次截断参数」对照 FAIL：calls=2、accepted=0 却 outcome=no_change/no_progress=0；当前 rejected/no_progress=1。最小复现与日志保存在 `baseline-repro-test.go` / `baseline-malformed.log`。临时基线 worktree 在取证后删除。
- 当前原文 JSON 字符串展示保留内部空白/引号，不把人话新行当成新的 Host g 标签；写入不 normalizeSpace，不接纳空白改写引文。合法空 ops helper 改为 []（旧 helper 误生成 null）；null 在 API 边界判坏参。

本地实现完成，未 push/部署、未发送 IM、未访问真实模型凭据。真实产品质量仍待主代理当版 MF 原场景验收，不能把本地 fake native/事务通过当质量 pass。部署需特别核对所选 provider 接受 strict/单工具指定/parallel=false 参数，LF记录 finish_reason 与实际 token 上限，候选须是短主题和逐字人话；下一轮提问独立证实召回。

