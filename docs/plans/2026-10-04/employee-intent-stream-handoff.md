# 按需事项查找与首轮流式反馈交接

本轮实现及本地验证完成，具备集成条件；发布和真实产品验收未执行。目标是明确独立新Task不复述旧报告，并在需要查旧工作时由首轮模型自然反馈，再完成必要读取和最终答复。唯一当前状态入口为[实施Plan执行表](employee-intent-stream-implementation.md#当前执行表与交接2026-10-04唯一入口)。

## 版本与责任

- Aone独立分支：`feat/employee-intent-stream-feedback`。完整实现代码提交：`46cc61872c792162f20881098da21e53c211df51`。基线：统一发布源`eb62d0f06e7c92819c9076fb72541737d95ec980`，保留其steer、progress和评测能力。
- 主实现工作区：`/Users/yuanzhan/.codex/worktrees/employee-stream-feedback/dt-fde-multica`。其他session的主目录WIP未纳入。
- 发布接手方：「发布和验收」，线程`01a10278-0dd8-7a83-be4c-72fc351bbecf`；用户明确授权开发完成后交接。仅推本分支，不触发共享发布或修改Runtime。
- 工具协议候选reader21，新增迁移10040/10041。若统一发布还有其他在途协议，接手方统一marker且保留全部能力；禁止回退/覆盖当前reader20功能。旧Config工具、prompt与请求hash不改。

## 实际改动及原因

新输入先放可信REQUEST DECISION规则：先判断当前人的工作意图，再查旧事项。取消普通输入默认最近五项及成功报告；精确引用只保留Own元数据。模型需要旧工作时使用find_tasks，按当前场域本人匹配优先，零匹配才在observed group返回共享元数据；DM不跨人。动态refs进native journal，read/continue/stop/steer/builds_on重新校验source与权限。共享可读不授予控制或执行材料读取。

首轮沿现有模型/路由请求走SSE，完整公开first_feedback帧经Host提前入队；没有另一个反馈专用模型。业务工具仍等完整finish_reason、整批与参数验证。帧不含thinking或工具语法，receipt不表示Task/Run受理、结果或终态。Host只允许首次真实请求接受新帧，恢复已入账native call不重复。

反馈通知/outbox与journal同事务；专用running发送门复验来源、权限、quiet及final。Complete原子取消pending，reserve CAS避免被迟到发送复活；unknown/accepted按原查询不重发、无Router callback，不阻塞最终答复。通知存储失败是可选拒绝并可继续read；权威、lease、journal仍硬门。

参考GawkBot固定提交`71e82a1809565281cbd0bf8185d3c125b715d934`的RuleZero排序与公开输出/工具分离。PRI-101实例证明存在中间反馈，但不证明首模型流时序或真实取消；本实现据此保留独立final和可信执行receipt。来源说明见`server/internal/service/employeeloop/SOURCE_MAP.md`，架构合同见`docs/employee-loop.md`。

## 已验证与未证明

本地隔离PostgreSQL，provider为真实HTTP SSE受控夹具或发送替身；没有真实模型/IM/Runtime执行。本轮集成结果：

- Handler 29个顶层合同通过：新输入、优先层/Shared权限、独立dispatch、原Task控制、旧journal、memory/quiet受影响路径及流式→Journal→Host完整链。早帧时model response仍pending、find_tasks未执行；完整批次后读取；缓存恢复0额外HTTP；final取消未提交帧。
- `dingtalkresponse/modelregistry/employeeloop/llm/cmd/migrate`定向包回归通过，共130个顶层合同；新增流式provider授权测试后的modelregistry最终17个顶层通过。计数有包重叠，不相加制造总用例数。
- Loop/SDK race通过，server与migrate构建通过。全迁移在空独立库完成，新feedback唯一索引回读`indisvalid=true, indisunique=true`；注册INVALID索引清理hook。
- SPEC/EVALS定义检查及`make eval-check`通过：新增6个风险用例，固定20 P0与既有ID保持。新例的runner可用性和真实运行另由接手方确定。
- 独立只读审查发现首physical恢复门缺口，已修并用未接受/已入账恢复两子例复验；修后审查无剩余阻断。

证据目录：`/Users/yuanzhan/d1/employee-e2e-evidence/EMPLOYEE-INTENT-STREAM-INTEGRATION-20261004/`，以`handler-final.log`、`modelregistry-final.log`、`packages.log`、`stream-race.log`、`build.log`、`migrate.log`、`eval-check-final.log`及`cleanup-readback.log`为最终结果。早期`handler.log`是producer资格夹具未匹配导致的失败过程，保留但不作为最终结果；早期modelregistry配置revision夹具错误已修。

三个独立库`employee_intent_stream_integration_1004`、`employee_first_feedback_1004`、`employee_task_discovery_1004`已删除，pg_database联合回读0。无routine暂停、账号切换、共享环境或Runtime更改；无后台测试残留。

## 发布后的最短验收

接手方先按当前manifest集成本分支，检查全live reader门、迁移、实际模型的native streaming及include_usage支持，再在已获准原场域依次验证：

| 场景 | 一次性核验要点 | 不通过反例 |
| --- | --- | --- |
| 原NEW-TASK失败原话 | 保留原请求人/场域/旧成功History，明确另开蓝杉；新Task/Run绑定当前source，Python实际sleep及计算，旧Task不变，真实结果一次送达 | 只给旧12秒/55、口算、继续旧Task、清History换群 |
| 本人查询与续接 | 小甲较早相关事项、小乙更近同名项、小甲无关项并存；查找当前scene+requester，必要read后答/续接；方法继承 | 按最新选小乙、无fresh read宣称当前状态 |
| 共享及DM隔离 | 本人相关目标零匹配才group共享；仅goal/state/time，控制/history/builds_on拒绝；DM不跨人 | 私有标记/结果泄漏、可读即取得修改权 |
| 自然反馈→工具→final | 单一人来源首generation公开完整帧，原请求HTTP次数无额外反馈轮；早入队及实际IM、必要工具、final分别取证 | 固定机械ACK、thinking/工具参数外发、首句充完成、final被抑制 |
| 直答/格式/安静/直接受理 | 精确数字或JSON及直接dispatch/stop/quiet不加首句；quiet恢复沿原权限 | 额外preamble、重复接单、未恢复权限就外发 |
| 故障/恢复 | 隔离provider截断与非法batch零业务效果；重发首请求不新建帧，已入账重放唯一；pending/final竞态取消，unknown查询无callback | 半参数执行、迟到pending复活、unknown重发、关闭原callback |

每例同时核用户效果、来源/Task/Run、实际工具、上下文来源、权限及副作用；IM、API/journal、SLS、LF分别证明相关结论。首句存在不证明更快，定义检查不证明模型选择，Python答案正确不证明实跑。

保留限制：query仅goal字面匹配；普通History中的事实选择仍是独立模型质量问题。本批未改通用History事实选择或其他记忆/提醒范围。已reserve或外部提交的反馈不能保证撤回，第三方可能晚于final送达。原NEW-TASK真实失败仍待原条件复验，不随本地检查改为通过。跨日、多真人或长等待沿用户既定验收边界处理，不为这两项扩成新发布循环。
