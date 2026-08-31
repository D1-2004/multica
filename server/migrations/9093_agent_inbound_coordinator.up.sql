-- Per-agent inbound short loop. On by default so existing agents start
-- judging reply vs issue; only an explicit owner off switch skips it.
ALTER TABLE agent
ADD COLUMN IF NOT EXISTS inbound_coordinator BOOLEAN NOT NULL DEFAULT true;
