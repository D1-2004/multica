-- Per-agent switch for sharing one FC/E2B sandbox across tasks in the same
-- scene and trigger. Existing agents stay on: the runtime document only
-- carries the concurrency cap. Idempotent so a replayed stem is a no-op.
ALTER TABLE agent ADD COLUMN IF NOT EXISTS sandbox_connection_reuse boolean NOT NULL DEFAULT true;
