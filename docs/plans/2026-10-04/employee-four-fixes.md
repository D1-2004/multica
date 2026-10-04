# Employee 四项局部修复（冻结范围）

用户2026-10-04授权：只修新任务归属、遗忘遗漏、提醒事实、持续安静（文字称三项，列出四个行为，按枚举执行）。不包括SLA、Runtime、MF纯记忆、完整R5；不重启上一轮验收。

## 工作流与参考

主代理集成，开发者独立worktree，保护主checkout及其他session。基线45605cff04，功能基线f4c4790/epoch17；上一轮发布结论和证据保留。本轮默认本地修复、限定验证和交付；上一轮禁止新增真实测试/部署尚未解除，本轮不进行新的真实IM、构建镜像或部署。
参考本地 `/Users/yuanzhan/github/gawkbot@71e82a1809565281cbd0bf8185d3c125b715d934`。采用其prompt/tool语义选择、事实工作包、持久化控制模式；不新增关键词派发、全局记忆扫描或进程内暂停状态。每项在实现前补具体source/function映射及适配原因。

## 冻结验收清单

| 项 | 原失败 | 修复与有价值的验证 | 责任 |
| --- | --- | --- | --- |
| 新Task | 明确开独立事项仍continue旧Task | 语义合同优先级；对比新独立/重做原任务/依赖旧结果，保留Python继承；Task/Run/source权限不弱化 | task-facts worker |
| 遗忘 | TaskWake缺MemoryPrincipal | 与brief共用可信DM/nonautomation本人资格；历史剔除本人墓碑，其他owner不读；新输入测试，旧冻结snapshot不改写 | privacy worker |
| 提醒事实 | 实际催过却说没催，>33min却说半小时内 | 持久化reminder/send/answer时间进入有界可信snapshot；未知明确标注；已答抑制和配额行为不改 | task-facts worker |
| 持续安静 | ACK后机器人quote仍触发回答 | 先参考Gawk控制语义并明确持续状态/授权解除/场域范围；落数据库，保护取消恢复和幂等；机器人不能解除 | root |

每例同一检查表：用户效果、来源/Task/Run绑定、实际工具、模型上下文来源、权限、副作用、幂等、取消、迟答、恢复。只验证受影响路径；使用独立DB，不碰预发/共享业务数据。真实E2E未跑与代码/本地测试通过分别记录。完成回填证据、提交、开放限制并更新唯一执行表，不将旧失败自动改绿。

## 当前状态

已完成四项本地修复、集成及限定验证；未部署/真实IM。本轮结果与开放边界见[交付报告](employee-four-fixes-report.md)。14个集成定向测试通过，最终安静和Loop复核通过；三独立DB清理回读0。旧snapshot、Router callback提交前和真实模型语义限制保留，不扩大处理。


## 预发部署与短E2E（用户本轮新授权）
用户要求部署后简单E2E，解除此前这四项禁止部署/真实IM的边界；不授权全R5/Runtime切换。按用户当前主工作区docs/development-delivery.md和Employee领域操作合同执行（交付树尚无新通用入口文件，读主工作区现行合同，未复制其他session WIP）。
发布唯一主代理；实际remote源feat/tag-multitenant需核对，运行目标app342160/pipeline66，最新run占用先读。限一次发布；外部发布约20分钟检查点，测试约20分钟，无复杂等待；新发现登记，原失败不自动重新开发/发布。
每例依次检查用户效果、source/Task/Run、真实工具、输入来源、权限/副作用。固定RealNiubility employee33af235e、tenant44675729、runtime461原配置，以当波API回读为准；DWS使用进程私有prod网关，Actor带显式profile，不改全局。发布必须release含四项代码、live各副本新启动/epoch18、fence normal才开始。
1. 新事项：原蓝杉场域明确独立新开，同题旧Task不能continue；真实Python stdout55和实际交付同时验证。
2. 遗忘：短测试私人事实capture→DM回读→精确forget→新TaskWake汇总，GEN输入旧值/派生旧答不复活。不得把history unavailable当通过；记录新本地修复不治理旧snapshot限制。
3. 提醒事实：短有截止收集，两真人回答后汇总；process_facts、答复时刻、真实IM结果和叙事一致。若没有实际提醒，不能签真实催促后叙事通过；最多短等待，不推进30min剧本。
4. 安静：原G08群，发起者要求保持安静→异账号引用/@（完整短窗口无回复，Job Quiet/0模型）→本人恢复→重新回应；收尾确保active。
不变更routine；若必须临时pause，先存原值、结束恢复并GET回读。仅精确清理本轮新memory/collection，不抹原失败历史；控制active回读，演员租约释放。IM/API/SLS/LF分面保存EMPLOYEE-FOUR-FIXES-PRE-20261004并逐项更新唯一表。


## 部署与短E2E最终回填（10:30）
已一次发布source11d6eb5061/release74802f1829/run3110373861，10:00:29成功，两live epoch18；真实短测完成，见[报告](employee-four-fixes-pre-report.md)。新Task原场域FAIL，TaskWake过滤/短收集/安静恢复已证；记忆lookup与删除确认仍不一致。业务消息10:25停止，恢复/精确清理完成，不继续自动修复或发布，不签实际催促/全R5。
