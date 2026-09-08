ALTER TABLE agent
    DROP COLUMN IF EXISTS dingtalk_response_policy_revision,
    DROP COLUMN IF EXISTS dingtalk_show_ai_tag;
