-- A Tag is a workspace's single multi-tenant digital employee. The row points
-- at the template agent whose configuration is published as revisions and
-- applied to each tenant's employee agent (see tag_tenant). The primary key
-- enforces at most one Tag per workspace. Only deployment platform operators
-- create or remove it; relationships are resolved in application code, so
-- there are deliberately no foreign keys.
CREATE TABLE IF NOT EXISTS workspace_tag (
    workspace_id uuid PRIMARY KEY,
    agent_id uuid NOT NULL,
    sidebar_visible boolean NOT NULL DEFAULT TRUE,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
