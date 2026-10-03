CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_delivery_frozen_queue ON webhook_delivery (available_at, created_at) WHERE status = 'queued_frozen';
