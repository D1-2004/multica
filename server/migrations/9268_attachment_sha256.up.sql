ALTER TABLE attachment
    ADD COLUMN IF NOT EXISTS sha256 TEXT NOT NULL DEFAULT '';

ALTER TABLE attachment
    DROP CONSTRAINT IF EXISTS attachment_sha256_hex;

ALTER TABLE attachment
    ADD CONSTRAINT attachment_sha256_hex
    CHECK (sha256 = '' OR sha256 ~ '^[0-9a-f]{64}$');
