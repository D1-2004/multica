ALTER TABLE attachment
    DROP CONSTRAINT IF EXISTS attachment_sha256_hex;

ALTER TABLE attachment
    DROP COLUMN IF EXISTS sha256;
