CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_coordinator_follow_up_pending ON coordinator_issue_follow_up(issue_id,agent_id,created_at) WHERE task_id IS NULL;
