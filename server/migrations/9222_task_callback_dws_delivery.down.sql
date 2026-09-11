ALTER TABLE task_execution_update_outbox DROP COLUMN IF EXISTS dws_delivery;
ALTER TABLE task_completion_outbox DROP COLUMN IF EXISTS dws_delivery;
