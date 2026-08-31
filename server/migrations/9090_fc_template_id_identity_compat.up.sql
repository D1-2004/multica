-- Stage A keeps the deprecated build-ID columns and metadata intact while
-- production rolls from the previous binary one replica at a time. The old
-- stable-release worker still reads both, so physical/data cleanup must wait
-- until every replica runs template-ID-only code.
ALTER TABLE fc_e2b_stable_release_target
    DROP CONSTRAINT IF EXISTS fc_e2b_stable_release_target_provider_check;
