-- Scene Memory of one Agent work scene, keyed by scene_id
-- (docs/agent-scene.md). Identity columns live in agent_scene only.

-- name: GetAgentSceneMemory :one
SELECT * FROM agent_scene_memory
WHERE scene_id = @scene_id AND workspace_id = @workspace_id AND agent_id = @agent_id;

-- name: ListAgentSceneMemoryByAgent :many
SELECT * FROM agent_scene_memory
WHERE workspace_id = @workspace_id AND agent_id = @agent_id
ORDER BY updated_at DESC, scene_id DESC
LIMIT @list_limit;

-- name: MarkAgentSceneMemoryDirty :one
WITH upsert AS (
    INSERT INTO agent_scene_memory (
        scene_id, workspace_id, agent_id,
        dirty_revision, dirty_since, dirty_through_at, dirty_through_evidence_id,
        available_at, last_trigger_job_id, last_trigger_coord_trace_id,
        last_trigger_idempotency_key, last_trigger_at, last_trigger_evidence_id,
        pending_from_at, pending_from_evidence_id,
        blocked_at, last_error_code, last_error
    ) VALUES (
        @scene_id, @workspace_id, @agent_id,
        1, now(), @dirty_through_at, @dirty_through_evidence_id,
        now() + interval '4 seconds', @last_trigger_job_id, @last_trigger_coord_trace_id,
        @last_trigger_idempotency_key, @dirty_through_at, @dirty_through_evidence_id,
        @dirty_through_at, @dirty_through_evidence_id,
        NULL, '', ''
    )
    ON CONFLICT (scene_id)
    DO UPDATE SET
        dirty_revision = agent_scene_memory.dirty_revision + 1,
        dirty_since = COALESCE(agent_scene_memory.dirty_since, now()),
        dirty_through_at = CASE
            WHEN agent_scene_memory.dirty_through_at IS NULL THEN EXCLUDED.dirty_through_at
            WHEN EXCLUDED.dirty_through_at IS NULL THEN agent_scene_memory.dirty_through_at
            WHEN (EXCLUDED.dirty_through_at, EXCLUDED.dirty_through_evidence_id)
               > (agent_scene_memory.dirty_through_at, agent_scene_memory.dirty_through_evidence_id)
            THEN EXCLUDED.dirty_through_at
            ELSE agent_scene_memory.dirty_through_at
        END,
        dirty_through_evidence_id = CASE
            WHEN agent_scene_memory.dirty_through_at IS NULL THEN EXCLUDED.dirty_through_evidence_id
            WHEN EXCLUDED.dirty_through_at IS NULL THEN agent_scene_memory.dirty_through_evidence_id
            WHEN (EXCLUDED.dirty_through_at, EXCLUDED.dirty_through_evidence_id)
               > (agent_scene_memory.dirty_through_at, agent_scene_memory.dirty_through_evidence_id)
            THEN EXCLUDED.dirty_through_evidence_id
            ELSE agent_scene_memory.dirty_through_evidence_id
        END,
        available_at = LEAST(
            COALESCE(agent_scene_memory.dirty_since, now()) + interval '2 minutes',
            now() + CASE
                WHEN agent_scene_memory.bootstrapped_at IS NULL THEN interval '4 seconds'
                ELSE interval '30 seconds'
            END
        ),
        last_trigger_job_id = EXCLUDED.last_trigger_job_id,
        last_trigger_coord_trace_id = EXCLUDED.last_trigger_coord_trace_id,
        last_trigger_idempotency_key = EXCLUDED.last_trigger_idempotency_key,
        last_trigger_at = EXCLUDED.last_trigger_at,
        last_trigger_evidence_id = EXCLUDED.last_trigger_evidence_id,
        pending_from_at = CASE
            WHEN agent_scene_memory.pending_from_at IS NULL THEN EXCLUDED.pending_from_at
            WHEN EXCLUDED.pending_from_at IS NULL THEN agent_scene_memory.pending_from_at
            WHEN (EXCLUDED.pending_from_at, EXCLUDED.pending_from_evidence_id)
               < (agent_scene_memory.pending_from_at, agent_scene_memory.pending_from_evidence_id)
            THEN EXCLUDED.pending_from_at
            ELSE agent_scene_memory.pending_from_at
        END,
        pending_from_evidence_id = CASE
            WHEN agent_scene_memory.pending_from_at IS NULL THEN EXCLUDED.pending_from_evidence_id
            WHEN EXCLUDED.pending_from_at IS NULL THEN agent_scene_memory.pending_from_evidence_id
            WHEN (EXCLUDED.pending_from_at, EXCLUDED.pending_from_evidence_id)
               < (agent_scene_memory.pending_from_at, agent_scene_memory.pending_from_evidence_id)
            THEN EXCLUDED.pending_from_evidence_id
            ELSE agent_scene_memory.pending_from_evidence_id
        END,
        blocked_at = NULL,
        last_error_code = '',
        last_error = '',
        updated_at = now()
    WHERE NULLIF(btrim(EXCLUDED.last_trigger_idempotency_key), '') IS NULL
       OR agent_scene_memory.last_trigger_idempotency_key IS DISTINCT FROM EXCLUDED.last_trigger_idempotency_key
    RETURNING *
)
SELECT * FROM upsert
UNION ALL
SELECT agent_scene_memory.* FROM agent_scene_memory
WHERE NOT EXISTS (SELECT 1 FROM upsert)
  AND scene_id = @scene_id
  AND workspace_id = @workspace_id
  AND agent_id = @agent_id
LIMIT 1;

-- name: ClaimAgentSceneMemory :one
WITH candidate AS (
    SELECT sm.scene_id
    FROM agent_scene_memory sm
    INNER JOIN agent a ON a.id = sm.agent_id AND a.workspace_id = sm.workspace_id
    INNER JOIN agent_scene s ON s.id = sm.scene_id AND s.workspace_id = sm.workspace_id AND s.agent_id = sm.agent_id
    WHERE sm.dirty_revision > sm.flushed_revision
      AND sm.available_at <= now()
      AND sm.blocked_at IS NULL
      AND (sm.lease_token IS NULL OR sm.lease_expires_at <= now())
      AND a.agent_scene_memory_write_enabled
      AND a.archived_at IS NULL
    ORDER BY sm.available_at, sm.dirty_since NULLS FIRST, sm.scene_id
    FOR UPDATE OF sm SKIP LOCKED
    LIMIT 1
)
UPDATE agent_scene_memory AS row
SET lease_token = gen_random_uuid(),
    lease_expires_at = now() + interval '2 minutes',
    attempt_count = row.attempt_count + 1,
    lease_target_dirty_revision = row.dirty_revision,
    lease_target_through_at = row.dirty_through_at,
    lease_target_through_evidence_id = row.dirty_through_evidence_id,
    lease_expected_memory_revision = row.memory_revision,
    updated_at = now()
FROM candidate
WHERE row.scene_id = candidate.scene_id
RETURNING row.*;

-- name: RenewAgentSceneMemoryLease :execrows
UPDATE agent_scene_memory
SET lease_expires_at = now() + interval '2 minutes',
    updated_at = now()
WHERE scene_id = @scene_id
  AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: CommitAgentSceneMemoryBatch :one
UPDATE agent_scene_memory
SET memory_text = CASE WHEN @replace_text::boolean THEN @memory_text ELSE memory_text END,
    memory_revision = CASE WHEN @replace_text::boolean THEN memory_revision + 1 ELSE memory_revision END,
    source_cursor_at = @source_cursor_at,
    source_cursor_evidence_id = @source_cursor_evidence_id,
    bootstrapped_at = COALESCE(bootstrapped_at, now()),
    last_flush_meta = @last_flush_meta,
    last_flushed_at = now(),
    history_resume_before = NULL,
    attempt_count = 0,
    updated_at = now()
WHERE scene_id = @scene_id
  AND lease_token = @lease_token
  AND lease_expires_at > now()
  AND memory_revision = @expected_memory_revision
RETURNING *;

-- name: FinishAgentSceneMemoryClaim :execrows
UPDATE agent_scene_memory
SET flushed_revision = lease_target_dirty_revision,
    dirty_since = CASE
        WHEN dirty_revision > lease_target_dirty_revision THEN dirty_since
        ELSE NULL
    END,
    lease_token = NULL,
    lease_expires_at = NULL,
    lease_target_dirty_revision = NULL,
    lease_target_through_at = NULL,
    lease_target_through_evidence_id = '',
    lease_expected_memory_revision = NULL,
    last_error_code = '',
    last_error = '',
    attempt_count = 0,
    pending_from_at = CASE
        WHEN dirty_revision > lease_target_dirty_revision THEN pending_from_at
        ELSE NULL
    END,
    pending_from_evidence_id = CASE
        WHEN dirty_revision > lease_target_dirty_revision THEN pending_from_evidence_id
        ELSE ''
    END,
    history_resume_before = NULL,
    updated_at = now()
WHERE scene_id = @scene_id
  AND lease_token = @lease_token
  AND lease_expires_at > now()
  AND source_cursor_at IS NOT NULL
  AND lease_target_through_at IS NOT NULL
  AND (source_cursor_at, source_cursor_evidence_id)
      >= (lease_target_through_at, lease_target_through_evidence_id);

-- name: RetryAgentSceneMemoryClaim :execrows
UPDATE agent_scene_memory
SET available_at = now() + make_interval(secs => @delay_seconds::double precision),
    lease_token = NULL,
    lease_expires_at = NULL,
    lease_target_dirty_revision = NULL,
    lease_target_through_at = NULL,
    lease_target_through_evidence_id = '',
    lease_expected_memory_revision = NULL,
    last_error_code = @last_error_code,
    last_error = @last_error,
    updated_at = now()
WHERE scene_id = @scene_id
  AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: ReleaseAgentSceneMemoryPending :execrows
UPDATE agent_scene_memory
SET available_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    lease_target_dirty_revision = NULL,
    lease_target_through_at = NULL,
    lease_target_through_evidence_id = '',
    lease_expected_memory_revision = NULL,
    updated_at = now()
WHERE scene_id = @scene_id
  AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: BlockAgentSceneMemory :execrows
UPDATE agent_scene_memory
SET blocked_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    lease_target_dirty_revision = NULL,
    lease_target_through_at = NULL,
    lease_target_through_evidence_id = '',
    lease_expected_memory_revision = NULL,
    last_error_code = @last_error_code,
    last_error = @last_error,
    updated_at = now()
WHERE scene_id = @scene_id
  AND lease_token = @lease_token
  AND lease_expires_at > now()
  AND dirty_revision = lease_target_dirty_revision;

-- name: ResetAgentSceneMemory :one
INSERT INTO agent_scene_memory (
    scene_id, workspace_id, agent_id,
    memory_text, memory_revision, bootstrapped_at,
    source_cursor_at, source_cursor_evidence_id,
    dirty_revision, flushed_revision
) VALUES (
    @scene_id, @workspace_id, @agent_id,
    '', 0, now(), COALESCE(@source_cursor_at, date_trunc('second', now())),
    @source_cursor_evidence_id, 0, 0
)
ON CONFLICT (scene_id)
DO UPDATE SET
    memory_text = '',
    memory_revision = agent_scene_memory.memory_revision + 1,
    last_flush_meta = '{}'::jsonb,
    last_flushed_at = NULL,
    source_cursor_at = COALESCE(@source_cursor_at, date_trunc('second', now())),
    source_cursor_evidence_id = @source_cursor_evidence_id,
    bootstrapped_at = COALESCE(agent_scene_memory.bootstrapped_at, now()),
    dirty_revision = CASE
        WHEN (agent_scene_memory.dirty_through_at, agent_scene_memory.dirty_through_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
          OR (agent_scene_memory.last_trigger_at, agent_scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.dirty_revision
        ELSE agent_scene_memory.flushed_revision
    END,
    dirty_since = CASE
        WHEN (agent_scene_memory.dirty_through_at, agent_scene_memory.dirty_through_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
          OR (agent_scene_memory.last_trigger_at, agent_scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.dirty_since
        ELSE NULL
    END,
    dirty_through_at = CASE
        WHEN (agent_scene_memory.dirty_through_at, agent_scene_memory.dirty_through_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.dirty_through_at
        ELSE NULL
    END,
    dirty_through_evidence_id = CASE
        WHEN (agent_scene_memory.dirty_through_at, agent_scene_memory.dirty_through_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.dirty_through_evidence_id
        ELSE ''
    END,
    last_trigger_job_id = CASE
        WHEN (agent_scene_memory.last_trigger_at, agent_scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.last_trigger_job_id
        ELSE NULL
    END,
    last_trigger_coord_trace_id = CASE
        WHEN (agent_scene_memory.last_trigger_at, agent_scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.last_trigger_coord_trace_id
        ELSE ''
    END,
    last_trigger_idempotency_key = CASE
        WHEN (agent_scene_memory.last_trigger_at, agent_scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.last_trigger_idempotency_key
        ELSE ''
    END,
    last_trigger_at = CASE
        WHEN (agent_scene_memory.last_trigger_at, agent_scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.last_trigger_at
        ELSE NULL
    END,
    last_trigger_evidence_id = CASE
        WHEN (agent_scene_memory.last_trigger_at, agent_scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.last_trigger_evidence_id
        ELSE ''
    END,
    pending_from_at = CASE
        WHEN (agent_scene_memory.last_trigger_at, agent_scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.last_trigger_at
        ELSE NULL
    END,
    pending_from_evidence_id = CASE
        WHEN (agent_scene_memory.last_trigger_at, agent_scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN agent_scene_memory.last_trigger_evidence_id
        ELSE ''
    END,
    history_resume_before = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    lease_target_dirty_revision = NULL,
    lease_target_through_at = NULL,
    lease_target_through_evidence_id = '',
    lease_expected_memory_revision = NULL,
    attempt_count = 0,
    last_error_code = '',
    last_error = '',
    blocked_at = NULL,
    updated_at = now()
RETURNING *;

-- name: SetAgentSceneMemoryHistoryResume :execrows
UPDATE agent_scene_memory
SET history_resume_before = @history_resume_before,
    updated_at = now()
WHERE scene_id = @scene_id
  AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: CountValidAgentSceneMemoryLeases :one
SELECT count(*)::bigint FROM agent_scene_memory
WHERE lease_token IS NOT NULL AND lease_expires_at > now();

-- name: DeleteAgentSceneMemoryByWorkspace :exec
DELETE FROM agent_scene_memory WHERE workspace_id = @workspace_id;

-- name: DeleteAgentSceneMemoryByAgent :exec
DELETE FROM agent_scene_memory WHERE workspace_id = @workspace_id AND agent_id = @agent_id;

-- name: ReplaceAgentSceneMemoryText :one
UPDATE agent_scene_memory
SET memory_text = @memory_text,
    memory_revision = memory_revision + 1,
    last_flush_meta = '{"source":"owner_edit"}'::jsonb,
    last_flushed_at = now(),
    source_cursor_at = date_trunc('second', now()),
    source_cursor_evidence_id = 'owner-edit',
    flushed_revision = dirty_revision,
    dirty_since = NULL,
    dirty_through_at = NULL,
    dirty_through_evidence_id = '',
    pending_from_at = NULL,
    pending_from_evidence_id = '',
    history_resume_before = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    lease_target_dirty_revision = NULL,
    lease_target_through_at = NULL,
    lease_target_through_evidence_id = '',
    lease_expected_memory_revision = NULL,
    attempt_count = 0,
    last_error_code = '',
    last_error = '',
    blocked_at = NULL,
    updated_at = now()
WHERE scene_id = @scene_id
  AND workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND memory_revision = @expected_memory_revision
RETURNING *;
