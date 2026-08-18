DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM fc_e2b_stable_release
        WHERE status IN ('developer_rollout', 'awaiting_rollout', 'terminated')
    ) THEN
        RAISE EXCEPTION 'cannot remove developer-first rollout while releases use the new states';
    END IF;

    IF EXISTS (
        SELECT template_id, template_build_id
        FROM fc_e2b_stable_release
        GROUP BY template_id, template_build_id
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot restore template/build uniqueness after repeated releases exist';
    END IF;
END
$$;

DROP INDEX fc_e2b_stable_release_target_worker_idx;

ALTER TABLE fc_e2b_stable_release_target
    DROP COLUMN is_developer;

CREATE INDEX fc_e2b_stable_release_target_worker_idx
    ON fc_e2b_stable_release_target (
        release_id,
        status,
        batch_index,
        lease_expires_at,
        runtime_id
    );

DROP INDEX fc_e2b_stable_release_one_active_idx;

ALTER TABLE fc_e2b_stable_release
    DROP CONSTRAINT fc_e2b_stable_release_status_check,
    DROP COLUMN developer_rollout_started_at,
    DROP COLUMN developer_rollout_completed_at,
    ADD CONSTRAINT fc_e2b_stable_release_status_check CHECK (
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
    ADD CONSTRAINT fc_e2b_stable_release_template_id_template_build_id_key
        UNIQUE (template_id, template_build_id);

CREATE UNIQUE INDEX fc_e2b_stable_release_one_active_idx
    ON fc_e2b_stable_release ((true))
    WHERE status IN ('validating', 'rolling_out', 'observing', 'paused', 'rolling_back');
