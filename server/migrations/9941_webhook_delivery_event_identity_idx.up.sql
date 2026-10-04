-- Event identity lookup for webhook ingress: an authenticated delivery holds
-- its (trigger, event id) whatever its later status, failed included, so the
-- ingress looks up every non-rejected delivery with that id before inserting.
-- The partial unique dedupe index skips failed rows and cannot serve it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_delivery_event_identity
    ON webhook_delivery (trigger_id, dedupe_key)
    WHERE dedupe_key IS NOT NULL;
