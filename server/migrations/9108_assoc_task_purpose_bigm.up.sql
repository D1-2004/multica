-- CJK keyword search on assoc_task.purpose. Same PolarDB-safe guard as issue search:
-- CREATE INDEX CONCURRENTLY cannot run inside DO, so this is a brief-lock GIN
-- on a new empty table. Skip if pg_bigm (then pg_trgm) is unavailable.
DO $$
BEGIN
  CREATE INDEX IF NOT EXISTS assoc_task_purpose_bigm_idx
    ON assoc_task USING gin (purpose gin_bigm_ops);
EXCEPTION WHEN OTHERS THEN
  BEGIN
    CREATE INDEX IF NOT EXISTS assoc_task_purpose_trgm_idx
      ON assoc_task USING gin (LOWER(purpose) gin_trgm_ops);
  EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'skipping assoc_task purpose search index (pg_bigm/pg_trgm not installed)';
  END;
END
$$;
