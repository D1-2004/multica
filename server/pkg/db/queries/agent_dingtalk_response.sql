-- name: GetAgentDingTalkResponsePolicy :one
SELECT inbound_coordinator, inbound_coordinator_user_decision, dingtalk_show_ai_tag, dingtalk_response_enabled, dingtalk_response_policy_revision
FROM agent
WHERE id = $1;

-- name: ListAgentDingTalkResponsePoliciesByIDs :many
SELECT id, inbound_coordinator, inbound_coordinator_user_decision, dingtalk_show_ai_tag, dingtalk_response_enabled, dingtalk_response_policy_revision
FROM agent
WHERE id = ANY(sqlc.arg('ids')::uuid[]);

-- name: UpdateAgentDingTalkResponsePolicy :one
UPDATE agent SET
    inbound_coordinator = COALESCE(sqlc.narg('inbound_coordinator')::boolean, inbound_coordinator),
    inbound_coordinator_user_decision = CASE WHEN NOT COALESCE(sqlc.narg('inbound_coordinator')::boolean, inbound_coordinator) THEN false ELSE COALESCE(sqlc.narg('user_decision')::boolean, inbound_coordinator_user_decision) END,
    dingtalk_show_ai_tag = COALESCE(sqlc.narg('show_ai_tag')::boolean, dingtalk_show_ai_tag),
    dingtalk_response_enabled = COALESCE(sqlc.narg('response_enabled')::boolean, dingtalk_response_enabled),
    dingtalk_response_policy_revision = dingtalk_response_policy_revision + CASE
        WHEN inbound_coordinator IS DISTINCT FROM COALESCE(sqlc.narg('inbound_coordinator')::boolean, inbound_coordinator)
          OR inbound_coordinator_user_decision IS DISTINCT FROM (CASE WHEN NOT COALESCE(sqlc.narg('inbound_coordinator')::boolean, inbound_coordinator) THEN false ELSE COALESCE(sqlc.narg('user_decision')::boolean, inbound_coordinator_user_decision) END)
          OR dingtalk_show_ai_tag IS DISTINCT FROM COALESCE(sqlc.narg('show_ai_tag')::boolean, dingtalk_show_ai_tag)
          OR dingtalk_response_enabled IS DISTINCT FROM COALESCE(sqlc.narg('response_enabled')::boolean, dingtalk_response_enabled)
        THEN 1 ELSE 0 END,
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING inbound_coordinator, inbound_coordinator_user_decision, dingtalk_show_ai_tag, dingtalk_response_enabled, dingtalk_response_policy_revision;

-- name: UpdateLocalRuntimeDingTalkCapabilities :exec
UPDATE agent_runtime
SET metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{client_capabilities}', sqlc.arg('capabilities')::jsonb, true),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND runtime_mode = 'local'
  AND metadata->'client_capabilities' IS DISTINCT FROM sqlc.arg('capabilities')::jsonb;
