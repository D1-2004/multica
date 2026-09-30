-- Scene and personal Bearer credentials for internal connectors. ciphertext is
-- sealed with the internal connector secret box and binds every scope field.
CREATE TABLE IF NOT EXISTS context_connector_credential (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    connector_id uuid NOT NULL,
    scope_type text NOT NULL,
    org_id text NOT NULL DEFAULT '',
    scope_key text NOT NULL,
    ciphertext bytea NOT NULL,
    hint text NOT NULL DEFAULT '',
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT context_connector_credential_scope_type_check CHECK (scope_type IN ('scene', 'person'))
);
