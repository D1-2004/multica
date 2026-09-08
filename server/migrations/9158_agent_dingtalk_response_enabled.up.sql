ALTER TABLE agent ADD COLUMN IF NOT EXISTS dingtalk_response_enabled boolean NOT NULL DEFAULT false;

-- Fence already-running configuration-driven workers before the employee-owned
-- policy worker starts. Preserve monotonic versions and all in-flight actions.
UPDATE dingtalk_response_policy_rollout
SET revision = revision + 1, enabled = true, updated_at = now();
