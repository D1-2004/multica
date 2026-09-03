ALTER TABLE scene_memory
DROP COLUMN IF EXISTS last_trigger_evidence_id,
DROP COLUMN IF EXISTS last_trigger_at;
