-- 9510 registered every conversation stored scene configuration named, and
-- gave it the dm kind only on positive evidence, else group. That group is a
-- default, not an observation (docs/agent-scene.md §8): an earlier release
-- also stored 1:1 chats under their own scene key. kind_source tells the two
-- apart. Only a trusted inbound event that states the conversation type may
-- settle a 'migrated' kind (scene.Resolve); every other kind mismatch stays a
-- conflict.
ALTER TABLE agent_scene ADD COLUMN IF NOT EXISTS kind_source text NOT NULL DEFAULT 'observed';

DO $$
BEGIN
    ALTER TABLE agent_scene ADD CONSTRAINT agent_scene_kind_source_check
        CHECK (kind_source IN ('observed', 'migrated'));
EXCEPTION WHEN duplicate_object THEN
    NULL;
END $$;

-- Scenes 9510 registered from stored configuration that no inbound event has
-- referenced since (no Coordinator job or association event carries them).
UPDATE agent_scene s SET kind_source = 'migrated'
WHERE s.kind_source = 'observed'
  AND s.source_namespace = 'dingtalk.open_conversation_id'
  AND (
      EXISTS (SELECT 1 FROM context_capability_binding t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.scope_key = s.id::text)
   OR EXISTS (SELECT 1 FROM context_connector_credential t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.scope_key IN (s.id::text, s.external_scene_id))
   OR EXISTS (SELECT 1 FROM context_config_grant t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.scope_key = s.id::text)
   OR EXISTS (SELECT 1 FROM context_config_link t
              WHERE t.agent_id = s.agent_id AND (t.scope_key = s.id::text OR t.extra_scene_key = s.id::text))
   OR EXISTS (SELECT 1 FROM context_scope_mcp_config t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.scope_key = s.id::text)
   OR EXISTS (SELECT 1 FROM context_prompt_component t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.scope_key = s.id::text)
   OR EXISTS (SELECT 1 FROM agent_scene_config t
              WHERE t.agent_id = s.agent_id AND t.platform = 'dingtalk' AND t.scene_key = s.external_scene_id)
  )
  AND NOT EXISTS (SELECT 1 FROM inbound_coordinator_job j
                  WHERE j.agent_id = s.agent_id AND (j.command #>> '{agent_scene,scene_id}') = s.id::text)
  AND NOT EXISTS (SELECT 1 FROM assoc_event e
                  WHERE e.agent_id = s.agent_id AND e.scene_id = s.id);

-- A migrated scene takes the kind trusted records written before scene ids
-- agree on: the retired Scene Memory row's kind and the conversation type
-- of the agent's inbound Coordinator jobs. Conflicting or missing evidence
-- leaves it migrated for the next inbound event to settle.
WITH evidence AS (
    SELECT agent_id, scene_key AS cid, scene_kind AS kind
    FROM scene_memory WHERE scene_kind IN ('group', 'dm')
    UNION
    SELECT agent_id, command #>> '{event,data,conversation,openConversationId}',
        CASE lower(btrim(command #>> '{event,data,conversation,type}'))
            WHEN 'group' THEN 'group' WHEN '2' THEN 'group'
            WHEN 'single' THEN 'dm' WHEN 'p2p' THEN 'dm' WHEN 'private' THEN 'dm'
            WHEN 'direct' THEN 'dm' WHEN 'dm' THEN 'dm' WHEN '1' THEN 'dm'
        END
    FROM inbound_coordinator_job
), decided AS (
    SELECT agent_id, cid, min(kind) AS kind
    FROM evidence
    WHERE kind IS NOT NULL AND cid IS NOT NULL
    GROUP BY agent_id, cid
    HAVING count(DISTINCT kind) = 1
)
UPDATE agent_scene s SET scene_kind = d.kind, kind_source = 'observed', updated_at = now()
FROM decided d
WHERE s.kind_source = 'migrated'
  AND s.source_namespace = 'dingtalk.open_conversation_id'
  AND s.agent_id = d.agent_id AND s.external_scene_id = d.cid;
