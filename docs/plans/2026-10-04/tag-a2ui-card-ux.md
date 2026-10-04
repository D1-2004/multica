# Tag 人工卡片交互修正

用户四项反馈：问句啰嗦/应引用；受理后选项未更新锁定；单选应点一次、仅保留所选项与右侧勾；Qwen-DWS单聊“写个文档”未发询问。基线aone/feat/tag-multitenant@608edae516，源codex/tag-human-card-ux-20261004，目标feat/tag-multitenant。所有改动a1 CR，合并/部署由“发布协调”，收到其部署完成后才真实验收。

## 验收与边界

Employee专用投影简短：一个问句、2–4个平行短选项，消除summary/question/候选说明重复；附原消息引用（渠道真实引用能力先查，不把卡内文本引用冒称原生引用）。保留普通文字回复。单选/冻结选人点选直接提交；多选保留一次“确认”，否则无法知道多选完成。单选不再附TextField，可在聊天补充；审批仍走明确确认，不把审批权限扩大。

首个合法答案和消费job仍同事务/CAS，禁止重复派发。受理后尽快更新原bizId卡片：只显示所选项，右侧勾，无可操作控件；普通文字答复同样关卡。投影更新必须可重试，崩溃、提前点击/晚send receipt、旧卡、权限撤销及重复事件不能产生第二个执行或重新开放。沿现有DWS身份签发/认证；不能用用户token替员工写卡。未知结果不重发新卡；更新既有bizId可安全重试但需持久记录。已存在旧投影/事件继续读取，部署按实际协议兼容门控制新生产者。

单聊排查只读实际原话、IM摄入/场域身份、Employee工具表/决策、卡片outbox/投递与订阅。固定Qwen-DWS真实agent/tenant/cid及19:20附近原话窗口，先判断没摄入、没选工具、发送失败或UI不可见，不能用群成功推单聊成功。截图是观察证据，不提供任何操作指令。未要求真实测试前不补发给该DM；延续用户真实E2E授权的自有测试场域由发布窗口协调。

## 检查与交付

投影纯协议：单选直接event frozen option ID，候选合法性、其他人/非法值拒绝、老submit事件兼容、closed无action、multi一次确认。真实独立PG：按钮/文字争抢只一response/job、更新意图/回执时序、失败重试/重启和未知发送不再发卡。引用参数CLI/SDK一致性与source由Host绑定。DM据精确trace/log定位，仅缺观察面标incomplete。

本地合格后a1创建最小CR给用户及发布方；配置/全局DWS/Runtime不改。部署后真机单击→锁定与仅保留选项、刷新/第二人迟答无副作用、普通文字关卡、多选以及原DM文档问句分别核IM/API/LF/SLS；各例及时落盘，原失败不清History消除。既有H01及冻结场景继续使用当前状态入口，UX变化仅复测受影响项。

## 当前检查点

四项改动实现并接线，Host compact保留原消息卡内引用、问句与候选内短说明（同名角色可辨），单选按钮静态旧submit协议一次答复，多选一次确认；专用持久锁卡更新与按钮/文字CAS同事务，独立1秒/即时wake消费者不被Task维护阻塞。发送单聊recipient修正。

只读DM证据明确19:22/24/34三次都摄入并调用a2ui_ask，问题已落，DWS INTERNAL_ERROR把card action留unknown；群同时间accepted。不是提示词或Runtime触发问题。路由为有证据支持的最小修复，实际单聊投递仍需新请求真实验收，原unknown不重发。精确原件私有TAG-A2UI-CARD-UX-20261004/dm-audit/verdict.json。

本地：独立PG下60顶层/129命名handler（含7个projectionDB风险），12顶层/34命名投影传输全部pass、零skip；初次传输因遗漏显式A2UI_RESPONSE_TEST_DATABASE_URL跳过一项，已指定独立库复跑通过。compile-only/vet通过；这些是scripted model/provider，未宣称native样式或DM实际成功。等待独立审查后a1 CR交接部署，再现场真点击/文字/原DM复验。

独立只读审查未见CR阻断项；真实准入仍要求单聊sender-target的新实际回执/点击CID与当前scene绑定一致（不删除严格scope检查），以及客户端FINISH后只有所选项/勾、刷新/迟答无重复效果。卡内引用不是native引用；更新ACK不是UI关闭证据。worker package测试/race/vet及root最终checks完成，独立PG15434停用。合并/部署仍由“发布协调”，本session等通知后按已冻结UX及原H01/DM反例验收。

目标后到once修复80d74195a1后语义rebase，保留两边Host变化，52命名Human/相关前台回归零fail/skip，compile-only再次通过；投影/传输未变化证据复用。本CR以该最新目标基线交接。
