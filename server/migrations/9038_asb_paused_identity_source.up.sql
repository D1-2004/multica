-- A paused ASB sandbox keeps the BUC credential directory without consuming
-- running quota. It is resumed only for maintenance and cold task inheritance.
ALTER TABLE agent_enterprise_identity
    ADD COLUMN buc_identity_source_sandbox_id TEXT,
    ADD COLUMN buc_identity_source_runtime_id UUID REFERENCES agent_runtime(id) ON DELETE RESTRICT,
    ADD COLUMN buc_identity_source_updated_at TIMESTAMPTZ;

DROP INDEX agent_enterprise_identity_credential_rotation_idx;

ALTER TABLE agent_enterprise_identity
    DROP CONSTRAINT agent_enterprise_identity_active_credentials_check;

-- A BUC refresh response does not contain a new OIDC ID token, so the
-- platform-owned token trio introduced by migration 261 cannot safely create
-- future sandboxes. Existing bindings need one new interactive authorization
-- to establish an ASB-managed credential source.
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
    ADD CONSTRAINT agent_enterprise_identity_active_source_check CHECK (
        status <> 'active'
        OR (
            buc_identity_source_sandbox_id IS NOT NULL
            AND btrim(buc_identity_source_sandbox_id) <> ''
            AND buc_identity_source_runtime_id IS NOT NULL
            AND buc_identity_source_updated_at IS NOT NULL
            AND authx_refresh_token_encrypted IS NOT NULL
            AND octet_length(authx_refresh_token_encrypted) > 28
            AND authx_refresh_expires_at IS NOT NULL
        )
    );

CREATE INDEX agent_enterprise_identity_authx_rotation_idx
    ON agent_enterprise_identity (authx_refresh_expires_at, token_version)
    WHERE status = 'active';
