ALTER TABLE fc_e2b_stable_release
    DROP CONSTRAINT fc_e2b_stable_release_status_check,
    DROP CONSTRAINT fc_e2b_stable_release_template_id_template_build_id_key,
    ADD COLUMN developer_rollout_started_at TIMESTAMPTZ,
    ADD COLUMN developer_rollout_completed_at TIMESTAMPTZ,
    ADD CONSTRAINT fc_e2b_stable_release_status_check CHECK (
        status IN (
            'validating',
            'developer_rollout',
            'awaiting_rollout',
            'rolling_out',
            'observing',
            'completed',
            'paused',
            'rolling_back',
            'rolled_back',
            'terminated',
            'failed'
        )
    );

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

ALTER TABLE fc_e2b_stable_release_target
    ADD COLUMN is_developer BOOLEAN NOT NULL DEFAULT false;

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
