CREATE TABLE IF NOT EXISTS runner_machine (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    name VARCHAR(120) NOT NULL,
    os VARCHAR(32) NOT NULL,
    arch VARCHAR(32) NOT NULL,
    public_key BYTEA NOT NULL,
    client_version VARCHAR(64) NOT NULL DEFAULT '',
    last_seen_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revoked_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT runner_machine_public_key_length CHECK (octet_length(public_key) = 32),
    CONSTRAINT runner_machine_owner_public_key UNIQUE (owner_id, public_key)
);

CREATE INDEX IF NOT EXISTS idx_runner_machine_owner
    ON runner_machine(owner_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_runner_machine_online
    ON runner_machine(last_seen_at DESC)
    WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS agent_runner_binding (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    machine_id UUID NOT NULL REFERENCES runner_machine(id) ON DELETE CASCADE,
    bound_by UUID NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
    roots JSONB NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoked_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_runner_binding_roots_array CHECK (jsonb_typeof(roots) = 'array')
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runner_binding_active_unique
    ON agent_runner_binding(agent_id, machine_id)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_agent_runner_binding_agent
    ON agent_runner_binding(workspace_id, agent_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_agent_runner_binding_machine
    ON agent_runner_binding(machine_id)
    WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS runner_pairing_session (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    owner_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    pairing_token_hash CHAR(64) NOT NULL UNIQUE,
    device_code_hash CHAR(64) UNIQUE,
    user_code VARCHAR(12) UNIQUE,
    public_key BYTEA,
    machine_name VARCHAR(120),
    os VARCHAR(32),
    arch VARCHAR(32),
    client_version VARCHAR(64) NOT NULL DEFAULT '',
    roots JSONB,
    state VARCHAR(24) NOT NULL DEFAULT 'pending',
    machine_id UUID REFERENCES runner_machine(id) ON DELETE SET NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    approved_at TIMESTAMPTZ,
    denied_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT runner_pairing_session_state CHECK (
        state IN ('pending', 'device_pending', 'approved', 'denied', 'consumed')
    ),
    CONSTRAINT runner_pairing_session_public_key_length CHECK (
        public_key IS NULL OR octet_length(public_key) = 32
    ),
    CONSTRAINT runner_pairing_session_roots_array CHECK (
        roots IS NULL OR jsonb_typeof(roots) = 'array'
    )
);

CREATE INDEX IF NOT EXISTS idx_runner_pairing_session_agent
    ON runner_pairing_session(agent_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_runner_pairing_session_expiry
    ON runner_pairing_session(expires_at)
    WHERE state IN ('pending', 'device_pending', 'approved');

CREATE TABLE IF NOT EXISTS runner_auth_challenge (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id UUID NOT NULL REFERENCES runner_machine(id) ON DELETE CASCADE,
    challenge_hash CHAR(64) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_runner_auth_challenge_machine
    ON runner_auth_challenge(machine_id, expires_at DESC);

CREATE TABLE IF NOT EXISTS runner_call (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    task_id UUID NOT NULL REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
    machine_id UUID NOT NULL REFERENCES runner_machine(id) ON DELETE CASCADE,
    tool_name VARCHAR(64) NOT NULL,
    arguments JSONB NOT NULL DEFAULT '{}'::jsonb,
    roots JSONB NOT NULL,
    result JSONB,
    status VARCHAR(16) NOT NULL DEFAULT 'queued',
    error_code VARCHAR(64),
    error_message TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT runner_call_status CHECK (
        status IN ('queued', 'running', 'succeeded', 'failed', 'expired')
    ),
    CONSTRAINT runner_call_arguments_object CHECK (jsonb_typeof(arguments) = 'object'),
    CONSTRAINT runner_call_roots_array CHECK (jsonb_typeof(roots) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_runner_call_machine_queue
    ON runner_call(machine_id, created_at)
    WHERE status = 'queued';

CREATE INDEX IF NOT EXISTS idx_runner_call_expiry
    ON runner_call(expires_at)
    WHERE status IN ('queued', 'running');

CREATE INDEX IF NOT EXISTS idx_runner_call_completed
    ON runner_call(completed_at)
    WHERE completed_at IS NOT NULL;

COMMENT ON TABLE runner_machine IS
    'User-owned local Runner identities authenticated by an Ed25519 public key';
COMMENT ON TABLE agent_runner_binding IS
    'Revocable many-to-many bindings between Agents and local Runner machines';
COMMENT ON TABLE runner_pairing_session IS
    'Short-lived browser-approved OAuth device authorization for a Runner binding';
COMMENT ON TABLE runner_call IS
    'Durable cross-replica rendezvous for task-scoped Runner MCP calls';
