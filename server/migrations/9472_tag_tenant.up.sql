-- A tenant of a Tag: one enterprise served by the Tag, embodied by exactly one
-- employee agent. The employee agent is an ordinary agent row that owns the
-- tenant's DingTalk digital-employee binding, dispatch endpoint, scenes and
-- enterprise-level context, so the tenant's enterprise (OrgId) is the
-- employee's bound identity org and is not duplicated here.
-- applied_revision is the tag_config_revision last materialized onto the
-- employee agent; NULL until the first apply. No foreign keys by design.
CREATE TABLE IF NOT EXISTS tag_tenant (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    tag_agent_id uuid NOT NULL,
    employee_agent_id uuid NOT NULL,
    name text NOT NULL,
    applied_revision integer,
    applied_at timestamptz,
    applied_by uuid,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tag_tenant_name_check CHECK (char_length(name) BETWEEN 1 AND 64),
    CONSTRAINT tag_tenant_applied_revision_check CHECK (applied_revision IS NULL OR applied_revision > 0),
    CONSTRAINT tag_tenant_distinct_agents_check CHECK (employee_agent_id <> tag_agent_id)
);
