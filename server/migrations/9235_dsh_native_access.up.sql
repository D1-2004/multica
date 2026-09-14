CREATE TABLE IF NOT EXISTS dsh_native_access (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    user_id uuid NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    sandbox_id text NOT NULL CHECK (sandbox_id <> ''),
    kind text NOT NULL CHECK (kind IN ('entry', 'session', 'revoked')),
    token_hash text NOT NULL CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    exchanged_at timestamptz,
    revoked_at timestamptz
);
