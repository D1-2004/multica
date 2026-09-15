CREATE INDEX CONCURRENTLY IF NOT EXISTS dsh_schedule_due ON dsh_schedule (next_due_at, workspace_id, agent_id) WHERE cancelled_at IS NULL AND next_due_at IS NOT NULL;
