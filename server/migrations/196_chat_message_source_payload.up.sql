-- Persist the adapter-sanitized original channel callback beside the user
-- message so a claimed Agent task can inspect fields that the normalized text
-- model does not understand yet. Credentials are removed before this write.
ALTER TABLE chat_message
ADD COLUMN source_payload JSONB;

ALTER TABLE chat_message
ADD CONSTRAINT chat_message_source_payload_object
CHECK (source_payload IS NULL OR jsonb_typeof(source_payload) = 'object');
