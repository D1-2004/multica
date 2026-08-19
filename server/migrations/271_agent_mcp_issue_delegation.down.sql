ALTER TABLE comment DROP COLUMN IF EXISTS agent_mcp_claim_id;
DROP TABLE IF EXISTS agent_mcp_delegation;
DROP INDEX IF EXISTS idx_issue_agent_mcp_origin_unique;

UPDATE issue
SET origin_type = NULL,
    origin_id = NULL
WHERE origin_type = 'agent_mcp';

ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN (
        'autopilot',
        'quick_create',
        'lark_chat',
        'slack_chat',
        'agent_create',
        'dingtalk_chat'
    ));
