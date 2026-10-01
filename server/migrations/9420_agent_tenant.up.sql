-- Tenants (租户) of an agent: each enterprise that reuses the agent, created
-- explicitly with a name and its DingTalk OrgId. The agent's DingTalk
-- identity org (agent_dingtalk_identity.org_id) counts as a tenant without a
-- row; a row for it only renames it. The org-level context (scope_type 'org'
-- in the context_* tables) and the group and person scenes of an org apply
-- only while that org is a tenant. No foreign keys; the workspace deletion
-- sweep removes rows explicitly.
CREATE TABLE IF NOT EXISTS agent_tenant (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    org_id text NOT NULL,
    name text NOT NULL,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_tenant_org_id_check CHECK (org_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    CONSTRAINT agent_tenant_name_check CHECK (char_length(name) BETWEEN 1 AND 64)
);
