-- name: CreateInboundCoordinatorJob :one
INSERT INTO inbound_coordinator_job (
    acceptance_id, workspace_id, agent_id, user_id, endpoint_namespace_id,
    dispatch_endpoint_id, idempotency_key, command, chat_session_id, user_message_id,
    available_at
) VALUES (
    @acceptance_id, @workspace_id, @agent_id, @user_id, @endpoint_namespace_id,
    @dispatch_endpoint_id, @idempotency_key, @command, @chat_session_id, @user_message_id,
    @available_at
)
RETURNING *;

-- name: GetInboundCoordinatorJobByAcceptance :one
SELECT * FROM inbound_coordinator_job WHERE acceptance_id = @acceptance_id;

-- name: ClaimInboundCoordinatorJob :one
WITH candidate AS (
    SELECT id
    FROM inbound_coordinator_job
    WHERE (
        status = 'pending' AND available_at <= now()
    ) OR (
        status = 'running' AND lease_expires_at <= now()
    )
    ORDER BY available_at, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE inbound_coordinator_job AS job
SET status = 'running',
    attempt_count = job.attempt_count + 1,
    lease_token = gen_random_uuid(),
    lease_expires_at = now() + interval '1 minute',
    updated_at = now()
FROM candidate
WHERE job.id = candidate.id
RETURNING job.*;

-- name: CompleteInboundCoordinatorJob :execrows
UPDATE inbound_coordinator_job
SET status = 'completed',
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = NULL,
    updated_at = now()
WHERE id = @id AND status = 'running' AND lease_token = @lease_token;

-- name: RetryInboundCoordinatorJob :execrows
UPDATE inbound_coordinator_job
SET status = 'pending',
    available_at = @available_at,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = @last_error,
    updated_at = now()
WHERE id = @id AND status = 'running' AND lease_token = @lease_token;

-- name: FailInboundCoordinatorJob :execrows
UPDATE inbound_coordinator_job
SET status = 'failed',
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = @last_error,
    updated_at = now()
WHERE id = @id AND status = 'running' AND lease_token = @lease_token;

-- name: IsCoordinatorChatSession :one
SELECT EXISTS (
    SELECT 1 FROM inbound_coordinator_job WHERE chat_session_id = @chat_session_id
) AS is_coordinator;

-- name: GetInboundCoordinatorJobStatusByChatSession :one
SELECT status
FROM inbound_coordinator_job
WHERE chat_session_id = @chat_session_id;

-- name: DeleteInboundCoordinatorJobByChatSession :exec
DELETE FROM inbound_coordinator_job
WHERE chat_session_id = @chat_session_id;

-- name: CoordinatorChatMessageExists :one
SELECT EXISTS (
    SELECT 1
    FROM chat_message
    WHERE chat_session_id = @chat_session_id
      AND role = 'assistant'
      AND message_kind = 'coordinator'
) AS exists;

-- name: FindPendingInboundCoordinatorJobForConversation :one
SELECT *
FROM inbound_coordinator_job
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND status = 'pending'
  AND command #>> '{event,data,conversation,openConversationId}' = @conversation_id
ORDER BY created_at DESC
LIMIT 1
FOR UPDATE;

-- name: ListPendingInboundCoordinatorJobsForConversation :many
SELECT *
FROM inbound_coordinator_job
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND status = 'pending'
  AND id <> @exclude_id
  AND command #>> '{event,data,conversation,openConversationId}' = @conversation_id
ORDER BY created_at ASC
FOR UPDATE;

-- name: UpdateInboundCoordinatorJobCollect :one
UPDATE inbound_coordinator_job
SET command = @command,
    available_at = @available_at,
    updated_at = now()
WHERE id = @id AND status = 'pending'
RETURNING *;

-- name: CompleteCoalescedInboundCoordinatorJob :execrows
UPDATE inbound_coordinator_job
SET status = 'completed',
    last_error = NULL,
    updated_at = now()
WHERE id = @id AND status = 'pending';

-- name: AppendCoordinatorUserMessage :execrows
UPDATE chat_message
SET content = @content
WHERE id = @id AND role = 'user';

-- name: UpdateInboundCoordinatorJobCommand :execrows
UPDATE inbound_coordinator_job
SET command = @command, updated_at = now()
WHERE id = @id AND status = 'running';
