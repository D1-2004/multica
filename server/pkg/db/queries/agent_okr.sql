-- name: UpsertAgentOKRLabel :one
-- OKR labels are server-managed and land in the 'issue' namespace so the agent
-- can attach them to issues through the ordinary labeling path. Keyed on the
-- catalog's (workspace, resource_type, lower(name)) uniqueness so renaming an
-- OKR to an existing label reuses that label instead of failing.
INSERT INTO issue_label (workspace_id, resource_type, name, description, color)
VALUES ($1, 'issue', $2, $3, $4)
ON CONFLICT (workspace_id, resource_type, (LOWER(name)))
DO UPDATE SET
    description = EXCLUDED.description,
    updated_at = now()
RETURNING *;

-- name: ListAgentOKRs :many
-- Objectives first, each followed by its key results, in authored order.
SELECT
    okr.id, okr.workspace_id, okr.agent_id, okr.kind, okr.parent_id,
    okr.label_id, okr.position, okr.created_at, okr.updated_at,
    label.name AS label_name,
    label.color AS label_color,
    label.description AS label_description
FROM agent_okr okr
JOIN issue_label label ON label.id = okr.label_id
WHERE okr.agent_id = $1 AND okr.workspace_id = $2
ORDER BY
    COALESCE(okr.parent_id, okr.id),
    okr.kind = 'key_result',
    okr.position,
    okr.created_at;

-- name: CreateAgentOKR :one
INSERT INTO agent_okr (workspace_id, agent_id, kind, parent_id, label_id, position)
VALUES ($1, $2, $3, sqlc.narg('parent_id'), $4, $5)
RETURNING *;

-- name: DeleteAgentOKRsByAgent :exec
DELETE FROM agent_okr WHERE agent_id = $1 AND workspace_id = $2;

-- name: ListAgentOKRLabelIDs :many
-- Used to decide which labels a rewrite orphaned. The catalog rows themselves
-- are left in place: a label may already be attached to issues, and silently
-- deleting it would strip tags off historical work.
SELECT label_id FROM agent_okr WHERE agent_id = $1 AND workspace_id = $2;
