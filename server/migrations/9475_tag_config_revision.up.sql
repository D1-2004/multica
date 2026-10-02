-- Published, immutable snapshots of a Tag template agent's shared
-- configuration (instructions, MCP config, model and runtime, skills, granted
-- connectors and the offer catalog). Applying a revision to a tenant copies the
-- snapshot onto that tenant's employee agent; nothing reads a revision at task
-- claim time, so the claim path is unchanged. No foreign keys by design.
CREATE TABLE IF NOT EXISTS tag_config_revision (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    tag_agent_id uuid NOT NULL,
    revision integer NOT NULL,
    snapshot jsonb NOT NULL,
    note text NOT NULL DEFAULT '',
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tag_config_revision_positive_check CHECK (revision > 0),
    CONSTRAINT tag_config_revision_snapshot_check CHECK (jsonb_typeof(snapshot) = 'object')
);
