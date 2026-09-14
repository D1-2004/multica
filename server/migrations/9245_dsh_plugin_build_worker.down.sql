ALTER TABLE dsh_plugin_build
    DROP COLUMN IF EXISTS worker_phase,
    DROP COLUMN IF EXISTS create_intent,
    DROP COLUMN IF EXISTS provider_scope,
    DROP COLUMN IF EXISTS sandbox_id,
    DROP COLUMN IF EXISTS archive_sha256,
    DROP COLUMN IF EXISTS archive_size,
    DROP COLUMN IF EXISTS runtime_lock_sha256,
    DROP COLUMN IF EXISTS worker_error,
    DROP COLUMN IF EXISTS claim_id,
    DROP COLUMN IF EXISTS claim_expires_at,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS worker_started_at;
