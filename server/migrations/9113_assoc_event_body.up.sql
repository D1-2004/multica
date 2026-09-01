-- Clipped IM body for Coordinator LLM rerank. Empty until the next
-- inbound/outbound write fills it. Application code clips to 160 runes.
ALTER TABLE assoc_event
ADD COLUMN IF NOT EXISTS body TEXT NOT NULL DEFAULT '';
