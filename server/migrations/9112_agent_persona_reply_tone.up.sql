-- Coordinator reply voice. Empty means the inbound loop uses a concise
-- colleague default. Independent of instructions, which remain the sandbox
-- working rules.
ALTER TABLE agent
ADD COLUMN IF NOT EXISTS persona TEXT NOT NULL DEFAULT '';

ALTER TABLE agent
ADD COLUMN IF NOT EXISTS reply_tone TEXT NOT NULL DEFAULT '';
