ALTER TABLE dsh_employee_profile
 DROP COLUMN IF EXISTS native_sync_revision,
 DROP COLUMN IF EXISTS native_sync_fingerprint;
