CREATE TABLE asb_runtime_credential (
    runtime_id UUID PRIMARY KEY REFERENCES agent_runtime(id) ON DELETE CASCADE,
    api_key_encrypted BYTEA NOT NULL CHECK (octet_length(api_key_encrypted) > 28),
    api_key_hint TEXT NOT NULL CHECK (char_length(api_key_hint) BETWEEN 1 AND 16),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE agent_enterprise_identity
    ADD COLUMN buc_tokens_encrypted BYTEA,
    ADD COLUMN buc_access_expires_at TIMESTAMPTZ;

-- Legacy bindings only persisted BUC credentials inside an ASB anchor
-- sandbox. They cannot be migrated without exporting a live user credential,
-- so require one explicit reauthorization into the platform-owned store.
UPDATE agent_enterprise_identity
SET status = 'needs_reauth',
    authx_refresh_token_encrypted = NULL,
    authx_refresh_expires_at = NULL,
    token_version = token_version + 1,
    updated_at = now()
WHERE status = 'active';

DROP INDEX agent_enterprise_identity_anchor_maintenance_idx;

ALTER TABLE agent_enterprise_identity
    DROP CONSTRAINT agent_enterprise_identity_check,
    DROP COLUMN buc_anchor_sandbox_id,
    DROP COLUMN anchor_maintained_at,
    ADD CONSTRAINT agent_enterprise_identity_active_credentials_check CHECK (
        status <> 'active'
        OR (
            buc_tokens_encrypted IS NOT NULL
            AND octet_length(buc_tokens_encrypted) > 28
            AND buc_access_expires_at IS NOT NULL
            AND authx_refresh_token_encrypted IS NOT NULL
            AND octet_length(authx_refresh_token_encrypted) > 28
            AND authx_refresh_expires_at IS NOT NULL
        )
    );

CREATE INDEX agent_enterprise_identity_credential_rotation_idx
    ON agent_enterprise_identity (
        LEAST(buc_access_expires_at, authx_refresh_expires_at),
        token_version
    )
    WHERE status = 'active';
