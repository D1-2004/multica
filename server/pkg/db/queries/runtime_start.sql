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
    last_stage = 'task_serialization_blocked',
    error_code = 'TASK-SERIALIZATION-BLOCKED',
    finished_at = now(),
    updated_at = now()
WHERE id = @id
  AND task_id = @task_id
  AND runtime_id = @runtime_id
  AND status = 'starting'
RETURNING *;

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
-- Return one task per Runtime without a global LIMIT so a busy tenant cannot
-- hide every other tenant before exact credential scopes are resolved in Go.
SELECT DISTINCT ON (task.runtime_id) task.*
FROM agent_task_queue AS task
JOIN LATERAL (
    SELECT attempt.backend,
           attempt.status,
           attempt.error_code,
           attempt.finished_at,
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
          AND COALESCE(latest_attempt.finished_at, latest_attempt.updated_at)
              <= now() - make_interval(secs => sqlc.arg('retry_seconds')::double precision)
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
ORDER BY task.runtime_id, task.priority DESC, task.created_at ASC, task.id ASC;
