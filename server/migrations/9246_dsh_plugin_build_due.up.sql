CREATE INDEX CONCURRENTLY IF NOT EXISTS dsh_plugin_build_due_idx
    ON dsh_plugin_build(next_attempt_at, workspace_id)
    WHERE worker_phase <> 'done' AND (state = 'queued' OR worker_phase = 'cleanup');
