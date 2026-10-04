-- Scene-scoped context configuration moves from the raw openConversationId
-- to the Agent work scene id (docs/agent-scene.md).
--
-- 1. Every conversation a tenant org stored scene configuration, grants,
--    links or agent_scene_config for is registered in agent_scene. The kind
--    is dm only on positive evidence (a personal link minted in that 1:1
--    chat, or agent_scene_config recorded it as dm); before scene ids the
--    scene scope applied to groups only. Rows without an org, or with a key
--    that is not a conversation id, are not migrated and no longer apply.
-- 2. Scene-scope keys (and personal links' extra 1:1 scene) are rewritten
--    to the scene id. Connector credentials seal their scope key, so the
--    server re-keys those at startup (contextcap.ReconcileSceneCredentials).
--
-- Idempotent: rewritten keys are UUIDs and never match a conversation id.
WITH src AS (
    SELECT workspace_id, agent_id, org_id, scope_key AS cid, scope_title AS title, updated_at AS at
    FROM context_capability_binding WHERE scope_type = 'scene'
    UNION ALL
    SELECT workspace_id, agent_id, org_id, scope_key, '', updated_at
    FROM context_connector_credential WHERE scope_type = 'scene'
    UNION ALL
    SELECT workspace_id, agent_id, org_id, scope_key, scope_title, updated_at
    FROM context_config_grant WHERE scope_type = 'scene'
    UNION ALL
    SELECT workspace_id, agent_id, org_id, scope_key, scope_title, created_at
    FROM context_config_link WHERE scope_type = 'scene'
    UNION ALL
    SELECT workspace_id, agent_id, org_id, extra_scene_key, '', created_at
    FROM context_config_link WHERE scope_type = 'person' AND extra_scene_key <> ''
    UNION ALL
    SELECT workspace_id, agent_id, org_id, scope_key, '', updated_at
    FROM context_scope_mcp_config WHERE scope_type = 'scene'
    UNION ALL
    SELECT workspace_id, agent_id, org_id, scope_key, '', updated_at
    FROM context_prompt_component WHERE scope_type = 'scene'
    UNION ALL
    SELECT workspace_id, agent_id, org_id, scene_key, scene_title, updated_at
    FROM agent_scene_config WHERE platform = 'dingtalk'
), dm AS (
    SELECT workspace_id, agent_id, org_id, extra_scene_key AS cid
    FROM context_config_link WHERE scope_type = 'person' AND extra_scene_key <> ''
    UNION
    SELECT workspace_id, agent_id, org_id, scene_key
    FROM agent_scene_config WHERE platform = 'dingtalk' AND scene_kind = 'dm'
)
INSERT INTO agent_scene (
    workspace_id, agent_id, provider, tenant_org_id, source_namespace,
    scene_kind, external_scene_id, title, last_active_at
)
SELECT src.workspace_id, src.agent_id, 'dingtalk', src.org_id, 'dingtalk.open_conversation_id',
    CASE WHEN bool_or(dm.cid IS NOT NULL) THEN 'dm' ELSE 'group' END,
    src.cid,
    left(COALESCE(max(NULLIF(btrim(src.title), '')), ''), 256),
    COALESCE(max(src.at), now())
FROM src
LEFT JOIN dm ON dm.workspace_id = src.workspace_id AND dm.agent_id = src.agent_id
    AND dm.org_id = src.org_id AND dm.cid = src.cid
WHERE src.org_id <> '' AND src.org_id = btrim(src.org_id) AND char_length(src.org_id) <= 128
  AND src.cid LIKE 'cid%' AND octet_length(src.cid) <= 256 AND src.cid !~ '[[:space:][:cntrl:]]'
GROUP BY src.workspace_id, src.agent_id, src.org_id, src.cid
ON CONFLICT (workspace_id, agent_id, provider, tenant_org_id, source_namespace, external_scene_id) DO NOTHING;

UPDATE context_capability_binding t SET scope_key = s.id::text
FROM agent_scene s
WHERE t.scope_type = 'scene' AND s.workspace_id = t.workspace_id AND s.agent_id = t.agent_id
  AND s.provider = 'dingtalk' AND s.tenant_org_id = t.org_id
  AND s.source_namespace = 'dingtalk.open_conversation_id' AND s.external_scene_id = t.scope_key;

UPDATE context_config_grant t SET scope_key = s.id::text
FROM agent_scene s
WHERE t.scope_type = 'scene' AND s.workspace_id = t.workspace_id AND s.agent_id = t.agent_id
  AND s.provider = 'dingtalk' AND s.tenant_org_id = t.org_id
  AND s.source_namespace = 'dingtalk.open_conversation_id' AND s.external_scene_id = t.scope_key;

UPDATE context_config_link t SET scope_key = s.id::text
FROM agent_scene s
WHERE t.scope_type = 'scene' AND s.workspace_id = t.workspace_id AND s.agent_id = t.agent_id
  AND s.provider = 'dingtalk' AND s.tenant_org_id = t.org_id
  AND s.source_namespace = 'dingtalk.open_conversation_id' AND s.external_scene_id = t.scope_key;

UPDATE context_config_link t SET extra_scene_key = s.id::text
FROM agent_scene s
WHERE t.scope_type = 'person' AND t.extra_scene_key <> ''
  AND s.workspace_id = t.workspace_id AND s.agent_id = t.agent_id
  AND s.provider = 'dingtalk' AND s.tenant_org_id = t.org_id
  AND s.source_namespace = 'dingtalk.open_conversation_id' AND s.external_scene_id = t.extra_scene_key;

UPDATE context_scope_mcp_config t SET scope_key = s.id::text
FROM agent_scene s
WHERE t.scope_type = 'scene' AND s.workspace_id = t.workspace_id AND s.agent_id = t.agent_id
  AND s.provider = 'dingtalk' AND s.tenant_org_id = t.org_id
  AND s.source_namespace = 'dingtalk.open_conversation_id' AND s.external_scene_id = t.scope_key;

UPDATE context_prompt_component t SET scope_key = s.id::text
FROM agent_scene s
WHERE t.scope_type = 'scene' AND s.workspace_id = t.workspace_id AND s.agent_id = t.agent_id
  AND s.provider = 'dingtalk' AND s.tenant_org_id = t.org_id
  AND s.source_namespace = 'dingtalk.open_conversation_id' AND s.external_scene_id = t.scope_key;
