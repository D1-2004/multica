-- Earliest unconsumed trigger in a dirty window, plus DWS page-cap resume.
-- last_trigger is the latest inbound; pending_from is the oldest so a
-- debounce burst of late messages is not reduced to a single evidence id.
ALTER TABLE scene_memory
ADD COLUMN IF NOT EXISTS pending_from_at TIMESTAMPTZ,
ADD COLUMN IF NOT EXISTS pending_from_evidence_id TEXT NOT NULL DEFAULT '',
ADD COLUMN IF NOT EXISTS history_resume_before TIMESTAMPTZ;
