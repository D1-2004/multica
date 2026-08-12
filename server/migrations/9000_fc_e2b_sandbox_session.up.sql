-- Renumbered from fork migration 133 (upstream reused the 132-163 range).
-- The migration runner tracks applied migrations by full filename stem, so
-- environments that already ran 133_fc_e2b_sandbox_session will re-apply this
-- file under its new stem — every statement below is therefore idempotent.
CREATE TABLE IF NOT EXISTS fc_e2b_sandbox_session (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    runtime_id UUID NOT NULL REFERENCES agent_runtime(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('chat', 'issue')),
    scope_id UUID NOT NULL,
    sandbox_id TEXT NOT NULL,
    template TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'stale')),
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (runtime_id, scope_type, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_fc_e2b_sandbox_session_runtime
    ON fc_e2b_sandbox_session(runtime_id, status, expires_at);
