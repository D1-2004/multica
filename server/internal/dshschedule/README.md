# Persistent native Schedule admission

This package is the PostgreSQL transaction core for native DSH reminders. It is
wired to application task admission and the shared-database scheduler. The
native Schedule tool adapter and real batch acceptance are still
unfinished; its presence is not proof of reminder delivery.

The task-authenticated management API is implemented in
`internal/service/dsh_schedule.go` and `internal/handler/dsh_schedule.go`. It
derives standing ownership from the active bound task and current membership,
rechecks invocation policy, and exposes registration/list/cancellation receipts.
See `docs/plans/2026-09-15-dsh-schedule-admission.md` for remaining admission and
native adapter work. These APIs have not yet been deployed to preproduction.

The cloud failure motivating it is concrete: the original task completed and its
context was released; the official in-process timer later called `agent.followup`
and the model request failed with `task context is absent, unready or expired`.
Neither retaining the old task token nor claiming an in-memory timer is durable
fixes that lifecycle boundary.

## Transaction contract

The integrating service must:

1. Authenticate registration/cancellation through the currently bound task.
   Derive employee, native Session, source task and standing member identity from
   server-side attribution. Model arguments cannot choose those identities.
2. Acquire the existing admission locks in their established order, retain parent
   locks through commit, and verify current invocation authority. Browser entry
   grants cannot become stored automation credentials.
3. Call `Store.Register` with the official parser's immutable UTC rule. Retry the
   same record after an uncertain response. Changed content conflicts, and a
   cancelled or consumed record cannot be reactivated by registration replay.
4. A periodic shared-database scan must revisit every overdue active record. Do
   not apply the autopilot cron job's five-minute lateness cutoff. Database time,
   not the worker clock, determines eligibility.
5. Call `Store.Dispatch` inside the authorized admission transaction. Its enqueue
   callback rechecks the standing owner's current permissions, creates a fresh
   task and input, and uses `AdoptNativeExecution` for the original Session and
   `Due.RequestID`. The callback performs no cloud/HTTP dispatch and no commit.
6. Commit the task, native binding, occurrence receipt and next-due advancement
   together. Only after successful commit publish a wake hint or return a native
   dispatch receipt. Use the existing durable task dispatch/recovery machinery.
   Roll back the entire transaction on any error, including callback failure.
7. Cancellation locks the same row. It prevents future admissions; cancelling an
   already admitted task uses the existing task cancellation path. Preserve the
   schedule tombstone to reject delayed creation retries.
8. Workspace/employee deletion must delete occurrence and schedule rows while
   holding the admission parent locks. Do not leave private reminder text behind.

The occurrence request identity includes workspace, employee, native Session,
session-local Schedule ID and UTC occurrence time. It is independent of sandbox
generation and the old task lease. No execution token is stored in either table.

One-shot reminders remain eligible while overdue. Recurring reminders select the
latest overdue occurrence and keep their original creation anchor, matching the
official Schedule semantics. The platform now admits the complete recurring batch with official framing; the
native adapter must synchronize its local projection only after the platform's
durable receipt.

## Validation boundaries

`go test ./internal/dshschedule` runs pure rule/identity/framing tests. The four ledger
PostgreSQL tests skip unless `DSH_SCHEDULE_TEST_DATABASE_URL` explicitly selects a
real preproduction test database. They use an isolated schema and two independent
pools to exercise atomic rollback, concurrent admission, cancellation, immutable
replay and native binding checks. Their enqueue callback is a database fixture;
passing them alone is not application authorization or end-to-end delivery proof.

Outstanding integration gates: native create/list/delete acknowledgement;
real application transaction validation; deletion cleanup;
offline Host recovery; model-visible delivery in the original Session; real periodic
batch delivery; cancellation before/after admission; and lost-response replay.

## Due worker and task transport

`Queue.Candidates` discovers one candidate per native Session and standing owner
using database time. Failure deferral
is persistent, bounded from 30 to 300 seconds, and compares the observed next due
instant so a delayed error cannot postpone a successfully advanced occurrence.
A cancelled/consumed reminder remains ineligible. `Sweep` examines at most 64
records, bounds each attempt to 10 seconds and continues after denied or failed
admissions. The registered job bounds each scan to 20 seconds and runs in its own
instance of the existing lease scheduler to avoid delaying the older jobs.

`service/dsh_schedule_dispatch.go` checks current member/employee/runtime/scope
permission before resolving optional agent-owned connected apps and again inside
the write transaction. It creates fresh automatic tasks with `trigger_owner` and
`dsh_schedule` attribution. Originator and initiator stay NULL. Chat tasks own a
new immutable input batch; issue and standalone tasks use the native prompt
transport. The receipt, input, native binding and next due update commit together.
The standard durable task queue supplies launch/recovery after commit.

`LoadExecution` reconstructs a prompt from the occurrence and immutable record.
Both the FC launcher and claim handler verify it against task scope and evidence.
Standalone follow-ups keep the original task's mapped native Session. A missing
receipt or incapable Runtime cannot silently fall back to generic task prompting.
No old task context, token or credential is copied. These are implemented contracts;
cloud/database behavior and native adapter delivery are still unverified.

Rollout gate: `MULTICA_DSH_SCHEDULE_DISPATCH_ENABLED` defaults to false and is
preserved by `src/main.sh`. Deploy all replicas with the occurrence-aware claim
and launch paths before enabling it. Registration may persist while dispatch is
disabled; late reminders remain pending. The flag has not been enabled in
preproduction. Before rolling back to a binary without this transport, disable
dispatch and drain admitted reminder tasks. Native adapter/batch integration and
its acceptance must also pass before exposing the complete feature to users.

## Complete recurring batches

The pinned official Schedule 0.1.5-rc.2 source (`types/runtime.js:dueDecision` and
`types/domain.js:renderEveryReminderBatchFraming`) gives overdue one-shots priority,
then batches one latest occurrence of every overdue recurring rule in target and
creation order. `PlanBatch` and `Batch.Framing` preserve that contract. Rules with
different standing owners remain separate because one task cannot impersonate
multiple members. A single recurring rule still uses the official batch framing.

`Store.Dispatch` takes a transaction-scoped native Session advisory lock, then
locks the owner's complete due set with NOWAIT. It never skips a locked sibling.
One task/native request owns the full ordered batch; every occurrence retains its
individual ID and `batch_ordinal` (migration9254). All receipts and advances commit
or roll back together. `LoadExecution` checks contiguous ordinals and recomputes
the full batch request ID, detecting missing, repeated, reordered or foreign rows.
The inherited native transport size bound is checked before admission; oversized
batches are not clipped or consumed.

Discovery groups by Session/owner and excludes a group while any due sibling is
backing off. Failure defers the observed group, with the seed next-due and database
observation time fencing records already advanced or changed by another replica.
Migration9255 adds the concurrent partial Session/owner due index. Per-task wake
still uses the standard durable queue and fresh credentials.

Batch tests cover priority/order, identity, injection-resistant payload, full-set
reconstruction and size rejection. The PostgreSQL fixture adds whole-batch
rollback and a locked cancellation sibling; application fixtures admit two every
rules as one task in chat/issue/standalone scopes. These real DB cases still skip
locally and are not passing preproduction evidence. No Runtime source or native
Schedule tool was changed in this batch implementation step. The dispatch gate
remains off, and none of the Schedule commits has been deployed yet.
