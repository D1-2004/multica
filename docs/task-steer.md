# Task steer

Steer interrupts an execution and resumes its provider session with the
correction as the next user input. It does not write into the old process's
stdin. The task remains on its original Issue or Chat; `agent_scene` is the
Host-resolved `scene.Ref` described in `agent-scene.md`, never a new scene key.

## State and ownership

An authenticated dispatch uses `control.action=dispatch`,
`control.sessionMode=continue`, `control.queueMode=steer`. Chat dispatches and
Issue continuations support this mode. Omitted control preserves enqueue.
Fresh-session steer is rejected: it cannot promise continuity.

Human Issue clients can also `POST /api/issues/{id}/steer` with
`{"content":"the correction"}` and a stable `Idempotency-Key` header. The
endpoint checks workspace membership and permission to invoke the Issue's
assigned agent, and uses the same transaction and replay receipt. It creates a
member comment as the explicit next input. No second Issue is created.

The Issue/Chat and agent claim locks serialize cancellation, pending-input
coalescing and claim. Steer commits a cancellation and one explicit successor.
New corrections in the cancellation window join that unclaimed successor.
Every accepted correction retains its completion callback in the shared run's
private context. Older callbacks use the existing nullable-root outbox shape,
with a stable per-dispatch request id; coalescing neither invents another run
nor replaces an earlier accepted callback. A Chat row folded into a successor
relinquishes its callback before its inert row is canceled.
Unrelated queued work and other agents are preserved. Message replay uses the
existing dispatch deduplication receipt; it must not cancel a later run.

Steer cancellation of a claimed task sets `context.process_stop_pending=true` in
the same transaction as `status=cancelled`. Cancelled is a logical terminal
state, not evidence that a process exited. Both claim paths and cloud launch
arbitration treat this marker as an occupied serialization lane. No elapsed
time clears it. The successor remains queued and holds its Issue/Chat sandbox
in use throughout the handoff.

The daemon joins provider termination and transcript flushing before sending
`POST /api/daemon/tasks/{id}/cancel-ack` with
`process_group_stopped=true`. The server clears the marker idempotently and
wakes queued work. An old daemon's empty acknowledgement may settle its legacy
Chat transcript but cannot open the execution barrier. Missing acknowledgements
fail closed; upgrade the daemon or prove the sandbox process group has exited
before retrying the acknowledgement. Windows lacks process-group ownership and
cannot provide this proof yet.

For existing FC/E2B images, the server also uses the task-owned stop script's
second scan. All sandboxes must return a version-3 receipt with zero remaining
and zero unreadable processes. Disabled stops, missing receipts and errors do
not clear the barrier. Pending steer requires the stop even when
`runtime.fc_e2b_sdk_rollout` is off; that switch still selects SDK or CLI
transport. No timer or logical cancellation substitutes for exit proof.

Explicit Chat steer can resume the canceled first turn when the sandbox is
warm and the stored agent/runtime configuration identity still matches. It
does not require a previous completed answer. A cold sandbox or changed
configuration still withholds resume. Local sessions retain their existing
daemon workdir gate; DSH's fresh-native-session contract remains unchanged.

Provider session pinning remains valid after cancellation. After the barrier
opens, the established Issue/Chat session and workdir lookup supplies the prior
session; existing compatibility gates still apply. Steer is a request to resume,
not permission to bypass an absent session or incompatible working directory.

## Issue comments

Steered external follow-ups commit their member comment and successor together.
Pending corrections coalesce using the established comment-delivery receipt;
the latest author supplies attribution and task identity context. Cancelling an
Issue task also reconciles undelivered comments, including comments arriving
before cancellation acknowledgement. Existing routing, explicit-agent mentions,
owner authorization and note/agent-loop suppression remain authoritative.

## Limits and validation

Unlinked autopilot executions are deferred: they have no stable continuation
anchor, their sandbox is single-use, and their normal terminal callback completes
the automation run. Supporting them needs a durable run-input API, a retained
automation sandbox scope and one automation completion across attempts. Do not
pretend a new automation run resumes the old one.

Tests must exercise both claim APIs, cancellation acknowledgement, repeated and
concurrent corrections, sandbox retention and a SIGTERM-ignoring descendant
that closes inherited stdout. Pre-release acceptance records predecessor and
successor task IDs, acknowledgement time, provider session, workdir, deployed
SHA and corrected output. Database cancellation alone is insufficient evidence.
