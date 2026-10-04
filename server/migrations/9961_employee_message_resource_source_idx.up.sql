CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_message_resource_source_idx ON employee_message_resource (workspace_id, agent_id, receipt_id, source_message_id, message_id, resource_id);
