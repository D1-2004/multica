-- name: UpsertAgentOKRLabel :one
-- OKR labels are server-managed and land in the 'issue' namespace so the agent
-- can attach them to issues through the ordinary labeling path. A rewrite may
-- reuse only labels referenced by that agent's previous OKR set. A same-name
-- ordinary label or another agent's OKR label makes the conflict return no row;
-- the handler turns that into an explicit 409 instead of silently taking over
-- somebody else's catalog entry.
INSERT INTO issue_label (workspace_id, resource_type, name, description, color)
VALUES ($1, 'issue', $2, $3, $4)
ON CONFLICT (workspace_id, resource_type, (LOWER(name)))
DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    color = EXCLUDED.color,
    updated_at = now()
WHERE issue_label.id = ANY(sqlc.arg('reusable_label_ids')::uuid[])
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
JOIN agent_okr objective ON objective.id = CASE
    WHEN okr.kind = 'objective' THEN okr.id
    ELSE okr.parent_id
END
    AND objective.kind = 'objective'
    AND objective.agent_id = okr.agent_id
    AND objective.workspace_id = okr.workspace_id
WHERE okr.agent_id = $1 AND okr.workspace_id = $2
ORDER BY
    objective.position,
    objective.created_at,
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
-- deleting it would strip tags off historical work. Legacy labels shared by
-- multiple agents are excluded: neither agent may rewrite a shared vocabulary
-- entry until it chooses an unambiguous name.
SELECT DISTINCT mine.label_id
FROM agent_okr mine
WHERE mine.agent_id = $1
  AND mine.workspace_id = $2
  AND NOT EXISTS (
      SELECT 1
      FROM agent_okr other
      WHERE other.workspace_id = mine.workspace_id
        AND other.label_id = mine.label_id
        AND other.agent_id <> mine.agent_id
  );

-- name: IsLabelReferencedByAgentOKR :one
-- Generic label update/delete must not mutate the live vocabulary injected
-- into an Agent prompt. Orphaned labels left by an OKR rewrite are deliberately
-- editable/deletable because no current agent_okr row references them.
SELECT EXISTS (
    SELECT 1
    FROM agent_okr
    WHERE label_id = sqlc.arg('label_id')::uuid
      AND workspace_id = sqlc.arg('workspace_id')::uuid
)::boolean;

-- name: ListAgentOKRUsage :many
-- Cost and token spend per configured OKR label in one pass. This is the
-- target/label combination cost: every task on a labeled Issue participates,
-- including collaborating agents. Executor splits come from label usage task
-- rows, not from this summary.
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
WITH model_pricing AS (
    SELECT sqlc.arg('model_pricing')::jsonb AS catalog
), priced_task_usage AS (
    SELECT
        tu.*,
        CASE
            WHEN tu.cost_usd_ticks IS NOT NULL THEN tu.cost_usd_ticks
            WHEN rate.value IS NOT NULL THEN ROUND((
                tu.input_tokens * (rate.value->>'input')::numeric +
                tu.output_tokens * (rate.value->>'output')::numeric +
                tu.cache_read_tokens * (rate.value->>'cache_read')::numeric +
                tu.cache_write_tokens * (rate.value->>'cache_write')::numeric
            ) * 10000)::bigint
        END AS effective_cost_usd_ticks
    FROM task_usage tu
    CROSS JOIN model_pricing pricing
    LEFT JOIN LATERAL (
        SELECT COALESCE(
            pricing.catalog -> LOWER(tu.model),
            CASE
                WHEN POSITION('/' IN LOWER(tu.model)) > 1
                 AND POSITION('/' IN LOWER(tu.model)) < LENGTH(LOWER(tu.model))
                THEN pricing.catalog -> SUBSTRING(
                    LOWER(tu.model) FROM POSITION('/' IN LOWER(tu.model)) + 1
                )
            END
        ) AS value
    ) rate ON TRUE
), task_totals AS (
    SELECT
        okr.label_id AS label_id,
        atq.id AS task_id,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ), 0)::bigint AS total_tokens,
        COALESCE(SUM(tu.effective_cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ) FILTER (WHERE tu.id IS NOT NULL AND tu.effective_cost_usd_ticks IS NULL), 0)::bigint AS uncosted_tokens,
        COALESCE(
            BOOL_AND(tu.effective_cost_usd_ticks IS NOT NULL) FILTER (WHERE tu.id IS NOT NULL),
            FALSE
        ) AS is_priced
    FROM agent_okr okr
    JOIN issue_to_label il ON il.label_id = okr.label_id
    JOIN issue i ON i.id = il.issue_id AND i.workspace_id = okr.workspace_id
    JOIN agent_task_queue atq ON atq.issue_id = i.id
    LEFT JOIN priced_task_usage tu ON tu.task_id = atq.id
    WHERE okr.agent_id = $1 AND okr.workspace_id = $2
    GROUP BY okr.label_id, atq.id
)
SELECT
    label_id,
    COALESCE(SUM(total_tokens), 0)::bigint AS total_tokens,
    COALESCE(SUM(total_cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
    COALESCE(SUM(uncosted_tokens), 0)::bigint AS uncosted_tokens,
    COUNT(*)::bigint AS task_count,
    COUNT(*) FILTER (WHERE NOT is_priced)::bigint AS unpriced_task_count
FROM task_totals
GROUP BY label_id;
