CREATE INDEX CONCURRENTLY IF NOT EXISTS dsh_schedule_session_due_idx
ON dsh_schedule(workspace_id,agent_id,session_id,owner_member_id,next_due_at)
WHERE cancelled_at IS NULL AND next_due_at IS NOT NULL;
