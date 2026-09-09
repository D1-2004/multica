# FC sandbox lifecycle checks

Multica can renew FC/E2B sandboxes while their current tasks have active states.
The lifecycle worker runs in every server replica. Redis selects one executor
per check round. Existing PostgreSQL task, startup-attempt and sandbox-session
tables supply task state, sandbox identity and observed expiry.

## Enablement

The feature defaults off. Set `runtime.fc_e2b.sandbox_renewal_enabled` in the
managed runtime document, or `MULTICA_FC_E2B_SANDBOX_RENEWAL_ENABLED=true` in
environment-only installations. Redis is required. `timeout_seconds` supplies
the renewal duration and must be at least 300 seconds when renewal is enabled.
The normal value remains 3600 seconds.

Deploy the new server binary to every replica before adding the setting to
Diamond: older binaries reject unknown config fields. This feature adds no
database migration. Turning the setting off stops subsequent worker rounds;
a round already in progress can finish under its frozen configuration snapshot.

Already-running tasks are eligible immediately after enablement if their latest
valid startup attempt identifies an FC sandbox. No execution enrollment or
daemon-token binding is required.

## Scheduling and ownership

- Every replica tries to claim every 5 seconds. A successful round advances the
  next check by 30 seconds. There is no replay of historical timer ticks.
- Redis keys share a cluster hash tag. The namespace includes the deployment,
  Multica server URL and FC API URL. All replicas must use identical values.
- One Lua script checks Redis TIME, next-run time and lock availability, then
  creates a unique token with a 90-second lease. A completed round atomically
  checks ownership, advances next-run time and releases its lease.
- Lease renewal checks the token every 20 seconds. Each complete round is
  bounded to 60 seconds with four concurrent workers. Candidates without a
  reuse mapping come first, followed by earlier cached sandbox expiry. There is
  no fixed first-100 cutoff. Monitor round timeouts before increasing fleet size.
- Loss of Redis ownership cancels the round. An interrupted round leaves its
  uncertain lease to expire. Redis failure never enables per-node execution.
- Redis failover can cause duplicate attempts. FC calls are bounded and each
  attempt queries actual provider state before deciding whether to renew.
  PostgreSQL instance advisory locks also coordinate background checks with
  synchronous warm reuse. Fresh task and latest-attempt checks reject replaced
  executions before renewal and before any task failure is committed.

## Existing task state and provider state

`agent_task_queue` supplies the authoritative task state. Eligible states are
`dispatched`, `running` and `waiting_local_directory`; queued and terminal tasks
are excluded. `agent_task_runtime_start_attempt` supplies the sandbox ID from
the latest attempt for that task, with matching Runtime, `aliyun_fc` backend,
`starting` or `claimed` status and a nonempty sandbox ID. A newer attempt excludes
the older one even if the newer attempt is not eligible itself.

`fc_e2b_sandbox_session.expires_at` is updated after a successful provider read.
The worker also covers tasks without a warm-reuse mapping; it does not create a
replacement bookkeeping row. FC queries use the deployment's current API URL.

The task-status HTTP handler and existing stale-task sweeper are unchanged.
There is no new task heartbeat, heartbeat table or check-version record. Active
task state does not prove the process is healthy: a hung task can receive renewal
until the existing completion, failure, cancellation or timeout mechanisms change
its state. Provider/network errors alone do not terminate a task.

Warm reuse queries the actual sandbox even if the cached database expiry has
passed. It renews when remaining lifetime is below the smaller of ten minutes
and one third of `timeout_seconds`. New sandboxes also have their provider expiry
confirmed before use. A confirmed missing warm sandbox is invalidated and the
normal creation path can create a replacement.

During execution, the worker uses `GET /sandboxes/{id}` and, when necessary,
`POST /sandboxes/{id}/timeout` with `{"timeout": seconds}`. This replaces remaining
lifetime from the request time. Successful HTTP status alone is insufficient:
the worker reads the actual `endAt` back and requires adequate remaining time.
API credentials and response bodies are never logged; redirects are rejected.
The adapter follows the [FC timeout contract](https://help.aliyun.com/zh/functioncompute/timeout)
and the [E2B OpenAPI specification](https://github.com/e2b-dev/E2B/blob/main/spec/openapi.yml).

Two consecutive provider 404 responses mark the instance unavailable. The
associated current task follows the existing failure transaction with reason
`sandbox_expired`. A task row lock plus a fresh latest-attempt check prevents
that result from failing a replacement execution. This reason does not opt into
automatic task replay. A paused instance is not automatically resumed or killed.

Task completion stops renewal. The existing warm reuse retention remains; there
is no new active kill or timeout-shortening policy. An already-submitted provider
request may finish while the task transitions to a terminal state.

## Boundaries and acceptance

This module renews sandbox resources. It does not refresh daemon, relay, trace,
Agent Identity or third-party credentials. FC launch currently issues a
one-hour daemon/relay credential bundle. A reused sandbox can now survive across
successive tasks, but uninterrupted single-task execution beyond credential
expiry requires a separate Runtime credential-refresh integration. Do not
advertise this feature as unlimited task lifetime.

Before production enablement, use an isolated FC sandbox under the target account
to verify that a running process survives the original expiry after renewal,
that its files remain, and that provider TTL limits match the desired policy.
Those live provider checks are separate from the local tests.

For a short acceptance run, use an isolated deployment with `timeout_seconds=300`
and a newly created sandbox. Run a task that emits progress for about ten minutes.
Renewal becomes eligible below 100 seconds remaining. Search all server replicas
for `FC sandbox renewed` and correlate `task_id`, `attempt_id`, `sandbox_id`,
`old_expires_at` and `expires_at`. Independently read FC `endAt`, then verify the
original process continues beyond its original expiry and the task completes.

Inspect the existing rows with the execution task UUID:

```sql
SELECT t.id AS task_id, t.status, a.id AS attempt_id,
       a.sandbox_id, s.expires_at
FROM agent_task_queue t
JOIN agent_task_runtime_start_attempt a ON a.task_id = t.id
LEFT JOIN fc_e2b_sandbox_session s
  ON s.runtime_id = a.runtime_id AND s.sandbox_id = a.sandbox_id
  AND s.sandbox_backend = 'aliyun_fc' AND s.status = 'running'
WHERE t.id = '<task UUID>'::uuid
ORDER BY a.created_at DESC, a.id DESC
LIMIT 1;
```

There are no new `last_alive_at` or `checked_at` fields to inspect. If the task has
no reuse mapping, observe provider expiry directly. After the task is terminal and
any in-flight check finishes, a sandbox without other active tasks stops receiving
renewal. The feature switch still defaults off.

Local tests use `DATABASE_URL` for an isolated database with the existing schema and
`REDIS_TEST_URL` for Redis. They cover concurrent claims, lease takeover, stale
owner rejection, scheduling, active task selection, replacement attempts, task
completion during a provider lookup, warm reuse, expiry sync and provider errors.
FC HTTP calls use local test servers.
No test needs a real agent CLI or cloud credentials.

The development baseline `develop@ecbb03df1` has five independently reproduced
test failures: a trace-environment assertion, three stable-template fixtures/catalog
assertions, and duplicate legacy migration prefix 9093. Keep those separate from
renewal tests. The schema used for this revision's integration tests contains only
the 471 existing migrations, without the earlier draft's lifecycle table.
On 2026-09-08, 140 related top-level tests passed with the race detector after
excluding those five baseline failures. Query-generation consistency, `go vet`,
the server build and `git diff --check` also passed for the revised implementation.

Regenerate the new queries with `scripts/generate-fc-sandbox-sqlc.py --sqlc <path>`.
The script follows the fork's existing targeted generation convention; it defers
schema-only migration 271 for sqlc analysis and leaves unrelated generated APIs
untouched. `--check` verifies generated query files without writing them.

## Change history

- 2026-09-08: Initial local design added Redis-coordinated FC lifecycle checks and
  a separate execution-heartbeat table to address interrupted warm sandbox tasks.
- 2026-09-08: Revise before release to reuse the existing task, startup-attempt and
  sandbox-session tables, as requested. Remove the draft migrations, execution
  enrollment, heartbeat writes and check versions. Existing active tasks can now
  be checked directly; credential refresh remains a separate integration.
