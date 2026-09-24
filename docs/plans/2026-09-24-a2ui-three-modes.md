# A2UI 关闭 / 所有人 / 指定名单

## 用户目标
用三选一替换 A2UI 总开关与始终显示的名单，清楚说明三种行为；仅指定名单模式展示姓名输入。增加真正的所有人模式，不靠空名单或特殊姓名表达全量。

## 设计
- API mode：off / all / named。现有 enabled bool 与名单为旧客户端兼容边界。
- 持久化保留 enabled bool，加 audience=all/named；mode 由二者推导，避免多份权威状态。
- 旧配置默认映射 off/named；新所有人模式必须显式选择。用户已确认目标智能体最终模式为关闭（off），上线后应用。
- all 跳过姓名放量条件，仍保留数字员工渠道、原作者ID核验、单作者收集窗和回调幂等；off沿用普通处理，named完整姓名匹配。
- 关闭入站判断关闭选择模式；调整模式不影响已冻结卡片。

## 分工
- backend：migration、SQL/sqlc、API映射/验证、Host/collect三模式判断及针对性测试。
- frontend：core schema/types、共享三选一UI、名单显隐、四语言、兼容与交互验证。
- 根任务：合同/案例同步、集成审查、预发发布、API与浏览器验收。

## 验收
- 三模式读写、非法值、旧API兼容、局部更新保留和policy revision。
- all允许未知姓名但不扩大身份授权；named仍拒绝非名单；off不发卡；多作者分窗。
- 前端只有named显示名单；保存失败恢复、只读禁止编辑。
- 预发部署确认精确revision和健康；在指定智能体页面验证选项与名单显隐，按用户确认选择最终模式。

## 进度
已完成实现与预发发布，目标智能体已设为关闭。

## 本地验证
- core 接口/兼容测试62项、设置组件与父页集成36项通过；core/views typecheck通过。
- Host三模式/姓名/作者/collect/API参数映射窄测试通过；server build、handler go vet与两个sqlc一致性检查通过。
- 新增三项数据库集成验证：mode往返及revision、非法/矛盾参数不可部分写入、并发局部更新不丢字段。未连接真实数据库运行，不将跳过记为通过。
- 当前浏览器空间由用户控制，工具要求停止页面操作；不擅自接管或绕开。UI以组件交互测试验证，部署后通过API确认目标mode=off。

- 交叉审查修复：mode/names保存不再由父页面提前乐观落缓存；服务器成功后更新，失败保留名单草稿。真实子组件+QueryClient集成验证通过。

## 预发交付结果
- 功能提交 `80daa7aeb5e7ecab9036c449fda361750b533e2c`；发布合并 `64c68ef5d`。
- run `3109768297` 构建、预发部署、集成测试均SUCCESS；deploy `161907060` SUCCESS，allEnd=true；人工预发验证关卡保留。
- 9306 migration由发布入口执行，bootstrap回读为already applied；未手工执行迁移。
- 目标 `e2293e9e-1e79-4926-b0e6-da4cb693add0` mode=off，旧enabled=false，revision14；保存后详情和列表均回读一致。非法值、null、mode/legacy bool冲突实际返回400，原配置不变。健康检查 /health、/healthz 均200。
- 本地直接数据库回滚探针进程被终止，未取得SQL三模式实库往返通过证据；不替代前述未运行的3项数据库集成测试。应用API实际保存/读取通过与此分别报告。
- 未操作用户已接管的浏览器；三选一、名单显隐、失败草稿及服务器确认通过组件/父页集成测试验证。未运行新的真实IM发卡/点击闭环。
