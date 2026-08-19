-- name: CreateAgentMCPDelegationClaim :one
INSERT INTO agent_mcp_delegation (
    endpoint_id,
    client_id,
    accepted_credential_id,
    public_task_id,
    request_id,
    request_fingerprint,
    operation
)
SELECT
    endpoint.id,
    client.id,
    credential.id,
    sqlc.arg('public_task_id'),
    sqlc.arg('request_id'),
    sqlc.arg('request_fingerprint'),
    sqlc.arg('operation')
FROM agent_a2a_endpoint endpoint
JOIN agent
  ON agent.id = endpoint.agent_id
 AND agent.workspace_id = endpoint.workspace_id
 AND agent.owner_id = endpoint.delegated_by_user_id
 AND agent.archived_at IS NULL
 AND agent.runtime_id IS NOT NULL
JOIN member owner_member
  ON owner_member.workspace_id = agent.workspace_id
 AND owner_member.user_id = agent.owner_id
JOIN a2a_client client
  ON client.endpoint_id = endpoint.id
 AND client.id = sqlc.arg('client_id')
 AND client.status = 'active'
 AND 'send' = ANY(client.scopes)
JOIN a2a_client_credential credential
  ON credential.client_id = client.id
 AND credential.id = sqlc.arg('credential_id')
 AND credential.status = 'active'
 AND (credential.expires_at IS NULL OR credential.expires_at > now())
WHERE endpoint.id = sqlc.arg('endpoint_id')
  AND endpoint.workspace_id = sqlc.arg('workspace_id')
  AND endpoint.agent_id = sqlc.arg('agent_id')
  AND endpoint.public_agent_id = sqlc.arg('public_agent_id')
ON CONFLICT (client_id, request_id) DO NOTHING
RETURNING *;

-- name: GetAgentMCPDelegationByRequest :one
SELECT *
FROM agent_mcp_delegation
WHERE endpoint_id = sqlc.arg('endpoint_id')
  AND client_id = sqlc.arg('client_id')
  AND request_id = sqlc.arg('request_id');

-- name: LockAgentMCPDelegationAdmission :one
WITH locked_agent AS MATERIALIZED (
    SELECT agent.*
    FROM agent
    JOIN agent_runtime runtime
      ON runtime.id = agent.runtime_id
     AND runtime.workspace_id = agent.workspace_id
    JOIN member owner_member
      ON owner_member.workspace_id = agent.workspace_id
     AND owner_member.user_id = agent.owner_id
    WHERE agent.id = sqlc.arg('agent_id')
      AND agent.workspace_id = sqlc.arg('workspace_id')
      AND agent.owner_id = sqlc.arg('owner_id')
      AND agent.archived_at IS NULL
      AND agent.runtime_id IS NOT NULL
    FOR SHARE OF agent, runtime
), locked_endpoint AS MATERIALIZED (
    SELECT endpoint.*
    FROM agent_a2a_endpoint endpoint
    JOIN locked_agent agent
      ON agent.id = endpoint.agent_id
     AND agent.workspace_id = endpoint.workspace_id
     AND agent.owner_id = endpoint.delegated_by_user_id
    WHERE endpoint.id = sqlc.arg('endpoint_id')
      AND endpoint.public_agent_id = sqlc.arg('public_agent_id')
    FOR SHARE OF endpoint
), locked_client AS MATERIALIZED (
    SELECT client.*
    FROM a2a_client client
    JOIN locked_endpoint endpoint ON endpoint.id = client.endpoint_id
    WHERE client.id = sqlc.arg('client_id')
      AND client.status = 'active'
      AND 'send' = ANY(client.scopes)
    FOR SHARE OF client
), locked_credential AS MATERIALIZED (
    SELECT credential.*
    FROM a2a_client_credential credential
    JOIN locked_client client ON client.id = credential.client_id
    WHERE credential.id = sqlc.arg('credential_id')
      AND credential.status = 'active'
      AND (credential.expires_at IS NULL OR credential.expires_at > now())
    FOR SHARE OF credential
)
SELECT delegation.*
FROM agent_mcp_delegation delegation
JOIN locked_endpoint endpoint ON endpoint.id = delegation.endpoint_id
JOIN locked_client client ON client.id = delegation.client_id
JOIN locked_credential credential ON TRUE
WHERE delegation.endpoint_id = sqlc.arg('endpoint_id')
  AND delegation.client_id = sqlc.arg('client_id')
  AND delegation.request_id = sqlc.arg('request_id')
-- A follow-up comment is inserted by IssueCommentService on a separate
-- transaction and references this row through a foreign key. NO KEY UPDATE
-- still serializes callers changing the claim while remaining compatible
-- with the FK's KEY SHARE lock.
FOR NO KEY UPDATE OF delegation;

-- name: BindAgentMCPDelegation :one
UPDATE agent_mcp_delegation
SET issue_id = sqlc.arg('issue_id'),
    root_local_task_id = sqlc.arg('root_local_task_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND issue_id IS NULL
RETURNING *;

-- name: SetAgentMCPDelegationIssue :one
UPDATE agent_mcp_delegation
SET issue_id = sqlc.arg('issue_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND (issue_id IS NULL OR issue_id = sqlc.arg('issue_id'))
RETURNING *;

-- name: BindAgentMCPDelegationTask :one
UPDATE agent_mcp_delegation
SET root_local_task_id = sqlc.arg('root_local_task_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND issue_id = sqlc.arg('issue_id')
  AND root_local_task_id IS NULL
RETURNING *;

-- name: GetFirstAgentTaskForIssueAndAgent :one
SELECT *
FROM agent_task_queue
WHERE issue_id = sqlc.arg('issue_id')
  AND agent_id = sqlc.arg('agent_id')
ORDER BY created_at ASC, id ASC
LIMIT 1;

-- name: GetAgentMCPFollowUpCommentByClaim :one
SELECT *
FROM comment
WHERE agent_mcp_claim_id = sqlc.arg('claim_id')
LIMIT 1;

-- name: GetAgentMCPDelegationTask :one
WITH RECURSIVE selected AS (
    SELECT delegation.*
    FROM agent_mcp_delegation delegation
    WHERE delegation.endpoint_id = sqlc.arg('endpoint_id')
      AND delegation.client_id = sqlc.arg('client_id')
      AND delegation.public_task_id = sqlc.arg('public_task_id')
      AND delegation.issue_id IS NOT NULL
      AND delegation.root_local_task_id IS NOT NULL
), lineage AS (
    SELECT
        task.*,
        0 AS depth
    FROM selected
    JOIN agent_task_queue task ON task.id = selected.root_local_task_id

    UNION ALL

    SELECT
        child.*,
        parent.depth + 1
    FROM agent_task_queue child
    JOIN lineage parent ON child.parent_task_id = parent.id
    WHERE child.agent_id = parent.agent_id
      AND child.issue_id = parent.issue_id
), current_task AS (
    SELECT *
    FROM lineage
    ORDER BY depth DESC, created_at DESC, id DESC
    LIMIT 1
)
SELECT
    selected.id AS delegation_id,
    selected.public_task_id,
    selected.request_id,
    selected.operation,
    selected.created_at AS delegation_created_at,
    selected.updated_at AS delegation_updated_at,
    issue.id AS issue_id,
    issue.number AS issue_number,
    issue.title AS issue_title,
    issue.description AS issue_description,
    issue.status AS issue_status,
    issue.priority AS issue_priority,
    issue.project_id,
    issue.parent_issue_id,
    issue.stage AS issue_stage,
    issue.start_date AS issue_start_date,
    issue.due_date AS issue_due_date,
    issue.created_at AS issue_created_at,
    issue.updated_at AS issue_updated_at,
    current_task.id AS current_local_task_id,
    current_task.status AS task_status,
    current_task.result AS task_result,
    current_task.error AS task_error,
    current_task.failure_reason AS task_failure_reason,
    current_task.created_at AS task_created_at,
    current_task.dispatched_at AS task_dispatched_at,
    current_task.started_at AS task_started_at,
    current_task.completed_at AS task_completed_at,
    workspace.slug AS workspace_slug,
    workspace.issue_prefix
FROM selected
JOIN issue ON issue.id = selected.issue_id
JOIN workspace ON workspace.id = issue.workspace_id
JOIN current_task ON TRUE;

-- name: GetAgentMCPDelegationByIssue :one
SELECT delegation.*
FROM agent_mcp_delegation delegation
WHERE delegation.endpoint_id = sqlc.arg('endpoint_id')
  AND delegation.client_id = sqlc.arg('client_id')
  AND delegation.issue_id = sqlc.arg('issue_id')
ORDER BY delegation.created_at ASC
LIMIT 1;

-- name: ListAgentMCPDelegationsByIssue :many
SELECT delegation.*
FROM agent_mcp_delegation delegation
WHERE delegation.endpoint_id = sqlc.arg('endpoint_id')
  AND delegation.client_id = sqlc.arg('client_id')
  AND delegation.issue_id = sqlc.arg('issue_id')
  AND delegation.root_local_task_id IS NOT NULL
ORDER BY delegation.created_at ASC, delegation.id ASC
LIMIT 100;

-- name: ListAgentMCPDelegationComments :many
SELECT comment.*
FROM comment
WHERE comment.issue_id = sqlc.arg('issue_id')
  AND EXISTS (
      SELECT 1
      FROM agent_mcp_delegation delegation
      WHERE delegation.endpoint_id = sqlc.arg('endpoint_id')
        AND delegation.client_id = sqlc.arg('client_id')
        AND delegation.issue_id = comment.issue_id
  )
ORDER BY comment.created_at ASC, comment.id ASC
LIMIT sqlc.arg('row_limit');

-- name: ListAgentMCPDelegationArtifacts :many
SELECT DISTINCT attachment.*
FROM agent_mcp_delegation delegation
JOIN attachment
  ON attachment.workspace_id = sqlc.arg('workspace_id')
LEFT JOIN comment ON comment.id = attachment.comment_id
WHERE delegation.endpoint_id = sqlc.arg('endpoint_id')
  AND delegation.client_id = sqlc.arg('client_id')
  AND delegation.issue_id = sqlc.arg('issue_id')
  AND (
      attachment.issue_id = delegation.issue_id
      OR comment.issue_id = delegation.issue_id
  )
ORDER BY attachment.created_at ASC, attachment.id ASC;

-- name: GetAgentMCPDelegationArtifact :one
SELECT attachment.*
FROM agent_mcp_delegation delegation
JOIN attachment ON attachment.id = sqlc.arg('attachment_id')
LEFT JOIN comment ON comment.id = attachment.comment_id
WHERE delegation.endpoint_id = sqlc.arg('endpoint_id')
  AND delegation.client_id = sqlc.arg('client_id')
  AND attachment.workspace_id = sqlc.arg('workspace_id')
  AND (
      attachment.issue_id = delegation.issue_id
      OR comment.issue_id = delegation.issue_id
  )
LIMIT 1;
