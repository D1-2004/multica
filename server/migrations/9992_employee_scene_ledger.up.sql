-- Deterministic per-scene journal (GawkBot task_ledger at scene level): one
-- entry per finished Employee wake and per terminal Task, assembled from Host
-- facts only, never from a model self-summary. Indexes follow.
CREATE TABLE IF NOT EXISTS employee_scene_ledger (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    scene_id uuid NOT NULL,
    entry_kind text NOT NULL CHECK (entry_kind IN ('wake', 'task_terminal')),
    source_id text NOT NULL CHECK (char_length(source_id) BETWEEN 1 AND 160),
    occurred_at timestamptz NOT NULL,
    entry jsonb NOT NULL CHECK (jsonb_typeof(entry) = 'object' AND octet_length(entry::text) <= 8192),
    created_at timestamptz NOT NULL DEFAULT now()
);
