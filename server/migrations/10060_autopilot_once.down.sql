-- Refuse destructive rollback before dropping any guard or persisted time.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM autopilot_trigger WHERE kind = 'once') THEN
        RAISE EXCEPTION 'cannot roll back one-shot scheduling while once triggers exist';
    END IF;
END $$;
ALTER TABLE autopilot_trigger DROP CONSTRAINT IF EXISTS autopilot_trigger_once_time_check;
ALTER TABLE autopilot_trigger DROP CONSTRAINT IF EXISTS autopilot_trigger_kind_check;
ALTER TABLE autopilot_trigger ADD CONSTRAINT autopilot_trigger_kind_check
    CHECK (kind IN ('schedule', 'webhook', 'api', 'dingtalk_message'));
ALTER TABLE autopilot_trigger DROP COLUMN IF EXISTS run_at;
