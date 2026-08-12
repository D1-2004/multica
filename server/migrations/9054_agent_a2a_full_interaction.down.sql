DROP TABLE IF EXISTS a2a_push_delivery;
DROP TABLE IF EXISTS a2a_push_config;
DROP TABLE IF EXISTS a2a_task_event;
DROP TABLE IF EXISTS a2a_artifact;
DROP TABLE IF EXISTS a2a_task_turn;

DROP INDEX IF EXISTS idx_a2a_task_binding_identity;

ALTER TABLE a2a_task_binding
    DROP CONSTRAINT IF EXISTS a2a_task_binding_public_state_check,
    DROP COLUMN IF EXISTS public_state,
    DROP COLUMN IF EXISTS status_message,
    DROP COLUMN IF EXISTS status_updated_at,
    DROP COLUMN IF EXISTS next_event_sequence;
