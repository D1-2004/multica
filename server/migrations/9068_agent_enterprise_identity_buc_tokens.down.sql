ALTER TABLE agent_enterprise_identity
    DROP COLUMN IF EXISTS buc_access_expires_at,
    DROP COLUMN IF EXISTS buc_id_token_encrypted,
    DROP COLUMN IF EXISTS buc_refresh_token_encrypted,
    DROP COLUMN IF EXISTS buc_access_token_encrypted;
