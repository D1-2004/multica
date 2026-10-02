-- Agent work scene directory (docs/agent-scene.md). Every read is scoped by
-- workspace and agent; a scene id alone never reaches another agent's scene.

-- name: GetAgentScene :one
SELECT * FROM agent_scene
WHERE id = @id AND workspace_id = @workspace_id AND agent_id = @agent_id;

-- name: FindAgentSceneByLocator :one
SELECT * FROM agent_scene
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND provider = @provider
  AND tenant_org_id = @tenant_org_id
  AND source_namespace = @source_namespace
  AND external_scene_id = @external_scene_id;

-- name: InsertAgentScene :one
-- Registers a scene; a concurrent registration of the same locator inserts
-- nothing (no row) and the caller reads the winner back.
INSERT INTO agent_scene (
    workspace_id, agent_id, provider, tenant_org_id, source_namespace,
    scene_kind, external_scene_id, title, last_active_at
) VALUES (
    @workspace_id, @agent_id, @provider, @tenant_org_id, @source_namespace,
    @scene_kind, @external_scene_id, @title, @last_active_at
)
ON CONFLICT (workspace_id, agent_id, provider, tenant_org_id, source_namespace, external_scene_id)
DO NOTHING
RETURNING *;

-- name: TouchAgentScene :one
-- Records activity: a non-empty title replaces the stored one and
-- last_active_at only moves forward.
UPDATE agent_scene
SET title = CASE WHEN @title::text = '' THEN title ELSE @title::text END,
    last_active_at = GREATEST(last_active_at, @last_active_at::timestamptz),
    updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND agent_id = @agent_id
RETURNING *;

-- name: ListAgentScenesByTenant :many
-- One page of an agent's conversation scenes in a tenant org, newest
-- activity first (agent_scene_agent_active_idx).
SELECT * FROM agent_scene
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND tenant_org_id = @tenant_org_id
  AND scene_kind IN ('group', 'dm')
  AND (NOT @groups_only::boolean OR scene_kind = 'group')
ORDER BY last_active_at DESC, id DESC
LIMIT @page_limit OFFSET @page_offset;

-- name: ListAgentScenesByIDs :many
SELECT * FROM agent_scene
WHERE workspace_id = @workspace_id
  AND agent_id = @agent_id
  AND id = ANY(@ids::uuid[]);

-- name: DeleteAgentScenesByAgent :exec
DELETE FROM agent_scene WHERE workspace_id = @workspace_id AND agent_id = @agent_id;

-- name: SettleAgentSceneKind :one
-- A trusted inbound event that states the conversation type settles a kind
-- migration 9510 assigned without evidence (kind_source = 'migrated',
-- docs/agent-scene.md §8). An observed kind is never changed.
UPDATE agent_scene
SET scene_kind = @scene_kind, kind_source = 'observed', updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND agent_id = @agent_id
  AND kind_source = 'migrated'
RETURNING *;

-- name: AgentServesTenantOrg :one
-- Whether a tenant was created for the agent in org_id (agent_tenant); the
-- identity org is a tenant without a row (docs/agent-scene.md §3).
SELECT EXISTS (
    SELECT 1 FROM agent_tenant
    WHERE workspace_id = @workspace_id AND agent_id = @agent_id AND org_id = @org_id
)::bool AS served;
