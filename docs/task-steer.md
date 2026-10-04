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
second scan. All sandboxes must return a version-4 receipt with positive
`quiescent` proof, zero remaining owned processes and zero unresolved matching
runtime/port runners. Unreadable unrelated services are diagnostic only;
unreadable matching runners still hold the barrier. Missing receipts and errors do
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

Human input without a transport identity token omits all token/expiry/source
fields and clears an earlier merged input's credentials. A new correction also
rearms the predecessor's stop observation if the first exit proof was missing.

## Task Service steer

Steer is also a control of the EmployeeTask domain (`internal/employeetask`, the
Task Service). The EmployeeTask is the durable continuation anchor that an Issue
or Chat provides on the older surfaces, so executions without an Issue or Chat
(Direct Runs) can be steered too. `employeetask.Capabilities` reports
`steer=true, live_steer=false` for both dispatch backends: steer always means
cancel plus resume, never input written into a running process.

`service.EmployeeTaskControl.Steer` routes by dispatch mode:

- Issue backend: the Issue steer above, through `EmployeeIssueBackend.Continue`
  with `queueMode=steer`. The successor queue row is mapped to a new Run.
- Direct backend: one transaction locks workspace, the latest Run's queue row,
  EmployeeTask and agent claim, the order completion and cancellation use, and
  retries if a new Run appeared meanwhile. An unclaimed active Run (including an earlier steer
  successor) absorbs the correction: its prompt is rebuilt and its Run input
  boundary moves. A claimed Run is cancelled with `CancelAgentTaskForSteer`, its
  Run is recorded `cancelled`, the host records `writer_fenced` evidence, and one
  successor queue row plus Run is created with `priority=4`, `task_steer=true`
  and `steer_predecessor_task_id`. A succeeded task continues the same way
  without a cancellation. A task a human stopped is never reopened by a
  correction (409); only the cancellation the same steer made may reopen it.

The ledger records the correction as a `steer` entry. `StartRun` treats a failed
or cancelled Run as an unresolved writer until a `writer_fenced` entry exists.
Evidence is `claim_barrier` (the row holds `process_stop_pending`, so no
successor of the same EmployeeTask can be claimed before exit proof),
`process_stopped` (acknowledged) or `never_claimed`. A failed Run, or an old
cancellation without the barrier, proves nothing: steer returns 409 and changes
nothing.

A Direct successor is rebuilt from the predecessor's frozen
`employee_direct_input`, never from runtime-enriched top-level state. Its
prompt is the original work packet with every accepted correction rendered as
the compiler's `CURRENT CORRECTIONS` block, so a cold start still has the whole
goal and a resumed session sees the correction first. Identity tokens never
carry over; the correction's own dispatch context may supply new identity
token keys and nothing else, so delivery (`employee_job_id`,
`employee_source_ref`, notice policy) stays bound to the original request.
Personal connectors are recomputed only when the host verified that the
correction comes from the task's own requester. At claim, a successor receives
the predecessor's pinned provider session and workdir when the runtime matches
(walking past at most five predecessors that never started); the daemon's workdir and
context compatibility gates still decide whether it resumes. A cancelled Run
that was replaced by a successor never produces an Employee cancellation
notice; the successor reports the outcome.

Before canceling or merging, the service reads the complete accepted corrections
and checks the new total: at most 100 entries and 64 KiB. Read errors and overflow
abort the transaction without stopping the current writer. New queues record
`direct_steer_corrections_version=1`; terminal proof reconstructs their complete
accepted input boundary. Historical queues without that marker retain the old
20-entry rendering proof, without rewriting their saved prompt.

Delivery remains anchored to the original accepted request. Execution facts
resolve a separate effect source from the steer ledger and completed tool
checkpoint. The inherited file-only policy has a flat original-authorization
locator; it never treats a prior Run's file receipt as this Run's delivery.
Failures still report to the verified delivery target even when success-only
quiet metadata is malformed.

Entry points:

- Humans: `POST /api/employee-tasks/{id}/steer` with `{"content":"..."}` and
  `Idempotency-Key`. `{id}` is an EmployeeTask ID or the queue task ID of one of
  its Runs. Callers need invoke permission for the agent; for Direct tasks they
  must also be the requester (queue originator) or manage the agent. The 202
  response carries `outcome` (`interrupted`, `merged`, `continued`), the
  successor's queue task and whether it awaits exit proof.
- EmployeeLoop: the `steer_task` tool corrects the requester's own Direct task
  in the same scene. The Host selects the target only when the requester has
  exactly one candidate (running, or ready/succeeded within 30 minutes; stopped
  and failed tasks never qualify); otherwise it returns the candidates and the
  model must select the current source-bound `task_ref` or ask. Legacy UUID
  `task_id` remains accepted; a short alias in that legacy field resolves only
  through this wake's frozen source/requester binding. Supplying both target
  fields is rejected before any effect. The successor's result is delivered as the
  answer to the original request.
  The tool was introduced at marker 7; source-bound steer now requires
  `[employee-loop:18]` on all live backend replicas before new Employee work
  is admitted. Older frozen UUID/implicit calls remain supported.

Session continuity needs the provider's resume pointer pinned while the run is
still active: the daemon pins it when a backend reveals the session on a
running status. Claude and Codex always did; Pi and OpenCode do since
`68c2d6a213` (runtime-events). FC images
built before that commit give a steer successor no prior session or workdir,
so it restarts from the full packet with the corrections (verified on pre with
FC Pi, 2026-10-03).

For FC, the post-commit terminal observer runs the task-owned stop collection;
positive exit proof opens the claim barrier before the successor launches.
Issue and Chat scopes reuse a healthy warm sandbox; ordinary Direct executions
with `sandbox_connection_reuse` enabled retain their frozen scene/person bucket
across steer. Reuse is conditional on health, expiration, configuration and
capacity. An unscoped Direct sandbox is single-use, so its successor starts cold
with the full packet. The employee-filesystem branch currently skips scene
connection reuse; a Direct queue with neither Issue nor Chat uses its queue ID
as the DSH execution scope, so that branch does not guarantee the predecessor's
sandbox or session is reused.

Each task acquisition renews sandbox lifetime through `POST /timeout` followed
by a confirming `GET`; ordinary FC sandboxes have a default floor of 4800
seconds (80 minutes). This is renewal at acquisition, not periodic renewal
throughout a running task. A pending stop barrier retains the sandbox during
handoff; idle retention is still bounded. A missing pinned provider session,
incompatible workdir or runtime context, or a cold sandbox may force a fresh
session; the successor must still execute the complete corrected work packet.

The Qwen provider adapter currently does not return positive process-group
exit proof. FC can obtain it from the server's second stop scan; local Qwen
without that proof can retain the barrier. This is a provider-specific limitation,
not something inferred from an employee's display name. The 2026-10-04
Qwen-DWS restaurant task actually used provider Pi, as recorded in its
`agent_task` trace.

## Limits and validation

Unlinked autopilot executions without an EmployeeTask are deferred: they have
no stable continuation anchor, their sandbox is single-use, and their normal
terminal callback completes the automation run. Routing them through a Direct
EmployeeTask gives them the anchor above; until then, do not pretend a new
automation run resumes the old one. Execution-event facts are not recorded for
steer successors yet, because their `run_started` source is the steer, not a
`dispatch_task` tool journal.

Tests must exercise both claim APIs, cancellation acknowledgement, repeated and
concurrent corrections, sandbox retention and a SIGTERM-ignoring descendant
that closes inherited stdout. Pre-release acceptance records predecessor and
successor task IDs, acknowledgement time, provider session, workdir, deployed
SHA and corrected output. Database cancellation alone is insufficient evidence.
