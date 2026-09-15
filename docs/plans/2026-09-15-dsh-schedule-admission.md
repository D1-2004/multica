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
The current workspace/employee/task coordinates are server-derived. The private
adapter supplies the original source task as provenance, verified against the
same employee/native Session and resolved creator. The service locks
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
provenance. The official parser requires a future instant when creating the native
intent; publication can arrive after that instant. First publication with a fresh
task verifies the original bound task's human creator or full Schedule execution
ledger against the current owner. A missing/foreign source or accountability-only
source fails; a completed current bearer task still fails. The historical task
never supplies execution credentials or current invocation permission.
Cancellation is restricted
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

Outstanding: real preproduction validation of due discovery/retry and fresh task
admission for chat/issue/task native scopes; full native create/list/delete adapter
and durable receipt projection; recurring same-Session batch semantics; current
permission and deletion races on real preproduction; offline Host recreation;
model-visible and user-visible delivery; cancellation and uncertain commit tests.

## Due admission implementation

- `internal/dshschedule/worker.go`: database-time discovery, 64-record bound,
  per-candidate timeout, persisted capped retry and stale-error next-due fence.
- Migrations 9252/9253: nullable retry time and capped failure count; a separate
  concurrent partial index supports retry ordering. Old writes default to no
  deferral. Schedule creation is not enabled through native tools during rollout.
- `internal/service/dsh_schedule_dispatch.go`: two authorization checks around
  optional external overlay resolution, current parent and employee locks, fresh
  task attribution, atomic chat input/native binding/receipt, post-commit wake.
- `internal/attribution`: `dsh_schedule` evidence and trigger vocabulary. The
  occurrence request ID identifies the cause; accountability is not impersonation.
- `internal/dshschedule/execution.go`, `internal/service/fc_e2b.go`,
  `internal/handler/dsh_schedule_dispatch.go`: durable occurrence-driven native
  transport and standalone scope restoration. No task JSON controls Session
  identity. Missing binding, wrong scope/provider/backend and missing capability
  fail closed; a transient DB read is a retryable 503.
- `internal/scheduler/jobs_dsh_schedule.go` and `cmd/server/main.go`: existing
  PostgreSQL lease engine, 30-second scans in an independent loop, 20-second scan
  timeout. Existing Autopilot/rollup job loops are unchanged.

Additional contrasts: denied oldest record versus later healthy record; stale
failure bookkeeping versus an already advanced periodic occurrence; automatic
trigger versus fabricated direct human originator; original standalone scope
versus new task scope; missing receipt versus temporary database failure; capable
native DSH versus other providers/backends. Tests are colocated with worker,
execution, service, claim and scheduler code. The application database fixture
covers chat/issue/standalone concurrent admissions with separate pools and a
permission change between preflight and the write. It requires an explicitly
selected migrated real preproduction database and is skipped locally. These
changes are not deployed and the official native tool adapter remains open.

Rollout gate: `MULTICA_DSH_SCHEDULE_DISPATCH_ENABLED` defaults to false and is
preserved by `src/main.sh`. Deploy all replicas with the occurrence-aware claim
and launch paths before enabling it. Registration may persist while dispatch is
disabled; late reminders remain pending. The flag has not been enabled in
preproduction. Before rolling back to a binary without this transport, disable
dispatch and drain admitted reminder tasks. Native adapter/batch integration and
its acceptance must also pass before exposing the complete feature to users.

## Recurring batch correction

The official installed dependency is `@deepseek-ai/dsh-schedule@0.1.5-rc.2`.
Its source selects the earliest overdue one-shot before recurring work, otherwise
all overdue recurring rules become one target/create-ordered prompt. The earlier
single-record dispatcher did not yet satisfy this behavior.

`batch.go` now plans and frames the full set. `postgres.go` acquires a Session
transaction advisory lock and uses NOWAIT for all due siblings, so a locked
cancellation cannot create a partial batch. Batch request identity differs from
individual occurrence identities; migration9254 persists the occurrence ordinal.
`execution.go` reconstructs every row in order and rejects missing/duplicated or
reordered input. Native prompt size validation happens before task creation.
`worker.go` selects and backs off by Session and owning member; migration9255
supports that lookup. Automatic attribution still names one standing owner, so
records of different members are never combined into one execution principal.

Pure contrast tests now cover one-shot priority versus earlier periodic targets,
creation-order ties versus lexical IDs, one-rule periodic batch framing, changed
batch membership/order versus stable replay, hostile text versus fixed framing,
and oversized complete input versus silent truncation. Database fixtures cover
all-or-nothing task/occurrence writes, locked sibling cancellation, request reuse
after rollback, and the three application scope types. Database tests remain
unverified in real preproduction. Native create/list/delete recovery, after/at
rule metadata, local projection acknowledgement and delayed registration after
an uncertain response remain explicit adapter work; the official parser will
remain authoritative. No Runtime or production change was performed.
