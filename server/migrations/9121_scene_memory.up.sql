-- Exact Scene Memory for a bound digital-employee conversation.
-- No PRIMARY KEY / FOREIGN KEY: indexes are created CONCURRENTLY in follow-up files.

CREATE TABLE IF NOT EXISTS scene_memory (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    platform TEXT NOT NULL DEFAULT 'dingtalk',
    org_id TEXT NOT NULL,
    scene_key TEXT NOT NULL,
    scene_kind TEXT NOT NULL
        CHECK (scene_kind IN ('group', 'dm')),
    scene_title TEXT NOT NULL DEFAULT '',

    memory_text TEXT NOT NULL DEFAULT '',
    memory_revision BIGINT NOT NULL DEFAULT 0,
    bootstrapped_at TIMESTAMPTZ,
    source_cursor_at TIMESTAMPTZ,
    source_cursor_evidence_id TEXT NOT NULL DEFAULT '',

    dirty_revision BIGINT NOT NULL DEFAULT 0,
    flushed_revision BIGINT NOT NULL DEFAULT 0,
    dirty_since TIMESTAMPTZ,
    dirty_through_at TIMESTAMPTZ,
    dirty_through_evidence_id TEXT NOT NULL DEFAULT '',
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    lease_target_dirty_revision BIGINT,
    lease_target_through_at TIMESTAMPTZ,
    lease_target_through_evidence_id TEXT NOT NULL DEFAULT '',
    lease_expected_memory_revision BIGINT,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error_code TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    blocked_at TIMESTAMPTZ,

    last_trigger_job_id UUID,
    last_trigger_coord_trace_id TEXT NOT NULL DEFAULT '',
    last_trigger_idempotency_key TEXT NOT NULL DEFAULT '',
    last_flush_meta JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(last_flush_meta) = 'object' AND pg_column_size(last_flush_meta) <= 8192),
    last_flushed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (dirty_revision >= flushed_revision)
);
