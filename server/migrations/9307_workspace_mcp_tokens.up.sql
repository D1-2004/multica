CREATE TABLE IF NOT EXISTS workspace_mcp_token (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    subject_user_id uuid NOT NULL,
    created_by uuid NOT NULL,
    name text NOT NULL,
    token_hash text NOT NULL,
    token_prefix text NOT NULL,
    scopes text[] NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    last_used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_mcp_audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    token_id uuid NOT NULL,
    subject_user_id uuid NOT NULL,
    actor_user_id uuid NOT NULL,
    tool_name text NOT NULL,
    resource_id text,
    result text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
