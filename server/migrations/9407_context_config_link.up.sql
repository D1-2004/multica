-- Agent-issued configuration links. Only the SHA-256 hash of the token is
-- stored; scene links are reusable until expiry, person links are single use.
CREATE TABLE IF NOT EXISTS context_config_link (
    token_hash text PRIMARY KEY,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scope_type text NOT NULL,
    org_id text NOT NULL DEFAULT '',
    scope_key text NOT NULL,
    scope_title text NOT NULL DEFAULT '',
    source_task_id uuid,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    consumed_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT context_config_link_scope_type_check CHECK (scope_type IN ('scene', 'person'))
);
