CREATE INDEX CONCURRENTLY IF NOT EXISTS sandbox_send_receipt_due_idx ON sandbox_send_receipt (next_attempt_at, lease_until) WHERE next_attempt_at IS NOT NULL;
