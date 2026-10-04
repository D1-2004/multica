ALTER TABLE connector_app DROP CONSTRAINT IF EXISTS connector_app_install_slug_chk;
ALTER TABLE connector_app DROP CONSTRAINT IF EXISTS connector_app_identifier_chk;
ALTER TABLE connector_app
    DROP COLUMN IF EXISTS optional_secret_hint,
    DROP COLUMN IF EXISTS optional_secret_ciphertext,
    DROP COLUMN IF EXISTS private_key_hint,
    DROP COLUMN IF EXISTS private_key_ciphertext,
    DROP COLUMN IF EXISTS install_slug,
    DROP COLUMN IF EXISTS app_identifier;
