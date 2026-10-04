-- Invitation reminders (05-watchdog.md C3). A policy row exists only when the
-- original requester explicitly authorized reminders; it is written in the
-- same transaction as the invitation and is never changed afterwards. Without
-- a policy row no reminder is ever sent. One reminder row per ordinal records
-- the send intent (through the response outbox) or why it was held.
-- Relationships are application-owned (no foreign keys); indexes are built
-- concurrently in the following single-statement migrations.
CREATE TABLE IF NOT EXISTS employee_task_invitation_reminder_policy (
    invitation_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    task_id uuid NOT NULL,
    collection_id uuid NOT NULL,
    max_count integer NOT NULL DEFAULT 1 CHECK (max_count BETWEEN 1 AND 3),
    first_after_seconds integer NOT NULL CHECK (first_after_seconds BETWEEN 60 AND 604800),
    interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 600 AND 604800),
    authority_actor_ref text NOT NULL CHECK (char_length(authority_actor_ref) BETWEEN 1 AND 256),
    source_namespace text NOT NULL CHECK (char_length(source_namespace) BETWEEN 1 AND 128),
    source_key text NOT NULL CHECK (char_length(source_key) BETWEEN 1 AND 512),
    instruction_quote text NOT NULL CHECK (char_length(instruction_quote) BETWEEN 1 AND 500),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS employee_task_invitation_reminder (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    task_id uuid NOT NULL,
    collection_id uuid NOT NULL,
    invitation_id uuid NOT NULL,
    target_scene_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 1 AND 3),
    body text NOT NULL DEFAULT '',
    state text NOT NULL CHECK (state IN ('enqueued', 'held', 'suppressed')),
    reason text NOT NULL DEFAULT '' CHECK (octet_length(reason) <= 64),
    action_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_task_invitation_reminder_action_check CHECK (state <> 'enqueued' OR action_id IS NOT NULL)
);
