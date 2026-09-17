ALTER TABLE dsh_employee_profile
 ADD COLUMN IF NOT EXISTS native_sync_revision bigint NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS native_sync_fingerprint text NOT NULL DEFAULT '';
