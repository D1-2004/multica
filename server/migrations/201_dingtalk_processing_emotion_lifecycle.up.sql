-- Durable lifecycle for DingTalk Stream's "thinking" emotion.
--
-- Stream attaches the emotion before identity/runtime resolution starts.  The
-- task/session identifiers therefore arrive later.  Keeping this state in a
-- DingTalk-specific table lets the terminal path race safely with an in-flight
-- add request and lets any replica retry a failed recall.
CREATE TABLE dingtalk_processing_emotion (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id      UUID NOT NULL REFERENCES channel_installation(id) ON DELETE CASCADE,
    source_message_id    TEXT NOT NULL,
    open_conversation_id TEXT NOT NULL,
    open_msg_id          TEXT NOT NULL,
    robot_code           TEXT NOT NULL,
    chat_session_id      UUID REFERENCES chat_session(id) ON DELETE CASCADE,
    task_id              UUID,
    state                TEXT NOT NULL CHECK (state IN ('adding', 'active', 'settled')),
    add_completed        BOOLEAN NOT NULL DEFAULT false,
    attempt_count        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until          TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (installation_id, source_message_id)
);

CREATE INDEX idx_dingtalk_processing_emotion_task
    ON dingtalk_processing_emotion (chat_session_id, task_id)
    WHERE task_id IS NOT NULL;

CREATE INDEX idx_dingtalk_processing_emotion_retry
    ON dingtalk_processing_emotion (next_attempt_at)
    WHERE state IN ('adding', 'settled');
