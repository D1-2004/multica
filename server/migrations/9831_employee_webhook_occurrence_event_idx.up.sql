CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_webhook_occurrence_event_idx ON employee_webhook_occurrence (source, source_event_id);
