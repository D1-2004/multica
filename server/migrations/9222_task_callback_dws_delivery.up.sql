-- Callback acceptance and DWS delivery are independently durable. No credentials are stored.
ALTER TABLE task_completion_outbox ADD COLUMN IF NOT EXISTS dws_delivery JSONB;
ALTER TABLE task_execution_update_outbox ADD COLUMN IF NOT EXISTS dws_delivery JSONB;
