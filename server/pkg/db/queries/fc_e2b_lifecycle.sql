-- name: ListFCE2BSandboxExecutionChecks :many
-- Existing task state determines eligibility; only the latest startup attempt
-- can identify its sandbox. Missing reuse mappings do not hide active tasks.
SELECT a.*
FROM agent_task_runtime_start_attempt a
JOIN agent_task_queue t ON t.id = a.task_id AND t.runtime_id = a.runtime_id
WHERE t.status IN ('dispatched', 'running', 'waiting_local_directory')
  AND a.backend = 'aliyun_fc' AND a.status IN ('starting', 'claimed')
  AND a.sandbox_id <> ''
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_runtime_start_attempt newer
    WHERE newer.task_id = a.task_id AND (newer.created_at, newer.id) > (a.created_at, a.id)
  )
ORDER BY COALESCE((
    SELECT min(s.expires_at) FROM fc_e2b_sandbox_session s
    WHERE s.runtime_id = a.runtime_id AND s.sandbox_id = a.sandbox_id
      AND s.sandbox_backend = 'aliyun_fc' AND s.status = 'running'
  ), '-infinity'::timestamptz), a.created_at, a.id;

-- name: UpdateFCE2BSandboxSessionExpiry :exec
UPDATE fc_e2b_sandbox_session
SET expires_at = @expires_at, updated_at = now()
WHERE sandbox_id = @sandbox_id AND runtime_id = @runtime_id
  AND sandbox_backend = 'aliyun_fc' AND status = 'running';

-- name: GetFCE2BSessionSandboxForLifecycle :one
-- Cached expiry can lag a successful provider renewal whose DB write failed.
-- The caller holds the scope lock and verifies actual provider lifetime.
SELECT sandbox_id FROM fc_e2b_sandbox_session
WHERE runtime_id = @runtime_id AND scope_type = @scope_type AND scope_id = @scope_id
  AND template = @template AND sandbox_backend = 'aliyun_fc'
  AND identity_fingerprint = '' AND status = 'running'
ORDER BY updated_at DESC LIMIT 1;

-- name: MarkFCE2BSandboxSessionMissing :exec
UPDATE fc_e2b_sandbox_session
SET status = 'stale', updated_at = now()
WHERE sandbox_id = @sandbox_id AND runtime_id = @runtime_id
  AND sandbox_backend = 'aliyun_fc' AND status = 'running';

-- name: LockFCE2BSandboxExecutionTask :one
SELECT t.id FROM agent_task_queue t
JOIN agent_task_runtime_start_attempt a ON a.task_id = t.id AND a.runtime_id = t.runtime_id
WHERE t.id = @task_id AND t.runtime_id = @runtime_id
  AND a.id = @attempt_id AND a.sandbox_id = @sandbox_id
  AND a.backend = 'aliyun_fc' AND a.status IN ('starting', 'claimed')
  AND t.status IN ('dispatched', 'running', 'waiting_local_directory')
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_runtime_start_attempt newer
    WHERE newer.task_id = t.id AND (newer.created_at, newer.id) > (a.created_at, a.id)
  )
FOR UPDATE OF t;
