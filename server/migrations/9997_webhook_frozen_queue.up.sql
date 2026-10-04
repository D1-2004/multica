-- Widen before installing the guard so old producers can be isolated atomically.
ALTER TABLE webhook_delivery DROP CONSTRAINT IF EXISTS webhook_delivery_status_check;
ALTER TABLE webhook_delivery ADD CONSTRAINT webhook_delivery_status_check
    CHECK (status IN ('queued', 'queued_frozen', 'dispatched', 'rejected', 'ignored', 'failed')) NOT VALID;

CREATE OR REPLACE FUNCTION multica_guard_webhook_frozen_queue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'queued' AND (NEW.source_binding @> '{"v":1}'::jsonb
        OR (TG_OP = 'UPDATE' AND OLD.status = 'queued_frozen')) THEN
        NEW.status := 'queued_frozen';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_webhook_frozen_queue ON webhook_delivery;
CREATE TRIGGER trg_webhook_frozen_queue BEFORE INSERT OR UPDATE ON webhook_delivery
    FOR EACH ROW EXECUTE FUNCTION multica_guard_webhook_frozen_queue();
-- Existing leases are retained: rollout must settle pre-migration consumers.
UPDATE webhook_delivery SET status = 'queued_frozen'
    WHERE status = 'queued' AND source_binding @> '{"v":1}'::jsonb;
