ALTER TABLE agent_dingtalk_identity
ADD COLUMN IF NOT EXISTS organization_name TEXT NOT NULL DEFAULT '';
