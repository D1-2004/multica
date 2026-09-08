-- name: ListReusableDingTalkIdentities :many
-- Only identities personally bound by the current owner are reusable.
SELECT DISTINCT ON (i.dws_uid, i.org_id)
    i.agent_id AS source_agent_id, a.name AS source_agent_name,
    i.account_display_name, i.organization_name
FROM agent_dingtalk_identity i
JOIN agent a ON a.id = i.agent_id AND a.workspace_id = i.workspace_id
WHERE i.workspace_id = sqlc.arg('workspace_id')
  AND a.owner_id = sqlc.arg('user_id') AND i.bound_by = sqlc.arg('user_id')
  AND a.archived_at IS NULL AND a.id <> sqlc.arg('target_agent_id')
ORDER BY i.dws_uid, i.org_id, i.bound_at DESC, i.agent_id;

-- name: ReuseDingTalkIdentity :one
-- Lock Agent ownership and source authorization while copying. Identity and audit
-- are one statement; a failed audit rolls the operation back. Never overwrite a
-- different identity. Repeating a successful request preserves the original bind.
WITH owned_agents AS MATERIALIZED (
    SELECT a.id FROM agent a
    WHERE a.workspace_id = sqlc.arg('workspace_id') AND a.owner_id = sqlc.arg('user_id')
      AND a.archived_at IS NULL
      AND a.id IN (sqlc.arg('source_agent_id'), sqlc.arg('target_agent_id'))
    ORDER BY a.id FOR UPDATE
), source AS MATERIALIZED (
    SELECT i.* FROM agent_dingtalk_identity i
    WHERE i.workspace_id = sqlc.arg('workspace_id')
      AND i.agent_id = sqlc.arg('source_agent_id') AND i.bound_by = sqlc.arg('user_id')
      AND i.agent_id IN (SELECT id FROM owned_agents)
      AND sqlc.arg('target_agent_id') IN (SELECT id FROM owned_agents)
    FOR SHARE
), copied AS (
    INSERT INTO agent_dingtalk_identity
      (agent_id, workspace_id, dws_uid, org_id, organization_name,
       account_display_name, account_avatar_url, bound_by, bound_at, updated_at)
    SELECT sqlc.arg('target_agent_id'), workspace_id, dws_uid, org_id,
      organization_name, account_display_name, account_avatar_url,
      sqlc.arg('user_id'), now(), now() FROM source
    ON CONFLICT (agent_id) DO UPDATE SET updated_at = agent_dingtalk_identity.updated_at
    WHERE agent_dingtalk_identity.workspace_id = EXCLUDED.workspace_id
      AND agent_dingtalk_identity.dws_uid = EXCLUDED.dws_uid
      AND agent_dingtalk_identity.org_id = EXCLUDED.org_id
      AND agent_dingtalk_identity.bound_by = EXCLUDED.bound_by
    RETURNING agent_id, workspace_id
), audited AS (
    INSERT INTO activity_log (workspace_id, actor_type, actor_id, action, details)
    SELECT workspace_id, 'member', sqlc.arg('user_id'), 'agent_dingtalk_identity_reused',
      jsonb_build_object('agent_id', agent_id, 'source_agent_id', sqlc.arg('source_agent_id')::text)
    FROM copied RETURNING id
)
SELECT copied.agent_id FROM copied, audited;
