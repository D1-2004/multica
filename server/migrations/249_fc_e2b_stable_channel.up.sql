CREATE TABLE fc_e2b_stable_release (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key TEXT NOT NULL UNIQUE,
    request_fingerprint TEXT NOT NULL,
    template_id TEXT NOT NULL,
    template_build_id TEXT NOT NULL,
    template_alias TEXT NOT NULL DEFAULT '',
    git_commit TEXT NOT NULL CHECK (git_commit ~ '^[0-9a-f]{40}$'),
    acr_digest TEXT NOT NULL CHECK (acr_digest ~ '^sha256:[0-9a-f]{64}$'),
    note TEXT NOT NULL DEFAULT '',
    actor_user_id UUID NOT NULL REFERENCES "user"(id),
    bootstrap BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'validating' CHECK (
        status IN (
            'validating',
            'rolling_out',
            'observing',
            'completed',
            'paused',
            'rolling_back',
            'rolled_back',
            'failed'
        )
    ),
    current_batch SMALLINT NOT NULL DEFAULT 0 CHECK (current_batch BETWEEN 0 AND 4),
    target_percentage SMALLINT NOT NULL DEFAULT 0 CHECK (
        target_percentage IN (0, 5, 25, 50, 100)
    ),
    previous_template_id TEXT NOT NULL DEFAULT '',
    previous_template_build_id TEXT NOT NULL DEFAULT '',
    previous_template_alias TEXT NOT NULL DEFAULT '',
    manifest JSONB NOT NULL DEFAULT '{}'::jsonb,
    total_targets INTEGER NOT NULL DEFAULT 0 CHECK (total_targets >= 0),
    updated_targets INTEGER NOT NULL DEFAULT 0 CHECK (updated_targets >= 0),
    failed_targets INTEGER NOT NULL DEFAULT 0 CHECK (failed_targets >= 0),
    rollout_started_at TIMESTAMPTZ,
    batch_started_at TIMESTAMPTZ,
    next_batch_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    validation_error TEXT NOT NULL DEFAULT '',
    paused_from_status TEXT NOT NULL DEFAULT '',
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (template_id, template_build_id)
);

CREATE UNIQUE INDEX fc_e2b_stable_release_one_active_idx
    ON fc_e2b_stable_release ((true))
    WHERE status IN ('validating', 'rolling_out', 'observing', 'paused', 'rolling_back');

CREATE INDEX fc_e2b_stable_release_worker_idx
    ON fc_e2b_stable_release (status, next_batch_at, lease_expires_at, created_at);

CREATE TABLE fc_e2b_stable_channel (
    channel TEXT PRIMARY KEY CHECK (channel = 'stable'),
    current_template_id TEXT NOT NULL,
    current_template_build_id TEXT NOT NULL,
    current_template_alias TEXT NOT NULL,
    current_release_id UUID NOT NULL REFERENCES fc_e2b_stable_release(id),
    active_release_id UUID REFERENCES fc_e2b_stable_release(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE fc_e2b_stable_release_target (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    release_id UUID NOT NULL REFERENCES fc_e2b_stable_release(id) ON DELETE CASCADE,
    runtime_id UUID NOT NULL REFERENCES agent_runtime(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider IN ('hermes', 'opencode', 'pi')),
    batch_index SMALLINT NOT NULL CHECK (batch_index BETWEEN 1 AND 4),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'updating', 'updated', 'failed', 'rolling_back', 'rolled_back')
    ),
    previous_template_id TEXT NOT NULL DEFAULT '',
    previous_template_build_id TEXT NOT NULL DEFAULT '',
    previous_template_alias TEXT NOT NULL DEFAULT '',
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (release_id, runtime_id)
);

CREATE INDEX fc_e2b_stable_release_target_worker_idx
    ON fc_e2b_stable_release_target (
        release_id,
        status,
        batch_index,
        lease_expires_at,
        runtime_id
    );
