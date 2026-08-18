-- Keep the union valid until the underlying feature migration is rolled back.
-- This migration only reconciles ordering differences and owns no data.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN (
        'autopilot',
        'quick_create',
        'lark_chat',
        'slack_chat',
        'agent_create',
        'dingtalk_chat',
        'wecom_chat',
        'agent_mcp'
    ))
    NOT VALID;
ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_type_check;
