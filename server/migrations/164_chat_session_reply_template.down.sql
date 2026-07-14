ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS reply_delivered_at,
    DROP COLUMN IF EXISTS reply_delivery_error,
    DROP COLUMN IF EXISTS reply_delivery_status,
    DROP COLUMN IF EXISTS reply_config,
    DROP COLUMN IF EXISTS reply_template;

DROP INDEX IF EXISTS chat_session_creator_session_key_idx;

ALTER TABLE chat_session
    DROP COLUMN IF EXISTS reply_config,
    DROP COLUMN IF EXISTS reply_template,
    DROP COLUMN IF EXISTS session_key;
