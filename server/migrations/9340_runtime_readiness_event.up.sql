CREATE TABLE IF NOT EXISTS runtime_readiness_event (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    event_at timestamptz NOT NULL,
    reason text NOT NULL
);
