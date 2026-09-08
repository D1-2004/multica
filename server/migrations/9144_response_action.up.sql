CREATE TABLE IF NOT EXISTS response_action (
    id TEXT NOT NULL,
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    request_id TEXT NOT NULL,
    task_id UUID,
    issue_id UUID,
    kind TEXT NOT NULL CHECK (kind IN ('message.send', 'reaction.clear')),
    input JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'provider_accepted', 'delivered', 'failed', 'unknown', 'silent', 'cancelled')),
    provider_task_id TEXT NOT NULL DEFAULT '',
    provider_conversation_id TEXT NOT NULL DEFAULT '',
    provider_message_id TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    receipt_state TEXT NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ DEFAULT now(),
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    first_attempt_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS response_route (
    callback_url TEXT NOT NULL,
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    input JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
