-- Persist the current inbound trigger separately from dirty_through.
-- dirty_through is a high-water mark and never moves backward, so a
-- same-second smaller evidence id or an earlier occurred_at would otherwise
-- disappear from the lease cutoff. Columns are appended so SELECT * scans
-- stay compatible with older generated field lists during this fork.
ALTER TABLE scene_memory
ADD COLUMN IF NOT EXISTS last_trigger_at TIMESTAMPTZ,
ADD COLUMN IF NOT EXISTS last_trigger_evidence_id TEXT NOT NULL DEFAULT '';
