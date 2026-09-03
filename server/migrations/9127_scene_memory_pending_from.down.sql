ALTER TABLE scene_memory
DROP COLUMN IF EXISTS history_resume_before,
DROP COLUMN IF EXISTS pending_from_evidence_id,
DROP COLUMN IF EXISTS pending_from_at;
