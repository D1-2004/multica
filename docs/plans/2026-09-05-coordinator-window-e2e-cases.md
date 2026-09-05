# Coordinator window e2e cases

Fixtures: `.agents/skills/scene-memory-e2e/references/scene-window-plays.md`  
Runner: `.agents/skills/scene-memory-e2e/scripts/run-window-plays.py`

Each round: `python3 .agents/skills/scene-memory-e2e/scripts/run-window-plays.py round --token <TOKEN>` then SLS + IM reread.

## Round log

| Round | Token | Pipeline 66 | Result |
|---|---|---|---|
| 0 | R5-4059 | 3106904059 / `99921f42e` | W5 not dropped; ACK collect_split; Host window_ack missed (display prefix) |
| 1 | E1-2007 | 3106904753 / `669d5a654` | W5 issue not dropped; ACK no LLM (C1 product pass); Host decided log missing (C2); WRAP reused old 竞业限制 reply (C8); W6 as 配角 ok; R9B Host no PAPER-A |

## Open cases

### C1 Host ACK vs DisplayContent
- Found: R5 W7 SLS `action=silence` via LLM, `reason` empty not `window_ack`
- Fix: `669d5a654` `stripInboundDisplay` + per-line `allAckText`
- Prove: E1 ACK Decide has `reason=window_ack` and no `inbound_coordinator_llm_request`, or Host-silence SLS row

### C2 Host-silence missing `inbound_coordinator_decided`
- Found: review of `99921f42e`; E1 ACK had collect_split and no llm_request, but also no `inbound_coordinator_decided reason=window_ack`
- Fix: `hostSilence` logs decided with reason
- Prove: next round SLS `reason=window_ack`

### C8 WRAP reused previous 竞业限制
- Found: E1 WRAP Decide `action=reply` 「刚才已经发到群里了」
- Fix: runner asks a unique 出差报销说明 + token
- Status: patched in runner

### C3 Parked ask + ACK must not drop the ask
- Found: R4/R5 mixed window silenced W5
- Fix: collect/absorb same-kind + KeepWorkUtterances
- Prove: `collect_split incoming_ack=true`; W5 token `action=issue`

### C4 Isolation R9A/R9B
- Prove: R9B Decide `look_into` / Host has no `{token}-PAPER-A`

### C5 Wrap-up already told
- Prove: after sandbox spoke on cid, `task_finished_loop_decided reason=already_told_scene`; no 已报群里

### C6 shouldReply / 处理中
- Prove: IM reread no `{"shouldReply":true}`, no stuck 处理中/处理失败

### C7 R9 isolation groups have no 冬翔
- Found: E1 W6A `OpendId is not in conversation` sending as 主角 to R9A
- Fix: W6 sends as 配角 (`AT_DXXH`); R9A/R9B members are dxxh + 测试号 only
- Status: patched in runner; prove this round with 配角 send
