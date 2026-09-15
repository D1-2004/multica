# Persistent native Schedule admission

This package is the PostgreSQL transaction core for native DSH reminders. It is
not yet wired to the native Schedule tools or the application scheduler. Its
presence does not enable reminder delivery.

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
official Schedule semantics. The native adapter still needs to preserve official
same-session recurring-batch framing and synchronize its local projection only
after the platform's durable receipt.

## Validation boundaries

`go test ./internal/dshschedule` runs pure rule/identity/framing tests. The two
PostgreSQL tests skip unless `DSH_SCHEDULE_TEST_DATABASE_URL` explicitly selects a
real preproduction test database. They use an isolated schema and two independent
pools to exercise atomic rollback, concurrent admission, cancellation, immutable
replay and native binding checks. Their enqueue callback is a database fixture;
passing them alone is not application authorization or end-to-end delivery proof.

Outstanding integration gates: native create/list/delete acknowledgement;
current-authority service admission; periodic wake/retry wiring; deletion cleanup;
offline Host recovery; model-visible delivery in the original Session; periodic
batch semantics; cancellation before/after admission; and lost-response replay.
