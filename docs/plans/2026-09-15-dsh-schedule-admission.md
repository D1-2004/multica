# Persistent DSH Schedule admission

## Trigger and intended behavior

A real FC probe created an official Schedule reminder, completed the original
platform task, released its context, and observed the due native follow-up fail
with `task context is absent, unready or expired`. Keeping the old task credential
would violate execution-scoped identity. Each reminder occurrence instead needs
new authorized platform task admission into its original native Session.

This work preserves the full DSH/AgenticFS acceptance scope. Reminder management
and storage are intermediate implementation steps, not completed delivery.

## Current implementation

`internal/dshschedule` stores immutable records, cancellation tombstones and
per-occurrence task receipts in PostgreSQL. Dispatch calculates due time from the
database, preserves late one-shot reminders, and selects the latest overdue
creation-aligned recurring occurrence. An enqueue callback, native task binding,
receipt and next-due advancement must share one transaction.

`internal/service/dsh_schedule.go` and `internal/handler/dsh_schedule.go` add
registration, bounded list and cancellation through the current task token.
Workspace/employee/source task coordinates are server-derived. The service locks
Runtime, employee and workspace parents, checks the current active task and native
binding, locks current membership, and applies the same invocation policy as
native chat. Changed runtime, archived employee, ended task, absent member or
failed permission check cannot mutate reminders.

A direct human originator resolves standing ownership through current membership.
A parent without a human originator needs an actual persisted Schedule occurrence
linked to the same employee/native Session. Audit accountability alone cannot mint
standing authority. Dispatch must preserve automatic-trigger attribution rather
than pretend the owner sent a fresh human message or reuse their old task token.

A retry under a fresh authorized task of the same owner may read the unchanged
existing record, including cancelled/consumed state. It does not overwrite source
provenance. New registration requires a future instant. Cancellation is restricted
to the record owner and does not claim to cancel an already admitted task.

Workspace deletion removes both reminder tables in the parent transaction. The
employee archive check prevents further admissions; hard-delete and rolling
old/new-replica cleanup behavior remain part of integration review before enabling
native reminder creation.

## Policy and source mapping

The Coordinator prompt, action vocabulary, assembly version and behavioral
routing are unchanged. Native scheduled reminders do not masquerade as a new
trusted inbound message or a Coordinator `continue_work` decision. The relevant
shared obligations from `inbound-coordinator-loop.md` and registry2026-09-14.3 are:

- COORD.F01/F08: authenticated employee and exact native Session determine scope;
  client fields and audit ownership cannot invent an execution principal.
- COORD.F03/F19: persisted, admitted, model-completed and delivered are separate
  facts; database connection failures are not passing transaction tests.
- COORD.F13/F17: authority is rechecked at the write boundary; cancellation and
  lost-response replay must not expand or repeat the committed effect.

Implementation references are the handler/service/store files above and
`dsh_native_chat.go#lockDSHEmployeeAdmission`. Management tests are colocated in
`dsh_schedule_test.go`; store transaction cases are in
`internal/dshschedule/postgres_test.go`.

## Contrast cases and verification

- Same authenticated task versus different route task/workspace: mismatches fail
  before database access. Human JWT/PAT/native grants cannot use this endpoint.
- Same JSON reminder versus caller-supplied owner/task/token fields: authority
  fields and malformed/trailing JSON are rejected.
- Same employee/task identity versus ended task, runtime switch, archive, local
  runtime or another workspace: only the active matching identity is eligible.
- Same record retry versus changed content/owner: unchanged state is returned;
  changed immutable registration conflicts. Cancelled/consumed records stay so.
- Same due occurrence across replicas: one task/receipt after commit; a failed
  enclosing transaction rolls both back and retains retry identity.
- Same cancellation before versus after admission: before prevents dispatch;
  after requires task cancellation instead of claiming the already queued task
  disappeared.

Local pure/unit/compile checks and policy structural checks are recorded separately
from live acceptance. The FC database harness failed connecting to preproduction;
a separate cloud probe resolved DNS but timed out connecting to TCP5432. Neither
transaction suite has passed against the real database. Both probe sandboxes were
confirmed absent after cleanup. The application has not yet deployed these APIs.

Outstanding: due-record discovery/retry through the shared scheduler; fresh task
admission for chat/issue/task native scopes; full native create/list/delete adapter
and durable receipt projection; recurring same-Session batch semantics; current
permission and deletion races on real preproduction; offline Host recreation;
model-visible and user-visible delivery; cancellation and uncertain commit tests.
