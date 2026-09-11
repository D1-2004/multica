CREATE TABLE agent_event_trigger (
    agent_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1,
    autopilot_id uuid,
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE agent_event_route (
    target_identity text NOT NULL,
    source_id text NOT NULL,
    agent_id uuid NOT NULL,
    revision bigint NOT NULL,
    enabled boolean NOT NULL,
    synced_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE agent_event_stream (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    source_key text NOT NULL,
    resource_key text NOT NULL,
    first_pending_at timestamptz,
    last_pending_at timestamptz,
    last_dispatch_at timestamptz,
    due_at timestamptz,
    active_batch_id uuid
);
CREATE TABLE agent_event (
    seq bigint GENERATED ALWAYS AS IDENTITY,
    stream_id uuid NOT NULL,
    event_id text NOT NULL,
    payload jsonb,
    runtime_context jsonb,
    received_at timestamptz NOT NULL DEFAULT now(),
    batch_id uuid,
    consumed_at timestamptz
);
CREATE TABLE agent_event_batch (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    stream_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','completed','failed')),
    attempts integer NOT NULL DEFAULT 0,
    run_id uuid,
    task_id uuid,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
