CREATE TABLE IF NOT EXISTS dsh_schedule (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    session_id text NOT NULL CHECK (session_id ~ '^(session-)?[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    schedule_id text NOT NULL CHECK (schedule_id ~ '^schedule-[1-9][0-9]{0,15}$'),
    owner_member_id uuid NOT NULL,
    source_task_id uuid NOT NULL,
    prompt text NOT NULL CHECK (octet_length(prompt) BETWEEN 1 AND 32768),
    first_due_at timestamptz NOT NULL,
    every_seconds bigint NOT NULL DEFAULT 0 CHECK (every_seconds = 0 OR every_seconds BETWEEN 300 AND 315537897599),
    next_due_at timestamptz,
    cancelled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (next_due_at IS NULL OR next_due_at >= first_due_at)
);
CREATE TABLE IF NOT EXISTS dsh_schedule_occurrence (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    session_id text NOT NULL,
    schedule_id text NOT NULL,
    occurrence_at timestamptz NOT NULL,
    request_id uuid NOT NULL,
    task_id uuid NOT NULL,
    admitted_at timestamptz NOT NULL DEFAULT now()
);
