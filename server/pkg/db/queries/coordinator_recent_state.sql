-- name: ListRecentCoordinatorState :many
-- Read only the Host-anchored preceding windows, never another endpoint or scene.
WITH anchor AS (
    SELECT current_job.id, current_job.created_at, current_job.endpoint_namespace_id, current_job.command
    FROM inbound_coordinator_job current_job
    WHERE current_job.id = @anchor_job_id AND current_job.workspace_id = @workspace_id AND current_job.agent_id = @agent_id
      AND NULLIF(BTRIM(current_job.command #>> '{event,data,conversation,openConversationId}'), '') IS NOT NULL
)
SELECT anchor.id AS anchor_id,
    BTRIM(anchor.command #>> '{event,data,conversation,openConversationId}')::text AS anchor_conversation_id,
    job.id AS job_id, job.created_at, job.updated_at,
    COALESCE(job.status, '')::text AS job_status,
    LEFT(COALESCE(job.command #>> '{event,data,messages,0,text}', ''), 120)::text AS request_excerpt,
    COALESCE(LENGTH(job.command #>> '{event,data,messages,0,text}') > 120, false)::boolean AS request_truncated,
    COALESCE(job.command ? '_coordinator_plan', false)::boolean AS plan_present,
    COALESCE(job.command #>> '{_coordinator_plan,PlanVersion}', '')::text AS plan_version,
    COALESCE(job.command #>> '{_coordinator_plan,Action}', '')::text AS plan_action,
    CASE WHEN jsonb_typeof(job.command #> '{_coordinator_plan,Items}') = 'array'
      THEN jsonb_array_length(job.command #> '{_coordinator_plan,Items}') ELSE -1 END::integer AS planned_work_count,
    CASE WHEN jsonb_typeof(job.command #> '{_coordinator_plan,Items}') = 'array'
      THEN (SELECT COALESCE(jsonb_agg(item -> 'action_key'), '[]'::jsonb)
            FROM jsonb_array_elements(job.command #> '{_coordinator_plan,Items}') AS item)
      ELSE NULL::jsonb END AS plan_item_keys,
    CASE WHEN jsonb_typeof(job.command #> '{_coordinator_plan,CompletedActionKeys}') = 'array'
      THEN job.command #> '{_coordinator_plan,CompletedActionKeys}' ELSE NULL::jsonb END AS completed_action_keys,
    CASE WHEN jsonb_typeof(job.command #> '{_coordinator_plan,IssueResults}') = 'array'
      THEN (SELECT COALESCE(jsonb_agg(jsonb_build_object(
             'action', receipt -> 'action', 'issue_id', receipt -> 'issue_id',
             'task_id', receipt -> 'task_id', 'comment_id', receipt -> 'comment_id')), '[]'::jsonb)
            FROM jsonb_array_elements(job.command #> '{_coordinator_plan,IssueResults}') AS receipt)
      ELSE NULL::jsonb END AS issue_results
FROM anchor
LEFT JOIN LATERAL (
    SELECT prior.id, prior.created_at, prior.updated_at, prior.status, prior.command
    FROM inbound_coordinator_job prior
    WHERE prior.workspace_id = @workspace_id AND prior.agent_id = @agent_id
      AND prior.endpoint_namespace_id = anchor.endpoint_namespace_id
      AND (prior.command #>> '{source,platform}') IS NOT DISTINCT FROM (anchor.command #>> '{source,platform}')
      AND (prior.command #>> '{source,type}') IS NOT DISTINCT FROM (anchor.command #>> '{source,type}')
      AND NULLIF(BTRIM(prior.command #>> '{event,data,conversation,openConversationId}'), '')
        = BTRIM(anchor.command #>> '{event,data,conversation,openConversationId}')
      AND prior.id <> anchor.id AND prior.created_at < anchor.created_at
    ORDER BY prior.created_at DESC, prior.id DESC
    LIMIT 3
) job ON true
ORDER BY job.created_at DESC, job.id DESC;
