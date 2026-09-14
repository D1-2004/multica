ALTER TABLE dsh_plugin_build
    ADD COLUMN IF NOT EXISTS worker_phase text NOT NULL DEFAULT 'queued',
    ADD COLUMN IF NOT EXISTS create_intent uuid,
    ADD COLUMN IF NOT EXISTS provider_scope text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS sandbox_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS archive_sha256 text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS archive_size bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS runtime_lock_sha256 text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS worker_error text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS claim_id uuid,
    ADD COLUMN IF NOT EXISTS claim_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS worker_started_at timestamptz;
