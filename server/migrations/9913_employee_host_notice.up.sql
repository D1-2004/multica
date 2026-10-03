-- Host-initiated scene messages (task wakes, invitations, watchdog notices)
-- linked to the scene dialogue they belong to, so later turns can read them
-- back as assistant history. Relationships are application-owned (no FKs);
-- each index is built concurrently by its own migration.
CREATE TABLE IF NOT EXISTS employee_host_notice (
    action_id text NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    source_kind text NOT NULL,
    source_id text NOT NULL,
    origin_receipt_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_host_notice_source_kind_check CHECK (source_kind IN ('task_wake', 'invitation', 'watchdog', 'task_plan'))
);
