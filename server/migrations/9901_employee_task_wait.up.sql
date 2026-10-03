-- Wait facts a lifecycle v2 goal depends on. employee_task.state is the
-- aggregate projection of these rows. Relationships are application-owned and
-- indexes are built concurrently in separate migrations.
CREATE TABLE IF NOT EXISTS employee_task_wait (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    task_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('collection', 'human_input', 'schedule', 'external')),
    ref_id text NOT NULL CHECK (ref_id <> '' AND length(ref_id) <= 512),
    mandatory boolean NOT NULL,
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'satisfied', 'cancelled')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    goal_revision bigint NOT NULL CHECK (goal_revision > 0),
    opened_seq bigint NOT NULL CHECK (opened_seq > 0),
    resolved_seq bigint,
    evidence_ref text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_task_wait_resolution_check CHECK (
        (state = 'open' AND resolved_seq IS NULL) OR
        (state <> 'open' AND resolved_seq > opened_seq))
);
