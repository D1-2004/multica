ALTER TABLE fc_e2b_stable_release
    DROP CONSTRAINT fc_e2b_stable_release_template_id_template_build_id_key,
    ALTER COLUMN template_id SET DEFAULT '',
    ALTER COLUMN template_build_id SET DEFAULT '',
    ADD COLUMN sandbox_backend TEXT NOT NULL DEFAULT 'aliyun_fc',
    ADD COLUMN artifact_kind TEXT NOT NULL DEFAULT 'e2b_template',
    ADD COLUMN artifact_ref TEXT NOT NULL DEFAULT '',
    ADD COLUMN artifact_build_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN artifact_digest TEXT NOT NULL DEFAULT '',
    ADD COLUMN previous_artifact_ref TEXT NOT NULL DEFAULT '',
    ADD COLUMN previous_artifact_build_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN previous_artifact_digest TEXT NOT NULL DEFAULT '';

UPDATE fc_e2b_stable_release
SET artifact_ref = template_id,
    artifact_build_id = template_build_id,
    artifact_digest = acr_digest,
    previous_artifact_ref = previous_template_id,
    previous_artifact_build_id = previous_template_build_id;

ALTER TABLE fc_e2b_stable_release
    ADD CONSTRAINT fc_e2b_stable_release_sandbox_backend_check CHECK (
        sandbox_backend IN ('aliyun_fc', 'asb')
    ),
    ADD CONSTRAINT fc_e2b_stable_release_artifact_kind_check CHECK (
        (sandbox_backend = 'aliyun_fc' AND artifact_kind = 'e2b_template')
        OR (sandbox_backend = 'asb' AND artifact_kind = 'oci_image')
    ),
    ADD CONSTRAINT fc_e2b_stable_release_artifact_ref_check CHECK (
        artifact_ref <> ''
    ),
    ADD CONSTRAINT fc_e2b_stable_release_artifact_digest_check CHECK (
        artifact_digest = '' OR artifact_digest ~ '^sha256:[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT fc_e2b_stable_release_previous_artifact_digest_check CHECK (
        previous_artifact_digest = ''
        OR previous_artifact_digest ~ '^sha256:[0-9a-f]{64}$'
    );

DROP INDEX fc_e2b_stable_release_one_active_idx;

CREATE UNIQUE INDEX fc_e2b_stable_release_one_active_idx
    ON fc_e2b_stable_release (sandbox_backend)
    WHERE status IN (
        'validating',
        'developer_rollout',
        'awaiting_rollout',
        'rolling_out',
        'observing',
        'paused',
        'rolling_back'
    );

CREATE INDEX fc_e2b_stable_release_backend_worker_idx
    ON fc_e2b_stable_release (
        sandbox_backend,
        status,
        next_batch_at,
        lease_expires_at,
        created_at
    );

CREATE UNIQUE INDEX fc_e2b_stable_release_artifact_idx
    ON fc_e2b_stable_release (
        sandbox_backend,
        artifact_ref,
        artifact_build_id
    )
    WHERE status <> 'failed';

ALTER TABLE fc_e2b_stable_channel
    ADD COLUMN sandbox_backend TEXT NOT NULL DEFAULT 'aliyun_fc',
    ADD COLUMN artifact_kind TEXT NOT NULL DEFAULT 'e2b_template',
    ADD COLUMN current_artifact_ref TEXT NOT NULL DEFAULT '',
    ADD COLUMN current_artifact_build_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN current_artifact_digest TEXT NOT NULL DEFAULT '';

UPDATE fc_e2b_stable_channel
SET current_artifact_ref = current_template_id,
    current_artifact_build_id = current_template_build_id
FROM fc_e2b_stable_release release
WHERE release.id = fc_e2b_stable_channel.current_release_id;

UPDATE fc_e2b_stable_channel
SET current_artifact_digest = release.artifact_digest
FROM fc_e2b_stable_release release
WHERE release.id = fc_e2b_stable_channel.current_release_id;

ALTER TABLE fc_e2b_stable_channel
    DROP CONSTRAINT fc_e2b_stable_channel_pkey,
    ADD CONSTRAINT fc_e2b_stable_channel_pkey PRIMARY KEY (sandbox_backend, channel),
    ADD CONSTRAINT fc_e2b_stable_channel_sandbox_backend_check CHECK (
        sandbox_backend IN ('aliyun_fc', 'asb')
    ),
    ADD CONSTRAINT fc_e2b_stable_channel_artifact_kind_check CHECK (
        (sandbox_backend = 'aliyun_fc' AND artifact_kind = 'e2b_template')
        OR (sandbox_backend = 'asb' AND artifact_kind = 'oci_image')
    ),
    ADD CONSTRAINT fc_e2b_stable_channel_artifact_digest_check CHECK (
        current_artifact_digest = ''
        OR current_artifact_digest ~ '^sha256:[0-9a-f]{64}$'
    );

ALTER TABLE fc_e2b_stable_release_target
    ADD COLUMN sandbox_backend TEXT NOT NULL DEFAULT 'aliyun_fc',
    ADD COLUMN previous_artifact_ref TEXT NOT NULL DEFAULT '',
    ADD COLUMN previous_artifact_build_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN previous_artifact_digest TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT fc_e2b_stable_release_target_sandbox_backend_check CHECK (
        sandbox_backend IN ('aliyun_fc', 'asb')
    ),
    ADD CONSTRAINT fc_e2b_stable_release_target_artifact_digest_check CHECK (
        previous_artifact_digest = ''
        OR previous_artifact_digest ~ '^sha256:[0-9a-f]{64}$'
    );

UPDATE fc_e2b_stable_release_target target
SET previous_artifact_ref = target.previous_template_id,
    previous_artifact_build_id = target.previous_template_build_id;

DROP INDEX fc_e2b_stable_release_target_worker_idx;

CREATE INDEX fc_e2b_stable_release_target_worker_idx
    ON fc_e2b_stable_release_target (
        sandbox_backend,
        release_id,
        is_developer,
        status,
        batch_index,
        lease_expires_at,
        runtime_id
    );

ALTER TABLE fc_e2b_sandbox_session
    ADD COLUMN sandbox_backend TEXT NOT NULL DEFAULT 'aliyun_fc',
    ADD COLUMN identity_fingerprint TEXT NOT NULL DEFAULT '',
    ADD COLUMN artifact_ref TEXT NOT NULL DEFAULT '';

UPDATE fc_e2b_sandbox_session
SET artifact_ref = template;

ALTER TABLE fc_e2b_sandbox_session
    DROP CONSTRAINT fc_e2b_sandbox_session_runtime_id_scope_type_scope_id_key,
    ADD CONSTRAINT fc_e2b_sandbox_session_sandbox_backend_check CHECK (
        sandbox_backend IN ('aliyun_fc', 'asb')
    ),
    ADD CONSTRAINT fc_e2b_sandbox_session_identity_fingerprint_check CHECK (
        identity_fingerprint = ''
        OR identity_fingerprint ~ '^[0-9a-f]{64}$'
    ),
    ADD CONSTRAINT fc_e2b_sandbox_session_runtime_scope_backend_identity_key
        UNIQUE (
            runtime_id,
            scope_type,
            scope_id,
            sandbox_backend,
            identity_fingerprint
        );

CREATE INDEX idx_fc_e2b_sandbox_session_identity
    ON fc_e2b_sandbox_session (
        workspace_id,
        runtime_id,
        sandbox_backend,
        identity_fingerprint,
        status,
        expires_at
    );

CREATE TABLE agent_enterprise_identity (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    raw_emp_id TEXT NOT NULL CHECK (raw_emp_id ~ '^[1-9][0-9]*$'),
    display_name TEXT NOT NULL DEFAULT '',
    buc_agent_id TEXT NOT NULL CHECK (buc_agent_id <> ''),
    agent_spiffe_id TEXT NOT NULL CHECK (agent_spiffe_id LIKE 'spiffe://%'),
    aip_id TEXT NOT NULL CHECK (aip_id <> ''),
    buc_anchor_sandbox_id TEXT,
    authx_refresh_token_encrypted BYTEA,
    authx_refresh_expires_at TIMESTAMPTZ,
    token_version BIGINT NOT NULL DEFAULT 1 CHECK (token_version > 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK (
        status IN ('active', 'needs_reauth', 'revoked')
    ),
    bound_by UUID NOT NULL REFERENCES "user"(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, agent_id),
    CHECK (
        status <> 'active'
        OR (
            buc_anchor_sandbox_id IS NOT NULL
            AND authx_refresh_token_encrypted IS NOT NULL
            AND octet_length(authx_refresh_token_encrypted) > 28
            AND authx_refresh_expires_at IS NOT NULL
        )
    )
);

CREATE INDEX agent_enterprise_identity_rotation_idx
    ON agent_enterprise_identity (authx_refresh_expires_at, token_version)
    WHERE status = 'active';

CREATE INDEX agent_enterprise_identity_anchor_maintenance_idx
    ON agent_enterprise_identity (updated_at, token_version)
    WHERE status = 'active';

CREATE INDEX agent_enterprise_identity_employee_idx
    ON agent_enterprise_identity (workspace_id, raw_emp_id)
    WHERE status <> 'revoked';

CREATE TABLE agent_enterprise_identity_attempt (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    actor_user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    requested_raw_emp_id TEXT NOT NULL CHECK (
        requested_raw_emp_id ~ '^[1-9][0-9]*$'
    ),
    state_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(state_hash) = 32),
    nonce_hash BYTEA NOT NULL CHECK (octet_length(nonce_hash) = 32),
    pkce_verifier_encrypted BYTEA,
    redirect_path TEXT NOT NULL CHECK (
        left(redirect_path, 1) = '/'
        AND left(redirect_path, 2) <> '//'
        AND position(E'\n' IN redirect_path) = 0
        AND position(E'\r' IN redirect_path) = 0
    ),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX agent_enterprise_identity_attempt_expiry_idx
    ON agent_enterprise_identity_attempt (expires_at)
    WHERE consumed_at IS NULL;
