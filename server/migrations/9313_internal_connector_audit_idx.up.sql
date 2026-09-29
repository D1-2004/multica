CREATE INDEX CONCURRENTLY internal_connector_call_audit_scope_idx ON internal_connector_call_audit (workspace_id, task_id, created_at DESC);
