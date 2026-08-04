ALTER TABLE fc_e2b_stable_release
    DROP CONSTRAINT fc_e2b_stable_release_artifact_built_at_check,
    DROP COLUMN artifact_built_at;
