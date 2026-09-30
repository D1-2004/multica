-- A target A2A key forwards exactly one source client for its whole life.
-- The target sees every forwarded call as its own single client, so letting a
-- second source client reuse the key (after rebinding, clearing and
-- recreating, or on another Agent) would expose the first client's tasks.
-- Rows are never deleted; only the key's SHA-256 is stored.
CREATE TABLE IF NOT EXISTS a2a_forward_token_binding (
    token_sha256 TEXT PRIMARY KEY,
    source_client_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT a2a_forward_token_binding_sha256 CHECK (token_sha256 ~ '^[0-9a-f]{64}$')
);
