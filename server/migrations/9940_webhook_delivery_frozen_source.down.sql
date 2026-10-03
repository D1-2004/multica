ALTER TABLE webhook_delivery
    DROP COLUMN IF EXISTS source_binding,
    DROP COLUMN IF EXISTS signing_secret_revision,
    DROP COLUMN IF EXISTS source_digest;
