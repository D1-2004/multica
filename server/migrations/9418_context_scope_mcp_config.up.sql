-- Custom MCP servers of one scene or person scope of an agent (the scene's
-- or the person's own 「自定义 MCP 服务器」, parallel to the agent's
-- mcp_config). A 1:1 chat scene stores its configuration in its person's
-- scope. Configuration only: stored and shown, not yet applied at runtime.
-- No foreign keys; the workspace deletion sweep removes rows explicitly.
CREATE TABLE IF NOT EXISTS context_scope_mcp_config (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scope_type text NOT NULL,
    org_id text NOT NULL DEFAULT '',
    scope_key text NOT NULL,
    mcp_config jsonb NOT NULL,
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT context_scope_mcp_config_scope_type_check CHECK (scope_type IN ('scene', 'person'))
);
