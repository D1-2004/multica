ALTER TABLE a2ui_interaction ADD COLUMN IF NOT EXISTS message_id text NOT NULL DEFAULT '';
ALTER TABLE a2ui_interaction ADD COLUMN IF NOT EXISTS thread_id text NOT NULL DEFAULT '';
ALTER TABLE a2ui_interaction ADD COLUMN IF NOT EXISTS source_ref text NOT NULL DEFAULT '';
