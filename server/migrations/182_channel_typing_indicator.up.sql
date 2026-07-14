-- Persist the pending "typing"/"thinking" indicators so any replica can clear
-- them.
--
-- The indicator is added on the inbound path, which the WS lease pins to one
-- replica, but it is cleared from the task-lifecycle event — published by
-- whichever replica served the daemon's `POST /tasks/{id}/complete`, a plain
-- load-balanced HTTP call. With the reaction ids in a process-local map, a
-- clear that landed on any other replica found nothing and silently no-opped:
-- the user got their answer with the 🤔 / 👀 / Typing reaction stuck on their
-- message forever, on roughly every other run.
--
-- (The code asserted this was safe — "multi-replica safety is inherited from
-- the inbound WS lease" — which is true of the *reply* but not of the clear:
-- the lease says which replica ingests, not which one reacts.)
--
-- target is opaque, platform-shaped JSON, so one table serves every channel:
--   dingtalk {"open_conversation_id": ..., "open_msg_id": ...}
--   lark     {"message_id": ..., "reaction_id": ...}
--   slack    {"channel_id": ..., "message_ts": ...}
--
-- Idempotent: this shipped as 168 before an upstream sync claimed that number,
-- and the runner keys applied migrations by full filename stem — so under its
-- new stem it re-runs against a database that already has the table.
CREATE TABLE IF NOT EXISTS channel_typing_indicator (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
    channel_type    TEXT NOT NULL,
    installation_id UUID NOT NULL,
    target          JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The clear path takes every pending indicator for one session of one channel.
CREATE INDEX IF NOT EXISTS idx_channel_typing_indicator_session
    ON channel_typing_indicator (chat_session_id, channel_type);
