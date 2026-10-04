-- Durable foreground participation; no process-local state or inferred expiry.
CREATE TABLE IF NOT EXISTS employee_scene_participation (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_id uuid NOT NULL,
    mode text NOT NULL CHECK (mode IN ('active', 'quiet')),
    requester_ref text NOT NULL CHECK (requester_ref <> ''),
    source_job_id uuid NOT NULL,
    source_ref text NOT NULL,
    instruction_quote text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);
