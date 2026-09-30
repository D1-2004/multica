-- A personal link minted in a 1:1 chat also grants the redeemer the scene
-- grant for that 1:1 conversation (a DM is a scene). extra_scene_key is the
-- DM openConversationId, '' for every other link.
ALTER TABLE context_config_link ADD COLUMN IF NOT EXISTS extra_scene_key text NOT NULL DEFAULT '';
