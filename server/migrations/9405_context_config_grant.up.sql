-- Time-bounded authority for a Multica user to configure one scene or
-- personal scope of an agent from the DingTalk mobile page.
CREATE TABLE IF NOT EXISTS context_config_grant (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scope_type text NOT NULL,
    org_id text NOT NULL DEFAULT '',
    scope_key text NOT NULL,
    scope_title text NOT NULL DEFAULT '',
    source text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT context_config_grant_scope_type_check CHECK (scope_type IN ('scene', 'person')),
    CONSTRAINT context_config_grant_source_check CHECK (source IN ('agent_link', 'jsapi'))
);
