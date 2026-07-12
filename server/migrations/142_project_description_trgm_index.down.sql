-- Non-concurrent to match the guarded up migration (which cannot use
-- CONCURRENTLY inside its DO block).
DROP INDEX IF EXISTS idx_project_description_trgm;
