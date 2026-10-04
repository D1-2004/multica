-- The accepted answer and its card update intent commit together. Provider
-- updates run outside that transaction under a durable, replaceable lease.
CREATE TABLE IF NOT EXISTS employee_human_card_projection (
    question_id uuid PRIMARY KEY,
    response_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_token uuid,
    lease_until timestamptz,
    last_error text NOT NULL DEFAULT '',
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_human_card_projection_state CHECK (state IN ('pending','completed','blocked')),
    CONSTRAINT employee_human_card_projection_attempts CHECK (attempts >= 0)
);
