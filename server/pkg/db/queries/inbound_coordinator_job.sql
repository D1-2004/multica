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

-- name: GetInboundCoordinatorJobByIdempotency :one
SELECT * FROM inbound_coordinator_job
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND idempotency_key = @idempotency_key
ORDER BY created_at ASC
LIMIT 1;

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

-- name: ListPendingInboundCoordinatorJobsForConversationCollect :many
SELECT *
FROM inbound_coordinator_job
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND status = 'pending'
  AND attempt_count = 0
  AND last_error IS NULL
  AND available_at > statement_timestamp()
  AND command #>> '{event,data,conversation,openConversationId}' = @conversation_id
ORDER BY created_at DESC
FOR UPDATE;

-- name: UpdateInboundCoordinatorJobCollect :one
UPDATE inbound_coordinator_job
SET command = @command,
    available_at = @available_at,
    updated_at = now()
WHERE id = @id AND status = 'pending'
RETURNING *;

-- name: AppendCoordinatorUserMessage :execrows
UPDATE chat_message
SET content = @content
WHERE id = @id AND role = 'user';

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
-- Scene-wide in-flight matters, used when the current window has no trusted
-- delegator identity. "In flight" means a task that is executing or about to:
--
--   * 'deferred' rows are excluded. They are either scheduled for a later
--     fire_at or waiting for external input, so they are not occupying the
--     employee now and must not hold a capacity slot indefinitely.
--   * A future fire_at is excluded for the same reason.
--   * Non-running rows stop counting once they pass @stale_after_secs. The
--     runtime sweeper already fails genuinely dead dispatched/queued rows
--     (runtime_sweeper.go); this is the backstop for the window where it has
--     not run yet. 'running' rows always count: their liveness is proven by
--     the daemon heartbeat, so a healthy multi-hour run legitimately occupies
--     the delegator's slot and the park deadline bounds the caller's wait.
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
    (assoc_edge.dst_type = 'scene' AND assoc_edge.dst_id = @scene_id)
    OR (assoc_edge.src_type = 'scene' AND assoc_edge.src_id = @scene_id)
  )
  AND agent_task_queue.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')
  AND (agent_task_queue.fire_at IS NULL OR agent_task_queue.fire_at <= now())
  AND (
    agent_task_queue.status = 'running'
    OR COALESCE(agent_task_queue.started_at, agent_task_queue.dispatched_at, agent_task_queue.created_at)
       > now() - make_interval(secs => @stale_after_secs::double precision)
  );

-- name: ResolveAssocPersonKeys :many
-- Every identifier that may address this person on an existing edge: the given
-- keys, the canonical person_key they alias to, and that person's other
-- aliases. assoc picks a canonical key per event (decimal uid first), so the
-- same person can own older edges under a different identifier; closing over
-- both directions keeps one person on one budget.
WITH seed AS (
  SELECT DISTINCT btrim(key) AS key
  FROM unnest(@person_keys::text[]) AS key
  WHERE btrim(key) <> ''
), canonical AS (
  SELECT key FROM seed
  UNION
  SELECT assoc_person_alias.person_key
  FROM assoc_person_alias
  WHERE assoc_person_alias.workspace_id = @workspace_id
    AND assoc_person_alias.agent_id = @agent_id
    AND assoc_person_alias.alias_key IN (SELECT key FROM seed)
)
SELECT key FROM canonical
UNION
SELECT assoc_person_alias.alias_key
FROM assoc_person_alias
WHERE assoc_person_alias.workspace_id = @workspace_id
  AND assoc_person_alias.agent_id = @agent_id
  AND assoc_person_alias.person_key IN (SELECT key FROM canonical)
ORDER BY 1;

-- name: CountActiveDelegatorTasksForConversation :one
-- In-flight matters that spend this delegator's budget in this scene. The
-- person is matched through assoc_person_alias in both directions, so a uid,
-- staffId or openDingTalkId recorded on an earlier matter still resolves to
-- the same person. A matter with no open task_person edge at all is unattributed and
-- counts against every delegator: missing attribution must not hide running
-- work. The in-flight rules are identical to CountActiveTasksForConversation.
WITH seed AS (
  SELECT DISTINCT btrim(key) AS key
  FROM unnest(@person_keys::text[]) AS key
  WHERE btrim(key) <> ''
), canonical AS (
  SELECT key FROM seed
  UNION
  SELECT assoc_person_alias.person_key
  FROM assoc_person_alias
  WHERE assoc_person_alias.workspace_id = @workspace_id
    AND assoc_person_alias.agent_id = @agent_id
    AND assoc_person_alias.alias_key IN (SELECT key FROM seed)
), delegator AS (
  SELECT key FROM canonical
  UNION
  SELECT assoc_person_alias.alias_key
  FROM assoc_person_alias
  WHERE assoc_person_alias.workspace_id = @workspace_id
    AND assoc_person_alias.agent_id = @agent_id
    AND assoc_person_alias.person_key IN (SELECT key FROM canonical)
)
SELECT count(DISTINCT assoc_task.issue_id)::bigint
FROM assoc_edge scene_edge
JOIN assoc_task
  ON assoc_task.workspace_id = scene_edge.workspace_id
 AND assoc_task.agent_id = scene_edge.agent_id
 AND (
    (scene_edge.src_type = 'task' AND scene_edge.src_id = assoc_task.id::text)
    OR (scene_edge.dst_type = 'task' AND scene_edge.dst_id = assoc_task.id::text)
 )
JOIN agent_task_queue
  ON agent_task_queue.issue_id = assoc_task.issue_id
 AND agent_task_queue.agent_id = scene_edge.agent_id
WHERE scene_edge.workspace_id = @workspace_id
  AND scene_edge.agent_id = @agent_id
  AND scene_edge.rel = 'task_scene'
  AND scene_edge.status = 'open'
  AND (
    (scene_edge.dst_type = 'scene' AND scene_edge.dst_id = @scene_id)
    OR (scene_edge.src_type = 'scene' AND scene_edge.src_id = @scene_id)
  )
  AND (
    EXISTS (
      SELECT 1 FROM assoc_edge person_edge
      WHERE person_edge.workspace_id = scene_edge.workspace_id
        AND person_edge.agent_id = scene_edge.agent_id
        AND person_edge.rel = 'task_person'
        AND person_edge.status = 'open'
        AND (
          (person_edge.src_type = 'task' AND person_edge.src_id = assoc_task.id::text
            AND person_edge.dst_type = 'person'
            AND person_edge.dst_id IN (SELECT key FROM delegator))
          OR (person_edge.dst_type = 'task' AND person_edge.dst_id = assoc_task.id::text
            AND person_edge.src_type = 'person'
            AND person_edge.src_id IN (SELECT key FROM delegator))
        )
    )
    OR NOT EXISTS (
      SELECT 1 FROM assoc_edge any_person
      WHERE any_person.workspace_id = scene_edge.workspace_id
        AND any_person.agent_id = scene_edge.agent_id
        AND any_person.rel = 'task_person'
        AND any_person.status = 'open'
        AND (
          (any_person.src_type = 'task' AND any_person.src_id = assoc_task.id::text)
          OR (any_person.dst_type = 'task' AND any_person.dst_id = assoc_task.id::text)
        )
    )
  )
  AND agent_task_queue.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')
  AND (agent_task_queue.fire_at IS NULL OR agent_task_queue.fire_at <= now())
  AND (
    agent_task_queue.status = 'running'
    OR COALESCE(agent_task_queue.started_at, agent_task_queue.dispatched_at, agent_task_queue.created_at)
       > now() - make_interval(secs => @stale_after_secs::double precision)
  );

-- name: CountOpenSceneMattersForConversation :one
SELECT count(DISTINCT assoc_task.issue_id)::bigint
FROM assoc_edge
JOIN assoc_task
  ON assoc_task.workspace_id = assoc_edge.workspace_id
 AND assoc_task.agent_id = assoc_edge.agent_id
 AND (
    (assoc_edge.src_type = 'task' AND assoc_edge.src_id = assoc_task.id::text)
    OR (assoc_edge.dst_type = 'task' AND assoc_edge.dst_id = assoc_task.id::text)
 )
WHERE assoc_edge.workspace_id = @workspace_id
  AND assoc_edge.agent_id = @agent_id
  AND assoc_edge.rel = 'task_scene'
  AND assoc_edge.status = 'open'
  AND assoc_task.status IN ('open', 'waiting')
  AND (
    (assoc_edge.dst_type = 'scene' AND assoc_edge.dst_id = @scene_id)
    OR (assoc_edge.src_type = 'scene' AND assoc_edge.src_id = @scene_id)
  );

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
