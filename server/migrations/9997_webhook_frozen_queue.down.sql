-- Never expose frozen work to an old consumer by mechanically changing queued state.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM webhook_delivery WHERE status = 'queued_frozen') THEN
        RAISE EXCEPTION 'pending frozen webhook deliveries require a compatible reader or a safe hold; cannot remove queue isolation'
            USING ERRCODE = '55000';
    END IF;
END $$;
DROP TRIGGER IF EXISTS trg_webhook_frozen_queue ON webhook_delivery;
DROP FUNCTION IF EXISTS multica_guard_webhook_frozen_queue();
ALTER TABLE webhook_delivery DROP CONSTRAINT IF EXISTS webhook_delivery_status_check;
ALTER TABLE webhook_delivery ADD CONSTRAINT webhook_delivery_status_check
    CHECK (status IN ('queued', 'dispatched', 'rejected', 'ignored', 'failed')) NOT VALID;
