# Coordinator 场景窗：预发固定场景

合同：`docs/plans/2026-09-05-coordinator-scene-window.md`。  
记忆召回仍走 `docs/plans/2026-09-02-coordinator-scene-memory-e2e.md`（P1–P11）。  
本页是 **W 窗 / wrap-up** 的可复用场景资产：cid、演员、@ id、过线、怎么重跑。不要每次另造群，除非隔离证明需要新 cid（W6 / P7）。

dws 保持预发。演员只有 冬翔 / 东翔测试号 / dxxh。不用菲迪。

## 固定场景

| 资产 | cid | 用途 |
|---|---|---|
| G1 场域测试群 | `cidShE01n1lg8XTpJFW5oknDw==` | wrap-up、W2 连发、记忆口径 |
| G2 冬翔测试群 | `cid52dllVmkRJLpUZPwxi0jtw==` | W3 双委托人、W5 第三事项停驻、W7 ACK |
| R9A | `cidcfObvQakjFCMc4c2I5o34Q==` | 隔离 A（minutes / 口径不串） |
| R9B | `cidwybOKJur9YbbjyLpjnbPaA==` | 隔离 B |
| DM 冬翔↔测试号 | `cid+bEFv7ngm9n79Q1vL9HYJw==` | P1 记忆；禁止 `+dm --to 东翔测试号` |

群里 @ 测试号必须同时：`--at-open-dingtalk-ids` **和** 正文 `<@同一个id>`。id 按发送方：

| 发送方 | 角色 | 测试号 openDingTalkId |
|---|---|---|
| 冬翔 | 主角 | `DIBwz3Bm4ugAGaaIaZvSXyAiEiE` |
| dxxh | 配角 | `Dl2XMiS9sbxHgSb1GMrRWVz6DHXBFkLb6iP` |

换群先 `+chat-members-list` 再抄 id，不要跨群复用。

Agent：`e2293e9e-1e79-4926-b0e6-da4cb693add0`，workspace `sombrero-galaxy-zleb`。预发二进制以流水线 66 最近一次 预发部署 SUCCESS 为准。

## 过线（W 窗）

探针用 ASCII token（如 `W5-ASK-4059`）。SLS 乱码 CJK，不要用中文当唯一证据。

| ID | 怎么演 | 过线 |
|---|---|---|
| W3 | G2：冬翔一项、dxxh 另一项。要打满 2 槽就间隔 >4s | 两个 `action=issue`；`item.delegator` 不串 |
| W5 | 2 个沙箱在途后再 @ 第三件（高铁/订票类，带 token） | `inbound_coordinator_job_parked` reason 含 `two in-flight`；槽位空后 **同一 token** `action=issue`，不得 `silence` |
| W7 | 停驻之后连发 谢谢/好的/收到/嗯/行/辛苦了/不用回了/没事 | `inbound_coordinator_job_collect_split incoming_ack=true`；ACK 窗 `action=silence`；真事项 token 不进 ACK 的 `current_message`；IM 不多回一句；无 处理中/处理失败 |
| WRAP | G1 @ 写一份说明发到群里 | 沙箱先发；`task_finished_loop_decided reason=already_told_scene`；无「已报群里/已发到群里」二刷 |
| W6 | R9A 有材料，R9B 要「我们群的纪要」 | R9B `look_into` 是 B 的 cid；Host/IM 不出现 A 的探针 |

W3 同一 4s 窗两条（两个 items）依赖发送延迟；dws 间隔常 >4s，会变成两个窗。那正好用来打满 W5 的 2 槽，不算 W3 失败。

## 重跑

```bash
python3 .agents/skills/scene-memory-e2e/scripts/run-window-plays.py status
python3 .agents/skills/scene-memory-e2e/scripts/run-window-plays.py w3a --token W5-ASK-$(date +%H%M)
# 等 6s
python3 .agents/skills/scene-memory-e2e/scripts/run-window-plays.py w3b --token ...
# 等两个 issue 在途（SLS decided）
python3 .agents/skills/scene-memory-e2e/scripts/run-window-plays.py w5 --token ...
python3 .agents/skills/scene-memory-e2e/scripts/run-window-plays.py acks
python3 .agents/skills/scene-memory-e2e/scripts/run-window-plays.py wrap --token ...
```

证据：钉钉回读 + `scripts/query-coordinator-sls.sh --env pre --cid '<cid>'`。召回仍只认下一轮 `user_prompt`。

## 已用此资产验过（预发 66）

| 日期 | 流水线 | SHA | 结论 |
|---|---|---|---|
| 2026-09-05 | 3106904059 | `d6eeda741` / `99921f42e` | W5 高铁停驻后 issue，ACK `collect_split`，token `R5-4059-W5` 未丢；无 shouldReply 透出；无 处理中卡住。G1 wrap-up `already_told_scene`。ACK 窗当时走了模型再 silence（Host `window_ack` 对展示文案前缀不稳，后续已补单测）。 |

[capture] 教训 | 停驻的下一窗不能把谢谢/好的并进真事项，否则模型整窗静默会丢掉第三件 @ | 预发 G2 R5-4059 / SLS collect_split + W5 issue | 2026-09-05
[capture] 资产 | G1/G2/R9A/R9B/DM cid 与冬翔/dxxh 侧 @ 测试号 id 是 Coordinator 窗与场域记忆的固定预发夹具 | 本页 | 2026-09-05
