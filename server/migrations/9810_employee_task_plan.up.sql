-- A pre-authorized work plan of a lifecycle v2 (explicit_goal) Task: its
-- ordered steps, which steps need a decision before they run, and a bounded
-- budget of follow-up actions. A plan revision is frozen when written; a new
-- goal revision supersedes it. Relationships are application-owned (no FKs)
-- and each index is built concurrently by its own migration.
CREATE TABLE IF NOT EXISTS employee_task_plan (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_id uuid NOT NULL,
    task_id uuid NOT NULL,
    plan_revision bigint NOT NULL CHECK (plan_revision > 0),
    goal_revision bigint NOT NULL CHECK (goal_revision > 0),
    steps jsonb NOT NULL CHECK (jsonb_typeof(steps) = 'array' AND jsonb_array_length(steps) BETWEEN 2 AND 8),
    step_budget integer NOT NULL CHECK (step_budget BETWEEN 1 AND 8),
    first_run_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'paused', 'completed', 'superseded', 'stopped')),
    state_reason text NOT NULL DEFAULT '',
    authority_ref text NOT NULL CHECK (authority_ref <> ''),
    source_namespace text NOT NULL,
    source_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
