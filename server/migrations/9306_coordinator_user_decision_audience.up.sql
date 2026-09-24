ALTER TABLE agent ADD COLUMN IF NOT EXISTS inbound_coordinator_user_decision_audience text NOT NULL DEFAULT 'named' CHECK (inbound_coordinator_user_decision_audience IN ('named', 'all'));
