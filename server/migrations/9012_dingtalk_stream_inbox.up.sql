-- Durable admission queue for DingTalk Stream callbacks.
--
-- The Stream connector must commit one row before returning the platform ACK.
-- Business processing happens later under a token-fenced lease, so a process
-- crash or a transient Dapr/database failure cannot lose an acknowledged
-- callback. installation_id intentionally has no foreign key: the generalized
-- channel_* schema keeps channel lifecycle integrity in the application layer.
CREATE TABLE IF NOT EXISTS dingtalk_stream_inbox (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id       UUID NOT NULL,
    client_id             TEXT NOT NULL,
    -- Non-secret source identity for SLS correlation. On redelivery these
    -- fields are refreshed to the connection that most recently delivered the
    -- callback; installation_id remains the immutable routing fence.
    connection_id         TEXT NOT NULL,
    node_id               TEXT NOT NULL,
    -- Prefer the logical bot msgId. The Stream frame messageId is only the
    -- fallback for malformed payloads that cannot expose a bot message id.
    dedupe_key             TEXT NOT NULL,
    stream_message_id      TEXT NOT NULL,
    bot_message_id         TEXT,
    topic                  TEXT NOT NULL,
    spec_version           TEXT,
    frame_time             BIGINT,
    -- frame.data contains sessionWebhook, a short-lived bearer-style reply
    -- credential. Application code encrypts it with MULTICA_DINGTALK_SECRET_KEY
    -- before INSERT. Successful/discarded rows clear it immediately; dead
    -- letters retain ciphertext until the seven-day purge so they can be
    -- manually requeued after dependency repair.
    payload_encrypted      BYTEA,
    status                 TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'processed', 'discarded', 'dead')),
    delivery_count         INTEGER NOT NULL DEFAULT 1 CHECK (delivery_count >= 1),
    attempt_count          INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    available_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_token            UUID,
    lease_expires_at       TIMESTAMPTZ,
    last_error_class       TEXT,
    last_error             TEXT,
    received_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at           TIMESTAMPTZ,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (installation_id, dedupe_key),
    CHECK (
        (status = 'processing' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR
        (status <> 'processing' AND lease_token IS NULL AND lease_expires_at IS NULL)
    ),
    CHECK (status NOT IN ('queued', 'processing') OR payload_encrypted IS NOT NULL)
);

-- Oldest non-terminal row is the installation lane head. Workers serialize
-- each installation with a session advisory lock and never skip this head.
CREATE INDEX IF NOT EXISTS idx_dingtalk_stream_inbox_lane_head
    ON dingtalk_stream_inbox (installation_id, received_at, id)
    WHERE status IN ('queued', 'processing');

CREATE INDEX IF NOT EXISTS idx_dingtalk_stream_inbox_queued_due
    ON dingtalk_stream_inbox (available_at, received_at)
    WHERE status = 'queued';

CREATE INDEX IF NOT EXISTS idx_dingtalk_stream_inbox_processing_lease
    ON dingtalk_stream_inbox (lease_expires_at)
    WHERE status = 'processing';
