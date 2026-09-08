ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS dingtalk_show_ai_tag boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS dingtalk_response_policy_revision bigint NOT NULL DEFAULT 1;
