-- Keep the browser-render receipt idempotent across retries, page reloads,
-- multiple tabs, and multiple server replicas. The timestamp is set by the
-- server; client-reported timing remains diagnostic metadata in SLS.
ALTER TABLE chat_message
ADD COLUMN client_receipt_recorded_at TIMESTAMPTZ;
