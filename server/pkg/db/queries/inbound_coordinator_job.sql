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
    SELECT job.id
    FROM inbound_coordinator_job job
    WHERE (
        (job.status = 'pending' AND job.available_at <= now())
        OR (job.status = 'running' AND job.lease_expires_at <= now())
    )
    AND NOT EXISTS (
        SELECT 1
        FROM inbound_coordinator_job running
        WHERE running.workspace_id = job.workspace_id
          AND running.agent_id = job.agent_id
          AND running.status = 'running'
          AND running.lease_expires_at > now()
          AND running.id IS DISTINCT FROM job.id
          AND COALESCE(job.command #>> '{event,data,conversation,openConversationId}', '') <> ''
          AND running.command #>> '{event,data,conversation,openConversationId}'
            = job.command #>> '{event,data,conversation,openConversationId}'
    )
    ORDER BY job.available_at, job.created_at
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

-- name: CountRunningInboundCoordinatorJobsForConversation :one
SELECT count(*)::bigint
FROM inbound_coordinator_job
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND status = 'running'
  AND lease_expires_at > now()
  AND id <> @exclude_id
  AND command #>> '{event,data,conversation,openConversationId}' = @conversation_id;

-- name: CountActiveTasksForConversation :one
SELECT count(DISTINCT assoc_task.issue_id)::bigint
FROM assoc_edge
JOIN assoc_task
  ON assoc_task.workspace_id = assoc_edge.workspace_id
 AND assoc_task.agent_id = assoc_edge.agent_id
 AND (
    (assoc_edge.src_type = 'task' AND assoc_edge.src_id = assoc_task.id::text)
    OR (assoc_edge.dst_type = 'task' AND assoc_edge.dst_id = assoc_task.id::text)
 )
JOIN agent_task_queue
  ON agent_task_queue.issue_id = assoc_task.issue_id
 AND agent_task_queue.agent_id = assoc_edge.agent_id
WHERE assoc_edge.workspace_id = @workspace_id
  AND assoc_edge.agent_id = @agent_id
  AND assoc_edge.rel = 'task_scene'
  AND assoc_edge.status = 'open'
  AND (
    (assoc_edge.dst_type = 'scene' AND assoc_edge.dst_id = @conversation_id)
    OR (assoc_edge.src_type = 'scene' AND assoc_edge.src_id = @conversation_id)
  )
  AND agent_task_queue.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory');

-- name: ParkInboundCoordinatorJob :execrows
UPDATE inbound_coordinator_job
SET status = 'pending',
    available_at = @available_at,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = @last_error,
    attempt_count = GREATEST(attempt_count - 1, 0),
    updated_at = now()
WHERE id = @id AND status = 'running' AND lease_token = @lease_token;
