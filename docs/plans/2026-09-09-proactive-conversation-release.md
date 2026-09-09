# Proactive conversation cutover

The Digital Employee option “主动处理会话所有新消息” sits below inbound judging and
atomically enables it. Observed messages enter the ordinary durable Coordinator
window. A decision plus durable Issue handoff finishes admission; executor runtime
does not hold the conversation. Busy-Issue additions keep their original sender,
receiving identity and conversation, merge on the next run, and are reconciled
against actual delivery receipts. Completion judging can see outstanding additions
when its existing option is enabled. No new event Autopilots are created.

## Release

Use existing group-awareness CRs: Router 35999116 and Multica 36022159. Deploy the
reply-target and bounded admission-retry Router first. Migrations 9211–9217 run
**automatically through the release order's existing `src/main.sh` pre-start step**;
never run them manually. The cutover pauses historical event Autopilots while the new
worker drains any previously admitted batches. Old replicas reject new legacy
admission; Router retains and retries those deliveries during rolling deployment.
Existing user settings and historical runs are preserved.

## Local evidence

- Router observed reply-target and DWS delivery tests passed; dispatch queue suite
  covers retry beyond ordinary attempt limits and ordinary terminal errors.
- Sixteen tests for Digital Employee settings and read-only/update behavior passed;
  views TypeScript checking passed.
- Coordinator, inbound jobs, task-finished loop, proactive admission, event trigger,
  Issue follow-up, and response-policy regression selection passed against isolated
  PostgreSQL 17. Server compilation passed.
- Policy 2026-09-09.1 structural registry checks passed: 19 obligations, 12 modules,
  20 contrast contracts. Structural checks do not certify live model behavior.
- Broad existing test suites contain failures also reproduced at parent 54cfea41c
  (including prompt fixtures, FC trace config, finalization fixture and binding
  test panic). The two candidate-only regressions found by comparison—prompt
  budget and inbound toggle dependency—were fixed and their targeted suites pass.

Pre-release migration, immutable build identity, and real message acceptance remain
separate gates, to be recorded after the release order finishes.
