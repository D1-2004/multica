# 东翔测试号真实沙箱验收

状态：真实 FC 沙箱执行、发送状态、正文/引用回读、无重复消息、策略强制与热任务刷新通过；客户端角标展示待观察。五条任务均完成，测试员工配置已恢复。用户取消本机 Daemon 实机验证，明确改用东翔测试号在真实沙箱测试，本报告不将 FC 结果等同于本机 Daemon 矩阵通过。

## 对象与范围

- 环境：Multica 预发；Workspace `d4f9ceed-d114-4312-bb30-dd791aee039b`。
- 员工：预发测试智能体 `e2293e9e-1e79-4926-b0e6-da4cb693add0`；DWS 身份为 Think测试组织 / 东翔测试，消息发送者为东翔测试号。
- 候选 Runtime：`c59f8a53-6590-4c1b-b959-866b8f728af7`；Template `pz27zf41o5r2vz53plfa`；Runtime commit `5a3643030765c82dfa2e4908a23cd9c408494d79`；Multica commit `819fc10e7026a68c96c7ad73dbf29d004f007584`。
- 新测试群：响应动作验收-20260907-EAST-5a3643，`cid2xz9UsZKgLNyhz7dhRKd1Q==`，仅东翔测试号本人，未加入其他人。
- 通过 Web Coordinator 创建只读核验事项 WS-233 和发送验收事项 WS-234。后续轮次用 WS-234 评论触发同一员工，保持事项与热会话归属。
- 新平台响应模式未启用，任务快照的 `platform_managed_lifecycle=false`。本轮验证真实沙箱外发及策略刷新；不覆盖尚未部署的 Router 新接待/清理链路、服务端响应 worker 或受管发送账本。

## 已取得的独立证据

| 轮次 | Task ID | 实际证据 |
| --- | --- | --- |
| 身份与版本 | `7ef814a4-95d1-43fe-b627-6931cf6dbec2` | 沙箱 manifest 中 Multica commit 一致，声明 `dws_message_policy_v1`；terminal 读取策略为 false；DWS get-self 成功且身份一致。 |
| 关闭策略、冲突参数 | `1377151b-f1d7-4ae1-88fe-e53f6d8824e9` | 新建单人测试群；保留 true/false/true 冲突参数发送 OFF-01；发送状态 SUCCESS；原子消息回读正文逐字一致。 |
| 开启策略、普通发送与引用回复 | `088bb757-a247-4dd3-9594-6fc8c4676d8c` | 下一任务策略 true，Task ID 更新；读取相同哨兵 `d9d8d479be8b2f13`，沿用 workdir 与 provider session；ON-02、REPLY-ON-03 均 SUCCESS，回读正文及引用来源正确。 |
| 开启策略出站参数预览 | `5b81ccb6-bb3b-495b-a06e-cc08421943e7` | 正常 wrapper 在 true 策略下将 false/true/false 冲突参数强制改为 `clawType="openClaw"`，正文和目标保持一致；`dry_run=true`，未实际发送额外消息。 |
| 切回关闭、原子与遗漏参数 | `5d7ecd92-2bf2-4763-aff3-af0b10cb4b44` | dry-run 确认原子 send 将故意传入的 true 覆盖为 `clawType=""`；快捷入口遗漏参数时不含 `clawType`。两条实际发送状态 SUCCESS；测试群完整回读共五条预期探针、各一条，无异常；沙箱哨兵文件已删除。 |

同一事项沿用 provider session `0aac7fa0-8d42-48f2-a748-7e86a5aa3a46`。五条消息分别以不同稳定幂等键发送，仅执行一次。真实 query-send-status 返回 `result.sendStatus="SUCCESS"` 及实际会话/消息 ID，与当前 Multica 解析合同一致。最后全量读取新群返回 `complete=true`、`hasMore=false`、五个探针各计数一、`ANOMALIES=[]`。

| 探针 | Message ID | Provider openTaskId |
| --- | --- | --- |
| OFF-01 | `msgBOiQYtNehBJF2yiRmuc3XA==` | `Y4PN6uY5s37obd3avDBlHeiXmFGkFPhbDghcv7Leto0=` |
| ON-02 | `msgj3pmhnYjkGLDMs1rQDPmWg==` | `gJlcwdc40Mds2FCYPT/YMz0h/haiGpGzVUDVdRjlGvY=` |
| REPLY-ON-03 | `msgT3Pt3Ks855t3aAru4uARKA==` | `suYeuCBir6VSXfsCoqaQKilCk6HTUv3Mnn2BdqSTLxQ=` |
| NATIVE-OFF-04 | `msgQW1QSJVqNi9RFFlTjbzt3A==` | `mPisBVwW1qogQvUls1Kqvm5539wCQIk4nNUvLY+SG6w=` |
| OMITTED-OFF-05 | `msgjHaK6ZOTO7QicQJlw/uf0g==` | `FCa/27kNdKZJHGdLXXAgiFUgK+RzaJPatm5ABqDDib4=` |

## AI 标识证据边界

三个实际原子消息回读均为 `messageAiSendFlag="DWS"`。固定版本 DWS 的快捷消息投影会省略扩展字段，故采用 `chat message list-by-ids` 原始回读。即使原始字段存在，也不能将来源字段当作客户端角标显示状态。

DWS `v1.0.60-beta.1` 的公开语义是 `--ai-tag=false` 不展示 AI 角标。源码中原子发送关闭时显式传 `clawType:""`；快捷发送关闭时省略 `clawType`；开启时由 `edition.ClawType()` 生成。当前本地可见源码没有 `messageAiSendFlag` 到客户端展示的映射，已请用户观察三条实际消息的客户端角标；未收到观察结果前不宣布视觉验收通过。

真实沙箱 dry-run 证明程序侧策略改写符合上述 DWS 合同：true 输出 `clawType=openClaw`，false 的原子入口输出空串、快捷入口省略该参数；原来的冲突参数和遗漏参数均受控，正文中的相似字符串保持原文。这里的 `clawType` 就是发送参数，不需要猜测额外的 `aiTag` JSON 字段；沙箱员工关于扫描器可能漏掉 `aiTag` 的自述未作为结论采用。

## 恢复与遗留

- 五条任务均已完成，最后一条于 2026-09-07 18:59:54 +08:00 完成。
- 已恢复原 Runtime `48ac8d56-8c72-4a11-8f8d-ee5a27c28635`、`chat_session_resume=false`、`dingtalk_show_ai_tag=false`，并通过 API 读回逐项验证。策略修订号按真实切换单调前进 `1 → 2 → 3`，未回退修订号。
- 测试群与五条探针保留供客户端核验；沙箱本次哨兵文件已删除并确认不存在。其他原配置与本机 DWS 环境未调整。
- Router SQL 024–029 仍需要迁移连接配置，完整响应链路与 P50/P95 待 Router 部署后验证。
- 本机临时证据 `/tmp/response-sandbox-test-state.json` 仅记录该实验的任务/工具结果与原配置，不含凭据；不提交原始推理或个人资料。
