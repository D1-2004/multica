ALTER TABLE agent ADD COLUMN IF NOT EXISTS inbound_coordinator_user_decision_names text[] NOT NULL DEFAULT '{}';
