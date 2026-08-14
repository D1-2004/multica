-- Agent-scoped inbound A2A endpoints. The endpoint, caller principal, and
-- rotatable credential are deliberately separate identities: rotating a
-- credential must not change ownership of an existing A2A task.

CREATE TABLE IF NOT EXISTS agent_a2a_endpoint (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    public_agent_id TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    delegated_by_user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
    card_name TEXT NOT NULL,
    card_description TEXT NOT NULL DEFAULT '',
    card_version TEXT NOT NULL DEFAULT '1.0.0',
    card_skills JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (agent_id),
    UNIQUE (public_agent_id),
    UNIQUE (id, workspace_id),
    CHECK (char_length(public_agent_id) BETWEEN 16 AND 128),
    CHECK (public_agent_id ~ '^[A-Za-z0-9_-]+$'),
    CHECK (char_length(btrim(card_name)) BETWEEN 1 AND 200),
    CHECK (char_length(card_description) <= 4000),
    CHECK (char_length(btrim(card_version)) BETWEEN 1 AND 64),
    CHECK (jsonb_typeof(card_skills) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_agent_a2a_endpoint_workspace
    ON agent_a2a_endpoint(workspace_id, created_at DESC);

CREATE TABLE IF NOT EXISTS a2a_client (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id UUID NOT NULL REFERENCES agent_a2a_endpoint(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'disabled', 'revoked')),
    scopes TEXT[] NOT NULL DEFAULT ARRAY['send', 'read']::TEXT[],
    rate_limit_per_minute INTEGER,
    max_concurrent_tasks INTEGER,
    created_by UUID NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
    updated_by UUID NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
    revoked_at TIMESTAMPTZ,
    revoked_by UUID REFERENCES "user"(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, endpoint_id),
    CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    CHECK (cardinality(scopes) > 0),
    CHECK (scopes <@ ARRAY['send', 'read', 'list', 'cancel']::TEXT[]),
    CHECK (rate_limit_per_minute IS NULL OR rate_limit_per_minute > 0),
    CHECK (max_concurrent_tasks IS NULL OR max_concurrent_tasks > 0),
    CHECK (status <> 'revoked' OR revoked_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_a2a_client_endpoint_status
    ON a2a_client(endpoint_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS a2a_client_credential (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id UUID NOT NULL REFERENCES a2a_client(id) ON DELETE CASCADE,
    key_id TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'revoked')),
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_by UUID NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
    revoked_at TIMESTAMPTZ,
    revoked_by UUID REFERENCES "user"(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (key_id),
    UNIQUE (token_hash),
    CHECK (char_length(key_id) BETWEEN 8 AND 128),
    CHECK (key_id ~ '^[A-Za-z0-9_-]+$'),
    CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    CHECK (char_length(token_prefix) BETWEEN 4 AND 32),
    CHECK (status <> 'revoked' OR revoked_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_a2a_client_credential_client_status
    ON a2a_client_credential(client_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS a2a_context (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id UUID NOT NULL,
    client_id UUID NOT NULL,
    public_context_id TEXT NOT NULL,
    chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (client_id, endpoint_id)
        REFERENCES a2a_client(id, endpoint_id) ON DELETE CASCADE,
    UNIQUE (endpoint_id, client_id, public_context_id),
    UNIQUE (chat_session_id),
    UNIQUE (id, endpoint_id, client_id),
    CHECK (char_length(public_context_id) BETWEEN 1 AND 256),
    CHECK (btrim(public_context_id) = public_context_id)
);

CREATE INDEX IF NOT EXISTS idx_a2a_context_client_activity
    ON a2a_context(endpoint_id, client_id, last_activity_at DESC);

CREATE TABLE IF NOT EXISTS a2a_task_binding (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id UUID NOT NULL,
    client_id UUID NOT NULL,
    context_id UUID NOT NULL,
    accepted_credential_id UUID REFERENCES a2a_client_credential(id) ON DELETE SET NULL,
    public_task_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    request_fingerprint TEXT NOT NULL,
    artifact_id TEXT NOT NULL,
    root_local_task_id UUID NOT NULL REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    input_chat_message_id UUID NOT NULL REFERENCES chat_message(id) ON DELETE CASCADE,
    request_id TEXT,
    cancel_requested_at TIMESTAMPTZ,
    failure_finalized_local_task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (context_id, endpoint_id, client_id)
        REFERENCES a2a_context(id, endpoint_id, client_id) ON DELETE CASCADE,
    UNIQUE (public_task_id),
    UNIQUE (artifact_id),
    UNIQUE (root_local_task_id),
    UNIQUE (input_chat_message_id),
    UNIQUE (client_id, message_id),
    CHECK (char_length(public_task_id) BETWEEN 16 AND 128),
    CHECK (public_task_id ~ '^[A-Za-z0-9_-]+$'),
    CHECK (char_length(message_id) BETWEEN 1 AND 256),
    CHECK (btrim(message_id) = message_id),
    CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    CHECK (char_length(artifact_id) BETWEEN 16 AND 128),
    CHECK (artifact_id ~ '^[A-Za-z0-9_-]+$'),
    CHECK (request_id IS NULL OR char_length(request_id) <= 256)
);

CREATE INDEX IF NOT EXISTS idx_a2a_task_binding_client_created
    ON a2a_task_binding(endpoint_id, client_id, created_at DESC, public_task_id DESC);

CREATE INDEX IF NOT EXISTS idx_a2a_task_binding_context_created
    ON a2a_task_binding(context_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_a2a_task_binding_failure_pending
    ON a2a_task_binding(updated_at, id)
    WHERE failure_finalized_local_task_id IS NULL
      AND cancel_requested_at IS NULL;
