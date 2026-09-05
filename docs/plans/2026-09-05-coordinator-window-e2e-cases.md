# Coordinator window e2e cases

Fixtures: `.agents/skills/scene-memory-e2e/references/scene-window-plays.md`  
Runner: `.agents/skills/scene-memory-e2e/scripts/run-window-plays.py`

Each round: `python3 .agents/skills/scene-memory-e2e/scripts/run-window-plays.py round --token <TOKEN>` then SLS + IM reread.

## Round log

| Round | Token | Pipeline 66 | Result |
|---|---|---|---|
| 0 | R5-4059 | 3106904059 / `99921f42e` | W5 not dropped; ACK collect_split; Host window_ack missed (display prefix) |
| 1 | E1-2007 | 3106904753 / `669d5a654` | W5 issue not dropped; ACK no LLM (C1 product pass); Host decided log missing (C2); WRAP reused old 竞业限制 reply (C8); W6 as 配角 ok; R9B Host no PAPER-A |
| 2 | E2-2024 | 3106906434 / `6c51b3544` | C2 `reason=window_ack` ×3 no ACK llm_request; W5 park then issue token kept; WRAP 出差报销 issue+IM; W6 配角; wrap-up already_told_scene; no shouldReply |
| 3 | E2103-loop | 3106906434 / `6c51b3544` | W5 park (`two in-flight`) then `action=issue` token kept; ACK `window_ack` ×3 no ACK `llm_request`; `collect_split incoming_ack=true`; W3B issue / W3A reply-reuse 排期; WRAP reply reused E2-2024-WRAP (C9); W6 配角, R9B cid no R9A cid; wrap-up `already_told_scene`; no shouldReply; W2B 处理中 stuck (C10); W5 inbound 处理失败 (sandbox 订票) |

## Open cases

### C1 Host ACK vs DisplayContent
- Found: R5 W7 SLS `action=silence` via LLM, `reason` empty not `window_ack`
- Fix: `669d5a654` `stripInboundDisplay` + per-line `allAckText`
- Prove: E1 ACK Decide has `reason=window_ack` and no `inbound_coordinator_llm_request`, or Host-silence SLS row
- Status: proved E2/E3 (`window_ack` ×3, no ACK `llm_request`)

### C2 Host-silence missing `inbound_coordinator_decided`
- Found: review of `99921f42e`; E1 ACK had collect_split and no llm_request, but also no `inbound_coordinator_decided reason=window_ack`
- Fix: `hostSilence` logs decided with reason
- Prove: next round SLS `reason=window_ack`
- Status: proved E2-2024 (`reason=window_ack` ×3, no ACK `llm_request`)

### C8 WRAP reused previous 竞业限制
- Found: E1 WRAP Decide `action=reply` 「刚才已经发到群里了」
- Fix: runner asks a unique 出差报销说明 + token
- Status: 竞业限制 reuse gone by E2/E3; same-type 出差报销 still reused (see C9)

### C3 Parked ask + ACK must not drop the ask
- Found: R4/R5 mixed window silenced W5
- Fix: collect/absorb same-kind + KeepWorkUtterances
- Prove: `collect_split incoming_ack=true`; W5 token `action=issue`
- Status: proved E2/E3

### C4 Isolation R9A/R9B
- Prove: R9B Decide `look_into` / Host has no `{token}-PAPER-A` from R9A
- Status: E3 R9B cid only (no R9A cid). Decide was `reply` not `look_into`. Host scene_memory contains PAPER-A from **this cid's** W6B asks, not R9A

### C5 Wrap-up already told
- Prove: after sandbox spoke on cid, `task_finished_loop_decided reason=already_told_scene`; no 已报群里

### C6 shouldReply / 处理中
- Prove: IM reread no `{"shouldReply":true}`, no stuck 处理中/处理失败
- Status: shouldReply clean E2/E3; E3 still has W2B `处理中` (C10) and W5 `处理失败` stamp

### C7 R9 isolation groups have no 冬翔
- Found: E1 W6A `OpendId is not in conversation` sending as 主角 to R9A
- Fix: W6 sends as 配角 (`AT_DXXH`); R9A/R9B members are dxxh + 测试号 only
- Status: patched in runner; proved E2/E3 配角 send (R9A/R9B ok)

### C9 WRAP reply-reuses previous-round 出差报销
- Found: E2103-loop WRAP Decide `action=reply` 「出差报销说明之前已经写好发到群里了（token E2-2024-WRAP），需要我再发一份新的吗？」; no new issue; wrap-up sandbox not exercised
- Fix: runner WRAP deliverable includes `{token}` in the title (`差旅住宿清单`) so assoc cannot match last round's 报销
- Prove: next round WRAP `action=issue` (or sandbox send) with this token; IM is the new 住宿清单 not a reuse question; no 竞业限制 / previous WRAP token in the reply

### C10 W2B `处理中` stuck after issue_busy retry
- Found: E2103-loop G1 W2B (`就约线上`) Decide `action=retry` ×6 `reason=issue_busy_park` on issue `049c6f99-…`; wrap-up later `already_told_scene`; IM still has emotion `处理中` on the W2B inbound (E1-2007-W2B still stamped too)
- Fix: Host/channel should complete the DingTalk silence callback (clear 处理中) when `ActionRetry` parks or when wrap-up silences the same cid; do not leave the confirmation @ spinning
- Prove: next round W2B has no leftover `处理中` after the meeting issue settles
- Status: not a one-line window_ack bug; do not ship this fire

## Round 3 detail (E2103-loop)

Pipeline 66 `3106906434` 预发部署 SUCCESS, SHA `6c51b3544`. dws pre. Actors 主角/配角 only.

| Check | Result |
|---|---|
| W3 | W3B `action=issue` token kept. W3A `action=reply` on existing 排期 issue `debb2ae0-…` (催 dxxh), not a new issue |
| W5 | job `04ca3ac8-…` parked `reason=scene already has two in-flight matters` ~21:04:36–21:06:05, then Decide `action=issue` look_into `scene_cid=cid52dllVmkRJLpUZPwxi0jtw==` text 「我去处理下周去上海的高铁订票」 |
| collect_split | 21:04:35 / 21:04:40 / 21:04:59 `incoming_ack=true` |
| ACK | `inbound_coordinator_decided reason=window_ack` ×3 (谢谢; 好的/收到/嗯/行/辛苦了; 不用回了/没事). G2 `llm_request` only W3A/W3B/W5 — no ACK llm |
| WRAP | FAIL C9: reply reused E2-2024-WRAP; not 竞业限制 |
| W6 | 配角 send ok. R9A reply 已记下 PAPER-A. R9B Decide `action=reply` on issue `2659a584-…` (not look_into). Host `conversation_id` is R9B, **no R9A cid**. PAPER-A in R9B Host is this cid's own asks (W6B injects the probe), not R9A leak |
| wrap-up | G2/G1 `task_finished_loop_decided reason=already_told_scene` (W3A issue `debb2ae0`, W2 issue `049c6f99`, W3B `70bb3750`). WRAP sandbox did not run |
| shouldReply | none on G1/G2/R9A/R9B |
| 处理中/失败 | C10: G1 W2B emotion `处理中`. G2 W5 inbound emotion `处理失败` (sandbox 订票; same class as E2 12306). G2 sandbox text 「收到，正在处理中。」 on 排期 follow-up |
