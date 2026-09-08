-- Disable and synchronize employee policies before rolling back application code.
-- Never rewind a policy version that Router may already have observed.
UPDATE dingtalk_response_policy_rollout
SET revision = revision + 1, enabled = false, updated_at = now();

ALTER TABLE agent DROP COLUMN IF EXISTS dingtalk_response_enabled;
