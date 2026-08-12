ALTER TABLE fc_e2b_stable_release
    ADD COLUMN artifact_built_at TIMESTAMPTZ;

-- Older ASB releases did not retain the Aone build timestamp. Keep the
-- historical list complete by using their original registration time.
UPDATE fc_e2b_stable_release
SET artifact_built_at = created_at
WHERE sandbox_backend = 'asb'
  AND artifact_built_at IS NULL;

ALTER TABLE fc_e2b_stable_release
    ADD CONSTRAINT fc_e2b_stable_release_artifact_built_at_check CHECK (
        (sandbox_backend = 'asb' AND artifact_built_at IS NOT NULL)
        OR (sandbox_backend = 'aliyun_fc' AND artifact_built_at IS NULL)
    );
