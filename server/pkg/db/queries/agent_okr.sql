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

-- name: ListAgentOKRUsage :many
-- Cost and token spend per OKR label, for the whole agent in one pass.
--
-- Per-label usage already exists as GetIssueLabelUsageSummary, but that is one
-- round trip per label; an agent with 3 objectives and 9 key results would pay
-- 12 of them to render one settings page. Same shape, grouped by label instead.
--
-- The inner grouping is per (label, task). task_usage is unique per
-- (task, provider, model), so a task that used two models has two rows: summing
-- them is right for tokens and cost, but COUNT(*) over the raw join would report
-- models instead of tasks, and the priced/unpriced verdict has to be decided per
-- task rather than per row.
WITH task_totals AS (
    SELECT
        okr.label_id AS label_id,
        atq.id AS task_id,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ), 0)::bigint AS total_tokens,
        COALESCE(SUM(tu.cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
        COALESCE(
            BOOL_AND(tu.cost_usd_ticks IS NOT NULL) FILTER (WHERE tu.id IS NOT NULL),
            FALSE
        ) AS is_priced
    FROM agent_okr okr
    JOIN issue_to_label il ON il.label_id = okr.label_id
    JOIN issue i ON i.id = il.issue_id AND i.workspace_id = okr.workspace_id
    JOIN agent_task_queue atq ON atq.issue_id = i.id
    LEFT JOIN task_usage tu ON tu.task_id = atq.id
    WHERE okr.agent_id = $1 AND okr.workspace_id = $2
    GROUP BY okr.label_id, atq.id
)
SELECT
    label_id,
    COALESCE(SUM(total_tokens), 0)::bigint AS total_tokens,
    COALESCE(SUM(total_cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
    COUNT(*)::bigint AS task_count,
    COUNT(*) FILTER (WHERE NOT is_priced)::bigint AS unpriced_task_count
FROM task_totals
GROUP BY label_id;
