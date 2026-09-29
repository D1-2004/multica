-- name: GetAgentA2AOperatorConfig :one
SELECT config.agent_id, config.workspace_id, config.deap_agent_uuid, config.a2a_identity_enabled,
       config.accept_prod_forward, config.updated_by, config.created_at, config.updated_at
FROM agent_a2a_operator_config config
JOIN agent a
  ON a.id = config.agent_id
 AND a.workspace_id = config.workspace_id
WHERE config.workspace_id = $1
  AND config.agent_id = $2;

-- name: SetAgentA2AIdentityEnabled :exec
INSERT INTO agent_a2a_operator_config (agent_id, workspace_id, a2a_identity_enabled, deap_agent_uuid, updated_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (agent_id) DO UPDATE
SET a2a_identity_enabled = EXCLUDED.a2a_identity_enabled,
    deap_agent_uuid = EXCLUDED.deap_agent_uuid,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
WHERE agent_a2a_operator_config.workspace_id = EXCLUDED.workspace_id;

-- name: SetAgentA2AAcceptProdForward :exec
INSERT INTO agent_a2a_operator_config (agent_id, workspace_id, accept_prod_forward, updated_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (agent_id) DO UPDATE
SET accept_prod_forward = EXCLUDED.accept_prod_forward,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
WHERE agent_a2a_operator_config.workspace_id = EXCLUDED.workspace_id;

-- name: ListAgentA2AForwardRegistrants :many
SELECT agent_id, registry_origin, workspace_id, client_id, client_uid, client_org_id,
       registered_rpc_url, registered_token_sha256, registered_at, last_error, created_at, updated_at
FROM agent_a2a_forward_registrant
WHERE agent_id = $1
  AND workspace_id = $2
ORDER BY registry_origin;

-- name: CreateAgentA2AForwardClient :exec
INSERT INTO agent_a2a_forward_client (client_id, agent_id, workspace_id, dws_uid, org_id)
VALUES ($1, $2, $3, $4, $5);

-- name: GetAgentA2AForwardClient :one
SELECT client_id, agent_id, workspace_id, dws_uid, org_id, created_at, retired_at
FROM agent_a2a_forward_client
WHERE client_id = $1
  AND agent_id = $2;

-- name: RetireAgentA2AForwardClient :exec
UPDATE agent_a2a_forward_client
SET retired_at = now()
WHERE client_id = $1
  AND agent_id = $2
  AND retired_at IS NULL;

-- name: SaveAgentA2AForwardRegistrant :exec
INSERT INTO agent_a2a_forward_registrant (
    agent_id, registry_origin, workspace_id, client_id, client_uid, client_org_id,
    registered_rpc_url, registered_token_sha256, registered_at, last_error
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (agent_id, registry_origin) DO UPDATE
SET client_id = EXCLUDED.client_id,
    client_uid = EXCLUDED.client_uid,
    client_org_id = EXCLUDED.client_org_id,
    registered_rpc_url = EXCLUDED.registered_rpc_url,
    registered_token_sha256 = EXCLUDED.registered_token_sha256,
    registered_at = EXCLUDED.registered_at,
    last_error = EXCLUDED.last_error,
    updated_at = now()
WHERE agent_a2a_forward_registrant.workspace_id = EXCLUDED.workspace_id;

-- name: IsA2ACredentialRevoked :one
-- Whether a key that no longer authenticates was revoked (as opposed to never
-- existing), so a forwarding registry can tell a withdrawn key apart.
SELECT EXISTS (
    SELECT 1
    FROM a2a_client_credential credential
    JOIN a2a_client client ON client.id = credential.client_id
    WHERE credential.token_hash = $1
      AND (credential.status <> 'active' OR client.status <> 'active')
);

-- name: UpsertAgentDingTalkIdentityManual :exec
-- The operator binds a DEAP digital employee identity directly; the row is the
-- same one the Integrations QR binding writes.
INSERT INTO agent_dingtalk_identity (
    agent_id, workspace_id, dws_uid, org_id, account_display_name, organization_name, bound_by
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (agent_id) DO UPDATE
SET dws_uid = EXCLUDED.dws_uid,
    org_id = EXCLUDED.org_id,
    account_display_name = EXCLUDED.account_display_name,
    organization_name = EXCLUDED.organization_name,
    account_avatar_url = '',
    bound_by = EXCLUDED.bound_by,
    bound_at = now(),
    updated_at = now()
WHERE agent_dingtalk_identity.workspace_id = EXCLUDED.workspace_id;

-- name: UpsertA2AForwardRegistration :execrows
-- A request signed earlier than the stored state (a replay or a reordered
-- retry) changes nothing, even when that state is a withdrawal.
INSERT INTO a2a_forward_registration (
    dws_uid, org_id, rpc_url, target_client_id, token_encrypted, token_sha256, target_agent_name, signed_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (dws_uid, org_id) DO UPDATE
SET rpc_url = EXCLUDED.rpc_url,
    target_client_id = EXCLUDED.target_client_id,
    token_encrypted = EXCLUDED.token_encrypted,
    token_sha256 = EXCLUDED.token_sha256,
    target_agent_name = EXCLUDED.target_agent_name,
    signed_at_ms = EXCLUDED.signed_at_ms,
    registered_at = now(),
    revoked_at = NULL
WHERE a2a_forward_registration.signed_at_ms < EXCLUDED.signed_at_ms;

-- name: GetA2AForwardRegistration :one
-- Only a live registration; withdrawn ones remain as ordering tombstones.
SELECT dws_uid, org_id, rpc_url, target_client_id, token_encrypted, token_sha256, target_agent_name,
       signed_at_ms, registered_at, revoked_at
FROM a2a_forward_registration
WHERE dws_uid = $1
  AND org_id = $2
  AND token_encrypted IS NOT NULL;

-- name: RevokeA2AForwardRegistration :exec
-- A registrant withdraws only the exact registration it made, and only with a
-- request signed after it, so a replayed withdrawal cannot remove a newer one.
UPDATE a2a_forward_registration
SET token_encrypted = NULL,
    revoked_at = now(),
    signed_at_ms = $5
WHERE dws_uid = $1
  AND org_id = $2
  AND rpc_url = $3
  AND token_sha256 = $4
  AND signed_at_ms < $5
  AND token_encrypted IS NOT NULL;

-- name: RejectA2AForwardRegistration :exec
-- Production retires a registration whose target rejected its key. A newer
-- registration (another key) is left alone.
UPDATE a2a_forward_registration
SET token_encrypted = NULL,
    revoked_at = now()
WHERE dws_uid = $1
  AND org_id = $2
  AND token_sha256 = $3
  AND token_encrypted IS NOT NULL;

-- name: ClaimA2AForwardTokenBinding :one
-- Returns the source client that owns a forward target: the requested one on
-- first use, or the earlier owner on every later call. token_sha256 holds the
-- target's binding key (see agentA2AForwardBindingKey), which stays the same
-- across key rotations of one target client.
INSERT INTO a2a_forward_token_binding (
    token_sha256, source_client_id, workspace_id, agent_id, created_by
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (token_sha256) DO UPDATE
SET token_sha256 = a2a_forward_token_binding.token_sha256
RETURNING source_client_id;
