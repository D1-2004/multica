CREATE TABLE IF NOT EXISTS workspace_filesystem_grant (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    access text NOT NULL CHECK (access IN ('none', 'read', 'write')),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    task_role_arn text NOT NULL DEFAULT '',
    task_policy_name text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid
);
