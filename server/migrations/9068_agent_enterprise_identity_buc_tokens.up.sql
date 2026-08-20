ALTER TABLE agent_enterprise_identity
    ADD COLUMN IF NOT EXISTS buc_access_token_encrypted BYTEA,
    ADD COLUMN IF NOT EXISTS buc_refresh_token_encrypted BYTEA,
    ADD COLUMN IF NOT EXISTS buc_id_token_encrypted BYTEA,
    ADD COLUMN IF NOT EXISTS buc_access_expires_at TIMESTAMPTZ;
