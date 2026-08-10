-- Agent-level inbound A2A persistence. Every management query below is
-- scoped by workspace, agent, and the agent's exact current owner. Public
-- protocol queries are scoped by the stable external client principal.

-- name: GetAgentA2AEndpointForOwner :one
SELECT endpoint.*
FROM agent_a2a_endpoint endpoint
JOIN agent a
  ON a.id = endpoint.agent_id
 AND a.workspace_id = endpoint.workspace_id
JOIN member m
  ON m.workspace_id = endpoint.workspace_id
 AND m.user_id = sqlc.arg('owner_user_id')
WHERE endpoint.workspace_id = sqlc.arg('workspace_id')
  AND endpoint.agent_id = sqlc.arg('agent_id')
  AND a.owner_id = sqlc.arg('owner_user_id');

-- name: GetAgentA2AEndpointByAgent :one
SELECT *
FROM agent_a2a_endpoint
WHERE agent_id = sqlc.arg('agent_id');

-- Removing a workspace member is a permanent A2A delegation boundary. These
-- queries are intentionally called as separate statements inside the member
-- removal transaction, in this exact order:
--
--   Agent -> endpoint -> client -> credential -> state updates
--
-- Separate lock statements matter under READ COMMITTED: if a concurrent child
-- insert committed while a parent-row lock was waiting, the next statement
-- gets a fresh snapshot and includes that child before locking the next layer.
-- Once a parent layer is locked FOR UPDATE, its FK fence prevents new children
-- from being inserted behind the revocation pass.

-- name: LockAgentsForMemberA2ARevocation :many
SELECT a.id
FROM agent a
WHERE a.workspace_id = sqlc.arg('workspace_id')
  AND a.owner_id = sqlc.arg('member_user_id')
ORDER BY a.id
FOR UPDATE OF a;

-- name: LockAgentA2AEndpointsForMemberRevocation :many
SELECT endpoint.id
FROM agent_a2a_endpoint endpoint
WHERE endpoint.workspace_id = sqlc.arg('workspace_id')
  AND endpoint.agent_id = ANY(sqlc.arg('agent_ids')::uuid[])
ORDER BY endpoint.id
FOR UPDATE OF endpoint;

-- name: LockA2AClientsForMemberRevocation :many
SELECT client.id
FROM a2a_client client
WHERE client.endpoint_id = ANY(sqlc.arg('endpoint_ids')::uuid[])
ORDER BY client.id
FOR UPDATE OF client;

-- name: LockA2ACredentialsForMemberRevocation :many
SELECT credential.id
FROM a2a_client_credential credential
WHERE credential.client_id = ANY(sqlc.arg('client_ids')::uuid[])
  AND credential.status = 'active'
ORDER BY credential.id
FOR UPDATE OF credential;

-- name: DisableAgentA2AEndpointsForMemberRevocation :many
UPDATE agent_a2a_endpoint endpoint
SET enabled = FALSE,
    updated_at = now()
WHERE endpoint.id = ANY(sqlc.arg('endpoint_ids')::uuid[])
RETURNING endpoint.id;

-- name: RevokeA2AClientsForMemberRevocation :many
UPDATE a2a_client client
SET status = 'revoked',
    revoked_at = now(),
    revoked_by = sqlc.arg('revoked_by'),
    updated_by = sqlc.arg('revoked_by'),
    updated_at = now()
WHERE client.id = ANY(sqlc.arg('client_ids')::uuid[])
  AND client.status <> 'revoked'
RETURNING client.id;

-- name: RevokeA2ACredentialsForMemberRevocation :many
UPDATE a2a_client_credential credential
SET status = 'revoked',
    revoked_at = now(),
    revoked_by = sqlc.arg('revoked_by'),
    updated_at = now()
WHERE credential.id = ANY(sqlc.arg('credential_ids')::uuid[])
  AND credential.status = 'active'
RETURNING credential.id;

-- name: UpsertAgentA2AEndpoint :one
-- Rebinding an endpoint after an agent-owner transfer is a security boundary,
-- not an ordinary config update. Before the new owner becomes the delegated
-- owner, permanently revoke every old caller principal and its active
-- credentials in this same statement. A same-owner PUT leaves callers intact.
WITH locked_owner_member AS MATERIALIZED (
    SELECT owner_member.workspace_id, owner_member.user_id
    FROM member owner_member
    WHERE owner_member.workspace_id = sqlc.arg('workspace_id')
      AND owner_member.user_id = sqlc.arg('owner_user_id')
    FOR KEY SHARE OF owner_member
), eligible_agent AS MATERIALIZED (
    SELECT a.id, a.workspace_id
    FROM agent a
    JOIN locked_owner_member owner_member
      ON owner_member.workspace_id = a.workspace_id
     AND owner_member.user_id = a.owner_id
    WHERE a.id = sqlc.arg('agent_id')
      AND a.workspace_id = sqlc.arg('workspace_id')
      AND a.owner_id = sqlc.arg('owner_user_id')
    -- Keep the owner predicate true until endpoint rebind and caller
    -- revocation have completed.
    FOR SHARE OF a
), current_endpoint AS MATERIALIZED (
    SELECT endpoint.id, endpoint.delegated_by_user_id
    FROM agent_a2a_endpoint endpoint
    JOIN eligible_agent a
      ON a.id = endpoint.agent_id
     AND a.workspace_id = endpoint.workspace_id
    FOR UPDATE OF endpoint
), revoked_clients AS (
    UPDATE a2a_client client
    SET status = 'revoked',
        revoked_at = now(),
        revoked_by = sqlc.arg('owner_user_id'),
        updated_by = sqlc.arg('owner_user_id'),
        updated_at = now()
    FROM current_endpoint endpoint
    WHERE client.endpoint_id = endpoint.id
      AND endpoint.delegated_by_user_id IS DISTINCT FROM sqlc.arg('owner_user_id')
      AND client.status <> 'revoked'
    RETURNING client.id
), revoked_credentials AS (
    UPDATE a2a_client_credential credential
    SET status = 'revoked',
        revoked_at = now(),
        revoked_by = sqlc.arg('owner_user_id'),
        updated_at = now()
    WHERE credential.client_id IN (SELECT id FROM revoked_clients)
      AND credential.status = 'active'
    RETURNING credential.id
), ownership_cleanup AS MATERIALIZED (
    -- The aggregate always yields one row and establishes an explicit data
    -- dependency, so both revocation CTEs finish before the endpoint rebind.
    SELECT
        (SELECT count(*) FROM revoked_clients) AS revoked_client_count,
        (SELECT count(*) FROM revoked_credentials) AS revoked_credential_count
)
INSERT INTO agent_a2a_endpoint AS existing (
    workspace_id,
    agent_id,
    public_agent_id,
    enabled,
    delegated_by_user_id,
    card_name,
    card_description,
    card_version,
    card_skills
)
SELECT
    a.workspace_id,
    a.id,
    sqlc.arg('public_agent_id'),
    sqlc.arg('enabled'),
    sqlc.arg('owner_user_id'),
    sqlc.arg('card_name'),
    sqlc.arg('card_description'),
    sqlc.arg('card_version'),
    sqlc.arg('card_skills')::jsonb
FROM eligible_agent a
CROSS JOIN ownership_cleanup
ON CONFLICT (agent_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    delegated_by_user_id = EXCLUDED.delegated_by_user_id,
    card_name = EXCLUDED.card_name,
    card_description = EXCLUDED.card_description,
    card_version = EXCLUDED.card_version,
    card_skills = EXCLUDED.card_skills,
    updated_at = now()
WHERE existing.workspace_id = EXCLUDED.workspace_id
RETURNING existing.*;

-- name: GetAgentA2AEndpointByPublicID :one
SELECT
    endpoint.*,
    a.owner_id AS agent_owner_id,
    a.runtime_id AS agent_runtime_id,
    a.archived_at AS agent_archived_at,
    COALESCE(a.owner_id = endpoint.delegated_by_user_id, FALSE) AS owner_binding_valid
FROM agent_a2a_endpoint endpoint
JOIN agent a
  ON a.id = endpoint.agent_id
 AND a.workspace_id = endpoint.workspace_id
WHERE endpoint.public_agent_id = sqlc.arg('public_agent_id');

-- name: GetPublishedAgentA2AEndpointByPublicID :one
SELECT
    endpoint.*,
    a.owner_id AS agent_owner_id,
    a.runtime_id AS agent_runtime_id
FROM agent_a2a_endpoint endpoint
JOIN agent a
  ON a.id = endpoint.agent_id
 AND a.workspace_id = endpoint.workspace_id
JOIN agent_runtime runtime
  ON runtime.id = a.runtime_id
 AND runtime.workspace_id = a.workspace_id
JOIN member owner_member
  ON owner_member.workspace_id = endpoint.workspace_id
 AND owner_member.user_id = a.owner_id
WHERE endpoint.public_agent_id = sqlc.arg('public_agent_id')
  AND endpoint.enabled = TRUE
  AND a.archived_at IS NULL
  AND a.runtime_id IS NOT NULL
  AND (
    (runtime.runtime_mode = 'local' AND runtime.provider = 'claude')
    OR (
      runtime.runtime_mode = 'cloud'
      AND runtime.provider = 'opencode'
      AND btrim(runtime.metadata->>'kind') = 'cloud-sandbox'
      AND lower(btrim(runtime.metadata->>'sandbox_backend')) = 'aliyun_fc'
      AND COALESCE(
        NULLIF(lower(btrim(runtime.metadata->>'provider')), ''),
        runtime.provider
      ) = 'opencode'
      AND lower(btrim(runtime.metadata->>'artifact_kind')) = 'e2b_template'
      AND btrim(COALESCE(runtime.metadata->>'artifact_ref', '')) <> ''
      AND runtime.metadata->'capabilities' ? 'a2a_inbound_opencode_v1'
    )
  )
  AND a.owner_id = endpoint.delegated_by_user_id;

-- name: ListAgentA2AClientsForOwner :many
SELECT client.*
FROM a2a_client client
JOIN agent_a2a_endpoint endpoint ON endpoint.id = client.endpoint_id
JOIN agent a
  ON a.id = endpoint.agent_id
 AND a.workspace_id = endpoint.workspace_id
JOIN member owner_member
  ON owner_member.workspace_id = endpoint.workspace_id
 AND owner_member.user_id = sqlc.arg('owner_user_id')
WHERE endpoint.workspace_id = sqlc.arg('workspace_id')
  AND endpoint.agent_id = sqlc.arg('agent_id')
  AND endpoint.delegated_by_user_id = sqlc.arg('owner_user_id')
  AND a.owner_id = sqlc.arg('owner_user_id')
ORDER BY client.created_at DESC, client.id DESC;

-- name: CreateAgentA2AClientForOwner :one
WITH locked_owner_member AS MATERIALIZED (
    SELECT owner_member.workspace_id, owner_member.user_id
    FROM member owner_member
    WHERE owner_member.workspace_id = sqlc.arg('workspace_id')
      AND owner_member.user_id = sqlc.arg('owner_user_id')
    FOR KEY SHARE OF owner_member
), locked_agent AS MATERIALIZED (
    SELECT a.id, a.workspace_id
    FROM agent a
    JOIN locked_owner_member owner_member
      ON owner_member.workspace_id = a.workspace_id
     AND owner_member.user_id = a.owner_id
    WHERE a.id = sqlc.arg('agent_id')
      AND a.workspace_id = sqlc.arg('workspace_id')
      AND a.owner_id = sqlc.arg('owner_user_id')
    FOR SHARE OF a
), locked_endpoint AS MATERIALIZED (
    SELECT endpoint.id
    FROM agent_a2a_endpoint endpoint
    JOIN locked_agent a
      ON a.id = endpoint.agent_id
     AND a.workspace_id = endpoint.workspace_id
    WHERE endpoint.workspace_id = sqlc.arg('workspace_id')
      AND endpoint.agent_id = sqlc.arg('agent_id')
      AND endpoint.delegated_by_user_id = sqlc.arg('owner_user_id')
    FOR SHARE OF endpoint
)
INSERT INTO a2a_client (
    endpoint_id,
    name,
    status,
    scopes,
    rate_limit_per_minute,
    max_concurrent_tasks,
    created_by,
    updated_by
)
SELECT
    endpoint.id,
    sqlc.arg('name'),
    'active',
    sqlc.arg('scopes')::text[],
    sqlc.narg('rate_limit_per_minute')::integer,
    sqlc.narg('max_concurrent_tasks')::integer,
    sqlc.arg('owner_user_id'),
    sqlc.arg('owner_user_id')
FROM locked_endpoint endpoint
RETURNING *;

-- name: UpdateAgentA2AClientForOwner :one
UPDATE a2a_client client
SET name = sqlc.arg('name'),
    status = sqlc.arg('status'),
    scopes = sqlc.arg('scopes')::text[],
    rate_limit_per_minute = sqlc.narg('rate_limit_per_minute')::integer,
    max_concurrent_tasks = sqlc.narg('max_concurrent_tasks')::integer,
    updated_by = sqlc.arg('owner_user_id'),
    updated_at = now()
FROM agent_a2a_endpoint endpoint
JOIN agent a
  ON a.id = endpoint.agent_id
 AND a.workspace_id = endpoint.workspace_id
JOIN member owner_member
  ON owner_member.workspace_id = endpoint.workspace_id
 AND owner_member.user_id = sqlc.arg('owner_user_id')
WHERE client.id = sqlc.arg('client_id')
  AND client.endpoint_id = endpoint.id
  AND client.status <> 'revoked'
  AND endpoint.workspace_id = sqlc.arg('workspace_id')
  AND endpoint.agent_id = sqlc.arg('agent_id')
  AND endpoint.delegated_by_user_id = sqlc.arg('owner_user_id')
  AND a.owner_id = sqlc.arg('owner_user_id')
RETURNING client.*;

-- name: RevokeAgentA2AClientForOwner :one
WITH revoked_client AS (
    UPDATE a2a_client client
    SET status = 'revoked',
        revoked_at = now(),
        revoked_by = sqlc.arg('owner_user_id'),
        updated_by = sqlc.arg('owner_user_id'),
        updated_at = now()
    FROM agent_a2a_endpoint endpoint
    JOIN agent a
      ON a.id = endpoint.agent_id
     AND a.workspace_id = endpoint.workspace_id
    JOIN member owner_member
      ON owner_member.workspace_id = endpoint.workspace_id
     AND owner_member.user_id = sqlc.arg('owner_user_id')
    WHERE client.id = sqlc.arg('client_id')
      AND client.endpoint_id = endpoint.id
      AND client.status <> 'revoked'
      AND endpoint.workspace_id = sqlc.arg('workspace_id')
      AND endpoint.agent_id = sqlc.arg('agent_id')
      AND endpoint.delegated_by_user_id = sqlc.arg('owner_user_id')
      AND a.owner_id = sqlc.arg('owner_user_id')
    RETURNING client.*
), revoked_credentials AS (
    UPDATE a2a_client_credential credential
    SET status = 'revoked',
        revoked_at = now(),
        revoked_by = sqlc.arg('owner_user_id'),
        updated_at = now()
    WHERE credential.client_id IN (SELECT id FROM revoked_client)
      AND credential.status = 'active'
    RETURNING credential.id
)
SELECT * FROM revoked_client;

-- name: ListAgentA2ACredentialsForOwner :many
SELECT
    credential.id,
    credential.client_id,
    credential.key_id,
    credential.token_prefix,
    credential.status,
    credential.expires_at,
    credential.last_used_at,
    credential.created_by,
    credential.revoked_at,
    credential.revoked_by,
    credential.created_at,
    credential.updated_at
FROM a2a_client_credential credential
JOIN a2a_client client ON client.id = credential.client_id
JOIN agent_a2a_endpoint endpoint ON endpoint.id = client.endpoint_id
JOIN agent a
  ON a.id = endpoint.agent_id
 AND a.workspace_id = endpoint.workspace_id
JOIN member owner_member
  ON owner_member.workspace_id = endpoint.workspace_id
 AND owner_member.user_id = sqlc.arg('owner_user_id')
WHERE credential.client_id = sqlc.arg('client_id')
  AND endpoint.workspace_id = sqlc.arg('workspace_id')
  AND endpoint.agent_id = sqlc.arg('agent_id')
  AND endpoint.delegated_by_user_id = sqlc.arg('owner_user_id')
  AND a.owner_id = sqlc.arg('owner_user_id')
ORDER BY credential.created_at DESC, credential.id DESC;

-- name: CreateAgentA2ACredentialForOwner :one
WITH locked_owner_member AS MATERIALIZED (
    SELECT owner_member.workspace_id, owner_member.user_id
    FROM member owner_member
    WHERE owner_member.workspace_id = sqlc.arg('workspace_id')
      AND owner_member.user_id = sqlc.arg('owner_user_id')
    FOR KEY SHARE OF owner_member
), locked_agent AS MATERIALIZED (
    SELECT a.id, a.workspace_id
    FROM agent a
    JOIN locked_owner_member owner_member
      ON owner_member.workspace_id = a.workspace_id
     AND owner_member.user_id = a.owner_id
    WHERE a.id = sqlc.arg('agent_id')
      AND a.workspace_id = sqlc.arg('workspace_id')
      AND a.owner_id = sqlc.arg('owner_user_id')
    FOR SHARE OF a
), locked_endpoint AS MATERIALIZED (
    SELECT endpoint.id
    FROM agent_a2a_endpoint endpoint
    JOIN locked_agent a
      ON a.id = endpoint.agent_id
     AND a.workspace_id = endpoint.workspace_id
    WHERE endpoint.workspace_id = sqlc.arg('workspace_id')
      AND endpoint.agent_id = sqlc.arg('agent_id')
      AND endpoint.delegated_by_user_id = sqlc.arg('owner_user_id')
    FOR SHARE OF endpoint
), locked_client AS MATERIALIZED (
    SELECT client.id
    FROM a2a_client client
    JOIN locked_endpoint endpoint ON endpoint.id = client.endpoint_id
    WHERE client.id = sqlc.arg('client_id')
      AND client.status <> 'revoked'
    FOR SHARE OF client
)
INSERT INTO a2a_client_credential (
    client_id,
    key_id,
    token_hash,
    token_prefix,
    status,
    expires_at,
    created_by
)
SELECT
    client.id,
    sqlc.arg('key_id'),
    sqlc.arg('token_hash'),
    sqlc.arg('token_prefix'),
    'active',
    sqlc.narg('expires_at')::timestamptz,
    sqlc.arg('owner_user_id')
FROM locked_client client
RETURNING
    id,
    client_id,
    key_id,
    token_prefix,
    status,
    expires_at,
    last_used_at,
    created_by,
    revoked_at,
    revoked_by,
    created_at,
    updated_at;

-- name: RevokeAgentA2ACredentialForOwner :one
UPDATE a2a_client_credential credential
SET status = 'revoked',
    revoked_at = now(),
    revoked_by = sqlc.arg('owner_user_id'),
    updated_at = now()
FROM a2a_client client
JOIN agent_a2a_endpoint endpoint ON endpoint.id = client.endpoint_id
JOIN agent a
  ON a.id = endpoint.agent_id
 AND a.workspace_id = endpoint.workspace_id
JOIN member owner_member
  ON owner_member.workspace_id = endpoint.workspace_id
 AND owner_member.user_id = sqlc.arg('owner_user_id')
WHERE credential.id = sqlc.arg('credential_id')
  AND credential.client_id = client.id
  AND client.id = sqlc.arg('client_id')
  AND credential.status = 'active'
  AND endpoint.workspace_id = sqlc.arg('workspace_id')
  AND endpoint.agent_id = sqlc.arg('agent_id')
  AND endpoint.delegated_by_user_id = sqlc.arg('owner_user_id')
  AND a.owner_id = sqlc.arg('owner_user_id')
RETURNING
    credential.id,
    credential.client_id,
    credential.key_id,
    credential.token_prefix,
    credential.status,
    credential.expires_at,
    credential.last_used_at,
    credential.created_by,
    credential.revoked_at,
    credential.revoked_by,
    credential.created_at,
    credential.updated_at;

-- name: GetAgentA2ACredentialByTokenHash :one
SELECT
    credential.id AS credential_id,
    credential.key_id AS credential_key_id,
    credential.token_prefix AS credential_token_prefix,
    credential.expires_at AS credential_expires_at,
    client.id AS client_id,
    client.name AS client_name,
    client.scopes AS client_scopes,
    client.rate_limit_per_minute,
    client.max_concurrent_tasks,
    endpoint.id AS endpoint_id,
    endpoint.workspace_id,
    endpoint.agent_id,
    endpoint.public_agent_id,
    endpoint.enabled AS endpoint_enabled,
    endpoint.delegated_by_user_id,
    a.owner_id AS agent_owner_id,
    a.runtime_id AS agent_runtime_id,
    a.archived_at AS agent_archived_at,
    COALESCE(a.owner_id = endpoint.delegated_by_user_id, FALSE) AS owner_binding_valid
FROM a2a_client_credential credential
JOIN a2a_client client ON client.id = credential.client_id
JOIN agent_a2a_endpoint endpoint ON endpoint.id = client.endpoint_id
JOIN agent a
  ON a.id = endpoint.agent_id
 AND a.workspace_id = endpoint.workspace_id
WHERE credential.token_hash = sqlc.arg('token_hash')
  AND credential.status = 'active'
  AND (credential.expires_at IS NULL OR credential.expires_at > now())
  AND client.status = 'active';

-- name: TouchAgentA2ACredentialLastUsed :exec
UPDATE a2a_client_credential
SET last_used_at = now()
WHERE id = sqlc.arg('credential_id')
  AND status = 'active'
  AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes');

-- name: GetA2AContextForClient :one
SELECT *
FROM a2a_context
WHERE endpoint_id = sqlc.arg('endpoint_id')
  AND client_id = sqlc.arg('client_id')
  AND public_context_id = sqlc.arg('public_context_id');

-- name: LockA2AContextForClient :one
SELECT *
FROM a2a_context
WHERE endpoint_id = sqlc.arg('endpoint_id')
  AND client_id = sqlc.arg('client_id')
  AND public_context_id = sqlc.arg('public_context_id')
FOR UPDATE;

-- name: CreateA2AContext :one
INSERT INTO a2a_context (
    endpoint_id,
    client_id,
    public_context_id,
    chat_session_id,
    expires_at
)
SELECT
    client.endpoint_id,
    client.id,
    sqlc.arg('public_context_id'),
    sqlc.arg('chat_session_id'),
    sqlc.narg('expires_at')::timestamptz
FROM a2a_client client
WHERE client.id = sqlc.arg('client_id')
  AND client.endpoint_id = sqlc.arg('endpoint_id')
ON CONFLICT (endpoint_id, client_id, public_context_id) DO NOTHING
RETURNING *;

-- name: TouchA2AContext :exec
UPDATE a2a_context
SET last_activity_at = now(),
    updated_at = now()
WHERE id = sqlc.arg('context_id')
  AND endpoint_id = sqlc.arg('endpoint_id')
  AND client_id = sqlc.arg('client_id');

-- name: GetA2AMessageClaimForClient :one
SELECT *
FROM a2a_task_binding
WHERE endpoint_id = sqlc.arg('endpoint_id')
  AND client_id = sqlc.arg('client_id')
  AND message_id = sqlc.arg('message_id');

-- name: LockAgentA2ASendAdmission :one
-- Linearization point for a new SendMessage. Locks are acquired in the same
-- outer-to-inner ownership order used by endpoint rebind/revocation:
-- agent/runtime -> endpoint -> client -> credential. The authoritative runtime
-- row is locked in this transaction so a mode/provider switch cannot race task
-- creation. A management write that commits first makes this query return no
-- rows; one that waits for this transaction observes the already-accepted task
-- after it resumes.
WITH locked_agent AS MATERIALIZED (
    SELECT a.id, a.workspace_id, a.owner_id, a.runtime_id
    FROM agent a
    JOIN agent_runtime runtime
      ON runtime.id = a.runtime_id
     AND runtime.workspace_id = a.workspace_id
    JOIN member owner_member
      ON owner_member.workspace_id = a.workspace_id
     AND owner_member.user_id = a.owner_id
    WHERE a.id = sqlc.arg('agent_id')
      AND a.workspace_id = sqlc.arg('workspace_id')
      AND a.archived_at IS NULL
      AND a.runtime_id IS NOT NULL
      -- Keep this predicate aligned with isAgentA2AEligibleRuntime, the
      -- published Card query, and the daemon's fail-closed execution guard.
      -- The cloud exception is deliberately tied to the platform-managed
      -- Aliyun FC sandbox shape, not to cloud/OpenCode runtimes in general.
      AND (
        (runtime.runtime_mode = 'local' AND runtime.provider = 'claude')
        OR (
          runtime.runtime_mode = 'cloud'
          AND runtime.provider = 'opencode'
          AND btrim(runtime.metadata->>'kind') = 'cloud-sandbox'
          AND lower(btrim(runtime.metadata->>'sandbox_backend')) = 'aliyun_fc'
          AND COALESCE(
            NULLIF(lower(btrim(runtime.metadata->>'provider')), ''),
            runtime.provider
          ) = 'opencode'
          AND lower(btrim(runtime.metadata->>'artifact_kind')) = 'e2b_template'
          AND btrim(COALESCE(runtime.metadata->>'artifact_ref', '')) <> ''
          AND runtime.metadata->'capabilities' ? 'a2a_inbound_opencode_v1'
        )
      )
    FOR SHARE OF a, runtime
), locked_endpoint AS MATERIALIZED (
    SELECT endpoint.*
    FROM agent_a2a_endpoint endpoint
    JOIN locked_agent a
      ON a.id = endpoint.agent_id
     AND a.workspace_id = endpoint.workspace_id
     AND a.owner_id = endpoint.delegated_by_user_id
    WHERE endpoint.id = sqlc.arg('endpoint_id')
      AND endpoint.public_agent_id = sqlc.arg('public_agent_id')
      AND endpoint.enabled = TRUE
    FOR UPDATE OF endpoint
), locked_client AS MATERIALIZED (
    SELECT client.*
    FROM a2a_client client
    JOIN locked_endpoint endpoint ON endpoint.id = client.endpoint_id
    WHERE client.id = sqlc.arg('client_id')
      AND client.status = 'active'
      AND 'send' = ANY(client.scopes)
    FOR UPDATE OF client
), locked_credential AS MATERIALIZED (
    SELECT credential.*
    FROM a2a_client_credential credential
    JOIN locked_client client ON client.id = credential.client_id
    WHERE credential.id = sqlc.arg('credential_id')
      AND credential.status = 'active'
      AND (credential.expires_at IS NULL OR credential.expires_at > now())
    FOR UPDATE OF credential
)
SELECT
    endpoint.id AS endpoint_id,
    endpoint.workspace_id,
    endpoint.agent_id,
    endpoint.public_agent_id,
    endpoint.card_name,
    endpoint.delegated_by_user_id,
    agent.runtime_id AS agent_runtime_id,
    client.id AS client_id,
    credential.id AS credential_id
FROM locked_endpoint endpoint
JOIN locked_agent agent ON agent.id = endpoint.agent_id
JOIN locked_client client ON client.endpoint_id = endpoint.id
JOIN locked_credential credential ON credential.client_id = client.id;

-- name: CreateA2ATaskBinding :one
INSERT INTO a2a_task_binding (
    endpoint_id,
    client_id,
    context_id,
    accepted_credential_id,
    public_task_id,
    message_id,
    request_fingerprint,
    artifact_id,
    root_local_task_id,
    input_chat_message_id,
    request_id
)
SELECT
    context.endpoint_id,
    context.client_id,
    context.id,
    credential.id,
    sqlc.arg('public_task_id'),
    sqlc.arg('message_id'),
    sqlc.arg('request_fingerprint'),
    sqlc.arg('artifact_id'),
    task.id,
    input_message.id,
    sqlc.narg('request_id')::text
FROM a2a_context context
JOIN a2a_client client
  ON client.id = context.client_id
 AND client.endpoint_id = context.endpoint_id
 AND client.status = 'active'
JOIN agent_a2a_endpoint endpoint ON endpoint.id = context.endpoint_id
JOIN agent a
  ON a.id = endpoint.agent_id
 AND a.workspace_id = endpoint.workspace_id
 AND a.owner_id = endpoint.delegated_by_user_id
 AND a.archived_at IS NULL
 AND a.runtime_id IS NOT NULL
JOIN member owner_member
  ON owner_member.workspace_id = endpoint.workspace_id
 AND owner_member.user_id = a.owner_id
JOIN a2a_client_credential credential
  ON credential.id = sqlc.arg('credential_id')
 AND credential.client_id = client.id
 AND credential.status = 'active'
 AND (credential.expires_at IS NULL OR credential.expires_at > now())
JOIN agent_task_queue task
  ON task.id = sqlc.arg('root_local_task_id')
 AND task.agent_id = endpoint.agent_id
 AND task.chat_session_id = context.chat_session_id
JOIN chat_message input_message
  ON input_message.id = sqlc.arg('input_chat_message_id')
 AND input_message.chat_session_id = context.chat_session_id
 AND input_message.task_id = task.id
 AND input_message.role = 'user'
WHERE context.id = sqlc.arg('context_id')
  AND context.endpoint_id = sqlc.arg('endpoint_id')
  AND context.client_id = sqlc.arg('client_id')
  AND endpoint.enabled = TRUE
  AND 'send' = ANY(client.scopes)
RETURNING *;

-- name: GetA2ATaskBindingForClient :one
SELECT *
FROM a2a_task_binding
WHERE endpoint_id = sqlc.arg('endpoint_id')
  AND client_id = sqlc.arg('client_id')
  AND public_task_id = sqlc.arg('public_task_id');

-- name: LockA2ATaskBindingForClient :one
SELECT *
FROM a2a_task_binding
WHERE endpoint_id = sqlc.arg('endpoint_id')
  AND client_id = sqlc.arg('client_id')
  AND public_task_id = sqlc.arg('public_task_id')
FOR UPDATE;

-- name: LockA2ATaskBindingForLocalTask :one
WITH RECURSIVE ancestors AS (
    SELECT task.id, task.parent_task_id
    FROM agent_task_queue task
    WHERE task.id = sqlc.arg('local_task_id')

    UNION ALL

    SELECT parent.id, parent.parent_task_id
    FROM agent_task_queue parent
    JOIN ancestors child ON parent.id = child.parent_task_id
)
SELECT binding.*
FROM a2a_task_binding binding
JOIN ancestors ON ancestors.id = binding.root_local_task_id
FOR UPDATE OF binding;

-- name: IsA2ALocalTask :one
-- The chat-session binding is authoritative for every local execution in an
-- A2A context, including future retry descendants. Task.context provides the
-- hot-path marker; this query is the durable fallback for older rows and for
-- fail-closed privacy checks when task metadata is incomplete.
SELECT EXISTS (
    SELECT 1
    FROM agent_task_queue task
    JOIN a2a_context context ON context.chat_session_id = task.chat_session_id
    WHERE task.id = sqlc.arg('local_task_id')
);

-- name: SetA2ATaskCancelRequested :one
UPDATE a2a_task_binding
SET cancel_requested_at = COALESCE(cancel_requested_at, now()),
    updated_at = now()
WHERE id = sqlc.arg('binding_id')
RETURNING *;

-- name: MarkA2ATaskFailureFinalized :one
UPDATE a2a_task_binding
SET failure_finalized_local_task_id = sqlc.arg('local_task_id'),
    updated_at = now()
WHERE id = sqlc.arg('binding_id')
  AND cancel_requested_at IS NULL
RETURNING *;

-- name: GetA2ATaskProjectionForClient :one
-- A public A2A task remains stable while local automatic retries create a
-- descendant chain. Only descendants that retain the same agent, chat, and
-- immutable chat input owner are part of this logical task; delegated work is
-- intentionally outside this projection.
WITH RECURSIVE selected_binding AS (
    SELECT binding.*
    FROM a2a_task_binding binding
    WHERE binding.endpoint_id = sqlc.arg('endpoint_id')
      AND binding.client_id = sqlc.arg('client_id')
      AND binding.public_task_id = sqlc.arg('public_task_id')
), lineage AS (
    SELECT
        task.id,
        task.parent_task_id,
        task.agent_id,
        task.chat_session_id,
        task.chat_input_task_id,
        task.status,
        task.result,
        task.error,
        task.failure_reason,
        task.created_at,
        task.dispatched_at,
        task.started_at,
        task.completed_at,
        0 AS depth
    FROM selected_binding binding
    JOIN agent_task_queue task ON task.id = binding.root_local_task_id

    UNION ALL

    SELECT
        child.id,
        child.parent_task_id,
        child.agent_id,
        child.chat_session_id,
        child.chat_input_task_id,
        child.status,
        child.result,
        child.error,
        child.failure_reason,
        child.created_at,
        child.dispatched_at,
        child.started_at,
        child.completed_at,
        parent.depth + 1
    FROM agent_task_queue child
    JOIN lineage parent ON child.parent_task_id = parent.id
    WHERE child.agent_id = parent.agent_id
      AND child.chat_session_id = parent.chat_session_id
      AND child.chat_input_task_id = parent.chat_input_task_id
), current_task AS (
    SELECT *
    FROM lineage
    ORDER BY depth DESC, created_at DESC, id DESC
    LIMIT 1
)
SELECT
    binding.id AS binding_id,
    binding.endpoint_id,
    binding.client_id,
    binding.public_task_id,
    context.public_context_id,
    binding.message_id,
    binding.request_fingerprint,
    binding.artifact_id,
    binding.root_local_task_id,
    current_task.id AS current_local_task_id,
    current_task.status AS task_status,
    current_task.result AS task_result,
    current_task.error AS task_error,
    current_task.failure_reason AS task_failure_reason,
    current_task.created_at AS task_created_at,
    current_task.dispatched_at AS task_dispatched_at,
    current_task.started_at AS task_started_at,
    current_task.completed_at AS task_completed_at,
    COALESCE(outcome.content, '')::text AS assistant_result_text,
    COALESCE(outcome.message_kind, '')::text AS assistant_message_kind,
    binding.cancel_requested_at,
    binding.failure_finalized_local_task_id,
    binding.created_at AS binding_created_at,
    binding.updated_at AS binding_updated_at
FROM selected_binding binding
JOIN a2a_context context ON context.id = binding.context_id
JOIN current_task ON TRUE
LEFT JOIN LATERAL (
    SELECT message.content, message.message_kind
    FROM chat_message message
    WHERE message.task_id = current_task.id
      AND message.role = 'assistant'
    ORDER BY message.created_at DESC, message.id DESC
    LIMIT 1
) outcome ON TRUE;

-- name: ListA2ATaskProjectionsForClient :many
-- Cursor ordering is stable across replicas and new inserts. The public task
-- id is the deterministic tie-breaker for equal creation timestamps.
WITH RECURSIVE selected_bindings AS (
    SELECT binding.*
    FROM a2a_task_binding binding
    WHERE binding.endpoint_id = sqlc.arg('endpoint_id')
      AND binding.client_id = sqlc.arg('client_id')
      AND (
          sqlc.narg('before_created_at')::timestamptz IS NULL
          OR (binding.created_at, binding.public_task_id) < (
              sqlc.narg('before_created_at')::timestamptz,
              sqlc.narg('before_public_task_id')::text
          )
      )
    ORDER BY binding.created_at DESC, binding.public_task_id DESC
    LIMIT sqlc.arg('page_limit')
), lineage AS (
    SELECT
        binding.id AS binding_id,
        task.id,
        task.parent_task_id,
        task.agent_id,
        task.chat_session_id,
        task.chat_input_task_id,
        task.status,
        task.result,
        task.error,
        task.failure_reason,
        task.created_at,
        task.dispatched_at,
        task.started_at,
        task.completed_at,
        0 AS depth
    FROM selected_bindings binding
    JOIN agent_task_queue task ON task.id = binding.root_local_task_id

    UNION ALL

    SELECT
        parent.binding_id,
        child.id,
        child.parent_task_id,
        child.agent_id,
        child.chat_session_id,
        child.chat_input_task_id,
        child.status,
        child.result,
        child.error,
        child.failure_reason,
        child.created_at,
        child.dispatched_at,
        child.started_at,
        child.completed_at,
        parent.depth + 1
    FROM agent_task_queue child
    JOIN lineage parent ON child.parent_task_id = parent.id
    WHERE child.agent_id = parent.agent_id
      AND child.chat_session_id = parent.chat_session_id
      AND child.chat_input_task_id = parent.chat_input_task_id
), ranked_lineage AS (
    SELECT
        lineage.*,
        row_number() OVER (
            PARTITION BY lineage.binding_id
            ORDER BY lineage.depth DESC, lineage.created_at DESC, lineage.id DESC
        ) AS lineage_rank
    FROM lineage
)
SELECT
    binding.id AS binding_id,
    binding.endpoint_id,
    binding.client_id,
    binding.public_task_id,
    context.public_context_id,
    binding.message_id,
    binding.request_fingerprint,
    binding.artifact_id,
    binding.root_local_task_id,
    current_task.id AS current_local_task_id,
    current_task.status AS task_status,
    current_task.result AS task_result,
    current_task.error AS task_error,
    current_task.failure_reason AS task_failure_reason,
    current_task.created_at AS task_created_at,
    current_task.dispatched_at AS task_dispatched_at,
    current_task.started_at AS task_started_at,
    current_task.completed_at AS task_completed_at,
    COALESCE(outcome.content, '')::text AS assistant_result_text,
    COALESCE(outcome.message_kind, '')::text AS assistant_message_kind,
    binding.cancel_requested_at,
    binding.failure_finalized_local_task_id,
    binding.created_at AS binding_created_at,
    binding.updated_at AS binding_updated_at
FROM selected_bindings binding
JOIN a2a_context context ON context.id = binding.context_id
JOIN ranked_lineage current_task
  ON current_task.binding_id = binding.id
 AND current_task.lineage_rank = 1
LEFT JOIN LATERAL (
    SELECT message.content, message.message_kind
    FROM chat_message message
    WHERE message.task_id = current_task.id
      AND message.role = 'assistant'
    ORDER BY message.created_at DESC, message.id DESC
    LIMIT 1
) outcome ON TRUE
ORDER BY binding.created_at DESC, binding.public_task_id DESC;
