# Employee scene work consumption

`scene_event_receipt` remains the provider fact and admission-route authority.
This package freezes its single `work` consumer as `coordinator` or `employee`;
that choice is independent of `legacy`/`unified` and survives mode changes.
The current Coordinator behavior remains in its original lane. A storage-only
`eventrouter.AdmitWithHook` callback commits the first receipt, owner and job in
one transaction. Replays never execute that hook. Historical receipts without
a consumption record stay in the original lane even after a mode change.

Mapped Employee inputs enter a PostgreSQL job. A pending window may absorb at
most 16 receipts and 32 messages during its first 250 ms; it adds no mandatory
wait before the first claim. Claim seals the window, and one scene has one
foreground job at a time. Each item preserves its authenticated endpoint
principal and per-message requester evidence. Long-running Direct tasks do not
hold the scene job.

Transactions lock workspace, then scene, then job. A held unmapped receipt has
no scene and locks workspace then receipt. It never creates a synthetic scene
or an execution job. Lease token, generation and expiry fence every checkpoint
and tool submission. The model does not hold database locks.

A request budget reservation is committed before each provider call. At most
three provider requests belong to a job, across restarts. Native completions and provider failures,
tool arguments/results and the terminal outcome are saved before subsequent
effects. Replays retain native call IDs and the existing Direct source keys.
Journal persistence failure stops later actions while preserving any returned
committed execution receipt. An accepted effect followed by a failed tool is
reported as partial acceptance;
committed effects are neither discarded nor regenerated.

The current tools accept self-contained new work, read an explicitly referenced
requester's own Task, and read its recent entries. Waiting, continuation and
control are not registered. Unsupported event sources and unresolved scenes
are durably held with a reason. Existing explicit Issue/chat targets retain
their original owner; an Employee-owned bound target is held until its adapter
exists. These limits are not a claim that the full R2 loop is complete.

Agent responsibilities use a current authored short contract when available;
otherwise all configured instructions are retained. The installed-skill
catalog is bounded metadata, not an authorization grant. Scene memory uses the
separate Employee namespace and no foreground learning/model pass.

Replies use the existing response/completion outboxes. A completed scene job
means its outcome and delivery intent were committed, not that DingTalk has
confirmed delivery. Callback-less replies use a stable scene notice ID.
Runtime completion notices are a separate required delivery consumer.

Jobs have a kind. `message` is a human window of 1-32 messages; `task_wake`
is one Host-derived wake of an existing Employee Task with no messages.
`AdmitTaskWake` writes the wake's own receipt (source
`employee.task_wake/<producer>`, category `wake`), consumption and job in one
transaction. It never fabricates a message and never joins a human window.
The source identity is producer source plus a stable event id: the first commit
freezes occurred_at and the fingerprint, an identical replay returns the
original job and a changed payload under the same identity is a conflict.
The payload only carries references. The Task's origin is resolved by a
`TaskOriginRegistry` reader chosen by the Task request entry's source
namespace; it returns the admission principal and kind, the delivery anchor
and an explicit history policy (`scene_principal`,
`scene_endpoint_principal`, `not_applicable`) from PostgreSQL and verifies
the principal's current permission. A Task without a registered reader cannot
be woken. A producer must hold a `TaskWakeHost`: admission is refused with
`ErrTaskWakeNotReady` until every live replica executes wakes.

`employee_host_notice` links a Host-initiated send (task wake reply, later
invitations and watchdog notices) to its scene and principal. It is written in
the transaction that enqueues the response action; once the provider confirms
delivery, the message appears as assistant history for that principal. A
withdrawal of memory evidence from its origin receipt hides it.

Claim takes only the job kinds, wake kinds and wake schema versions the binary
supports; other work stays pending for a binary that supports it. Within a
scene, a message window that is not completed is claimed before any wake, so
a wake never runs ahead of newer human input (GawkBot drains human input
before follow-ups; this is a PostgreSQL re-implementation, no code copied).

Enabling Employee requires a configured model with SDK retries disabled,
response services, an authenticated `employee-direct-v1` runtime capability,
and support for the current `EmployeeLoopReplicaMarker` on every live server
replica. Switching the
setting changes only new work. Before rolling back to a binary that lacks the
Employee consumer, drain accepted Employee work; changing the setting alone is
not a safe binary rollback procedure.
