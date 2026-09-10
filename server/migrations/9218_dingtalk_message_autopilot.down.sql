DROP TABLE IF EXISTS autopilot_message_event;
DROP TABLE IF EXISTS autopilot_message_window;
-- Preserve enum support and saved trigger configurations during binary rollback.
ALTER TABLE autopilot_run DROP COLUMN IF EXISTS runtime_context;
