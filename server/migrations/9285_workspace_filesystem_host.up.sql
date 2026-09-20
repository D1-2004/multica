CREATE TABLE IF NOT EXISTS workspace_filesystem_host (
    workspace_id uuid NOT NULL,
    mode text NOT NULL CHECK (mode IN ('read', 'write')),
    state text NOT NULL DEFAULT 'offline' CHECK (state IN ('offline', 'creating', 'running', 'retiring')),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    create_intent uuid,
    sandbox_id text NOT NULL DEFAULT '',
    template_id text NOT NULL DEFAULT '',
    volume_name text NOT NULL DEFAULT '',
    role_arn text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((state = 'offline' AND sandbox_id = '' AND create_intent IS NULL)
        OR (state = 'creating' AND sandbox_id = '' AND create_intent IS NOT NULL AND template_id <> '')
        OR (state IN ('running', 'retiring') AND sandbox_id <> '' AND create_intent IS NOT NULL AND template_id <> ''))
);
