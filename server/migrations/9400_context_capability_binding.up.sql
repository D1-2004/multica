-- Context capability bindings: the agent's offer catalog (scope_type='offer')
-- plus the scene (DingTalk group) and personal layers that opt into offered
-- library items. Relationships to agent / internal_connector / skill are
-- resolved in application code; there are deliberately no foreign keys.
CREATE TABLE IF NOT EXISTS context_capability_binding (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scope_type text NOT NULL,
    org_id text NOT NULL DEFAULT '',
    scope_key text NOT NULL DEFAULT '',
    scope_title text NOT NULL DEFAULT '',
    resource_type text NOT NULL,
    resource_id uuid NOT NULL,
    enabled boolean NOT NULL DEFAULT TRUE,
    created_by uuid,
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT context_capability_binding_scope_type_check CHECK (scope_type IN ('offer', 'scene', 'person')),
    CONSTRAINT context_capability_binding_resource_type_check CHECK (resource_type IN ('connector', 'skill'))
);
