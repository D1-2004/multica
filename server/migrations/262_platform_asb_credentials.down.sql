DROP INDEX agent_enterprise_identity_credential_rotation_idx;

ALTER TABLE agent_enterprise_identity
    DROP CONSTRAINT agent_enterprise_identity_active_credentials_check,
    ADD COLUMN buc_anchor_sandbox_id TEXT,
    ADD COLUMN anchor_maintained_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE agent_enterprise_identity
SET status = 'needs_reauth',
    buc_tokens_encrypted = NULL,
    buc_access_expires_at = NULL,
    authx_refresh_token_encrypted = NULL,
    authx_refresh_expires_at = NULL,
    token_version = token_version + 1,
    updated_at = now()
WHERE status = 'active';

ALTER TABLE agent_enterprise_identity
    DROP COLUMN buc_tokens_encrypted,
    DROP COLUMN buc_access_expires_at,
    ADD CONSTRAINT agent_enterprise_identity_check CHECK (
        status <> 'active'
        OR (
            buc_anchor_sandbox_id IS NOT NULL
            AND authx_refresh_token_encrypted IS NOT NULL
            AND octet_length(authx_refresh_token_encrypted) > 28
            AND authx_refresh_expires_at IS NOT NULL
        )
    );

CREATE INDEX agent_enterprise_identity_anchor_maintenance_idx
    ON agent_enterprise_identity (anchor_maintained_at, token_version)
    WHERE status = 'active';

DROP TABLE asb_runtime_credential;
