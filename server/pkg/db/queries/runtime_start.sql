-- name: CreateAgentTaskRuntimeStartAttempt :one
INSERT INTO agent_task_runtime_start_attempt (
    id,
    task_id,
    runtime_id,
    backend,
    protocol,
    last_stage
) VALUES (
    @id,
    @task_id,
    @runtime_id,
    @backend,
    @protocol,
    'launch_started'
)
RETURNING *;

-- name: SupersedeAgentTaskRuntimeStartAttemptForLease :one
-- A launcher may be restarted after its 90-second task lease expires. Fence
-- replacement with the newly acquired lease token, preserve the abandoned
-- attempt as evidence, and keep the task -> attempt lock order used by claim
-- and failure finalization.
WITH lease_owner AS MATERIALIZED (
    SELECT task.id
    FROM agent_task_queue AS task
    WHERE task.id = @task_id
      AND task.runtime_id = @runtime_id
      AND task.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')
      AND task.runtime_launch_lease_token = @lease_token
      AND task.runtime_launch_lease_expires_at > now()
    FOR UPDATE
), superseded AS (
    UPDATE agent_task_runtime_start_attempt AS attempt
    SET status = 'superseded',
        last_stage = 'launch_restarted',
        error_code = 'START-ATTEMPT-SUPERSEDED',
        error_detail = 'superseded by a launcher holding a newer task lease',
        finished_at = now(),
        updated_at = now()
    WHERE attempt.task_id = @task_id
      AND attempt.runtime_id = @runtime_id
      AND attempt.status = 'starting'
      AND EXISTS (SELECT 1 FROM lease_owner)
    RETURNING attempt.id
)
SELECT EXISTS (SELECT 1 FROM lease_owner) AS lease_valid,
       count(*)::bigint AS superseded_count
FROM superseded;

-- name: UpdateAgentTaskRuntimeStartSandbox :one
UPDATE agent_task_runtime_start_attempt
SET sandbox_id = @sandbox_id,
    cold_start = @cold_start,
    last_stage = @stage,
    updated_at = now()
WHERE id = @id
  AND task_id = @task_id
  AND runtime_id = @runtime_id
  AND status = 'starting'
RETURNING *;

-- name: RecordAgentTaskRuntimeStartStage :one
UPDATE agent_task_runtime_start_attempt
SET last_stage = @stage,
    runner_started_at = CASE
        WHEN @stage = 'runner_started' THEN COALESCE(runner_started_at, now())
        ELSE runner_started_at
    END,
    daemon_started_at = CASE
        WHEN @stage = 'daemon_started' THEN COALESCE(daemon_started_at, now())
        ELSE daemon_started_at
    END,
    updated_at = now()
WHERE id = @id
  AND task_id = @task_id
  AND runtime_id = @runtime_id
  AND status = 'starting'
RETURNING *;

-- name: RecordAgentTaskRuntimeStartRunnerExecSubmitted :one
-- The background runner may post runner_started before sandbox exec returns.
-- Do not overwrite that stronger observation with the launcher's later
-- runner_exec_submitted bookkeeping update.
UPDATE agent_task_runtime_start_attempt
SET last_stage = 'runner_exec_submitted',
    updated_at = now()
WHERE id = @id
  AND task_id = @task_id
  AND runtime_id = @runtime_id
  AND status = 'starting'
  AND runner_started_at IS NULL
RETURNING *;

-- name: GetAgentTaskRuntimeStartAttempt :one
SELECT *
FROM agent_task_runtime_start_attempt
WHERE id = @id
  AND task_id = @task_id
  AND runtime_id = @runtime_id;

-- name: GetStartingAgentTaskRuntimeStartAttemptByTask :one
SELECT *
FROM agent_task_runtime_start_attempt
WHERE task_id = @task_id
  AND runtime_id = @runtime_id
  AND status = 'starting';

-- name: GetLatestAgentTaskRuntimeStartAttemptByTask :one
-- Skill visibility and other execution-time policy must follow the backend
-- that actually launched this task, even after the shared Runtime row rotates
-- to another backend or artifact.
SELECT *
FROM agent_task_runtime_start_attempt
WHERE task_id = @task_id
  AND runtime_id = @runtime_id
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: FinalizeAgentTaskRuntimeStartAttemptForTask :one
WITH claimable_task AS MATERIALIZED (
    SELECT id, context
    FROM agent_task_queue
    WHERE id = @task_id
      AND runtime_id = @runtime_id
      AND status IN ('dispatched', 'running', 'waiting_local_directory')
    FOR UPDATE
)
UPDATE agent_task_runtime_start_attempt AS attempt
SET status = 'claimed',
    last_stage = 'claim_finalized',
    claim_finalized_at = now(),
    finished_at = now(),
    updated_at = now()
WHERE attempt.task_id = @task_id
  AND attempt.runtime_id = @runtime_id
  AND attempt.status = 'starting'
  AND EXISTS (
      SELECT 1
      FROM claimable_task
  )
  AND EXISTS (
      SELECT 1
      FROM claimable_task AS task
      WHERE EXISTS (
          SELECT 1
          FROM task_token AS token
          WHERE token.task_id = attempt.task_id
      )
      OR (
          @allow_tokenless_a2a::boolean
          AND task.context->>'multica_origin' = 'a2a'
      )
  )
RETURNING attempt.*;

-- name: FailAgentTaskRuntimeStartAttempt :one
UPDATE agent_task_runtime_start_attempt
SET status = @status,
    last_stage = @stage,
    error_code = @error_code,
    error_detail = @error_detail,
    finished_at = now(),
    updated_at = now()
WHERE id = @id
  AND task_id = @task_id
  AND runtime_id = @runtime_id
  AND status = 'starting'
RETURNING *;

-- name: FailAgentTaskForRuntimeStartAttempt :one
-- Lock and fail the task before the attempt. Claim finalization uses the same
-- task-then-attempt order. The EXISTS condition is rechecked if this update
-- waits behind a concurrent claim, so a claim that already finalized wins.
-- A token written by an older backend replica is also authoritative claim
-- proof during a rolling deployment.
WITH fail_candidate AS MATERIALIZED (
    SELECT task.id
    FROM agent_task_queue AS task
    WHERE task.id = @task_id
      AND task.runtime_id = @runtime_id
      AND task.status IN ('queued', 'dispatched')
      AND NOT EXISTS (
          SELECT 1
          FROM task_token AS token
          WHERE token.task_id = task.id
      )
    FOR UPDATE
)
UPDATE agent_task_queue AS task
SET status = 'failed',
    completed_at = now(),
    error = @error,
    failure_reason = 'runtime_start_failed',
    prepare_lease_expires_at = NULL
WHERE task.id IN (SELECT id FROM fail_candidate)
  AND EXISTS (
      SELECT 1
      FROM agent_task_runtime_start_attempt AS attempt
      WHERE attempt.id = @attempt_id
        AND attempt.task_id = task.id
        AND attempt.runtime_id = task.runtime_id
        AND attempt.status = 'starting'
  )
RETURNING task.*;

-- name: MarkAgentTaskRuntimeStartBlocked :one
UPDATE agent_task_runtime_start_attempt
SET status = 'blocked',
    last_stage = CASE WHEN last_stage = 'dsh_host_waiting' THEN last_stage ELSE 'task_serialization_blocked' END,
    error_code = CASE WHEN last_stage = 'dsh_host_waiting' THEN 'DSH-HOST-WAITING' ELSE 'TASK-SERIALIZATION-BLOCKED' END,
    finished_at = now(),
    updated_at = now()
WHERE id = @id
  AND task_id = @task_id
  AND runtime_id = @runtime_id
  AND status = 'starting'
RETURNING *;

-- name: ListDSHHostWaitingTasks :many
-- A Profile build or host reconciliation can finish without a running task
-- or open browser to wake this queued launch. Reuse the normal launch lease.
SELECT task.*
FROM agent_task_queue AS task
JOIN agent ON agent.id = task.agent_id AND agent.archived_at IS NULL
JOIN LATERAL (
    SELECT attempt.backend, attempt.status, attempt.error_code, attempt.finished_at, attempt.created_at,
           attempt.updated_at, attempt.last_stage, attempt.runner_started_at,
           attempt.daemon_started_at, attempt.claim_finalized_at
    FROM agent_task_runtime_start_attempt AS attempt
    WHERE attempt.task_id = task.id AND attempt.runtime_id = task.runtime_id
    ORDER BY attempt.created_at DESC, attempt.id DESC
    LIMIT 1
) AS latest ON true
WHERE task.status = 'queued'
  AND latest.backend = 'aliyun_fc'
  AND (NOT @events_only::boolean OR NOT @scoped::boolean OR task.agent_id = ANY(@rollout_agent_ids::uuid[]))
  AND (NOT @events_only::boolean OR EXISTS (SELECT 1 FROM runtime_readiness_event AS ready
       WHERE ready.workspace_id=agent.workspace_id
         AND ready.agent_id IN (task.agent_id,'00000000-0000-0000-0000-000000000000'::uuid)
         AND ready.event_at >= latest.created_at))
  AND (
      (latest.status = 'blocked'
       AND latest.error_code = 'DSH-HOST-WAITING'
       AND ((@dsh_event_wakeup::boolean AND (NOT @scoped::boolean OR task.agent_id = ANY(@rollout_agent_ids::uuid[]))) OR latest.finished_at <= now() - interval '30 seconds'))
      OR
      -- A rolling deployment or lost launcher can leave a pre-runner attempt
      -- starting forever. The normal lease/CAS path supersedes it safely.
      (latest.status = 'starting'
       AND (latest.last_stage IN ('launch_started', 'sandbox_resolving', 'dsh_host_waiting')
            OR (@recover_abandoned_launches::boolean AND (NOT @scoped::boolean OR task.agent_id = ANY(@rollout_agent_ids::uuid[])) AND latest.last_stage IN ('template_resolved', 'sandbox_ready', 'runner_probing', 'runner_probe_succeeded', 'task_environment_preparing', 'daemon_token_preparing')))
       AND ((@recover_abandoned_launches::boolean AND (NOT @scoped::boolean OR task.agent_id = ANY(@rollout_agent_ids::uuid[]))) OR latest.updated_at <= now() - interval '2 minutes')
       AND latest.runner_started_at IS NULL
       AND latest.daemon_started_at IS NULL
       AND latest.claim_finalized_at IS NULL
       AND ((@recover_abandoned_launches::boolean AND (NOT @scoped::boolean OR task.agent_id = ANY(@rollout_agent_ids::uuid[]))) OR EXISTS (SELECT 1 FROM dsh_employee_session AS session
                   WHERE session.workspace_id = agent.workspace_id
                     AND session.agent_id = task.agent_id
                     AND session.scope_id = COALESCE(task.issue_id, task.chat_session_id, task.id))))
  )
  AND (task.runtime_launch_lease_expires_at IS NULL OR task.runtime_launch_lease_expires_at <= now())
  AND COALESCE(task.context->>'deap_dws_token_required', 'false') <> 'true'
  AND (NOT (@recover_abandoned_launches::boolean AND (NOT @scoped::boolean OR task.agent_id = ANY(@rollout_agent_ids::uuid[]))) OR NOT EXISTS (SELECT 1 FROM task_token WHERE task_id = task.id))
ORDER BY task.created_at, task.id
LIMIT 32;

-- name: MarkAgentTaskRuntimeStartCapacityWaiting :one
-- Capacity pressure is not a task failure. Lock the queued task before its
-- startup attempt, matching the claim/failure lock order, so a concurrent
-- daemon claim wins cleanly and the attempt is never mislabeled as waiting
-- after the task has already left the queue.
WITH queued_task AS MATERIALIZED (
    SELECT task.id
    FROM agent_task_queue AS task
    WHERE task.id = @task_id
      AND task.runtime_id = @runtime_id
      AND task.status = 'queued'
    FOR UPDATE
)
UPDATE agent_task_runtime_start_attempt AS attempt
SET status = 'blocked',
    last_stage = 'sandbox_capacity_waiting',
    error_code = 'ASB-CAPACITY-WAITING',
    error_detail = @error_detail,
    finished_at = now(),
    updated_at = now()
WHERE attempt.id = @id
  AND attempt.task_id = @task_id
  AND attempt.runtime_id = @runtime_id
  AND attempt.status = 'starting'
  AND EXISTS (SELECT 1 FROM queued_task)
RETURNING attempt.*;

-- name: SupersedeAgentTaskRuntimeStartAttemptForTerminalTask :execrows
-- A cancellation can win the task row lock while the launcher is converting a
-- capacity error into a blocked attempt. Close that observability record only
-- when the task is already terminal; a concurrent claim keeps ownership of the
-- normal claim finalizer.
UPDATE agent_task_runtime_start_attempt AS attempt
SET status = 'superseded',
    last_stage = 'task_terminal_before_capacity_wait',
    error_code = 'TASK-TERMINAL-BEFORE-CAPACITY-WAIT',
    error_detail = 'task became terminal before ASB capacity wait was recorded',
    finished_at = now(),
    updated_at = now()
WHERE attempt.id = @id
  AND attempt.task_id = @task_id
  AND attempt.runtime_id = @runtime_id
  AND attempt.status = 'starting'
  AND EXISTS (
      SELECT 1
      FROM agent_task_queue AS task
      WHERE task.id = attempt.task_id
        AND task.runtime_id = attempt.runtime_id
        AND task.status IN ('completed', 'failed', 'cancelled')
  );

-- name: ListASBCapacityWaitingTasks :many
-- Only the latest startup attempt controls retry eligibility. A later
-- serialization block suppresses an older capacity-wait record, while an ASB
-- attempt abandoned past the task-launch lease is recovered after a crash.
-- Return a bounded batch per Runtime without a global LIMIT so a busy tenant
-- cannot hide every other tenant before exact credential scopes are resolved.
-- Sandbox capacity is FIFO for every task kind; chat and retry priorities do
-- not grant preferential access to a tenant's instance quota.
WITH eligible AS (
SELECT task.id,
       row_number() OVER (
           PARTITION BY task.runtime_id ORDER BY task.created_at ASC, task.id ASC
       ) AS runtime_position
FROM agent_task_queue AS task
JOIN LATERAL (
    SELECT attempt.backend,
           attempt.status,
           attempt.error_code,
           attempt.finished_at,
           attempt.created_at,
           attempt.updated_at
    FROM agent_task_runtime_start_attempt AS attempt
    WHERE attempt.task_id = task.id
      AND attempt.runtime_id = task.runtime_id
    ORDER BY attempt.created_at DESC, attempt.id DESC
    LIMIT 1
) AS latest_attempt ON true
WHERE task.status = 'queued'
  -- DEAP DWS tokens are request-bound and intentionally absent from durable
  -- task context, so those A2A launches cannot be resumed by this worker.
  AND COALESCE(task.context->>'deap_dws_token_required', 'false') <> 'true'
  AND (
      (
          latest_attempt.status = 'blocked'
          AND latest_attempt.error_code = 'ASB-CAPACITY-WAITING'
          AND (COALESCE(latest_attempt.finished_at, latest_attempt.updated_at)
              <= now() - make_interval(secs => sqlc.arg('retry_seconds')::double precision)
              OR (latest_attempt.created_at <= sqlc.narg('wake_before')::timestamptz
                  AND (NOT sqlc.arg('scoped')::boolean OR task.agent_id = ANY(sqlc.arg('rollout_agent_ids')::uuid[]))))
      )
      OR
      (
          latest_attempt.backend = 'asb'
          AND latest_attempt.status = 'starting'
          AND latest_attempt.updated_at
              <= now() - make_interval(secs => sqlc.arg('stale_seconds')::double precision)
      )
  )
  AND (
      task.runtime_launch_lease_expires_at IS NULL
      OR task.runtime_launch_lease_expires_at <= now()
  )
)
SELECT task.*
FROM eligible
JOIN agent_task_queue AS task ON task.id = eligible.id
WHERE eligible.runtime_position <= sqlc.arg('max_per_runtime')::integer
ORDER BY task.runtime_id, task.created_at ASC, task.id ASC;

-- name: ListLatestSandboxIDsByTaskIDs :many
-- Latest non-empty sandbox id per task, for issue execution-log rows.
SELECT DISTINCT ON (task_id)
    task_id,
    sandbox_id
FROM agent_task_runtime_start_attempt
WHERE task_id = ANY(sqlc.arg('task_ids')::uuid[])
  AND btrim(sandbox_id) <> ''
ORDER BY task_id, updated_at DESC, created_at DESC, id DESC;

-- name: ExpireDSHHostWaitingTasks :many
-- Use task-then-attempt locking, as in claim/failure finalization. A live
-- launcher, claim token, or newer non-waiting attempt owns the task instead.
WITH victims AS MATERIALIZED (
    SELECT task.id
    FROM agent_task_queue AS task
    JOIN LATERAL (
        SELECT attempt.id, attempt.status, attempt.error_code
        FROM agent_task_runtime_start_attempt AS attempt
        WHERE attempt.task_id = task.id AND attempt.runtime_id = task.runtime_id
        ORDER BY attempt.created_at DESC, attempt.id DESC LIMIT 1
    ) AS latest ON true
    WHERE task.status = 'queued'
      AND (NOT @scoped::boolean OR task.agent_id = ANY(@rollout_agent_ids::uuid[]))
      AND latest.status = 'blocked' AND latest.error_code = 'DSH-HOST-WAITING'
      AND (task.runtime_launch_lease_expires_at IS NULL OR task.runtime_launch_lease_expires_at <= now())
      AND NOT EXISTS (SELECT 1 FROM task_token WHERE task_id = task.id)
      AND EXISTS (
          SELECT 1 FROM agent_task_runtime_start_attempt AS first_wait
          WHERE first_wait.task_id = task.id AND first_wait.runtime_id = task.runtime_id
            AND first_wait.error_code = 'DSH-HOST-WAITING'
            AND first_wait.finished_at <= now() - interval '10 minutes'
      )
    ORDER BY task.created_at, task.id
    LIMIT 32
    FOR UPDATE OF task SKIP LOCKED
), failed AS (
    UPDATE agent_task_queue AS task
    SET status = 'failed', completed_at = now(), prepare_lease_expires_at = NULL,
        error = 'DSH host preparation exceeded the 10 minute waiting limit. Please retry.',
        failure_reason = 'runtime_start_failed'
    WHERE task.id IN (SELECT id FROM victims) AND task.status = 'queued'
    RETURNING task.*
), attempts AS (
    UPDATE agent_task_runtime_start_attempt AS attempt
    SET status = 'failed', error_code = 'DSH-HOST-WAIT-TIMEOUT',
        error_detail = 'DSH host preparation exceeded the 10 minute waiting limit',
        updated_at = now()
    WHERE attempt.task_id IN (SELECT id FROM failed)
      AND attempt.id = (
          SELECT latest.id FROM agent_task_runtime_start_attempt AS latest
          WHERE latest.task_id = attempt.task_id AND latest.runtime_id = attempt.runtime_id
          ORDER BY latest.created_at DESC, latest.id DESC LIMIT 1
      )
    RETURNING attempt.id
)
SELECT failed.* FROM failed;
