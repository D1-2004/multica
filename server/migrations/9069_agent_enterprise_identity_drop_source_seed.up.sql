-- Task sandboxes attach persisted BUC tokens directly. Active identity no
-- longer requires a retained ASB identity seed sandbox.
ALTER TABLE agent_enterprise_identity
    DROP CONSTRAINT IF EXISTS agent_enterprise_identity_active_source_check;

ALTER TABLE agent_enterprise_identity
    DROP CONSTRAINT IF EXISTS agent_enterprise_identity_active_credentials_check;

ALTER TABLE agent_enterprise_identity
    ADD CONSTRAINT agent_enterprise_identity_active_credentials_check CHECK (
        status <> 'active'
        OR (
            authx_refresh_token_encrypted IS NOT NULL
            AND octet_length(authx_refresh_token_encrypted) > 28
            AND authx_refresh_expires_at IS NOT NULL
        )
    );
