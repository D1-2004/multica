-- 9510 registered every conversation stored scene configuration named, and
-- gave it the dm kind only on positive evidence, else group. That group is a
-- default, not an observation (docs/agent-scene.md §8): an earlier release
-- also stored 1:1 chats under their own scene key. kind_source tells the two
-- apart. Only a trusted inbound event that states the conversation type may
-- settle a 'migrated' kind (scene.Resolve); every other kind mismatch stays a
-- conflict. Which scenes are migrated, and which evidence settles them, is
-- 9512.
ALTER TABLE agent_scene ADD COLUMN IF NOT EXISTS kind_source text NOT NULL DEFAULT 'observed';

DO $$
BEGIN
    ALTER TABLE agent_scene ADD CONSTRAINT agent_scene_kind_source_check
        CHECK (kind_source IN ('observed', 'migrated'));
EXCEPTION WHEN duplicate_object THEN
    NULL;
END $$;
