ALTER TABLE agent_scene DROP CONSTRAINT IF EXISTS agent_scene_kind_source_check;
ALTER TABLE agent_scene DROP COLUMN IF EXISTS kind_source;
