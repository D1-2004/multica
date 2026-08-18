-- Persist the adapter-sanitized original channel callback beside the user
-- message so a claimed Agent task can inspect fields that the normalized text
-- model does not understand yet. Credentials are removed before this write.
ALTER TABLE chat_message
ADD COLUMN IF NOT EXISTS source_payload JSONB;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'chat_message_source_payload_object'
          AND conrelid = 'chat_message'::regclass
    ) THEN
        ALTER TABLE chat_message
        ADD CONSTRAINT chat_message_source_payload_object
        CHECK (source_payload IS NULL OR jsonb_typeof(source_payload) = 'object');
    END IF;
END
$$;
