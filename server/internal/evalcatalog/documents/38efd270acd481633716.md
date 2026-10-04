# DS-09 前台输出合同修复

2026-10-04，承接 18-closeout 与 17-m8-first-canary。开发所有权：`employee/codex-foreground-output`，基线 `2f4e6005b4`；仅改前台新快照系统合同与必要评测，主代理负责集成、部署与原 DS-09 的真实 IM 复验。

## 问题与 Why

原始证据 `~/d1/employee-e2e-evidence/CODEX-RESUME-20261004-M8/ds09-lf-trace.json`：employee_model 一次 generation，实际模型 `bailian/deepseek-v4.1-flash`、configuration revision 21、employee-fast-v1，input/output 未截断。请求“只回编号”，答案却是编号后附解释；岗位默认“结论放第一句，背景和依据放后面”与仅面向 memory 的输出约束没有形成通用优先级。答案正确不抵消格式失败。

## 设计与出处

复用固定 GawkBot `71e82a1809565281cbd0bf8185d3c125b715d934` 的 `internal/team/prompt_builder.go::Build` 与本仓 SOURCE_MAP 已记录的职责：模型处理说话风格和语义，Host 持有来源、权限与效果。采用本仓 M3/近期对话的“新 Persona 快照冻结、旧 journal 原字节重放”模式，追加通用 `REPLY CONTRACT`，覆盖普通对话、群材料与 memory；明确当次输出限制优先于默认语气和解释要求。延续 GawkBot 问句不建 Issue 的边界（本仓 employee-foreground-boundary.md §8 引自固定源码 :655/:919），不把严格格式变成后台派发理由。

模型负责理解与表达；不改在线 Tag 模板、不改全局 BuildPrompt、不硬编码原答案，不加 Host 编号正则截断或第二次审核调用。规则同样保护歧义诚实、证据不足不假装确认，输出限制不授予权限，引用和历史不变成新的执行请求。

主代理补充的 M5 真实反例纳入表达合同：Host 拒绝无关成员 forget 后，模型仍说已清掉；授权遗忘或 reset 后复述旧值。明确以实际 accepted/refusal、当前授权 memory 快照判定效果，意图和旧 assistant 正文不是效果证明，确认遗忘后不借解释复述移除值。Host tombstone/history 投影检查由主代理另行核对，此切片不扩张存储或权限范围。

## 验收与范围

- 定向检查新 chat/task 快照含合同、旧快照不被补写；静态/脚本模型只算装配验证。
- 真实质量使用已有隔离 eval 的 `pkg/llm` 调用路径和已授权的当前 provider 配置，保留完整 LF 原请求，只在系统 Persona 加新合同。按主代理限制，只跑一次旧 baseline、一次原例候选与七项泛化：改编号、JSON、只回数字、DS-01 手头证据直答、DS-03 实质歧义、缺证与显式解释要求。保留所有失败与原始请求/响应。只调用模型，不接 IM/派发/数据库，不改预发配置。
- 本地真实模型 replay 证明输出质量的有限范围，不能替代原场域重新摄入/投递的 IM 验收；不得把静态断言绿灯称为真实质量。

## 状态

代码与 spec 已完成。`TestPersonaSelfProfileFrozenNewSnapshotsOnly`、`TestPersonaOldSnapshotReplaysWithoutM3Sections`、`TestPersonaAmbiguityRuleForChatWakes`、`TestTranscriptRulesOnlyWhenLoaded`、`TestPersonaMemoryRepliesAndTimeRules`、`TestPersonaNeverClaimsUnrunExecution` 六项在独立本地 `multica_codex_foreground_output_20261004` 实际通过，无 skip；验证冻结装配、chat/task 覆盖及重放边界，不认证模型质量。gofmt、handler 编译与 diff check 通过。

真实 replay 证据目录：`~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-OUTPUT/`。只读 Aone trait 未提供注册表内加密 Bailian 配置；经主代理授权复用本机既有 `~/.grok/dashscope.env`，只按 LF 的 `deepseek-v4.1-flash` 请求，未使用文件里的其他默认模型。此为 Bailian 同名模型的隔离实验，不是预发同账号配置。第一轮九项均发生 provider failure；补留结构化错误的一次诊断返回 HTTP 403、`access_denied`、`Access denied by API-Key restrictions.`，首个失败即停止，没有模型 fallback 或继续重试。没有成功 generation、没有质量 pass，不销掉 DS-09 原失败。完整请求与失败记录保存为权限 600 的私有 artifacts，初轮只记 transport failure 的证据不足明确保留。临时 trait 凭据文件已删除，未改线上配置、未发 IM。

M5 accepted/refusal 与遗忘后不复述条款仅有装配验证，真实质量仍待主代理原场域复验；Host tombstone/history 是否需修复也仍由主代理核查。本切片本地提交供主代理集成、部署并重跑原 DS-09/M5；SHA 由提交输出记录，部署与真实验收状态保持 pending。
