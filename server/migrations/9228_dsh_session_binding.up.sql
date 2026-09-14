CREATE TABLE IF NOT EXISTS dsh_employee_session (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scope_kind text NOT NULL CHECK (scope_kind IN ('chat', 'issue', 'task')),
    scope_id uuid NOT NULL,
    session_id text NOT NULL CHECK (session_id ~ '^session-[0-9a-f-]{36}$'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS dsh_task_binding (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    task_id uuid NOT NULL,
    session_id text NOT NULL CHECK (session_id ~ '^session-[0-9a-f-]{36}$'),
    request_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
