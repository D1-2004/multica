CREATE INDEX CONCURRENTLY IF NOT EXISTS inbound_coordinator_response_callback_idx
ON inbound_coordinator_job (workspace_id, agent_id, (command #>> '{completionCallback,responseUrl}'))
WHERE command #>> '{completionCallback,responseUrl}' IS NOT NULL;
