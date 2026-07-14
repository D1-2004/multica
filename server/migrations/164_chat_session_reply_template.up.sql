ALTER TABLE chat_session
    ADD COLUMN session_key TEXT,
    ADD COLUMN reply_template TEXT NOT NULL DEFAULT '',
    ADD COLUMN reply_config JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE UNIQUE INDEX chat_session_creator_session_key_idx
    ON chat_session (workspace_id, creator_id, session_key)
    WHERE session_key IS NOT NULL;

ALTER TABLE agent_task_queue
    ADD COLUMN reply_template TEXT NOT NULL DEFAULT '',
    ADD COLUMN reply_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN reply_delivery_status TEXT NOT NULL DEFAULT 'not_configured'
        CHECK (reply_delivery_status IN ('not_configured', 'pending', 'delivered', 'failed')),
    ADD COLUMN reply_delivery_error TEXT,
    ADD COLUMN reply_delivered_at TIMESTAMPTZ;
