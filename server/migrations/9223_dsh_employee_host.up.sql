CREATE TABLE IF NOT EXISTS dsh_employee_host (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    file_system_id text NOT NULL CHECK (file_system_id <> ''),
    space_id text NOT NULL CHECK (space_id <> ''),
    volume_name text NOT NULL CHECK (volume_name <> ''),
    access_point_arn text NOT NULL CHECK (access_point_arn <> ''),
    role_arn text NOT NULL CHECK (role_arn <> ''),
    vpc_id text NOT NULL CHECK (vpc_id <> ''),
    security_group_id text NOT NULL CHECK (security_group_id <> ''),
    vswitch_ids text[] NOT NULL CHECK (cardinality(vswitch_ids) > 0),
    state text NOT NULL DEFAULT 'offline' CHECK (state IN ('offline', 'creating', 'running', 'retiring')),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    create_intent uuid,
    sandbox_id text NOT NULL DEFAULT '',
    template_id text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((state = 'offline' AND sandbox_id = '' AND create_intent IS NULL)
        OR (state = 'creating' AND sandbox_id = '' AND create_intent IS NOT NULL AND template_id <> '')
        OR (state IN ('running', 'retiring') AND sandbox_id <> '' AND create_intent IS NOT NULL AND template_id <> ''))
);
