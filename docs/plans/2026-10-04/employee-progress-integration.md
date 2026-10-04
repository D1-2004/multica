# 后台主动进展：集成发布与窄验收

用户在源会话01a102a1明确授权同步到本交付会话并由此集成发布，已核对原human question reply。功能仅6e4af95175；源远端后续e23b8ff仅交接资料。本批不合入源父历史的重复业务实现，不复制主checkout/A2UI/前台feedback的WIP。

## 合同/版本与流程

基线为本交付115252fbd7；共享交付规范79645b5b7b按语义复用，旧批次五项问题移至沿革并保留链接。功能commit parent与1152的业务树无差异（差异仅mobile/Coordinator说明），摘取仅新增P。候选reader19、10020–10023；当前18及10000/10001基础保留，核组合reader，不用低marker覆盖。当前规范入口docs/development-delivery.md、employee-delivery-workflow/operations。

主代理唯一集成/发布/真实场域写入。约10分钟集成审查及受影响本地检查，约15分钟一次发布，约15分钟窄E2E检查点；外部等候越界先交精确状态与接手包，不不断重发。没有用户硬截止，但限制一次共享发布，不新建Runtime镜像，不跑88套，不自动合并别的会话未提交改动。

## 验收与环境

- 确认实际新claim已具备现有managed-MCP兼容能力，专用employee-progress只暴露report_progress；与旧claim/automation不混。用当波API固定server/workspace/agent/tenant/Runtime/template/角色/scene及现存routine，不靠历史manifest猜现状。
- 最小真实工作在running窗口产生经工具验证的一条阶段报告，实际Runtime调用report_progress→PG候选/wake→Loop reply/quiet→原会话真实一次IM投递。report accepted不当delivered，Task/Run不被progress推进；final独立。
- 最窄反例：同ID/正文重投只一次；若可在同一短任务覆盖，补终态拒旧报告或用户明确不发中间消息。不伪造task-token调用当Runtime真实canary，不以源码端口或fake测试签工具发现。
- B/C/D复用不受影响已证内容；当版只串联必要进度查询/完成后续改或在途纠正。此前BASE-TASK明确新执行被误判旧结果问答的FAIL保留，不清历史洗掉；未实现新任务优先级/前台首轮feedback不宣称已解决。
- 每例完整检查用户效果、receipt/source/Task/Run、真实工具、上下文来源、权限及副作用。IM/API/SLS/LF分面保存；旧Runtime generation缺面则限制相关结论，不扩展解码/镜像修复。
- 短任务需要仍在running的展示窗口，可用60–90秒有明确阶段的真实工作；不要30分钟等待/跨日。Runtime缺能力/启动失败时record invalid_env/blocked，不临时换共享Runtime。原共享发布窗口若被其他部署占用，先只读检查并等待适当检查点，不取消别人run。

## 恢复/交接

不默认pause routine；必要时先存原状态，结束恢复且GET回读。只清理本轮新增测试对象并保持audit，不动其他session。结论、精确提交、当波manifest、未证明项和责任及时写当前执行表；发布与真实验收分别签，不用本地32测覆盖真实FAIL。

当前：共享规范已合入，feature摘取进行中；未发布、未发消息、未改远端配置。

集成核对：共享规范9be0882fd8、feature93696eea86（源6e4af951）；业务接缝无冲突，5个缺少父规划的文档作为源资料保留，缺失大规划资源改指源远端，没有引入整支历史或主checkout WIP。窄独立审查未发现新P1/P2，真实新claim能力门尚待验证。独立本地库employee_progress_integration_1004创建、迁移及受影响检查进行中。

受影响集成检查：12个顶层测试通过、0 skip（全部8个P测试，加冻结恢复/混版/currentTask续接/steer），source业务树与集成一致，源32项/race复用。server编译、handler/employeeentry vet通过；新空库完整migrate成功。原已发布18仍normal且Runtime461未变。候选提交将一次发布至app342160/pipeline66；当前最新部署需按pipeline-before.json占用核验后触发，不重跑别人实例。

## 固定 release 组合检查点

本地组合从已发布 `6a86b3101b54ec82144c691f8fe646c683f3e181` 开始，先语义合入 steer 证据源 `5357e67fb416ed4793a500f5f5f9f11a8f0f4064`，再合入冻结本批 P 源 `e5ef5adbea6f628efba623cf8c73d38f37d71381`，不从并行会话开发分支整支合入。保留最新 steer 与持久场域参与实现；两源 loop19 组合提升为唯一 canonical loop20，混版反例使用旧 loop19。迁移 10020–10023 无冲突。共享交付合同采用新 generic 入口，已发布历史证据移至沿革并保留有效引用。A2UI 与首轮 feedback WIP 未纳入。

本轮仅准备本地可审查 release；主代理负责后续唯一发布与真实验收。验收标准为三源 ancestry 完整、P 接缝之外保留已发布业务、唯一 reader20、格式检查与 server 编译通过。`go test ./cmd/server -run '^$'` 只编译，不当行为测试；既有独立 PG 的 16 项顶层 PASS 日志位于 `/Users/yuanzhan/d1/employee-e2e-evidence/EMPLOYEE-PROGRESS-INTEGRATION-20261004/combined-tests.log`，按不受本次接缝影响的范围复用。本轮不使用共享 DB、不发消息、不做 Aone 或真实 canary 动作；未部署、真实 Runtime 工具调用与 IM/API/SLS/LF 验收仍由主代理接续。

本地完成结果：指定 `MULTICA_HANDLER_UNIT_TESTS_ONLY=1 go -C server test ./cmd/server -run '^$'` 返回 exit0 / `[no tests to run]`（仅 server 编译）；全部受影响 Go 文件 gofmt 检查和 `git diff --check` 通过。已核对复用日志为 16 项顶层 PASS、0 skip。冻结 P 的业务实现与源一致；router 仅保留已发布 eval 路由并追加 progress 路由，worker 只组合注释与 marker20，两处 reader 混版夹具改为旧19/组合20。已发布 steer 的 task-ref/legacy alias 文件无业务差异。本轮未运行这些夹具，不把旧日志当 reader20 行为的新执行证据。
