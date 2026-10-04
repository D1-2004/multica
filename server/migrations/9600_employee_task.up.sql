-- Indexes are built concurrently in separate migrations; relationships are application-owned.
CREATE TABLE IF NOT EXISTS employee_task (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scope_kind text NOT NULL CHECK (scope_kind IN ('scene', 'legacy_issue', 'legacy_chat')),
    scene_id uuid,
    legacy_id uuid,
    owner_loop text NOT NULL CHECK (owner_loop IN ('coordinator', 'employee')),
    dispatch_mode text NOT NULL CHECK (dispatch_mode IN ('direct', 'issue')),
    requester_ref text NOT NULL,
    definition jsonb NOT NULL,
    goal_revision bigint NOT NULL DEFAULT 1 CHECK (goal_revision > 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    state text NOT NULL DEFAULT 'ready' CHECK (state IN ('ready', 'running', 'succeeded', 'failed', 'cancelled')),
    last_entry_seq bigint NOT NULL DEFAULT 1 CHECK (last_entry_seq > 0),
    active_run_id uuid,
    issue_id uuid,
    source_namespace text NOT NULL,
    source_key text NOT NULL,
    create_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_task_scope_check CHECK (
        (scope_kind = 'scene' AND scene_id IS NOT NULL AND legacy_id IS NULL AND tenant_org_id <> '') OR
        (scope_kind <> 'scene' AND scene_id IS NULL AND legacy_id IS NOT NULL AND owner_loop = 'coordinator'))
);

CREATE TABLE IF NOT EXISTS employee_task_entry (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    task_id uuid NOT NULL,
    seq bigint NOT NULL CHECK (seq > 0),
    kind text NOT NULL CHECK (kind IN ('request', 'input', 'amendment', 'run_started', 'result', 'issue_bound')),
    source_namespace text NOT NULL,
    source_key text NOT NULL,
    actor_ref text NOT NULL DEFAULT '',
    goal_revision bigint NOT NULL CHECK (goal_revision > 0),
    run_id uuid,
    body text NOT NULL DEFAULT '',
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS employee_task_run (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    task_id uuid NOT NULL,
    queue_task_id uuid NOT NULL,
    goal_revision bigint NOT NULL CHECK (goal_revision > 0),
    input_seq bigint NOT NULL CHECK (input_seq > 0),
    state text NOT NULL DEFAULT 'running' CHECK (state IN ('running', 'succeeded', 'failed', 'cancelled')),
    result text NOT NULL DEFAULT '',
    result_ref text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
