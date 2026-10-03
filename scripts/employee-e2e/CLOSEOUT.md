# 第五批 harness 收尾能力与离线准入

2026-10-04。此表是代码和静态 registry 的可执行分类；没有发送 IM、建群或修改预发。不能据此认定平台摄入、发布、身份认证或用户效果通过。

## 能力合同

- harness `true` 只表示注册 driver / grader 已实现并经过离线检查。平台、发布、运维开关全部默认 `false`，每波由主代理将实时证据写进 manifest 后显式覆盖。
- 已支持：at_all, burst, combine_forward, deap_multi, evidence_v2, file_download, file_send, forward, grader_v2, memory_reset, negative_observe, pg_read, quote_reply, segments, setup_group, var_sets。
- 本轮排除撤回与原生表情：`recall` / `react` 未注册，草稿保全但不能执行。`webhook` / `aitable_insert` / `config_snapshot` 仍未实现。
- file_download 记录资源引用、字节、sha256、UTF-8/BOM、行数和前200行；只记录事实，不自动核对全部业务内容，T-06 的 pending_checks 需要独立检查原文件。
- memory_reset 是最终段后的清理；先完成下一轮记忆召回和原反例留证再清理，不能用清记忆避开失败。共享/私有清理及 Coordinator reset 各自留结果。
- pg_read 是授权管理 API，不是直接 SQL。学习行、WorkPacket learning 引用、逐人邀请/输入、occurrence planned_at/skip reason 仍是 API gap；不能把 expected/received 或 routine run 汇总代替它们。
- 分段跨日守住 not_before，checkpoint 不等于完成；否定观察必须同会话且全窗可读；读取失败、缺 hasMore 边界或分页上限不能证明没有消息/Task。
- LF 零列表不证明零调用。缺必需证据为 incomplete；vacuous、平台未证或 pending_checks 使语义通过至多 partial；语义判断不能覆写硬失败。缺部署窗口证据也不能完整 pass。

## 默认 dry-run（88条）

`python3 scripts/employee-e2e/e2e.py v2 dry-run`。完整 JSON 包含有效 capabilities；本轮 platform/release/ops 均为 false。suite_errors=[]。

- runnable_partial 19：G-01, G-02, G-03, G-04, G-05, G-06, G-07, G-08, G-10, G-14, G-16, G-18, M-02, M-07, M-08, M-12, C-13, P-02, P-06
- runnable 35：G-09, G-11, G-12, G-13, G-17, G-19, M-01, M-03, M-04, M-06, M-09, M-16, C-01, C-02, C-03, C-05, C-06, C-09, C-10, C-12, C-19, P-01, P-03, P-04, P-07, P-11, P-12, P-14, P-16, T-01, T-03, T-06, T-08, T-15, T-16
- waiting_release 7：G-15, C-15, C-16, P-05, T-05, T-12, T-13
- waiting_ops 21：M-05, M-11, M-14, M-15, M-17, C-04, C-07, C-08, C-11, C-14, C-17, C-18, P-08, P-09, P-10, P-15, P-17, T-02, T-04, T-09, T-14
- blocked_harness 5：M-10, M-13, T-07, T-10, T-11
- blocked_resource 1：P-13

## 证据来源与交接

五笔原提交须按补丁顺序集成：bcb38f580f → cfdd4c5ab6 → c633f2ceed → 7e2f45802c → eb1764ef3e；本次收尾基于这五笔，保护六个原脏文件。新提交不整支 merge。

本轮离线验证：`python3 -m unittest discover -s scripts/employee-e2e/tests -v`，68 项通过；CLI 参数核对只使用 `dws ... --help`，未执行真实写入。原六文件 diff、完整测试日志和含有效开关的 dry-run JSON 保存在 `~/d1/employee-e2e-evidence/CODEX-CLOSEOUT-20261004-HARNESS/`。

离线风险反例覆盖：读取失败/分页耗尽不算负证；错误会话不污染 negative_observe；连续发送不吞重复落地；REF-01a真人「停止这个」不把员工「已请求停止这个任务」误认成重复发送；空 LF 不算0调用；模型 proposed tool_calls 不替代 Host TOOL input，ERROR工具不算已接受effect；人工判定不覆写缺证与硬失败；25小时分段时间门。

具体函数和生产只读接口映射见 `.agents/skills/tag-eval/references/harness-source-map.md`。真实准入按当前仓库 `docs/employee-delivery-workflow.md` 及当波 manifest；本表中的默认可运行名单不是验收通过名单。
