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
| 4 | E2147-loop | 3106906434 / `6c51b3544` | W3A+W3B both `issue`; W5 park then `issue`; ACK `window_ack` ×3 no ACK llm; collect_split; C9 WRAP `issue` + IM 住宿清单 + wrap-up already_told; W6 R9B `issue` look_into R9B cid no R9A; no shouldReply; W5 inbound no 处理失败; C10 W2B 处理中 still |
| 5 | E2232-loop | 3106906434 / `6c51b3544` | W3A+W3B issue; W5 park then issue; ACK window_ack ×4 no ACK llm; collect_split; WRAP issue+IM 住宿清单+already_told; W6 IM isolation (R9B SLS lag); no shouldReply; this-token W2B no 处理中 (C10 not reproduced); old W2Bs still stamped |

## Open cases

### C1 Host ACK vs DisplayContent
- Found: R5 W7 SLS `action=silence` via LLM, `reason` empty not `window_ack`
- Fix: `669d5a654` `stripInboundDisplay` + per-line `allAckText`
- Prove: E1 ACK Decide has `reason=window_ack` and no `inbound_coordinator_llm_request`, or Host-silence SLS row
- Status: proved E2/E3/E4 (`window_ack` ×3, no ACK `llm_request`)

### C2 Host-silence missing `inbound_coordinator_decided`
- Found: review of `99921f42e`; E1 ACK had collect_split and no llm_request, but also no `inbound_coordinator_decided reason=window_ack`
- Fix: `hostSilence` logs decided with reason
- Prove: next round SLS `reason=window_ack`
- Status: proved E2-2024 (`reason=window_ack` ×3, no ACK `llm_request`)

### C8 WRAP reused previous 竞业限制
- Found: E1 WRAP Decide `action=reply` 「刚才已经发到群里了」
- Fix: runner asks a unique 出差报销说明 + token
- Status: 竞业限制 reuse gone by E2/E3; same-type 出差报销 reused E3 (C9); unique 住宿清单 proved E4

### C3 Parked ask + ACK must not drop the ask
- Found: R4/R5 mixed window silenced W5
- Fix: collect/absorb same-kind + KeepWorkUtterances
- Prove: `collect_split incoming_ack=true`; W5 token `action=issue`
- Status: proved E2/E3/E4

### C4 Isolation R9A/R9B
- Prove: R9B Decide `look_into` / Host has no `{token}-PAPER-A` from R9A
- Status: E4 R9B Decide `action=issue` look_into `scene_cid=cidwybOKJur9YbbjyLpjnbPaA==`, Host has no R9A cid. PAPER-A in Host is this cid's W6B current/history, not R9A memory

### C5 Wrap-up already told
- Prove: after sandbox spoke on cid, `task_finished_loop_decided reason=already_told_scene`; no 已报群里
- Status: proved E4 WRAP — sandbox sent 住宿清单, then `already_told_scene` on issue `856b2bf0-…`; no 已报群里 二刷

### C6 shouldReply / 处理中
- Prove: IM reread no `{"shouldReply":true}`, no stuck 处理中/处理失败
- Status: shouldReply clean E2–E4; E4 W5 inbound has no 处理失败; W2B `处理中` still (C10)

### C7 R9 isolation groups have no 冬翔
- Found: E1 W6A `OpendId is not in conversation` sending as 主角 to R9A
- Fix: W6 sends as 配角 (`AT_DXXH`); R9A/R9B members are dxxh + 测试号 only
- Status: patched in runner; proved E2–E4 配角 send (R9A/R9B ok)

### C9 WRAP reply-reuses previous-round 出差报销
- Found: E2103-loop WRAP Decide `action=reply` 「出差报销说明之前已经写好发到群里了（token E2-2024-WRAP），需要我再发一份新的吗？」; no new issue; wrap-up sandbox not exercised
- Fix: runner WRAP deliverable includes `{token}` in the title (`差旅住宿清单`) so assoc cannot match last round's 报销
- Prove: next round WRAP `action=issue` (or sandbox send) with this token; IM is the new 住宿清单 not a reuse question; no 竞业限制 / previous WRAP token in the reply
- Status: proved E2147-loop — Decide `action=issue` look_into 住宿清单; IM sandbox posted **E2147-loop 差旅住宿清单**; wrap-up `already_told_scene`; no 竞业限制 reply

### C10 W2B `处理中` stuck after issue_busy retry
- Found: E2103-loop G1 W2B (`就约线上`) Decide `action=retry` ×6 `reason=issue_busy_park` on issue `049c6f99-…`; wrap-up later `already_told_scene`; IM still has emotion `处理中` on the W2B inbound (E1-2007-W2B still stamped too)
- Fix: Host/channel should complete the DingTalk silence callback (clear 处理中) when `ActionRetry` parks or when wrap-up silences the same cid; do not leave the confirmation @ spinning
- Prove: next round W2B has no leftover `处理中` after the meeting issue settles
- Status: E5 `E2232-loop-W2B` inbound has **no** `处理中` (retries still fired on `049c6f99-…`, then IM replies). E3/E4 W2Bs still stamped. Keep open until a retry-storm round is clean without relying on a later reply. Not shipping this fire

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

## Round 4 detail (E2147-loop)

Pipeline 66 `3106906434` 预发部署 SUCCESS, SHA `6c51b3544`. dws pre. Actors 主角/配角 only.

| Check | Result |
|---|---|
| W3 | W3A `action=issue` token kept (new 排期, not reply-reuse). W3B `action=issue` token kept |
| W5 | job `a1cd4589-…` parked `two in-flight` ~21:48:44–21:50:03, then Decide `action=issue` 21:50:12 text 「我去处理订票需求…」. IM sandbox 订票 follow-up; inbound **no** 处理失败 |
| collect_split | 21:48:44 / 21:48:48 / 21:49:04 `incoming_ack=true` |
| ACK | `window_ack` ×3; G2 `llm_request` only W3A/W3B/W5 |
| WRAP | PASS C9: `action=issue` look_into 住宿清单; IM **E2147-loop 差旅住宿清单**; wrap-up `already_told_scene` issue `856b2bf0-…` |
| W6 | 配角 ok. R9A 已记下 PAPER-A. R9B `action=issue` look_into `scene_cid=` R9B; Host no R9A cid; IM searched this cid only (file not found) |
| wrap-up | G1 WRAP `already_told_scene`; G2 W3 `already_told_scene` issue `9cf2b661-…` |
| shouldReply | none |
| 处理中/失败 | C10: G1 W2B `E2147-loop-W2B` still `处理中` after retry on `049c6f99-…` |

## Round 5 detail (E2232-loop)

Pipeline 66 `3106906434` 预发部署 SUCCESS, SHA `6c51b3544`. dws pre. Actors 主角/配角 only.

| Check | Result |
|---|---|
| W3 | W3A `action=issue` token kept. W3B `action=issue` token kept |
| W5 | job `c3e6cda7-…` parked `two in-flight` ~22:33:53–22:34:40, then Decide `action=issue` 22:34:50 look_into `scene_cid=` G2. IM sandbox asked 出发城市; inbound no 处理失败 |
| collect_split | 22:33:51 / 22:33:56 / 22:34:08 / 22:34:20 `incoming_ack=true` |
| ACK | `window_ack` ×4 (谢谢; 好的/收到/嗯; 行/辛苦了/不用回了; 没事). G2 `llm_request` only W3A/W3B/W5 + wrap-up — no ACK llm |
| WRAP | PASS C9: `action=issue`; IM **E2232-loop 差旅住宿清单**; wrap-up `already_told_scene` issue `3ecfd893-…` |
| W6 | 配角 ok. R9A 已记下 PAPER-A. R9B IM 22:35:11 searched this cid only, no R9A leak. SLS decided/request for this token not ingested yet at query time |
| wrap-up | G1 WRAP `already_told_scene`. G2 W3A wrap-up `action=reply` 「已问 dxxh 下周排期，等他回。」 (first group report). G1 W2 wrap-up `already_told_scene` on `049c6f99-…` |
| shouldReply | none |
| 处理中/失败 | This-token W2B/W5 inbound have no 处理中/处理失败. Older E2147/E2103 W2Bs still `处理中` |
