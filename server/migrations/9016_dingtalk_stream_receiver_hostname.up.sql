-- Capture the transport host separately from the process-random node_id so a
-- callback can carry its receiver machine across the durable task handoff.
ALTER TABLE dingtalk_stream_inbox
    ADD COLUMN IF NOT EXISTS receiver_hostname TEXT;
