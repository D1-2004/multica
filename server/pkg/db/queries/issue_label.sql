-- name: ListLabels :many
WITH issue_label_task_usage AS (
    -- One row per task is the load-bearing grain here. A task can report more
    -- than one (provider, model) usage row, so aggregating before joining the
    -- label catalogue prevents task counts and per-label totals from being
    -- multiplied by that model fan-out.
    SELECT
        il.label_id,
        atq.id AS task_id,
        COUNT(tu.id)::bigint AS usage_row_count,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ), 0)::bigint AS total_tokens,
        COALESCE(SUM(tu.cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ) FILTER (WHERE tu.id IS NOT NULL AND tu.cost_usd_ticks IS NULL), 0)::bigint AS uncosted_tokens,
        COALESCE(
            BOOL_AND(tu.cost_usd_ticks IS NOT NULL) FILTER (WHERE tu.id IS NOT NULL),
            FALSE
        ) AS is_priced
    FROM issue_to_label il
    JOIN issue i ON i.id = il.issue_id
    JOIN agent_task_queue atq ON atq.issue_id = i.id
    LEFT JOIN task_usage tu ON tu.task_id = atq.id
    WHERE i.workspace_id = sqlc.arg('workspace_id')::uuid
      AND sqlc.arg('resource_type')::text = 'issue'
      AND sqlc.arg('include_usage')::boolean
    GROUP BY il.label_id, atq.id
), issue_label_usage AS (
    SELECT
        label_id,
        SUM(total_tokens)::bigint AS total_tokens,
        SUM(total_cost_usd_ticks)::bigint AS total_cost_usd_ticks,
        SUM(uncosted_tokens)::bigint AS uncosted_tokens,
        COUNT(*)::bigint AS task_count,
        COUNT(*) FILTER (WHERE is_priced)::bigint AS priced_task_count,
        COUNT(*) FILTER (WHERE NOT is_priced)::bigint AS unpriced_task_count
    FROM issue_label_task_usage
    GROUP BY label_id
)
SELECT l.*,
    CASE l.resource_type
        WHEN 'issue' THEN (SELECT COUNT(*) FROM issue_to_label x WHERE x.label_id = l.id)
        WHEN 'agent' THEN (SELECT COUNT(*) FROM agent_to_label x WHERE x.label_id = l.id)
        WHEN 'skill' THEN (SELECT COUNT(*) FROM skill_to_label x WHERE x.label_id = l.id)
        ELSE 0
    END::bigint AS usage_count,
    CASE WHEN l.resource_type = 'issue' THEN COALESCE(lu.total_tokens, 0) ELSE 0 END::bigint AS total_tokens,
    CASE WHEN l.resource_type = 'issue' THEN COALESCE(lu.total_cost_usd_ticks, 0) ELSE 0 END::bigint AS total_cost_usd_ticks,
    CASE WHEN l.resource_type = 'issue' THEN COALESCE(lu.uncosted_tokens, 0) ELSE 0 END::bigint AS uncosted_tokens,
    CASE WHEN l.resource_type = 'issue' THEN COALESCE(lu.task_count, 0) ELSE 0 END::bigint AS task_count,
    CASE WHEN l.resource_type = 'issue' THEN COALESCE(lu.priced_task_count, 0) ELSE 0 END::bigint AS priced_task_count,
    CASE WHEN l.resource_type = 'issue' THEN COALESCE(lu.unpriced_task_count, 0) ELSE 0 END::bigint AS unpriced_task_count
FROM issue_label l
LEFT JOIN issue_label_usage lu ON lu.label_id = l.id
WHERE l.workspace_id = sqlc.arg('workspace_id')::uuid
  AND l.resource_type = sqlc.arg('resource_type')::text
ORDER BY LOWER(name) ASC;

-- name: GetIssueLabelUsageSummary :one
WITH task_totals AS (
    SELECT
        atq.id AS task_id,
        COALESCE(MAX(tu.created_at), atq.completed_at, atq.created_at) AS activity_at,
        COUNT(tu.id)::bigint AS usage_row_count,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ), 0)::bigint AS total_tokens,
        COALESCE(SUM(tu.cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ) FILTER (WHERE tu.id IS NOT NULL AND tu.cost_usd_ticks IS NULL), 0)::bigint AS uncosted_tokens,
        COALESCE(
            BOOL_AND(tu.cost_usd_ticks IS NOT NULL) FILTER (WHERE tu.id IS NOT NULL),
            FALSE
        ) AS is_priced
    FROM issue_label l
    JOIN issue_to_label il ON il.label_id = l.id
    JOIN issue i ON i.id = il.issue_id AND i.workspace_id = l.workspace_id
    JOIN agent_task_queue atq ON atq.issue_id = i.id
    LEFT JOIN task_usage tu ON tu.task_id = atq.id
    WHERE l.id = sqlc.arg('label_id')::uuid
      AND l.workspace_id = sqlc.arg('workspace_id')::uuid
      AND l.resource_type = 'issue'
    GROUP BY atq.id, atq.completed_at, atq.created_at
), filtered_tasks AS (
    SELECT *
    FROM task_totals
    WHERE sqlc.narg('since')::timestamptz IS NULL
       OR activity_at >= sqlc.narg('since')::timestamptz
)
SELECT
    COALESCE(SUM(total_tokens), 0)::bigint AS total_tokens,
    COALESCE(SUM(total_cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
    COALESCE(SUM(uncosted_tokens), 0)::bigint AS uncosted_tokens,
    COUNT(*)::bigint AS task_count,
    COUNT(*) FILTER (WHERE is_priced)::bigint AS priced_task_count,
    COUNT(*) FILTER (WHERE NOT is_priced)::bigint AS unpriced_task_count
FROM filtered_tasks;

-- name: ListIssueLabelUsageDaily :many
WITH task_totals AS (
    SELECT
        atq.id AS task_id,
        COALESCE(MAX(tu.created_at), atq.completed_at, atq.created_at) AS activity_at,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ), 0)::bigint AS total_tokens,
        COALESCE(SUM(tu.cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ) FILTER (WHERE tu.id IS NOT NULL AND tu.cost_usd_ticks IS NULL), 0)::bigint AS uncosted_tokens,
        COALESCE(
            BOOL_AND(tu.cost_usd_ticks IS NOT NULL) FILTER (WHERE tu.id IS NOT NULL),
            FALSE
        ) AS is_priced
    FROM issue_label l
    JOIN issue_to_label il ON il.label_id = l.id
    JOIN issue i ON i.id = il.issue_id AND i.workspace_id = l.workspace_id
    JOIN agent_task_queue atq ON atq.issue_id = i.id
    LEFT JOIN task_usage tu ON tu.task_id = atq.id
    WHERE l.id = sqlc.arg('label_id')::uuid
      AND l.workspace_id = sqlc.arg('workspace_id')::uuid
      AND l.resource_type = 'issue'
    GROUP BY atq.id, atq.completed_at, atq.created_at
), filtered_tasks AS (
    SELECT *
    FROM task_totals
    WHERE sqlc.narg('since')::timestamptz IS NULL
       OR activity_at >= sqlc.narg('since')::timestamptz
)
SELECT
    DATE(activity_at AT TIME ZONE sqlc.arg('tz')::text) AS date,
    SUM(total_tokens)::bigint AS total_tokens,
    SUM(total_cost_usd_ticks)::bigint AS total_cost_usd_ticks,
    SUM(uncosted_tokens)::bigint AS uncosted_tokens,
    COUNT(*)::bigint AS task_count,
    COUNT(*) FILTER (WHERE is_priced)::bigint AS priced_task_count,
    COUNT(*) FILTER (WHERE NOT is_priced)::bigint AS unpriced_task_count
FROM filtered_tasks
GROUP BY DATE(activity_at AT TIME ZONE sqlc.arg('tz')::text)
ORDER BY DATE(activity_at AT TIME ZONE sqlc.arg('tz')::text) ASC;

-- name: ListIssueLabelUsageBreakdown :many
WITH label_tasks AS (
    SELECT
        atq.id AS task_id,
        COALESCE(MAX(tu.created_at), atq.completed_at, atq.created_at) AS activity_at
    FROM issue_label l
    JOIN issue_to_label il ON il.label_id = l.id
    JOIN issue i ON i.id = il.issue_id AND i.workspace_id = l.workspace_id
    JOIN agent_task_queue atq ON atq.issue_id = i.id
    LEFT JOIN task_usage tu ON tu.task_id = atq.id
    WHERE l.id = sqlc.arg('label_id')::uuid
      AND l.workspace_id = sqlc.arg('workspace_id')::uuid
      AND l.resource_type = 'issue'
    GROUP BY atq.id, atq.completed_at, atq.created_at
)
SELECT
    LOWER(tu.provider) AS provider,
    tu.model,
    SUM(
        tu.input_tokens + tu.output_tokens +
        tu.cache_read_tokens + tu.cache_write_tokens
    )::bigint AS total_tokens,
    COALESCE(SUM(tu.cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
    COALESCE(SUM(
        tu.input_tokens + tu.output_tokens +
        tu.cache_read_tokens + tu.cache_write_tokens
    ) FILTER (WHERE tu.cost_usd_ticks IS NULL), 0)::bigint AS uncosted_tokens,
    COUNT(DISTINCT lt.task_id)::bigint AS task_count,
    COUNT(DISTINCT lt.task_id) FILTER (WHERE tu.cost_usd_ticks IS NULL)::bigint AS unpriced_task_count
FROM label_tasks lt
JOIN task_usage tu ON tu.task_id = lt.task_id
WHERE sqlc.narg('since')::timestamptz IS NULL
   OR lt.activity_at >= sqlc.narg('since')::timestamptz
GROUP BY LOWER(tu.provider), tu.model
ORDER BY total_cost_usd_ticks DESC, total_tokens DESC, LOWER(tu.provider), tu.model;

-- name: ListIssueLabelUsageTasks :many
WITH task_totals AS (
    SELECT
        atq.id AS task_id,
        atq.agent_id,
        COALESCE(a.name, '')::text AS agent_name,
        i.id AS issue_id,
        CASE
            WHEN w.issue_prefix = '' THEN '#' || i.number::text
            ELSE w.issue_prefix || '-' || i.number::text
        END::text AS issue_identifier,
        i.title AS issue_title,
        atq.status,
        atq.created_at,
        atq.completed_at,
        atq.originator_user_id,
        atq.accountable_user_id,
        atq.originator_source,
        atq.delegated_from_task_id,
        atq.retry_of_task_id,
        atq.rerun_of_task_id,
        atq.rule_version_id,
        atq.trigger_evidence_kind,
        atq.trigger_evidence_ref_id,
        COALESCE(MAX(tu.created_at), atq.completed_at, atq.created_at) AS activity_at,
        CASE WHEN COUNT(tu.id) = 1 THEN MIN(LOWER(tu.provider)) ELSE '' END::text AS provider,
        CASE WHEN COUNT(tu.id) = 1 THEN MIN(tu.model) ELSE '' END::text AS model,
        COUNT(tu.id) > 0 AS has_usage,
        COALESCE(
            BOOL_AND(tu.cost_usd_ticks IS NOT NULL) FILTER (WHERE tu.id IS NOT NULL),
            FALSE
        )::boolean AS is_priced,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ), 0)::bigint AS total_tokens,
        COALESCE(SUM(tu.cost_usd_ticks), 0)::bigint AS total_cost_usd_ticks,
        COALESCE(SUM(
            tu.input_tokens + tu.output_tokens +
            tu.cache_read_tokens + tu.cache_write_tokens
        ) FILTER (WHERE tu.id IS NOT NULL AND tu.cost_usd_ticks IS NULL), 0)::bigint AS uncosted_tokens,
        COALESCE(
            JSONB_AGG(
                JSONB_BUILD_OBJECT(
                    'provider', LOWER(tu.provider),
                    'model', tu.model,
                    'total_tokens', tu.input_tokens + tu.output_tokens + tu.cache_read_tokens + tu.cache_write_tokens,
                    'total_cost_usd_ticks', COALESCE(tu.cost_usd_ticks, 0),
                    'uncosted_tokens', CASE WHEN tu.cost_usd_ticks IS NULL
                        THEN tu.input_tokens + tu.output_tokens + tu.cache_read_tokens + tu.cache_write_tokens
                        ELSE 0
                    END,
                    'is_priced', tu.cost_usd_ticks IS NOT NULL
                ) ORDER BY LOWER(tu.provider), tu.model
            ) FILTER (WHERE tu.id IS NOT NULL),
            '[]'::jsonb
        )::jsonb AS usage_breakdown
    FROM issue_label l
    JOIN issue_to_label il ON il.label_id = l.id
    JOIN issue i ON i.id = il.issue_id AND i.workspace_id = l.workspace_id
    JOIN workspace w ON w.id = i.workspace_id
    JOIN agent_task_queue atq ON atq.issue_id = i.id
    LEFT JOIN agent a ON a.id = atq.agent_id AND a.workspace_id = i.workspace_id
    LEFT JOIN task_usage tu ON tu.task_id = atq.id
    WHERE l.id = sqlc.arg('label_id')::uuid
      AND l.workspace_id = sqlc.arg('workspace_id')::uuid
      AND l.resource_type = 'issue'
    GROUP BY
        atq.id, atq.agent_id, a.name, i.id, i.number, i.title, w.issue_prefix,
        atq.status, atq.created_at, atq.completed_at,
        atq.originator_user_id, atq.accountable_user_id,
        atq.originator_source, atq.delegated_from_task_id,
        atq.retry_of_task_id, atq.rerun_of_task_id, atq.rule_version_id,
        atq.trigger_evidence_kind, atq.trigger_evidence_ref_id
), filtered_tasks AS (
    SELECT *
    FROM task_totals
    WHERE sqlc.narg('since')::timestamptz IS NULL
       OR activity_at >= sqlc.narg('since')::timestamptz
)
SELECT *
FROM filtered_tasks
ORDER BY
    CASE WHEN sqlc.arg('sort')::text = 'cost' THEN is_priced END DESC,
    CASE WHEN sqlc.arg('sort')::text = 'cost'   AND sqlc.arg('direction')::text = 'asc'  THEN total_cost_usd_ticks END ASC,
    CASE WHEN sqlc.arg('sort')::text = 'cost'   AND sqlc.arg('direction')::text = 'desc' THEN total_cost_usd_ticks END DESC,
    CASE WHEN sqlc.arg('sort')::text = 'tokens' AND sqlc.arg('direction')::text = 'asc'  THEN total_tokens END ASC,
    CASE WHEN sqlc.arg('sort')::text = 'tokens' AND sqlc.arg('direction')::text = 'desc' THEN total_tokens END DESC,
    CASE WHEN sqlc.arg('sort')::text = 'recent' AND sqlc.arg('direction')::text = 'asc'  THEN activity_at END ASC,
    CASE WHEN sqlc.arg('sort')::text = 'recent' AND sqlc.arg('direction')::text = 'desc' THEN activity_at END DESC,
    activity_at DESC,
    task_id ASC
LIMIT sqlc.arg('page_size')::int
OFFSET sqlc.arg('offset')::int;

-- name: GetLabel :one
SELECT * FROM issue_label
WHERE id = $1 AND workspace_id = $2;

-- name: CreateLabel :one
INSERT INTO issue_label (workspace_id, resource_type, name, description, color)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpsertIssueDelegationLabel :one
-- Delegation labels are server-managed and keyed by a stable, shortened Chat
-- session hash. Refreshing the description is safe because the exact session
-- id is deterministic for a given label name.
INSERT INTO issue_label (workspace_id, resource_type, name, description, color)
VALUES ($1, 'issue', $2, $3, $4)
ON CONFLICT (workspace_id, resource_type, (LOWER(name)))
DO UPDATE SET
    description = EXCLUDED.description,
    updated_at = now()
RETURNING *;

-- name: UpdateLabel :one
UPDATE issue_label SET
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    color = COALESCE(sqlc.narg('color'), color),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
  AND NOT EXISTS (
      SELECT 1 FROM agent_okr
      WHERE agent_okr.label_id = issue_label.id
        AND agent_okr.workspace_id = issue_label.workspace_id
  )
RETURNING *;

-- name: DeleteLabel :one
-- :one RETURNING id lets the handler distinguish missing rows from
-- infrastructure errors; it rechecks the OKR reference on ErrNoRows so a
-- concurrent reference becomes 409 rather than a misleading 404.
DELETE FROM issue_label
WHERE id = $1 AND workspace_id = $2
  AND NOT EXISTS (
      SELECT 1 FROM agent_okr
      WHERE agent_okr.label_id = issue_label.id
        AND agent_okr.workspace_id = issue_label.workspace_id
  )
RETURNING id;

-- The resource-label junctions deliberately have no foreign keys. Keeping
-- their cleanup in the same application transaction as the owner deletion
-- avoids database-level cascades with unreviewed locking and audit behavior.

-- name: DeleteIssueLabelAssignmentsByLabel :exec
DELETE FROM issue_to_label WHERE label_id = $1;

-- name: DeleteAgentLabelAssignmentsByLabel :exec
DELETE FROM agent_to_label WHERE label_id = $1;

-- name: DeleteSkillLabelAssignmentsByLabel :exec
DELETE FROM skill_to_label WHERE label_id = $1;

-- name: DeleteAgentLabelAssignmentsByAgent :exec
DELETE FROM agent_to_label WHERE agent_id = $1;

-- name: DeleteSkillLabelAssignmentsBySkill :exec
DELETE FROM skill_to_label WHERE skill_id = $1;

-- The single-entity cleanups above cover one agent/skill at a time. The runtime
-- variant below covers runtime and runtime-profile bulk hard deletes, where the
-- owning agents disappear without passing through a per-entity delete.
-- Workspace-wide cleanup lives in DeleteWorkspace so it is atomic with that
-- workspace's existing multi-table teardown.

-- name: DeleteAgentLabelAssignmentsBySystemRuntimeAgents :exec
-- Runtime teardown hard-deletes the system agents bound to the runtime (user
-- agents are unbound and kept since MUL-5559). Clear only those agents' label
-- links so none survive the agent hard-delete — a surviving unbound agent must
-- keep its labels.
DELETE FROM agent_to_label
WHERE agent_id IN (SELECT id FROM agent WHERE runtime_id = $1 AND kind = 'system');

-- name: AttachLabelToIssue :exec
-- Workspace-guarded INSERT: the WHERE EXISTS clauses ensure both the issue
-- and the label belong to the given workspace. A future caller that forgets
-- handler-level prechecks still cannot attach labels across workspaces.
INSERT INTO issue_to_label (issue_id, label_id)
SELECT sqlc.arg('issue_id')::uuid, sqlc.arg('label_id')::uuid
WHERE EXISTS (
    SELECT 1 FROM issue i
    WHERE i.id = sqlc.arg('issue_id')::uuid
      AND i.workspace_id = sqlc.arg('workspace_id')::uuid
)
AND EXISTS (
    SELECT 1 FROM issue_label l
    WHERE l.id = sqlc.arg('label_id')::uuid
      AND l.workspace_id = sqlc.arg('workspace_id')::uuid
      AND l.resource_type = 'issue'
)
ON CONFLICT DO NOTHING;

-- name: DetachLabelFromIssue :exec
-- Workspace-guarded DELETE: only deletes if the issue is in the given
-- workspace. Mirror of the attach query.
DELETE FROM issue_to_label
WHERE issue_id = sqlc.arg('issue_id')::uuid
  AND label_id = sqlc.arg('label_id')::uuid
  AND EXISTS (
      SELECT 1 FROM issue i
      WHERE i.id = sqlc.arg('issue_id')::uuid
        AND i.workspace_id = sqlc.arg('workspace_id')::uuid
  );

-- name: ListLabelsByIssue :many
-- Workspace filter at the SQL layer (mirrors GetProjectInWorkspace). Any caller
-- that passes the wrong workspace gets an empty list rather than leaking labels.
SELECT l.*
FROM issue_label l
JOIN issue_to_label il ON il.label_id = l.id
WHERE il.issue_id = sqlc.arg('issue_id')::uuid
  AND l.workspace_id = sqlc.arg('workspace_id')::uuid
  AND l.resource_type = 'issue'
ORDER BY LOWER(l.name) ASC;

-- name: ListLabelsForIssues :many
-- Bulk variant: fetch labels for many issues in one round-trip so the issue
-- list endpoints can fold labels into each row without N+1 queries from the
-- client. Workspace-guarded the same way as ListLabelsByIssue.
SELECT il.issue_id, l.*
FROM issue_label l
JOIN issue_to_label il ON il.label_id = l.id
WHERE il.issue_id = ANY(sqlc.arg('issue_ids')::uuid[])
  AND l.workspace_id = sqlc.arg('workspace_id')::uuid
  AND l.resource_type = 'issue'
ORDER BY il.issue_id, LOWER(l.name) ASC;

-- name: ListLabelsByAgent :many
SELECT l.*
FROM issue_label l
JOIN agent_to_label atl ON atl.label_id = l.id
WHERE atl.agent_id = sqlc.arg('agent_id')::uuid
  AND l.workspace_id = sqlc.arg('workspace_id')::uuid
  AND l.resource_type = 'agent'
ORDER BY LOWER(l.name) ASC;

-- name: ListLabelsForAgents :many
SELECT atl.agent_id, l.*
FROM issue_label l
JOIN agent_to_label atl ON atl.label_id = l.id
WHERE atl.agent_id = ANY(sqlc.arg('agent_ids')::uuid[])
  AND l.workspace_id = sqlc.arg('workspace_id')::uuid
  AND l.resource_type = 'agent'
ORDER BY atl.agent_id, LOWER(l.name) ASC;

-- name: AttachLabelToAgent :exec
INSERT INTO agent_to_label (agent_id, label_id)
SELECT sqlc.arg('agent_id')::uuid, sqlc.arg('label_id')::uuid
WHERE EXISTS (
    SELECT 1 FROM agent a
    WHERE a.id = sqlc.arg('agent_id')::uuid
      AND a.workspace_id = sqlc.arg('workspace_id')::uuid
)
AND EXISTS (
    SELECT 1 FROM issue_label l
    WHERE l.id = sqlc.arg('label_id')::uuid
      AND l.workspace_id = sqlc.arg('workspace_id')::uuid
      AND l.resource_type = 'agent'
)
ON CONFLICT DO NOTHING;

-- name: DetachLabelFromAgent :exec
DELETE FROM agent_to_label
WHERE agent_id = sqlc.arg('agent_id')::uuid
  AND label_id = sqlc.arg('label_id')::uuid
  AND EXISTS (
      SELECT 1 FROM agent a
      WHERE a.id = sqlc.arg('agent_id')::uuid
        AND a.workspace_id = sqlc.arg('workspace_id')::uuid
  );

-- name: ListLabelsBySkill :many
SELECT l.*
FROM issue_label l
JOIN skill_to_label stl ON stl.label_id = l.id
WHERE stl.skill_id = sqlc.arg('skill_id')::uuid
  AND l.workspace_id = sqlc.arg('workspace_id')::uuid
  AND l.resource_type = 'skill'
ORDER BY LOWER(l.name) ASC;

-- name: ListLabelsForSkills :many
SELECT stl.skill_id, l.*
FROM issue_label l
JOIN skill_to_label stl ON stl.label_id = l.id
WHERE stl.skill_id = ANY(sqlc.arg('skill_ids')::uuid[])
  AND l.workspace_id = sqlc.arg('workspace_id')::uuid
  AND l.resource_type = 'skill'
ORDER BY stl.skill_id, LOWER(l.name) ASC;

-- name: AttachLabelToSkill :exec
INSERT INTO skill_to_label (skill_id, label_id)
SELECT sqlc.arg('skill_id')::uuid, sqlc.arg('label_id')::uuid
WHERE EXISTS (
    SELECT 1 FROM skill s
    WHERE s.id = sqlc.arg('skill_id')::uuid
      AND s.workspace_id = sqlc.arg('workspace_id')::uuid
)
AND EXISTS (
    SELECT 1 FROM issue_label l
    WHERE l.id = sqlc.arg('label_id')::uuid
      AND l.workspace_id = sqlc.arg('workspace_id')::uuid
      AND l.resource_type = 'skill'
)
ON CONFLICT DO NOTHING;

-- name: DetachLabelFromSkill :exec
DELETE FROM skill_to_label
WHERE skill_id = sqlc.arg('skill_id')::uuid
  AND label_id = sqlc.arg('label_id')::uuid
  AND EXISTS (
      SELECT 1 FROM skill s
      WHERE s.id = sqlc.arg('skill_id')::uuid
        AND s.workspace_id = sqlc.arg('workspace_id')::uuid
  );
