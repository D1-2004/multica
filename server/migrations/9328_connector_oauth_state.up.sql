-- Pending OAuth connects. Only the SHA-256 hash of the state is stored; a
-- state is consumed once and expires after 10 minutes. verifier_ciphertext
-- is the sealed PKCE verifier. agent_id is NULL for the workspace scope.
-- No foreign keys: workspace deletion sweeps this table explicitly.
CREATE TABLE IF NOT EXISTS connector_oauth_state (
    state_hash text PRIMARY KEY,
    workspace_id uuid NOT NULL,
    connector_id uuid NOT NULL,
    agent_id uuid,
    scope_type text NOT NULL,
    org_id text NOT NULL DEFAULT '',
    scope_key text NOT NULL DEFAULT '',
    user_id uuid NOT NULL,
    verifier_ciphertext bytea NOT NULL,
    return_to text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connector_oauth_state_scope_type_check CHECK (scope_type IN ('workspace', 'scene', 'person'))
);
