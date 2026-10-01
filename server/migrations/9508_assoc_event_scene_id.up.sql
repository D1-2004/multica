-- Association events carry the Agent work scene they happened in
-- (agent_scene.id, docs/agent-scene.md). scene_key, the raw conversation id
-- the graph used before, is no longer written or read; assoc_scene is no
-- longer the scene directory.
ALTER TABLE assoc_event ADD COLUMN IF NOT EXISTS scene_id uuid;
