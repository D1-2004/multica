CREATE INDEX CONCURRENTLY IF NOT EXISTS dsh_schedule_retry_scan_idx
ON dsh_schedule (COALESCE(next_attempt_at,next_due_at),workspace_id,agent_id,session_id,schedule_id)
WHERE cancelled_at IS NULL AND next_due_at IS NOT NULL;
