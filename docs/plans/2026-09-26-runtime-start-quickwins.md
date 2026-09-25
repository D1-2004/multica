# PRI-47: bounded runtime startup recovery

Baseline: PRI-34, 800 cloud tasks / 99 probes, corrected report of 2026-09-25.
Coordinator, protocol prompts and production deployment are out of scope.

| Priority | Change | Expected benefit (not measured yet) | Size | Risk | Live Diamond key under runtime.fc_e2b |
| --- | --- | --- | --- | --- | --- |
| 1 | Q3: recover abandoned ordinary FC launches | The 447 s orphan wait becomes at most one 90 s lease plus a 30 s sweep under normal load | SQL, service, config, tests; ~150 lines | Low: retain lease, latest attempt and pre-runner fences | recover_abandoned_launches |
| 2 | Q2: expire repeated DSH waits after 10 minutes | Bound the observed 1.7 h / 24 h wait loops; this is a failure boundary, not successful execution speedup | SQL, service, config, tests; ~200 lines | Medium: only blocked tasks without live launch lease or claim token may expire | bound_dsh_host_wait |

Both keys default false in code. Publish true only in pre-release Diamond
`dt-fde-multica-runtime.json` / `DEFAULT_GROUP`; setting either false takes
effect on the next sweep without deployment. Old binaries reject unknown
fields, so publish only after the new pre-release replicas are up. Remove
these fields before binary rollback. Turning the TTL off does not resurrect
already failed tasks.

Design follows the idempotent reconciliation pattern described by
[Kubebuilder](https://book.kubebuilder.io/reference/good-practices): durable
state determines eligibility; a scan is a hint, and the existing database
launch lease fences competing replicas. No in-memory ownership or second
launcher path is introduced.

Q3 preserves the old DSH-only two-minute branch with the flag off. With it
on, an expired lease permits recovery of any FC task at a known pre-runner
stage (including template_resolved). Runner submission/receipt and claim
tokens forbid recovery. The recovery API still checks serialization.

Q2 measures from the first DSH waiting attempt for the same task/runtime,
not queue creation or the most recent retry. It only expires the latest
blocked DSH wait, under a task row lock, with no live lease or claim token.
A currently starting/claimed task wins. Failure propagation uses the normal
TaskService finalizer. The existing reason taxonomy is retained.

Q1 is deferred: the current implementation has no unified asynchronous
completion source for NAS provisioning, profile builds and host reconciliation.
Removing the 30-second eligibility filter alone is not event-driven recovery.
A follow-up should wire durable readiness events plus periodic reconciliation,
including the event-before-blocked and event-before-lease-release races.
Q4 has no recent capacity waits; Q5 already has partial wait reasons; Q6 has
no observed hung exec. These are deferred to keep this batch bounded.

Verification covers off/on selection, expired/live leases, submitted runners,
claim tokens, latest-attempt precedence, TTL measured across retries, and
concurrent claim/expiry. Pre-release acceptance additionally requires real
DingTalk inbound tasks and live switch rollback evidence. Local SQL tests
do not substitute for that evidence.

Local verification (2026-09-26): isolated PostgreSQL `multica_pri47`,
FC recovery/DSH TTL/latest attempt/claim fencing tests, related runtime-start,
FC launch and sweeper tests, Diamond listener on/off test, and go vet pass.
The broader selection has one baseline failure:
`TestFCE2BChatIdentityComesOnlyFromAgentBinding` expects a custom LLM trace sink
where current code uses the platform sink; reproduced at untouched
`43ce215a90`. It is excluded from the passing targeted run, not fixed here.
The schema-only local database needs the deployment_fence singleton seeded;
no pre-release database was modified for these tests.

Full sqlc generation is blocked by existing migration 271 referring to the
fork table created in 9025. Query generation used sqlc v1.29.0 with the
repository's existing temporary-schema approach (271 analyzed last), and
copied only the changed runtime_start queries to retain fork compatibility.
