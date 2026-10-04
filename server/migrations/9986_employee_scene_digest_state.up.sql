-- Employee scene digest (M11): per-scene flush scheduling state and the
-- lease-claimed background writer. Relationships are application-owned (no
-- foreign keys); indexes are built concurrently in the following
-- single-statement migrations.
CREATE TABLE IF NOT EXISTS employee_scene_digest_state (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    scene_id uuid NOT NULL,
    dirty_revision bigint NOT NULL DEFAULT 0 CHECK (dirty_revision >= 0),
    digested_revision bigint NOT NULL DEFAULT 0 CHECK (digested_revision >= 0),
    -- Scheduling hint: observed human transcript rows not yet digested.
    pending_human integer NOT NULL DEFAULT 0 CHECK (pending_human >= 0),
    first_pending_at timestamptz,
    last_human_at timestamptz,
    last_activity_at timestamptz,
    due_at timestamptz,
    -- Backoff or budget hold; due_at never precedes it.
    hold_until timestamptz,
    -- Digested-through transcript cursor (sent_at, provider_message_id).
    cursor_sent_at timestamptz,
    cursor_message_id text NOT NULL DEFAULT '' CHECK (char_length(cursor_message_id) <= 256),
    lease_token uuid,
    lease_until timestamptz,
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    no_progress_count integer NOT NULL DEFAULT 0 CHECK (no_progress_count >= 0),
    blocked_at timestamptz,
    blocked_reason text NOT NULL DEFAULT '' CHECK (char_length(blocked_reason) <= 256),
    budget_day date,
    budget_calls integer NOT NULL DEFAULT 0 CHECK (budget_calls BETWEEN 0 AND 24),
    last_run_id uuid,
    maintained_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_scene_digest_state_lease_check CHECK ((lease_token IS NULL) = (lease_until IS NULL))
);
