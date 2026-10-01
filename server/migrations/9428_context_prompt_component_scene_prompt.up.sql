-- The scene prompt (agent_scene_config.prompt, 场域提示词) becomes one prompt
-- component named 「场域提示词」. A group scene's prompt lands in the scene
-- scope; a 1:1 chat's prompt lands in its person's scope (a 1:1 chat's
-- configuration is its person's), resolved like contextcap.DirectScenePerson:
-- the sender staffId of the newest inbound Coordinator job of that
-- conversation that names one (a job that recorded no agent org belongs to
-- the agent's DingTalk identity org), else the person of a live grant from a
-- personal link redeemed in that chat. A 1:1 chat whose person is unknown has
-- nowhere to go and is skipped. When several 1:1 chats resolve to the same
-- person, the most recently edited prompt wins. The prompt column stays for
-- the rolling window. Idempotent: an existing component of the same name is
-- kept (ON CONFLICT DO NOTHING).
INSERT INTO context_prompt_component
    (workspace_id, agent_id, scope_type, org_id, scope_key, name, position, text, updated_by, created_at, updated_at)
SELECT DISTINCT ON (src.agent_id, src.scope_type, src.org_id, src.scope_key)
    src.workspace_id, src.agent_id, src.scope_type, src.org_id, src.scope_key, '场域提示词', 0, src.text, src.updated_by,
    src.updated_at, src.updated_at
FROM (
    SELECT c.workspace_id, c.agent_id, 'scene' AS scope_type, c.org_id, c.scene_key AS scope_key, BTRIM(c.prompt) AS text,
        c.updated_by, c.updated_at
    FROM agent_scene_config c
    WHERE c.scene_kind = 'group' AND BTRIM(c.prompt) <> ''
    UNION ALL
    SELECT c.workspace_id, c.agent_id, 'person', c.org_id, person.staff_id, BTRIM(c.prompt), c.updated_by, c.updated_at
    FROM agent_scene_config c
    CROSS JOIN LATERAL (
        SELECT COALESCE(
            (SELECT NULLIF(BTRIM(job.command #>> '{event,data,sender,staffId}'), '')
             FROM inbound_coordinator_job job
             WHERE job.agent_id = c.agent_id AND job.workspace_id = c.workspace_id
               AND BTRIM(job.command #>> '{event,data,conversation,openConversationId}') = c.scene_key
               AND lower(COALESCE(NULLIF(BTRIM(job.command #>> '{source,platform}'), ''), 'dingtalk')) = 'dingtalk'
               AND (c.org_id = '' OR COALESCE(
                    NULLIF(BTRIM(job.command #>> '{externalIdentity,dws,orgId}'), ''),
                    (SELECT BTRIM(i.org_id) FROM agent_dingtalk_identity i
                     WHERE i.workspace_id = c.workspace_id AND i.agent_id = c.agent_id),
                    '') = c.org_id)
               AND NULLIF(BTRIM(job.command #>> '{event,data,sender,staffId}'), '') IS NOT NULL
             ORDER BY job.created_at DESC, job.id DESC
             LIMIT 1),
            (SELECT NULLIF(BTRIM(g.scope_key), '')
             FROM context_config_link l
             JOIN context_config_grant g ON g.user_id = l.consumed_by AND g.agent_id = l.agent_id
               AND g.workspace_id = l.workspace_id AND g.scope_type = 'person' AND g.org_id = l.org_id
               AND g.scope_key = l.scope_key AND g.expires_at > now()
             WHERE l.workspace_id = c.workspace_id AND l.agent_id = c.agent_id AND l.org_id = c.org_id
               AND l.scope_type = 'person' AND l.extra_scene_key = c.scene_key AND l.consumed_by IS NOT NULL
             ORDER BY g.updated_at DESC, l.created_at DESC
             LIMIT 1)
        ) AS staff_id
    ) person
    WHERE c.scene_kind = 'dm' AND BTRIM(c.prompt) <> '' AND person.staff_id IS NOT NULL
      AND octet_length(person.staff_id) <= 256 AND person.staff_id !~ '[[:space:][:cntrl:]]'
) src
WHERE char_length(src.text) <= 8000
ORDER BY src.agent_id, src.scope_type, src.org_id, src.scope_key, src.updated_at DESC
ON CONFLICT (agent_id, scope_type, org_id, scope_key, name) DO NOTHING;
