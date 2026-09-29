-- name: GetAgentA2AOperatorConfig :one
SELECT config.agent_id, config.workspace_id, config.dws_uid, config.dws_org_id, config.deap_agent_uuid,
       config.identity_updated_by, config.identity_updated_at, config.forward_rpc_url,
       config.forward_token_encrypted, config.forward_source_client_id, config.forward_updated_by,
       config.forward_updated_at, config.created_at, config.updated_at
FROM agent_a2a_operator_config config
JOIN agent a
  ON a.id = config.agent_id
 AND a.workspace_id = config.workspace_id
WHERE config.workspace_id = $1
  AND config.agent_id = $2;

-- name: UpsertAgentA2AOperatorIdentity :exec
INSERT INTO agent_a2a_operator_config (
    agent_id, workspace_id, dws_uid, dws_org_id, deap_agent_uuid, identity_updated_by, identity_updated_at
) VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (agent_id) DO UPDATE
SET dws_uid = EXCLUDED.dws_uid,
    dws_org_id = EXCLUDED.dws_org_id,
    deap_agent_uuid = EXCLUDED.deap_agent_uuid,
    identity_updated_by = EXCLUDED.identity_updated_by,
    identity_updated_at = now(),
    updated_at = now()
WHERE agent_a2a_operator_config.workspace_id = EXCLUDED.workspace_id;

-- name: ClearAgentA2AOperatorIdentity :exec
UPDATE agent_a2a_operator_config
SET dws_uid = NULL,
    dws_org_id = NULL,
    deap_agent_uuid = NULL,
    identity_updated_by = $3,
    identity_updated_at = now(),
    updated_at = now()
WHERE workspace_id = $1
  AND agent_id = $2;

-- name: UpsertAgentA2AOperatorForward :exec
INSERT INTO agent_a2a_operator_config (
    agent_id, workspace_id, forward_rpc_url, forward_token_encrypted, forward_source_client_id,
    forward_updated_by, forward_updated_at
) VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (agent_id) DO UPDATE
SET forward_rpc_url = EXCLUDED.forward_rpc_url,
    forward_token_encrypted = EXCLUDED.forward_token_encrypted,
    forward_source_client_id = EXCLUDED.forward_source_client_id,
    forward_updated_by = EXCLUDED.forward_updated_by,
    forward_updated_at = now(),
    updated_at = now()
WHERE agent_a2a_operator_config.workspace_id = EXCLUDED.workspace_id;

-- name: ClearAgentA2AOperatorForward :exec
UPDATE agent_a2a_operator_config
SET forward_rpc_url = NULL,
    forward_token_encrypted = NULL,
    forward_source_client_id = NULL,
    forward_updated_by = $3,
    forward_updated_at = now(),
    updated_at = now()
WHERE workspace_id = $1
  AND agent_id = $2;
