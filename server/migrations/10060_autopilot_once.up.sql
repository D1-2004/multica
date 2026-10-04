-- One-shot schedules retain their absolute time after consumption. The run and
-- occurrence receipt own execution/delivery status; last_fired_at owns admission.
ALTER TABLE autopilot_trigger ADD COLUMN IF NOT EXISTS run_at timestamptz;
ALTER TABLE autopilot_trigger DROP CONSTRAINT IF EXISTS autopilot_trigger_kind_check;
ALTER TABLE autopilot_trigger ADD CONSTRAINT autopilot_trigger_kind_check
    CHECK (kind IN ('schedule', 'once', 'webhook', 'api', 'dingtalk_message'));
ALTER TABLE autopilot_trigger DROP CONSTRAINT IF EXISTS autopilot_trigger_once_time_check;
ALTER TABLE autopilot_trigger ADD CONSTRAINT autopilot_trigger_once_time_check
    CHECK ((kind = 'once' AND run_at IS NOT NULL AND cron_expression IS NULL)
        OR (kind <> 'once' AND run_at IS NULL));
