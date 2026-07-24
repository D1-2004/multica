-- =========================
-- Agent dispatch acceptance
-- =========================

-- name: ClaimAgentDispatchAcceptance :one
INSERT INTO agent_dispatch_acceptance AS existing (
    endpoint_id,
    agent_id,
    target_identity,
    idempotency_key,
    request_fingerprint
) VALUES (
    @endpoint_id,
    @agent_id,
    @target_identity,
    @idempotency_key,
    @request_fingerprint
)
ON CONFLICT (endpoint_id, idempotency_key) DO UPDATE
SET lease_token = gen_random_uuid(),
    lease_expires_at = now() + interval '5 minutes',
    updated_at = now()
WHERE existing.status = 'pending'
  AND existing.request_fingerprint = EXCLUDED.request_fingerprint
  AND existing.agent_id = EXCLUDED.agent_id
  AND existing.target_identity = EXCLUDED.target_identity
  AND existing.lease_expires_at <= now()
RETURNING *;

-- name: GetAgentDispatchAcceptance :one
SELECT * FROM agent_dispatch_acceptance
WHERE endpoint_id = @endpoint_id
  AND idempotency_key = @idempotency_key;

-- name: CompleteAgentDispatchAcceptance :one
UPDATE agent_dispatch_acceptance
SET status = 'accepted',
    response_status = @response_status,
    response_content_type = sqlc.narg('response_content_type'),
    response_body = @response_body,
    root_task_id = sqlc.narg('root_task_id'),
    lease_token = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = @id
  AND status = 'pending'
  AND lease_token = @lease_token
RETURNING *;

-- name: ReleaseAgentDispatchAcceptance :execrows
DELETE FROM agent_dispatch_acceptance
WHERE id = @id
  AND status = 'pending'
  AND lease_token = @lease_token;

-- name: GetAgentDispatchRootTaskByAcceptance :one
SELECT task.*
FROM agent_task_queue task
WHERE task.agent_id = @agent_id
  AND task.parent_task_id IS NULL
  AND task.context #>> '{dispatch_idempotency_key}' = @idempotency_key::text
  AND task.context #>> '{dispatch_endpoint_id}' = @endpoint_id::text
  AND task.context #>> '{completion_callback,url}' = @callback_url::text
  AND task.context #>> '{completion_callback,target}' = @target_identity::text
ORDER BY task.created_at ASC
LIMIT 1;
