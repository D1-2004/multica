# Coordinator 上下文回归

本目录的 Go 文件是用于固定版本工作树的测试资产，不参与服务器构建。把它复制到目标部署源码的 `server/internal/service/inboundcoord/` 后执行。它复用该版本的真实 Coordinator / 投影 / 工具 schema / finish check，使用真实模型和文件中声明的只读工具结果，不接数据库、任务派发、消息发送或记忆写入。

## 执行

先按 `dws-env` 验证预发环境和测试身份；按 `dta-ops-multica` 读取指定 Agent 的真实配置，并固定 Aone 成功构建的源码 SHA。原始 trace、岗位全文、身份和工具快照保存在仓库外权限700的证据目录，文件权限600，凭证不进入 fixture。

在固定 SHA 的隔离工作树里：

```bash
cp /path/to/target_regression_test.go server/internal/service/inboundcoord/
MULTICA_RUN_COORDINATOR_REPLAY=1 \
MULTICA_TARGET_REGRESSION_INPUT=/private/suite-input.json \
MULTICA_TARGET_REGRESSION_OUTPUT=/private/baseline.json \
MULTICA_TARGET_REGRESSION_REPEATS=3 \
go -C server test ./internal/service/inboundcoord \
  -run '^TestTargetRegression$' -count=1 -parallel=2 -timeout=30m
```

模型配置通过已有安全渠道提供 `MULTICA_LLM_API_KEY` / `MULTICA_LLM_BASE_URL`；不要将值写入命令示例或提交。可设置 `MULTICA_TARGET_REGRESSION_CASE` 正则筛选病例。没有 opt-in 时只编译并 skip，不调用模型。

## 输入

```json
{
  "cases": [{
    "id": "case-id",
    "turn": {},
    "reads": [{
      "tool": "assoc_recall",
      "args": {"since": "^48h$"},
      "result": {}
    }],
    "history": [],
    "history_error": ""
  }]
}
```

- `turn` 使用被测源码 `Turn` 的完整真实字段（CamelCase）；保留当前发言、原引用、发送人与固定水位。目标原样配置和生产岗位叠加组须单独标识，不能将空岗位通过结果用来认证长SOP。
- `reads` 按顺序首个参数正则匹配。`result` 是真实捕获或明确标记的合成原始结果，仍经过 Host 的真实归一化；`error` 明确模拟读取失败。未匹配读取报错，跨会话读取拒绝，缺省CID沿用当前场景。
- `context_read(kind=coordination_state)`使用Host数据库reader，本资产不连接数据库；该分支只能返回not_loaded，不能认证成功计数路径。真实查询需另用本地事务夹具或授权预发E2E验证。
- 首个正则匹配生效，必须先写完整q/since/limit规则并验证缺省分支；不能让q-only规则拦截7d查询。
- 只提供有证据的查询范围；不同q、时间、limit不得偷偷复用不覆盖该请求的结果。无法重现的读返回 unavailable，不能伪造为 empty。受控合成数据与原始生产快照分开登记。
- `history` 为 `HistoryLine[]`，经过真实 Host 水位和裁剪投影。没有采集历史时显式说明缺口；不要用空列表冒充真实历史为空。
- Oracle/预期只供评审，不能出现在模型输入、工具返回或文件名提示中。

## 判定

Go测试成功只证明 harness 完成，报告的 `semantic_assessment` 始终为 `not_evaluated`。逐例核对：

1. 夹具/身份/时间/范围是否完整，原文是否实际进入主模型。
2. 动作、目标、source refs、限制及工作交接是否正确；不能仅看顶层 `action=issue`。
3. 审核是否误放行/误拒，反复修正的原因是信息缺失还是协议错误。
4. 明确区分协调决策、私有事件持久化、执行器可见输入、真实下载与外部送达。
5. 保留所有失败及原始请求/响应；简单正则须经语义复核，尤其否定句、排除旧任务、真正提醒与真正恢复执行。

每个模型请求及 choices 存在输出旁的私有 `artifacts/` 目录；总报告在全部并行子测试完成后写入。重复采样独立，不复用模型缓存以外的可变Turn/审查缓存。

2026-09-09 首次完整矩阵与结果见 `docs/reports/2026-09-09-coordinator-target-regression.md`（结论形成后回填）。
