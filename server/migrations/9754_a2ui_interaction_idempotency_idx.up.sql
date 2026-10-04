CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS a2ui_interaction_idempotency_idx ON a2ui_interaction (agent_id, idempotency_key) WHERE idempotency_key <> '';
