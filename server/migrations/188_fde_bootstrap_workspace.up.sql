CREATE TABLE IF NOT EXISTS fde_bootstrap_intent (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expected_identity_hmac BYTEA NOT NULL CHECK (octet_length(expected_identity_hmac) = 32),
    source_workspace_id UUID NOT NULL,
    source_agent_id UUID NOT NULL,
    source_task_id UUID NOT NULL,
    source_chat_session_id UUID NOT NULL,
    source_installation_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'provisioning', 'ready', 'failed_retryable', 'expired')),
    user_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
    workspace_id UUID REFERENCES workspace(id) ON DELETE SET NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    last_error_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at),
    CHECK (status <> 'ready' OR (user_id IS NOT NULL AND workspace_id IS NOT NULL AND consumed_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS fde_bootstrap_intent_pending_expiry_idx
    ON fde_bootstrap_intent(expires_at)
    WHERE status IN ('pending', 'failed_retryable');

CREATE INDEX IF NOT EXISTS fde_bootstrap_intent_source_task_idx
    ON fde_bootstrap_intent(source_task_id, created_at DESC);

CREATE TABLE IF NOT EXISTS product_workspace_provisioning (
    user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    product_key TEXT NOT NULL CHECK (product_key ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    workspace_id UUID REFERENCES workspace(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'provisioning'
        CHECK (status IN ('provisioning', 'ready', 'failed_retryable')),
    last_error_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, product_key),
    UNIQUE (workspace_id),
    CHECK ((status = 'ready' AND workspace_id IS NOT NULL)
        OR (status IN ('provisioning', 'failed_retryable') AND workspace_id IS NULL))
);
