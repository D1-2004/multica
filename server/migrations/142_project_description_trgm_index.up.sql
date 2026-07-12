-- Fallback GIN index for SearchProjects LIKE matches when pg_bigm is not available.
--
-- Guarded on pg_trgm being installed, mirroring 032's bigram indexes: managed
-- Postgres (PolarDB/RDS) refuses CREATE EXTENSION to the app role, so 137 may
-- have skipped the extension — and an unguarded gin_trgm_ops index would then
-- abort the migration run and keep the server from starting. Guarding requires
-- dropping CONCURRENTLY (it cannot run inside a DO block); these indexes are
-- built on small tables and only speed up LIKE lookups, so the brief lock is
-- acceptable. Where pg_trgm IS installed, the resulting index is identical.
DO $$
BEGIN
  CREATE INDEX IF NOT EXISTS idx_project_description_trgm
    ON project USING gin (LOWER(COALESCE(description, '')) gin_trgm_ops);
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'skipping idx_project_description_trgm (pg_trgm not installed)';
END
$$;
