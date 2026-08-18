-- Establish the canonical issue.origin_type constraint after both upstream
-- migrations and fork-owned 9000+ migrations have run. On a fresh database,
-- 9049_agent_mcp_issue_delegation runs after upstream WeCom support and its
-- historical constraint definition does not include wecom_chat. On an
-- upgraded internal database, the 271 -> 9049 alias skips that SQL instead.
-- Reasserting the union here makes both paths converge on the same schema.
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
