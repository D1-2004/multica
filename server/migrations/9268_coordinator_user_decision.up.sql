ALTER TABLE agent ADD COLUMN IF NOT EXISTS inbound_coordinator_user_decision boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS coordinator_user_decision (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    job_id uuid NOT NULL,
    environment text NOT NULL,
    corp_id text NOT NULL,
    conversation_id text NOT NULL,
    initiator_id text NOT NULL,
    sender_uid text NOT NULL,
    sender_org_id text NOT NULL,
    state text NOT NULL DEFAULT 'prepared',
    version text NOT NULL,
    snapshot jsonb NOT NULL,
    proposal jsonb NOT NULL,
    submission jsonb,
    final_plan jsonb,
    card_biz_id text,
    send_request_id uuid NOT NULL,
    sent_at timestamptz,
    expires_at timestamptz,
    accepted_at timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_token uuid,
    lease_expires_at timestamptz,
    card_update_pending boolean NOT NULL DEFAULT false,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (state IN ('prepared','sending','send_unknown','waiting','accepted','resuming','dispatched','not_executed','expired','cancelled','failed'))
);

CREATE TABLE IF NOT EXISTS coordinator_user_decision_event (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    decision_id uuid NOT NULL,
    environment text NOT NULL,
    event_id text NOT NULL,
    operator_id text NOT NULL,
    outcome text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
