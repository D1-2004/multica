-- Marks the scenes 9510 registered from stored configuration whose kind no
-- trusted inbound event has confirmed, and settles them from evidence
-- (docs/agent-scene.md §8). Re-runnable: it re-evaluates every conversation
-- scene that stored configuration names, so a database where an earlier
-- 9511 settled kinds across orgs is re-evaluated with the org-scoped rule.

-- A scene stays observed only when an inbound Coordinator job carries it:
-- that job's dispatch resolved the scene with the kind its event stated.
-- Association events do not count; an outbound bind proves no kind.
UPDATE agent_scene s SET kind_source = 'migrated'
WHERE s.source_namespace = 'dingtalk.open_conversation_id'
  AND (
      EXISTS (SELECT 1 FROM context_capability_binding t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.scope_key = s.id::text)
   OR EXISTS (SELECT 1 FROM context_connector_credential t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.org_id = s.tenant_org_id
                AND t.scope_key IN (s.id::text, s.external_scene_id))
   OR EXISTS (SELECT 1 FROM context_config_grant t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.scope_key = s.id::text)
   OR EXISTS (SELECT 1 FROM context_config_link t
              WHERE t.agent_id = s.agent_id AND (t.scope_key = s.id::text OR t.extra_scene_key = s.id::text))
   OR EXISTS (SELECT 1 FROM context_scope_mcp_config t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.scope_key = s.id::text)
   OR EXISTS (SELECT 1 FROM context_prompt_component t
              WHERE t.agent_id = s.agent_id AND t.scope_type = 'scene' AND t.scope_key = s.id::text)
   OR EXISTS (SELECT 1 FROM agent_scene_config t
              WHERE t.workspace_id = s.workspace_id AND t.agent_id = s.agent_id AND t.platform = 'dingtalk'
                AND t.org_id = s.tenant_org_id AND t.scene_key = s.external_scene_id)
  )
  AND NOT EXISTS (SELECT 1 FROM inbound_coordinator_job j
                  WHERE j.agent_id = s.agent_id AND (j.command #>> '{agent_scene,scene_id}') = s.id::text);

-- A migrated scene takes a kind when every trusted record written before
-- scene ids, for the same workspace, agent, tenant org and conversation,
-- names that one kind: the retired Scene Memory row's kind, and the
-- conversation type of the agent's inbound Coordinator jobs (their recorded
-- dispatch org, else the agent's identity org). No evidence, or evidence of
-- both kinds, leaves it migrated for the next inbound event to settle.
WITH evidence AS (
    SELECT m.workspace_id, m.agent_id, m.org_id, m.scene_key AS cid, m.scene_kind AS kind
    FROM scene_memory m
    WHERE m.scene_kind IN ('group', 'dm')
    UNION
    SELECT j.workspace_id, j.agent_id,
        COALESCE(NULLIF(btrim(j.command #>> '{externalIdentity,dws,orgId}'), ''), i.org_id),
        j.command #>> '{event,data,conversation,openConversationId}',
        CASE lower(btrim(j.command #>> '{event,data,conversation,type}'))
            WHEN 'group' THEN 'group' WHEN '2' THEN 'group'
            WHEN 'single' THEN 'dm' WHEN 'p2p' THEN 'dm' WHEN 'private' THEN 'dm'
            WHEN 'direct' THEN 'dm' WHEN 'dm' THEN 'dm' WHEN '1' THEN 'dm'
        END
    FROM inbound_coordinator_job j
    LEFT JOIN agent_dingtalk_identity i ON i.workspace_id = j.workspace_id AND i.agent_id = j.agent_id
), decided AS (
    SELECT workspace_id, agent_id, org_id, cid, min(kind) AS kind
    FROM evidence
    WHERE kind IS NOT NULL AND cid IS NOT NULL AND org_id IS NOT NULL
    GROUP BY workspace_id, agent_id, org_id, cid
    HAVING count(DISTINCT kind) = 1
)
UPDATE agent_scene s SET scene_kind = d.kind, kind_source = 'observed', updated_at = now()
FROM decided d
WHERE s.kind_source = 'migrated'
  AND s.source_namespace = 'dingtalk.open_conversation_id'
  AND s.workspace_id = d.workspace_id AND s.agent_id = d.agent_id
  AND s.tenant_org_id = d.org_id AND s.external_scene_id = d.cid;
