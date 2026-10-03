CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_routine_occurrence_event_idx ON employee_routine_occurrence (source, source_event_id);
