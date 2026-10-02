-- Isolated EmployeeLoop memory. No legacy Coordinator memory is migrated.
CREATE TABLE IF NOT EXISTS employee_memory_state (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_id uuid NOT NULL,
    scope_kind text NOT NULL CHECK (scope_kind IN ('scene', 'private')),
    principal_id text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    reset_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((scope_kind = 'scene' AND principal_id = '') OR (scope_kind = 'private' AND char_length(principal_id) BETWEEN 1 AND 256)),
    CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128)
);
CREATE TABLE IF NOT EXISTS employee_learning (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_id uuid NOT NULL,
    scope_kind text NOT NULL CHECK (scope_kind IN ('scene', 'private')),
    principal_id text NOT NULL DEFAULT '',
    replay_key text NOT NULL CHECK (char_length(replay_key) = 64),
    record jsonb NOT NULL CHECK (jsonb_typeof(record) = 'object' AND pg_column_size(record) <= 16384),
    superseded_by uuid,
    forgotten_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((scope_kind = 'scene' AND principal_id = '') OR (scope_kind = 'private' AND char_length(principal_id) BETWEEN 1 AND 256)),
    CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128)
);
