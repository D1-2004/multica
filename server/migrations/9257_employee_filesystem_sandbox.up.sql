CREATE TABLE IF NOT EXISTS employee_filesystem_sandbox (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scope_id uuid NOT NULL CHECK (scope_id <> '00000000-0000-0000-0000-000000000000'),
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
