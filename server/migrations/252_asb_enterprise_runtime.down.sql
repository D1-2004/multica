DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM fc_e2b_stable_release
        WHERE sandbox_backend <> 'aliyun_fc'
    ) OR EXISTS (
        SELECT 1
        FROM fc_e2b_stable_channel
        WHERE sandbox_backend <> 'aliyun_fc'
    ) OR EXISTS (
        SELECT 1
        FROM fc_e2b_stable_release_target
        WHERE sandbox_backend <> 'aliyun_fc'
    ) OR EXISTS (
        SELECT 1
        FROM fc_e2b_sandbox_session
        WHERE sandbox_backend <> 'aliyun_fc'
           OR identity_fingerprint <> ''
    ) OR EXISTS (
        SELECT 1 FROM agent_enterprise_identity
    ) OR EXISTS (
        SELECT 1
        FROM fc_e2b_stable_release
        GROUP BY template_id, template_build_id
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION
            'cannot roll back migration 252 while ASB runtime or enterprise identity data exists';
    END IF;
END
$$;

DROP TABLE agent_enterprise_identity_attempt;
DROP TABLE agent_enterprise_identity;

DROP INDEX idx_fc_e2b_sandbox_session_identity;

ALTER TABLE fc_e2b_sandbox_session
    DROP CONSTRAINT fc_e2b_sandbox_session_runtime_scope_backend_identity_key,
    DROP CONSTRAINT fc_e2b_sandbox_session_sandbox_backend_check,
    DROP CONSTRAINT fc_e2b_sandbox_session_identity_fingerprint_check,
    ADD CONSTRAINT fc_e2b_sandbox_session_runtime_id_scope_type_scope_id_key
        UNIQUE (runtime_id, scope_type, scope_id),
    DROP COLUMN sandbox_backend,
    DROP COLUMN identity_fingerprint,
    DROP COLUMN artifact_ref;

DROP INDEX fc_e2b_stable_release_target_worker_idx;

CREATE INDEX fc_e2b_stable_release_target_worker_idx
    ON fc_e2b_stable_release_target (
        release_id,
        is_developer,
        status,
        batch_index,
        lease_expires_at,
        runtime_id
    );

ALTER TABLE fc_e2b_stable_release_target
    DROP CONSTRAINT fc_e2b_stable_release_target_sandbox_backend_check,
    DROP CONSTRAINT fc_e2b_stable_release_target_artifact_digest_check,
    DROP COLUMN sandbox_backend,
    DROP COLUMN previous_artifact_ref,
    DROP COLUMN previous_artifact_build_id,
    DROP COLUMN previous_artifact_digest;

ALTER TABLE fc_e2b_stable_channel
    DROP CONSTRAINT fc_e2b_stable_channel_pkey,
    DROP CONSTRAINT fc_e2b_stable_channel_sandbox_backend_check,
    DROP CONSTRAINT fc_e2b_stable_channel_artifact_kind_check,
    DROP CONSTRAINT fc_e2b_stable_channel_artifact_digest_check,
    ADD CONSTRAINT fc_e2b_stable_channel_pkey PRIMARY KEY (channel),
    DROP COLUMN sandbox_backend,
    DROP COLUMN artifact_kind,
    DROP COLUMN current_artifact_ref,
    DROP COLUMN current_artifact_build_id,
    DROP COLUMN current_artifact_digest;

DROP INDEX fc_e2b_stable_release_artifact_idx;
DROP INDEX fc_e2b_stable_release_backend_worker_idx;
DROP INDEX fc_e2b_stable_release_one_active_idx;

CREATE UNIQUE INDEX fc_e2b_stable_release_one_active_idx
    ON fc_e2b_stable_release ((true))
    WHERE status IN (
        'validating',
        'developer_rollout',
        'awaiting_rollout',
        'rolling_out',
        'observing',
        'paused',
        'rolling_back'
    );

ALTER TABLE fc_e2b_stable_release
    DROP CONSTRAINT fc_e2b_stable_release_sandbox_backend_check,
    DROP CONSTRAINT fc_e2b_stable_release_artifact_kind_check,
    DROP CONSTRAINT fc_e2b_stable_release_artifact_ref_check,
    DROP CONSTRAINT fc_e2b_stable_release_artifact_digest_check,
    DROP CONSTRAINT fc_e2b_stable_release_previous_artifact_digest_check,
    DROP COLUMN sandbox_backend,
    DROP COLUMN artifact_kind,
    DROP COLUMN artifact_ref,
    DROP COLUMN artifact_build_id,
    DROP COLUMN artifact_digest,
    DROP COLUMN previous_artifact_ref,
    DROP COLUMN previous_artifact_build_id,
    DROP COLUMN previous_artifact_digest,
    ADD CONSTRAINT fc_e2b_stable_release_template_id_template_build_id_key
        UNIQUE (template_id, template_build_id),
    ALTER COLUMN template_id DROP DEFAULT,
    ALTER COLUMN template_build_id DROP DEFAULT;
