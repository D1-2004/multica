CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dingtalk_response_policy_sync_source_idx ON dingtalk_response_policy_sync (target_identity, source_id);
