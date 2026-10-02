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

Enabling Employee requires a configured model with SDK retries disabled,
response services, an authenticated `employee-direct-v1` runtime capability,
and support for `[employee-loop:1]` on every live server replica. Switching the
setting changes only new work. Before rolling back to a binary that lacks the
Employee consumer, drain accepted Employee work; changing the setting alone is
not a safe binary rollback procedure.
