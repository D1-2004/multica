-- Install pg_trgm for the fallback trigram search indexes added in the next
-- migrations.
--
-- Skips gracefully when the role may not create extensions — the same
-- DO/EXCEPTION shape as 032 (pg_bigm) and 076 (pg_cron). Managed Postgres
-- (PolarDB/RDS) hands the app a non-superuser role and rejects CREATE EXTENSION
-- with SQLSTATE 42501 even for extensions Postgres marks as trusted, so an
-- unguarded CREATE EXTENSION aborts the entire migration run and the server
-- never starts. These indexes only speed up LIKE lookups (search.go runs
-- LOWER(col) LIKE either way), so skipping them costs performance, never
-- correctness.
--
-- To enable them on a managed instance, have the privileged account run
-- `CREATE EXTENSION pg_trgm;` once against the app database; 138-142 then
-- create their indexes on the next migration run.
DO $$
BEGIN
  CREATE EXTENSION IF NOT EXISTS pg_trgm;
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'pg_trgm not available (role may lack CREATE EXTENSION); skipping trigram indexes';
END
$$;
