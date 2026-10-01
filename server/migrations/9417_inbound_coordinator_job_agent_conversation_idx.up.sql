CREATE INDEX CONCURRENTLY IF NOT EXISTS inbound_coordinator_job_agent_conversation_idx ON inbound_coordinator_job (agent_id, (BTRIM(command #>> '{event,data,conversation,openConversationId}')));
