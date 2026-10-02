CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS context_scope_routine_scene_dedupe_idx ON context_scope_routine (scene_id, dedupe_key);
