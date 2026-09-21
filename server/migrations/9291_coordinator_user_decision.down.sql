DROP TABLE IF EXISTS coordinator_user_decision_event;
DROP TABLE IF EXISTS coordinator_user_decision;
ALTER TABLE agent DROP COLUMN IF EXISTS inbound_coordinator_user_decision;
