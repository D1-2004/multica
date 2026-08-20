ALTER TABLE agent_enterprise_identity
    DROP CONSTRAINT IF EXISTS agent_enterprise_identity_active_credentials_check;

ALTER TABLE agent_enterprise_identity
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
