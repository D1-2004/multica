CREATE TABLE internal_connector (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    upstream_url text NOT NULL,
    credential_ref text NOT NULL,
    allowed_tools jsonb NOT NULL DEFAULT '[]'::jsonb,
    enabled boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT internal_connector_tools_array CHECK (jsonb_typeof(allowed_tools) = 'array'),
    CONSTRAINT internal_connector_name_length CHECK (length(name) BETWEEN 1 AND 120)
);

CREATE TABLE internal_connector_agent (
    connector_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    PRIMARY KEY (connector_id, agent_id)
);

CREATE TABLE internal_connector_call_audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    task_id uuid NOT NULL,
    method text NOT NULL,
    tool_name text NOT NULL DEFAULT '',
    outcome text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
