ALTER TABLE autopilot_trigger DROP CONSTRAINT IF EXISTS autopilot_trigger_kind_check;
ALTER TABLE autopilot_trigger ADD CONSTRAINT autopilot_trigger_kind_check CHECK (kind IN ('schedule','webhook','api','dingtalk_message'));
ALTER TABLE autopilot_trigger ADD COLUMN IF NOT EXISTS merge_interval_minutes integer;
ALTER TABLE autopilot_trigger ADD COLUMN IF NOT EXISTS message_revision bigint NOT NULL DEFAULT 1;
ALTER TABLE autopilot_trigger ADD COLUMN IF NOT EXISTS message_accept_after timestamptz NOT NULL DEFAULT now();
ALTER TABLE autopilot_trigger DROP CONSTRAINT IF EXISTS autopilot_trigger_message_interval_check;
ALTER TABLE autopilot_trigger ADD CONSTRAINT autopilot_trigger_message_interval_check
    CHECK ((kind='dingtalk_message' AND merge_interval_minutes BETWEEN 1 AND 1440 AND merge_interval_minutes IS NOT NULL)
        OR (kind<>'dingtalk_message' AND merge_interval_minutes IS NULL));
ALTER TABLE autopilot_run DROP CONSTRAINT IF EXISTS autopilot_run_source_check;
ALTER TABLE autopilot_run ADD CONSTRAINT autopilot_run_source_check CHECK (source IN ('schedule','manual','webhook','api','dingtalk_message'));
ALTER TABLE autopilot_run ADD COLUMN IF NOT EXISTS runtime_context jsonb;

CREATE TABLE IF NOT EXISTS autopilot_message_window (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    trigger_id uuid NOT NULL,
    autopilot_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    source_id text NOT NULL,
    revision bigint NOT NULL,
    opened_at timestamptz NOT NULL DEFAULT now(),
    due_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'collecting' CHECK (status IN ('collecting','ready','dispatched','cancelled')),
    runtime_context jsonb NOT NULL,
    trigger_payload jsonb,
    run_id uuid,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    completed_at timestamptz
);
CREATE TABLE IF NOT EXISTS autopilot_message_event (
    trigger_id uuid NOT NULL,
    source_id text NOT NULL,
    event_id text NOT NULL,
    window_id uuid NOT NULL,
    conversation_id text NOT NULL,
    conversation_cid text NOT NULL DEFAULT '',
    conversation_title text NOT NULL DEFAULT '',
    conversation_type text NOT NULL,
    mentioned boolean NOT NULL,
    occurred_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (trigger_id,source_id,event_id)
);
