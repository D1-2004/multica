-- Extra write-only fields for pre-registered connector apps.
-- private_key is a GitHub App PEM. optional_secret is a GitHub webhook
-- secret or a Slack signing secret. Neither is ever selected into a response.

ALTER TABLE connector_app ADD COLUMN IF NOT EXISTS app_identifier text NOT NULL DEFAULT '';
ALTER TABLE connector_app ADD COLUMN IF NOT EXISTS install_slug text NOT NULL DEFAULT '';
ALTER TABLE connector_app ADD COLUMN IF NOT EXISTS private_key_ciphertext bytea;
ALTER TABLE connector_app ADD COLUMN IF NOT EXISTS private_key_hint text NOT NULL DEFAULT '';
ALTER TABLE connector_app ADD COLUMN IF NOT EXISTS optional_secret_ciphertext bytea;
ALTER TABLE connector_app ADD COLUMN IF NOT EXISTS optional_secret_hint text NOT NULL DEFAULT '';

ALTER TABLE connector_app DROP CONSTRAINT IF EXISTS connector_app_identifier_chk;
ALTER TABLE connector_app DROP CONSTRAINT IF EXISTS connector_app_install_slug_chk;
ALTER TABLE connector_app
    ADD CONSTRAINT connector_app_identifier_chk CHECK (char_length(app_identifier) <= 64),
    ADD CONSTRAINT connector_app_install_slug_chk CHECK (char_length(install_slug) <= 128);
