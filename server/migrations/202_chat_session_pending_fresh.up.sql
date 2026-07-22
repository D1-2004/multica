-- Persists a bare channel /reset until the next runnable chat task consumes it.
-- Row presence is the pending flag; the chat-session foreign key scopes and
-- cleans it up with the conversation.
CREATE TABLE IF NOT EXISTS chat_session_pending_fresh (
    chat_session_id UUID PRIMARY KEY REFERENCES chat_session(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
