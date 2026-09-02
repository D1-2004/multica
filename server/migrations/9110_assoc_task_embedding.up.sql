-- Reserve assoc_task.embedding for later semantic recall. v1 does not write
-- or index it. PolarDB may not expose the vector type to the app role.
DO $$
BEGIN
  ALTER TABLE assoc_task ADD COLUMN IF NOT EXISTS embedding vector;
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'skipping assoc_task.embedding (vector type not available)';
END
$$;
