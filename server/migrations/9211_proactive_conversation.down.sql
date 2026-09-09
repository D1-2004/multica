DROP TABLE IF EXISTS coordinator_issue_follow_up;
DROP TABLE IF EXISTS coordinator_observed_message;
ALTER TABLE agent_event_trigger DROP COLUMN IF EXISTS delivery_mode;
