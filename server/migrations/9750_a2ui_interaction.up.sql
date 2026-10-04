-- One A2UI card the server opened for a person to answer, or a chart/note
-- that only needed to be delivered. public_id is the handle callers and the
-- card share (ask:<uuid>, show:<uuid>, appr:<uuid>). The gateway's own biz id
-- is stored after send. No foreign keys: the application fences the row to
-- the sending agent.
CREATE TABLE IF NOT EXISTS a2ui_interaction (
    id uuid PRIMARY KEY,
    public_id text NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    sender_uid text NOT NULL,
    sender_org_id text NOT NULL,
    scene_id text NOT NULL DEFAULT '',
    conversation_id text NOT NULL DEFAULT '',
    -- message_id is the outbound card's openMessageId from the send receipt,
    -- the same id a dws text send returns. thread_id is the thread the card
    -- was sent into, when it has one. source_ref is the employee-loop handle
    -- receipt_id/openMsgId. The click finds this row by public_id; message_id
    -- plus the stored result is which card message received which reply.
    message_id text NOT NULL DEFAULT '',
    thread_id text NOT NULL DEFAULT '',
    source_ref text NOT NULL DEFAULT '',
    kind text NOT NULL,
    status text NOT NULL,
    header text NOT NULL DEFAULT '',
    question text NOT NULL DEFAULT '',
    request jsonb NOT NULL DEFAULT '{}'::jsonb,
    card_biz_id text NOT NULL DEFAULT '',
    event_id text NOT NULL DEFAULT '',
    result jsonb NOT NULL DEFAULT '{}'::jsonb,
    operator_uid text NOT NULL DEFAULT '',
    idempotency_key text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    CONSTRAINT a2ui_interaction_kind_check CHECK (kind IN ('confirm', 'choose', 'person', 'chart', 'note', 'approval')),
    CONSTRAINT a2ui_interaction_status_check CHECK (status IN ('open', 'delivered', 'answered', 'skipped', 'failed', 'approved', 'rejected'))
);
