CREATE INDEX CONCURRENTLY IF NOT EXISTS sandbox_send_receipt_task_idx ON sandbox_send_receipt (workspace_id, agent_id, task_id, target_conversation_id);
