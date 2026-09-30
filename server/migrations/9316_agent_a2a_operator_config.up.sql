-- Operator-only A2A settings for one Agent. Only deployment-listed operator
-- emails may write these rows (MULTICA_A2A_OPERATOR_EMAILS):
--   * dws_uid/dws_org_id bind the DEAP digital employee identity that A2A
--     tasks mint their Agent Identity ContextToken for;
--   * forward_rpc_url/forward_token_encrypted proxy one source client's
--     inbound A2A JSON-RPC (forward_source_client_id) to another environment's
--     Agent (production -> pre-release). Other clients keep running here, so
--     they never share the single target credential.
-- The forward token is sealed with MULTICA_A2A_PUSH_SECRET_KEY and never
-- returned by the API.
CREATE TABLE IF NOT EXISTS agent_a2a_operator_config (
    agent_id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    dws_uid TEXT,
    dws_org_id TEXT,
    deap_agent_uuid TEXT,
    identity_updated_by UUID,
    identity_updated_at TIMESTAMPTZ,
    forward_rpc_url TEXT,
    forward_token_encrypted BYTEA,
    forward_source_client_id UUID,
    forward_updated_by UUID,
    forward_updated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_a2a_operator_config_identity_pair CHECK (
        (dws_uid IS NULL) = (dws_org_id IS NULL)
    ),
    CONSTRAINT agent_a2a_operator_config_identity_format CHECK (
        dws_uid IS NULL OR (dws_uid ~ '^[1-9][0-9]{0,19}$' AND dws_org_id ~ '^[1-9][0-9]{0,19}$')
    ),
    CONSTRAINT agent_a2a_operator_config_deap_agent_uuid CHECK (
        deap_agent_uuid IS NULL OR char_length(deap_agent_uuid) BETWEEN 1 AND 128
    ),
    CONSTRAINT agent_a2a_operator_config_forward_pair CHECK (
        (forward_rpc_url IS NULL) = (forward_token_encrypted IS NULL)
        AND (forward_rpc_url IS NULL) = (forward_source_client_id IS NULL)
    ),
    CONSTRAINT agent_a2a_operator_config_forward_url CHECK (
        forward_rpc_url IS NULL OR (char_length(forward_rpc_url) <= 512 AND forward_rpc_url LIKE 'https://%')
    )
);
