-- name: GetSceneMemoryByID :one
SELECT * FROM scene_memory
WHERE id = @id AND workspace_id = @workspace_id AND agent_id = @agent_id;

-- name: GetSceneMemoryByIdentity :one
SELECT * FROM scene_memory
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND platform = @platform
  AND org_id = @org_id
  AND scene_key = @scene_key;

-- name: ListSceneMemoryByAgent :many
SELECT * FROM scene_memory
WHERE workspace_id = @workspace_id AND agent_id = @agent_id
ORDER BY updated_at DESC, id DESC
LIMIT @list_limit;

-- name: UpsertSceneMemoryDirty :one
WITH upsert AS (
    INSERT INTO scene_memory (
        workspace_id, agent_id, platform, org_id, scene_key, scene_kind, scene_title,
        dirty_revision, dirty_since, dirty_through_at, dirty_through_evidence_id,
        available_at, last_trigger_job_id, last_trigger_coord_trace_id,
        last_trigger_idempotency_key, last_trigger_at, last_trigger_evidence_id,
        pending_from_at, pending_from_evidence_id,
        blocked_at, last_error_code, last_error
    ) VALUES (
        @workspace_id, @agent_id, @platform, @org_id, @scene_key, @scene_kind, @scene_title,
        1, now(), @dirty_through_at, @dirty_through_evidence_id,
        now() + interval '4 seconds', @last_trigger_job_id, @last_trigger_coord_trace_id,
        @last_trigger_idempotency_key, @dirty_through_at, @dirty_through_evidence_id,
        @dirty_through_at, @dirty_through_evidence_id,
        NULL, '', ''
    )
    ON CONFLICT (workspace_id, agent_id, platform, org_id, scene_key)
    DO UPDATE SET
        scene_title = CASE
            WHEN EXCLUDED.scene_title = '' THEN scene_memory.scene_title
            ELSE EXCLUDED.scene_title
        END,
        dirty_revision = scene_memory.dirty_revision + 1,
        dirty_since = COALESCE(scene_memory.dirty_since, now()),
        dirty_through_at = CASE
            WHEN scene_memory.dirty_through_at IS NULL THEN EXCLUDED.dirty_through_at
            WHEN EXCLUDED.dirty_through_at IS NULL THEN scene_memory.dirty_through_at
            WHEN (EXCLUDED.dirty_through_at, EXCLUDED.dirty_through_evidence_id)
               > (scene_memory.dirty_through_at, scene_memory.dirty_through_evidence_id)
            THEN EXCLUDED.dirty_through_at
            ELSE scene_memory.dirty_through_at
        END,
        dirty_through_evidence_id = CASE
            WHEN scene_memory.dirty_through_at IS NULL THEN EXCLUDED.dirty_through_evidence_id
            WHEN EXCLUDED.dirty_through_at IS NULL THEN scene_memory.dirty_through_evidence_id
            WHEN (EXCLUDED.dirty_through_at, EXCLUDED.dirty_through_evidence_id)
               > (scene_memory.dirty_through_at, scene_memory.dirty_through_evidence_id)
            THEN EXCLUDED.dirty_through_evidence_id
            ELSE scene_memory.dirty_through_evidence_id
        END,
        available_at = LEAST(
            COALESCE(scene_memory.dirty_since, now()) + interval '2 minutes',
            now() + CASE
                WHEN scene_memory.bootstrapped_at IS NULL THEN interval '4 seconds'
                ELSE interval '30 seconds'
            END
        ),
        last_trigger_job_id = EXCLUDED.last_trigger_job_id,
        last_trigger_coord_trace_id = EXCLUDED.last_trigger_coord_trace_id,
        last_trigger_idempotency_key = EXCLUDED.last_trigger_idempotency_key,
        last_trigger_at = EXCLUDED.last_trigger_at,
        last_trigger_evidence_id = EXCLUDED.last_trigger_evidence_id,
        pending_from_at = CASE
            WHEN scene_memory.pending_from_at IS NULL THEN EXCLUDED.pending_from_at
            WHEN EXCLUDED.pending_from_at IS NULL THEN scene_memory.pending_from_at
            WHEN (EXCLUDED.pending_from_at, EXCLUDED.pending_from_evidence_id)
               < (scene_memory.pending_from_at, scene_memory.pending_from_evidence_id)
            THEN EXCLUDED.pending_from_at
            ELSE scene_memory.pending_from_at
        END,
        pending_from_evidence_id = CASE
            WHEN scene_memory.pending_from_at IS NULL THEN EXCLUDED.pending_from_evidence_id
            WHEN EXCLUDED.pending_from_at IS NULL THEN scene_memory.pending_from_evidence_id
            WHEN (EXCLUDED.pending_from_at, EXCLUDED.pending_from_evidence_id)
               < (scene_memory.pending_from_at, scene_memory.pending_from_evidence_id)
            THEN EXCLUDED.pending_from_evidence_id
            ELSE scene_memory.pending_from_evidence_id
        END,
        blocked_at = NULL,
        last_error_code = '',
        last_error = '',
        updated_at = now()
    WHERE NULLIF(btrim(EXCLUDED.last_trigger_idempotency_key), '') IS NULL
       OR scene_memory.last_trigger_idempotency_key IS DISTINCT FROM EXCLUDED.last_trigger_idempotency_key
    RETURNING *
)
SELECT * FROM upsert
UNION ALL
SELECT scene_memory.* FROM scene_memory
WHERE NOT EXISTS (SELECT 1 FROM upsert)
  AND workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND platform = @platform
  AND org_id = @org_id
  AND scene_key = @scene_key
LIMIT 1;

-- name: ClaimSceneMemory :one
WITH candidate AS (
    SELECT sm.id
    FROM scene_memory sm
    INNER JOIN agent a ON a.id = sm.agent_id AND a.workspace_id = sm.workspace_id
    WHERE sm.dirty_revision > sm.flushed_revision
      AND sm.available_at <= now()
      AND sm.blocked_at IS NULL
      AND (sm.lease_token IS NULL OR sm.lease_expires_at <= now())
      AND a.scene_memory_write_enabled
      AND a.archived_at IS NULL
    ORDER BY sm.available_at, sm.dirty_since NULLS FIRST, sm.id
    FOR UPDATE OF sm SKIP LOCKED
    LIMIT 1
)
UPDATE scene_memory AS row
SET lease_token = gen_random_uuid(),
    lease_expires_at = now() + interval '2 minutes',
    attempt_count = row.attempt_count + 1,
    lease_target_dirty_revision = row.dirty_revision,
    lease_target_through_at = row.dirty_through_at,
    lease_target_through_evidence_id = row.dirty_through_evidence_id,
    lease_expected_memory_revision = row.memory_revision,
    updated_at = now()
FROM candidate
WHERE row.id = candidate.id
RETURNING row.*;

-- name: RenewSceneMemoryLease :execrows
UPDATE scene_memory
SET lease_expires_at = now() + interval '2 minutes',
    updated_at = now()
WHERE id = @id
  AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: CommitSceneMemoryBatch :one
UPDATE scene_memory
SET memory_text = CASE WHEN @replace_text::boolean THEN @memory_text ELSE memory_text END,
    memory_revision = CASE WHEN @replace_text::boolean THEN memory_revision + 1 ELSE memory_revision END,
    scene_title = CASE
        WHEN NULLIF(btrim(@scene_title), '') IS NOT NULL THEN @scene_title
        ELSE scene_title
    END,
    source_cursor_at = @source_cursor_at,
    source_cursor_evidence_id = @source_cursor_evidence_id,
    bootstrapped_at = COALESCE(bootstrapped_at, now()),
    last_flush_meta = @last_flush_meta,
    last_flushed_at = now(),
    history_resume_before = NULL,
    updated_at = now()
WHERE id = @id
  AND lease_token = @lease_token
  AND lease_expires_at > now()
  AND memory_revision = @expected_memory_revision
RETURNING *;

-- name: FinishSceneMemoryClaim :execrows
UPDATE scene_memory
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
WHERE id = @id
  AND lease_token = @lease_token
  AND lease_expires_at > now()
  AND source_cursor_at IS NOT NULL
  AND lease_target_through_at IS NOT NULL
  AND (source_cursor_at, source_cursor_evidence_id)
      >= (lease_target_through_at, lease_target_through_evidence_id);

-- name: RetrySceneMemoryClaim :execrows
UPDATE scene_memory
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
WHERE id = @id
  AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: ReleaseSceneMemoryPending :execrows
UPDATE scene_memory
SET available_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    lease_target_dirty_revision = NULL,
    lease_target_through_at = NULL,
    lease_target_through_evidence_id = '',
    lease_expected_memory_revision = NULL,
    updated_at = now()
WHERE id = @id
  AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: BlockSceneMemory :execrows
UPDATE scene_memory
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
WHERE id = @id
  AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: ResetSceneMemory :one
INSERT INTO scene_memory (
    workspace_id, agent_id, platform, org_id, scene_key, scene_kind, scene_title,
    memory_text, memory_revision, bootstrapped_at,
    source_cursor_at, source_cursor_evidence_id,
    dirty_revision, flushed_revision
) VALUES (
    @workspace_id, @agent_id, @platform, @org_id, @scene_key, @scene_kind, @scene_title,
    '', 0, now(), COALESCE(@source_cursor_at, date_trunc('second', now())),
    @source_cursor_evidence_id, 0, 0
)
ON CONFLICT (workspace_id, agent_id, platform, org_id, scene_key)
DO UPDATE SET
    memory_text = '',
    memory_revision = scene_memory.memory_revision + 1,
    last_flush_meta = '{}'::jsonb,
    last_flushed_at = NULL,
    source_cursor_at = COALESCE(@source_cursor_at, date_trunc('second', now())),
    source_cursor_evidence_id = @source_cursor_evidence_id,
    bootstrapped_at = COALESCE(scene_memory.bootstrapped_at, now()),
    dirty_revision = CASE
        WHEN (scene_memory.dirty_through_at, scene_memory.dirty_through_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
          OR (scene_memory.last_trigger_at, scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.dirty_revision
        ELSE scene_memory.flushed_revision
    END,
    dirty_since = CASE
        WHEN (scene_memory.dirty_through_at, scene_memory.dirty_through_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
          OR (scene_memory.last_trigger_at, scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.dirty_since
        ELSE NULL
    END,
    dirty_through_at = CASE
        WHEN (scene_memory.dirty_through_at, scene_memory.dirty_through_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.dirty_through_at
        ELSE NULL
    END,
    dirty_through_evidence_id = CASE
        WHEN (scene_memory.dirty_through_at, scene_memory.dirty_through_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.dirty_through_evidence_id
        ELSE ''
    END,
    last_trigger_job_id = CASE
        WHEN (scene_memory.last_trigger_at, scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.last_trigger_job_id
        ELSE NULL
    END,
    last_trigger_coord_trace_id = CASE
        WHEN (scene_memory.last_trigger_at, scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.last_trigger_coord_trace_id
        ELSE ''
    END,
    last_trigger_idempotency_key = CASE
        WHEN (scene_memory.last_trigger_at, scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.last_trigger_idempotency_key
        ELSE ''
    END,
    last_trigger_at = CASE
        WHEN (scene_memory.last_trigger_at, scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.last_trigger_at
        ELSE NULL
    END,
    last_trigger_evidence_id = CASE
        WHEN (scene_memory.last_trigger_at, scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.last_trigger_evidence_id
        ELSE ''
    END,
    pending_from_at = CASE
        WHEN (scene_memory.last_trigger_at, scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.last_trigger_at
        ELSE NULL
    END,
    pending_from_evidence_id = CASE
        WHEN (scene_memory.last_trigger_at, scene_memory.last_trigger_evidence_id)
               > (COALESCE(@source_cursor_at, date_trunc('second', now())), @source_cursor_evidence_id)
        THEN scene_memory.last_trigger_evidence_id
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

-- name: SetSceneMemoryHistoryResume :execrows
UPDATE scene_memory
SET history_resume_before = @history_resume_before,
    updated_at = now()
WHERE id = @id
  AND lease_token = @lease_token
  AND lease_expires_at > now();

-- name: CountValidSceneMemoryLeases :one
SELECT count(*)::bigint FROM scene_memory
WHERE lease_token IS NOT NULL AND lease_expires_at > now();

-- name: DeleteSceneMemoryByWorkspace :exec
DELETE FROM scene_memory WHERE workspace_id = @workspace_id;

-- name: DeleteSceneMemoryByAgent :exec
DELETE FROM scene_memory WHERE workspace_id = @workspace_id AND agent_id = @agent_id;

-- name: ReplaceSceneMemoryText :one
UPDATE scene_memory
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
WHERE id = @id
  AND workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND memory_revision = @expected_memory_revision
RETURNING *;
