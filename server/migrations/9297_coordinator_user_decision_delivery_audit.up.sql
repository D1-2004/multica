ALTER TABLE coordinator_user_decision_event
 ADD COLUMN delivery_count bigint NOT NULL DEFAULT 1,
 ADD COLUMN last_received_at timestamptz NOT NULL DEFAULT now(),
 ADD COLUMN last_delivery_outcome text NOT NULL DEFAULT '';
