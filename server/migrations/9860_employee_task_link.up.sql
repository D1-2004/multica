-- Task-to-Task relations. builds_on records the upstream Tasks whose results a
-- downstream Task's work packet uses; blocked_by records a dependency whose
-- release is the upstream's real terminal fact (an employee_task_wait of kind
-- 'task'). entry_seq is the downstream ledger entry that recorded the link.
-- Relationships are application-owned; indexes are built in later migrations.
CREATE TABLE IF NOT EXISTS employee_task_link (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    task_id uuid NOT NULL,
    related_task_id uuid NOT NULL,
    relation text NOT NULL CHECK (relation IN ('builds_on', 'blocked_by')),
    entry_seq bigint NOT NULL CHECK (entry_seq > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_task_link_distinct_check CHECK (task_id <> related_task_id)
);
