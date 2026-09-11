-- Keep portable objective text independent from globally unique issue tags.
ALTER TABLE agent_okr ADD COLUMN IF NOT EXISTS authored_text text;
