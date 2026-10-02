-- Scene Memory state of one Agent work scene (agent_scene), keyed by its
-- scene_id: there is no separate memory id. Replaces scene_memory, which no
-- code reads or writes any more; its rows are not migrated (old dm rows may
-- be unknown types recorded as dm, and memory rebuilds from history).
-- workspace_id and agent_id are copies of the scene's, used to scope reads
-- and the claim's agent join. No PRIMARY KEY / FOREIGN KEY: indexes are
-- created CONCURRENTLY in follow-up files.
CREATE TABLE IF NOT EXISTS agent_scene_memory (
    scene_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,

    memory_text text NOT NULL DEFAULT '',
    memory_revision bigint NOT NULL DEFAULT 0,
    bootstrapped_at timestamptz,
    source_cursor_at timestamptz,
    source_cursor_evidence_id text NOT NULL DEFAULT '',

    dirty_revision bigint NOT NULL DEFAULT 0,
    flushed_revision bigint NOT NULL DEFAULT 0,
    dirty_since timestamptz,
    dirty_through_at timestamptz,
    dirty_through_evidence_id text NOT NULL DEFAULT '',
    available_at timestamptz NOT NULL DEFAULT now(),

    lease_token uuid,
    lease_expires_at timestamptz,
    lease_target_dirty_revision bigint,
    lease_target_through_at timestamptz,
    lease_target_through_evidence_id text NOT NULL DEFAULT '',
    lease_expected_memory_revision bigint,
    attempt_count integer NOT NULL DEFAULT 0,
    last_error_code text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    blocked_at timestamptz,

    last_trigger_job_id uuid,
    last_trigger_coord_trace_id text NOT NULL DEFAULT '',
    last_trigger_idempotency_key text NOT NULL DEFAULT '',
    last_trigger_at timestamptz,
    last_trigger_evidence_id text NOT NULL DEFAULT '',
    pending_from_at timestamptz,
    pending_from_evidence_id text NOT NULL DEFAULT '',
    history_resume_before timestamptz,
    last_flush_meta jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(last_flush_meta) = 'object' AND pg_column_size(last_flush_meta) <= 8192),
    last_flushed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (dirty_revision >= flushed_revision)
);
