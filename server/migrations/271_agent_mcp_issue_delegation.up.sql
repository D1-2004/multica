-- Hosted Agent MCP uses an Issue as the durable work object. The claim row is
-- intentionally small: task output stays in comments/attachments and file
-- bytes stay in the configured object store.

ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN (
        'autopilot',
        'quick_create',
        'lark_chat',
        'slack_chat',
        'agent_create',
        'dingtalk_chat',
        'agent_mcp'
    ));

CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_agent_mcp_origin_unique
    ON issue(workspace_id, origin_id)
    WHERE origin_type = 'agent_mcp';

CREATE TABLE IF NOT EXISTS agent_mcp_delegation (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id UUID NOT NULL REFERENCES agent_a2a_endpoint(id) ON DELETE CASCADE,
    client_id UUID NOT NULL REFERENCES a2a_client(id) ON DELETE CASCADE,
    accepted_credential_id UUID REFERENCES a2a_client_credential(id) ON DELETE SET NULL,
    public_task_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    request_fingerprint TEXT NOT NULL,
    operation TEXT NOT NULL DEFAULT 'delegate',
    issue_id UUID REFERENCES issue(id) ON DELETE CASCADE,
    root_local_task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (public_task_id),
    UNIQUE (client_id, request_id),
    CHECK (char_length(public_task_id) BETWEEN 16 AND 128),
    CHECK (public_task_id ~ '^[A-Za-z0-9_-]+$'),
    CHECK (char_length(request_id) BETWEEN 1 AND 256),
    CHECK (btrim(request_id) = request_id),
    CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    CHECK (operation IN ('delegate', 'follow_up')),
    CHECK (root_local_task_id IS NULL OR issue_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_agent_mcp_delegation_issue
    ON agent_mcp_delegation(client_id, issue_id, created_at DESC)
    WHERE issue_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_agent_mcp_delegation_pending
    ON agent_mcp_delegation(updated_at, id)
    WHERE issue_id IS NULL;

ALTER TABLE comment
    ADD COLUMN IF NOT EXISTS agent_mcp_claim_id UUID
    REFERENCES agent_mcp_delegation(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_comment_agent_mcp_claim_unique
    ON comment(agent_mcp_claim_id)
    WHERE agent_mcp_claim_id IS NOT NULL;
