CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_event_consumption_receipt_idx ON employee_event_consumption (receipt_id, consumer);
