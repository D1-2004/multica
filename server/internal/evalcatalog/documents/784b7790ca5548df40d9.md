# M8 首轮真实群摄入与主动接话证据

2026-10-04，预发生产代码 `a4c3aa4dab`，Qwen-Real revision 11/runtime461aabb2。本轮本地 retry 修复尚未部署，不作为此次线上版本。

## 前置与执行

Director 与 dxxh 以 Real Niubility profile 读取自身身份并刷新认证，所有 DWS 使用私有线上网关配置。`group_p_hx` 的 cid 为 `cid9pVqaL7ehXiznGozdw1ydQ==`。原群只有介绍卡，无服务端 scene；真实 Director @ 入站后由正常目录解析建立 `3c814f4d-5edc-4f2b-8d76-1f76d3df40b7`，没有按 cid 手工造场域。

- 00:27:39 @ 确认可见，00:27:45 员工真实回复。
- 00:29:58 Director 未 @：“本场候选编号有 DX7、BR3、CX9。”
- 00:30:07 dxxh（群显示 SixSix）未 @：“回执：DX7 已收到；CX9 已收到。”
- 00:30:14 Director 未 @：“值班核对：本场候选中，漏了哪一项回执？只回编号。”

## 分面结果

| 面 | 实际证据 | 结论 |
| --- | --- | --- |
| 环境 | SLS 00:26–00:36:20，两 pod 无新 server starting；fence 两副本 live、loop15/memory1/2/3 | valid |
| 摄入 | 三条 plain group_all received，observation stored；00:31:30.299 proactive admitted，waited_ms76131 | pass：没有把未收到事件冒充 quiet |
| 来源与执行 | Receipt `ddd3e420-fb57-4a0f-b16c-41ec5228142e`，job `dcbce175-ffac-46b3-9eff-dcd02d202c39`，completed reply、run_ids 空 | pass：同来源，未派后台 |
| 模型 | LF `dcbce175ffac46b39effdcd02d202c39` 的 employee_model，一次 generation；冻结历史含两位真人材料 | pass：真实上下文和原话，而非模型自述 |
| 投递 | 同源 response action delivered/ack；IM `msgeKtCkWztHCTZMTQ436tFsg==`，00:31:32 Qwen-Real 真实单条回复 | pass：真实效果 |
| 严格格式 | 回复为 `BR3` 后加“候选是 DX7、BR3、CX9 三项...所以漏的是 BR3” | **fail：违反只回编号** |

DS-09 整例为 fail，不能用答案正确、单条消息或摄入通过抵消格式失败。没有清历史、换演员或改题意重跑。下一轮针对模型输出约束保留这个原反例，不用 Host 编号正则截断伪造通过，也不把 ACK/介绍卡计入回答。

## 证据与未证明项

持久目录：`~/d1/employee-e2e-evidence/CODEX-RESUME-20261004-M8/`，含 manifest、identity、groups/registered-scene、逐步 UUID/messageId/readback、真实 IM、LF 原始 trace、SLS 命令/固定窗/结果和 verdict。

SLS 单次 size100/offset0，返回40条未触顶，无后续分页；CLI 没有 total/scan-complete 元数据，保留这个完整性边界。没有预发配置写入、未改常驻 routine。

MEMX-L1 的较早原话召回、M5 隔离/忘记/混版、MF flush 仍未验证。本次不算“群聊 Golden9 已通过”或“R5 完整实现”。
