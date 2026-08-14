ALTER TABLE chat_message
DROP CONSTRAINT IF EXISTS chat_message_source_payload_object;

ALTER TABLE chat_message
DROP COLUMN IF EXISTS source_payload;
