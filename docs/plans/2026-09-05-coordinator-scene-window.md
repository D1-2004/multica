# Coordinator Scene Window

Date: 2026-09-05
Branch: `feat/scene-memory-optimize`
Trigger: FDE教练 (菲迪) production day 2026-09-04 — 328 Decide loops, 68 user-facing busy lines, two-@ storms, cross-scene minutes leak.

This is the Host contract for inbound Coordinator after that day. It is not a prompt patch list.

## Problem

`inbound_coordinator_job` already persisted every inbound. The 4s collect only merged **pending** jobs. Once a window was `running` or had just finished with a busy canned reply, the next @ started another Decide. Busy was encoded as user-visible text (`这条先不并进正在处理的事项`). `finish` could emit only one issue, so two @s from two people could not be one window.

## Contract

Scene key: `workspace_id + agent_id + openConversationId`.

1. **One running Coordinator window per scene.** Claim skips a due job if that scene already has a non-expired `running` job. The due job stays `pending`. Messages are never deleted.
2. **At most two in-flight sandbox matters per scene.** If `assoc` `task_scene` already has two issues with queued/dispatched/running tasks, Claim parks the job for the next window.
3. **Busy follow-up is park, not speech.** `issue_comment_add` hitting `ErrIssueBusy` returns `action=retry`. The worker **parks** the job (`attempt_count` does not burn toward fail). ACK/thanks/`不用回了` Host-silence without LLM. Flood (`灌水` / `FLOOD`) silences. Never emit 不并进 / 手头这件还在做.
4. **A window Decide may emit 0–2 items**, truncated to remaining sandbox slots (`2 - active`). Same deliverable → one item. Two deliverables → two items. `item.delegator` copies that utterance's sender (name + ids). Host rejects a foreign delegator when the window has named speakers. A 3-@ burst in one collect still completes with at most two Issues; leftover asks in that merged command are a known gap.
5. **One spoken IM sentence per window.** Two items still get one ack.
6. **Sandbox stays on this cid.** `look_into` carries `scene_cid=`. Issue body forbids retrieving other groups' minutes/docs/messages.

4-second collect is unchanged: it only merges typing. Queuing across busy is the second clock.

```
inbound @  → pending job (never dropped)
                │
     Claim + absorb due siblings
                │
     scene has another running window?
           yes → park
     ACK/thanks window?
           yes → Decide (silence; skip the 2-task cap)
     2 running sandboxes?
           yes → park (available_at stays; collect cannot pull it earlier)
           no  → Decide once
                │
     finish new issue while 2 open/waiting matters on this cid?
           yes → 409 park (do not open a third)
                │
           Decide once
                │
        0 items → silence / reply
        1–2 items → create that many Issues (delegator per item)
                │
           complete → Notify → next pending window
```

## Code

| Piece | Where |
|---|---|
| Scene mutex on Claim | `pkg/db/queries/inbound_coordinator_job.sql` `ClaimInboundCoordinatorJob` |
| Park without burning attempts | `ParkInboundCoordinatorJob` |
| Active-task cap | `CountActiveTasksForConversation` |
| Worker park / Notify | `handler/inbound_coordinator_job.go` |
| Per-message sender on merge | `DispatchMessage.SenderDisplayName` |
| Window utterances, ACK, items | `inboundcoord/window.go` |
| finish.items | `inboundcoord/loop.go` `coordinatorFinishTool` |
| Two Issue creates | `handler/agent_dispatch_v2_handler.go` |
| Slot free → next window | `handler/task_finished_loop.go` Notify on every completed task, even when wrap-up is off |
| ACK skips 2-task cap | `parkIfSceneWindowBusy` after absorb; `AllWindowAck` |
| Per-item speaker | `overlayDispatchSender` before Issue create |
| Parked jobs stay queued | absorb only `available_at <= now()`; collect uses `GREATEST(available_at, …)` |
| ACK vs real ask | collect/absorb only merge same kind; mixed windows drop ACK lines before Decide |
| Third matter | `CountOpenSceneMattersForConversation` (assoc open/waiting) caps new Issue creates |

## Verification

Unit (this change):

- `inboundcoord` window ACK, two items, foreign delegator
- busy comment → park (`ActionRetry`) not canned reply
- merge stamps two senders

Pre-release IM (after this SHA is on pipeline 66), both Coordinator switches on, actors 冬翔 / 东翔测试号 / dxxh:

| ID | Play | Pass |
|---|---|---|
| W1 | Sandbox still running; 冬翔 sends two follow-ups | Zero IM busy lines; jobs stay pending then digest; none `failed` |
| W2 | Same sender, two lines, one ask | One Decide, one item, one Issue |
| W3 | Two senders, two asks, near-simultaneous | One window, two items, two Issues, delegators not swapped, one spoken line |
| W4 | Two senders, same ask | One item, first speaker is delegator |
| W5 | Two sandboxes running; third @ | Third stays pending; no third Issue until a slot frees |
| W6 | Group B asks for “our minutes” while group A has minutes | Group B must not receive group A content; look_into has B's cid |
| W7 | 谢谢 / 好的 / 不用回复了 ×8 | Host silence, no items |

Proof: IM reread + SLS `inbound_coordinator_decided` (`window_items`, `action=retry` park, `reason=window_ack`).

Durable fixtures and rerun: `.agents/skills/scene-memory-e2e/references/scene-window-plays.md`.

Live on pipeline 66 instance 3106904059 (`d6eeda741` / `99921f42e`), G2 token `R5-4059-W5`: third ask parked then `action=issue` (not silenced with the ACK burst); `inbound_coordinator_job_collect_split incoming_ack=true`; no `shouldReply` leak; wrap-up `already_told_scene` on G1 after sandbox spoke.

Production 67 is out of scope until pre plays pass.

## Out of scope

- Lengthening the 4s collect
- More forbidden phrases in `prompt.go`
- people-group-memory
- Assoc schema change
- Changing `agent_task_queue` schema
