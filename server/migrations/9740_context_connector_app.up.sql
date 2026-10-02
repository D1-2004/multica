-- A scene's own OAuth application of an official app that cannot register
-- dynamically (Slack, Asana, GitHub), saved on the configure page. A
-- connection started at that scene authorizes with it, and its tokens are
-- exchanged and refreshed with it; elsewhere the workspace's connector_app
-- applies. client_secret_ciphertext is sealed with the internal connector
-- secret box.
CREATE TABLE IF NOT EXISTS context_connector_app (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scope_type text NOT NULL,
    org_id text NOT NULL DEFAULT '',
    scope_key text NOT NULL,
    provider text NOT NULL,
    client_id text NOT NULL,
    client_secret_ciphertext bytea NOT NULL,
    client_secret_hint text NOT NULL DEFAULT '',
    created_by uuid,
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT context_connector_app_scope_type_check CHECK (scope_type IN ('scene'))
);
