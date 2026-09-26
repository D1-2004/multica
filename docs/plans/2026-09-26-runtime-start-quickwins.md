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


## Revised scope (2026-09-26 follow-up)

The new instruction supersedes the initial deferrals. Use the PRI-34 v2 numbering.
Q1 remains excluded: Coordinator budget, reasoning and finish retry require the
owner's separate decision. Q2-Q7 plus readiness timeout, hot exec coalescing and
bounded skill batching are in scope, with independent live flags. No changes to
Coordinator prompt/logic are permitted; Q5 is task-side delivery guidance only.

| Item | runtime.fc_e2b key | Contract |
| --- | --- | --- |
| Q2 | dsh_event_wakeup | Persist workspace/agent readiness timestamps, broadcast PostgreSQL hints, recheck after lease release, periodic sweep repairs missed hints |
| Q3 | bound_dsh_host_wait | Existing 10-minute cumulative wait bound plus typed provisioning/native/profile wait reasons |
| Q4 | recover_abandoned_launches | Existing recovery plus shutdown cancellation/join before releasing owned launch leases |
| Q5 | dingtalk_reply_command | Task-side quoted reply command using authoritative source identifiers; no Coordinator changes |
| Q6 | asb_event_wakeup | Capacity release events bypass retry delay; completion of a failed retry does not bypass backoff |
| Q7 | startup_observability | Structured task-correlated substage timings and Langfuse startup spans |
| Extra | bounded_ready_exec | Short readiness exec deadline, pipe-drain WaitDelay, total deadline |
| Extra | coalesced_hot_exec | Probe readiness and runner capability in one fixed script for reused FC sandboxes |
| Extra | batch_skill_resolve | Bounded small-bundle batching; preserve progressive resolution for large bundles |

Events are hints, never authority. Consumers reload durable state and take the
existing launch lease; an event published before blocked persistence remains
eligible and a release hint closes the lease race. False restores legacy
selection/backoff/commands on subsequent operations. No task may be marked
failed merely because this server is shutting down.

Tests must cover notification-before-blocked, lease-held notification, two
replicas, shutdown cancellation/lease fencing, ASB self-notification spin,
quoted delivery targets, bounded exec pipes and partial skill cache progress.
Real-task on/off/on evidence is required independently of config propagation.


Implementation detail: readiness uses `runtime_readiness_event` (one timestamp
per workspace/agent; nil agent is a workspace Profile build) and the existing
PostgreSQL LISTEN/NOTIFY pattern in `internal/daemonws/pg_notifier.go`. A 5-second,
four-worker bounded cloud-state reconciler finishes pending NAS/host receipts;
it does not submit runners. Completion emits the persisted event. Old binaries
can continue polling during rollout; add new Diamond keys only after deployment.
A missed completion write or notification is repaired by the original sweeper.

Small-bundle batching is server-side at claim (complete sets of at most 32 bundles
and 512 KiB encoded JSON); no FC image upgrade is needed. Larger sets retain refs and
progressive caching. Hot exec coalescing caches only a per-launch, per-sandbox
capability receipt; no capability cache crosses tasks or sandbox replacement.

### 2026-09-27: make bounded batching reachable for real agents

The original 8-bundle/128-KiB gate was unreachable: every claim includes 11
mandatory built-in bundles (306454 content bytes before JSON encoding), even
when an agent has no workspace skills. The revised limit includes that baseline
and the DWS policy bundle without dropping any skill or changing daemon/image
contracts. The gate bounds the entire serialized bundle array, including names,
descriptions, configuration, manifests and JSON escaping, rather than only file
contents. A larger set still uses all refs; mixed inline/ref responses remain
unsupported by older daemons. OFF retains the existing progressive cache path.

The regression uses the actual embedded built-ins plus a DWS policy and a small
workspace skill. This guards against another permanently unreachable rollout;
separate cases reject oversized supporting files, metadata and bundle counts.
No new production fault-injection or runtime configuration is introduced.

### Background readiness observation context

NAS/host operations resumed by the readiness reconciler must carry the same
optional startup recorder as foreground launches. Without it, a pending NAS
operation can finish its later steps without emitting any Q7 substage spans.
The worker uses the oldest queued task for the affected agent as a representative
trace link; this is not an ownership/launch decision and does not imply that a
shared resource's duration should be summed once per waiting task. Missing task
metadata must not prevent reconciliation. Q7 OFF avoids the task metadata read
and emits no added observations; later emissions still recheck the live switch.

Validation on 2026-09-26: targeted service/handler/DSH/Profile/runtimeconfig/server
checks pass, as do -race and go vet. The baseline health test also returns 503 at
untouched 43ce215a90, separately from the existing FC trace sink failure.
Real baseline: DingTalk message msgAkqseVuH3jWQcydiao6J1w==, task
5fcbbe61-33f9-4c3b-84fa-5d24bdfa86df, trace 3ff62e6a-eb4e-4c89-914c-832a6a4692d7.
Message 13:55:21, ACK 13:55:47, result 13:56:33 (+08:00); decision-to-start ~8s.
This one sample does not estimate percentiles. E2E on/off/on remains to be run.


## Q1 authorization and implementation (14:36 follow-up)

The later user comment 22fe680a-710a-4e50-a433-0811a17122a4 explicitly authorizes
Coordinator changes. Prior Q1 exclusions in this plan are historical, superseded
by this section. Add runtime.llm.coordinator_finish_recovery: 4096 main output,
DeepSeek flash thinking disabled, bounded finish-only serialization repair and
kind-specific schemas matching unchanged Host validation. Use the observed EOF
and start_work/state_refs failures as regressions. Do not rerun reads or commit
anything during serialization repair. This remains pre-release only.
