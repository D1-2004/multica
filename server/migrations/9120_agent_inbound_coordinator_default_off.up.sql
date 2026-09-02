-- Inbound short loop is opt-in. Existing agents that inherited DEFAULT true
-- from 9093, and every new insert, start with the switch off. Owners turn
-- it on from the agent inspector.
ALTER TABLE agent ALTER COLUMN inbound_coordinator SET DEFAULT false;
UPDATE agent SET inbound_coordinator = false WHERE inbound_coordinator = true;
