-- The A2A employee identity now lives in agent_dingtalk_identity, the same row
-- the Integrations DingTalk identity binds, so binding or clearing it in either
-- place changes both. agent_a2a_operator_config only records whether A2A turns
-- may use that identity and whether this pre-release Agent accepts production
-- forwards.
--
-- The 9316 identity and manual-forward columns are retired but kept: binaries
-- from before this migration still read them during a rolling deploy. A later
-- release drops them once no such binary runs.
ALTER TABLE agent_a2a_operator_config
    ADD COLUMN IF NOT EXISTS a2a_identity_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS accept_prod_forward BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS updated_by UUID;

-- Pre-release side: one row per Agent and production registry. client_id is
-- the dedicated A2A client that registry's forwards authenticate as, created
-- for one digital employee (client_uid/client_org_id). Key rotations keep the
-- client, so forwarded tasks stay readable and cancellable; a different
-- employee gets a new client. registered_* describe the registration the
-- registry last accepted.
CREATE TABLE IF NOT EXISTS agent_a2a_forward_registrant (
    agent_id UUID NOT NULL,
    registry_origin TEXT NOT NULL,
    workspace_id UUID NOT NULL,
    client_id UUID NOT NULL,
    client_uid TEXT NOT NULL,
    client_org_id TEXT NOT NULL,
    registered_rpc_url TEXT,
    registered_token_sha256 TEXT,
    registered_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, registry_origin),
    CONSTRAINT agent_a2a_forward_registrant_origin CHECK (
        char_length(registry_origin) <= 255 AND registry_origin LIKE 'https://%'
    ),
    CONSTRAINT agent_a2a_forward_registrant_identity CHECK (
        client_uid ~ '^[1-9][0-9]{0,19}$' AND client_org_id ~ '^[1-9][0-9]{0,19}$'
    ),
    CONSTRAINT agent_a2a_forward_registrant_registered CHECK (
        (registered_at IS NULL) = (registered_rpc_url IS NULL)
        AND (registered_at IS NULL) = (registered_token_sha256 IS NULL)
    ),
    CONSTRAINT agent_a2a_forward_registrant_token_sha256 CHECK (
        registered_token_sha256 IS NULL OR registered_token_sha256 ~ '^[0-9a-f]{64}$'
    )
);

-- Pre-release side: which digital employee each production-forward client was
-- created for, written before the client gets its first key and never
-- reassigned. The key check reads it, so a client replaced by another
-- employee's (retired_at) or one whose employee no longer matches the Agent
-- is refused even if revoking it failed.
CREATE TABLE IF NOT EXISTS agent_a2a_forward_client (
    client_id UUID PRIMARY KEY,
    agent_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    dws_uid TEXT NOT NULL,
    org_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at TIMESTAMPTZ,
    CONSTRAINT agent_a2a_forward_client_identity CHECK (
        dws_uid ~ '^[1-9][0-9]{0,19}$' AND org_id ~ '^[1-9][0-9]{0,19}$'
    )
);

-- Production side: one pre-release target per digital employee identity.
-- target_client_id is the pre-release client the key belongs to; production
-- binds one of its own source clients to it for good. The key is sealed with
-- MULTICA_A2A_PUSH_SECRET_KEY. A withdrawn or rejected registration keeps its
-- row with token_encrypted NULL, so signed_at_ms still orders later requests:
-- an older signed request never replaces or restores a newer state.
CREATE TABLE IF NOT EXISTS a2a_forward_registration (
    dws_uid TEXT NOT NULL,
    org_id TEXT NOT NULL,
    rpc_url TEXT NOT NULL,
    target_client_id UUID NOT NULL,
    token_encrypted BYTEA,
    token_sha256 TEXT NOT NULL,
    target_agent_name TEXT NOT NULL DEFAULT '',
    signed_at_ms BIGINT NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,
    PRIMARY KEY (dws_uid, org_id),
    CONSTRAINT a2a_forward_registration_identity CHECK (
        dws_uid ~ '^[1-9][0-9]{0,19}$' AND org_id ~ '^[1-9][0-9]{0,19}$'
    ),
    CONSTRAINT a2a_forward_registration_rpc_url CHECK (
        char_length(rpc_url) <= 512 AND rpc_url LIKE 'https://%'
    ),
    CONSTRAINT a2a_forward_registration_token_sha256 CHECK (token_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT a2a_forward_registration_agent_name CHECK (char_length(target_agent_name) <= 200),
    CONSTRAINT a2a_forward_registration_revoked CHECK ((token_encrypted IS NULL) = (revoked_at IS NOT NULL))
);
