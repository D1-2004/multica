CREATE TABLE IF NOT EXISTS workspace_filesystem_grant_role (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    generation bigint NOT NULL CHECK (generation >= 1),
    role_id uuid NOT NULL CHECK (role_id <> '00000000-0000-0000-0000-000000000000'),
    access text NOT NULL CHECK (access IN ('read', 'write')),
    role_arn text NOT NULL,
    policy_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
