CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_coordinator_follow_up_issue ON coordinator_issue_follow_up(issue_id,agent_id,created_at);
