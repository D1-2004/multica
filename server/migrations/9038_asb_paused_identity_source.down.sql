DROP INDEX agent_enterprise_identity_authx_rotation_idx;

ALTER TABLE agent_enterprise_identity
    DROP CONSTRAINT agent_enterprise_identity_active_source_check,
    ADD COLUMN buc_tokens_encrypted BYTEA,
    ADD COLUMN buc_access_expires_at TIMESTAMPTZ;

UPDATE agent_enterprise_identity
SET status = 'needs_reauth',
    buc_identity_source_sandbox_id = NULL,
    buc_identity_source_runtime_id = NULL,
    buc_identity_source_updated_at = NULL,
    authx_refresh_token_encrypted = NULL,
    authx_refresh_expires_at = NULL,
    token_version = token_version + 1,
    updated_at = now()
WHERE status = 'active';

ALTER TABLE agent_enterprise_identity
    DROP COLUMN buc_identity_source_updated_at,
    DROP COLUMN buc_identity_source_runtime_id,
    DROP COLUMN buc_identity_source_sandbox_id,
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
