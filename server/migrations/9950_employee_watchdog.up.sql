-- Employee watchdog: stall episodes and one deterministic notice intent per episode.
-- Relationships are application-owned (no foreign keys); indexes are built
-- concurrently in the following single-statement migrations.

-- Per-agent enable watermark. Only runs, waits and stops that started at or
-- after enabled_at are eligible, so the first rollout cannot replay history.
CREATE TABLE IF NOT EXISTS employee_watchdog_cursor (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    enabled_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- One observed period without real progress for one Task state boundary.
-- Identity: task + state kind + boundary + first no-progress time.
CREATE TABLE IF NOT EXISTS employee_watchdog_episode (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_id uuid NOT NULL,
    task_id uuid NOT NULL,
    run_id uuid,
    state_kind text NOT NULL CHECK (state_kind IN ('execution_queued', 'execution_running', 'execution_unreachable', 'waiting_inputs', 'scheduled_wait', 'stopping')),
    state_reason text NOT NULL DEFAULT '' CHECK (octet_length(state_reason) <= 64),
    boundary_key text NOT NULL CHECK (octet_length(boundary_key) BETWEEN 1 AND 256),
    since_at timestamptz NOT NULL,
    watermark_at timestamptz,
    watermark_source text NOT NULL DEFAULT '' CHECK (octet_length(watermark_source) <= 64),
    watermark_ref text NOT NULL DEFAULT '' CHECK (octet_length(watermark_ref) <= 256),
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'cleared', 'closed')),
    close_reason text NOT NULL DEFAULT '' CHECK (octet_length(close_reason) <= 64),
    opened_at timestamptz NOT NULL,
    closed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_watchdog_episode_closed_check CHECK ((state = 'open') = (closed_at IS NULL))
);

-- Notice intent: at most one per (episode, kind, recipient). It is delivered
-- only through the existing response outbox; an enqueued intent always names
-- its action so an unknown provider outcome is queried, never resubmitted.
CREATE TABLE IF NOT EXISTS employee_watchdog_notice (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    episode_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_id uuid NOT NULL,
    task_id uuid NOT NULL,
    run_id uuid,
    kind text NOT NULL CHECK (kind IN ('execution_queued', 'execution_running', 'execution_unreachable', 'waiting_inputs', 'scheduled_wait', 'stopping')),
    recipient_key text NOT NULL CHECK (octet_length(recipient_key) BETWEEN 1 AND 256),
    boundary_key text NOT NULL CHECK (octet_length(boundary_key) BETWEEN 1 AND 256),
    body text NOT NULL DEFAULT '',
    state text NOT NULL CHECK (state IN ('enqueued', 'held', 'suppressed')),
    reason text NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 64),
    action_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_watchdog_notice_action_check CHECK (state <> 'enqueued' OR action_id IS NOT NULL)
);
