-- name: CancelAgentTaskForSteer :one
-- Steer opts into an exit-proof barrier; ordinary cancellation keeps its
-- existing compatibility contract with installed runtimes.
UPDATE agent_task_queue
SET status = 'cancelled', completed_at = now(), prepare_lease_expires_at = NULL,
    context = CASE WHEN status IN ('dispatched','running','waiting_local_directory')
      THEN COALESCE(context,'{}'::jsonb) || '{"process_stop_pending":true}'::jsonb
      ELSE context END
WHERE id=$1 AND status IN ('queued','deferred','dispatched','running','waiting_local_directory')
RETURNING *;

-- name: AckAgentTaskProcessStopped :one
-- Only the owning daemon's positive process-group acknowledgement may open
-- this barrier. Logical terminal state and elapsed time prove nothing.
UPDATE agent_task_queue
SET context = (context - 'process_stop_pending') || jsonb_build_object('process_stopped_at', now())
WHERE id = $1 AND status = 'cancelled' AND context->>'process_stop_pending' = 'true'
RETURNING *;

-- name: HasUnacknowledgedTaskCancellation :one
SELECT EXISTS (
    SELECT 1 FROM agent_task_queue active
    WHERE active.agent_id = @agent_id AND active.status = 'cancelled'
      AND active.context->>'process_stop_pending' = 'true'
      AND ((sqlc.narg(issue_id)::uuid IS NOT NULL AND active.issue_id = sqlc.narg(issue_id)::uuid)
           OR (sqlc.narg(chat_session_id)::uuid IS NOT NULL AND active.chat_session_id = sqlc.narg(chat_session_id)::uuid))
);

-- name: SetSteerSuccessorContext :exec
UPDATE agent_task_queue
SET context = COALESCE(context, '{}'::jsonb) || COALESCE(@correction_context::jsonb, '{}'::jsonb) || '{"task_steer":true}'::jsonb,
    priority = 4
WHERE id = @id AND status = 'queued';

-- name: GetSteerChatSuccessor :one
SELECT * FROM agent_task_queue
WHERE chat_session_id = @chat_session_id AND agent_id = @agent_id
  AND id <> @exclude_task_id AND status = 'queued' AND context->>'task_steer' = 'true'
ORDER BY created_at, id LIMIT 1;

-- name: MergeSteerChatInput :exec
UPDATE chat_message SET task_id = @successor_id
WHERE task_id = @correction_task_id AND role = 'user';

-- name: MergeSteerIssueComment :one
-- A pending steer keeps all correction inputs, and the latest human owns the
-- complete execution identity snapshot. Other queued tasks are not selected.
UPDATE agent_task_queue
SET coalesced_comment_ids = (
      SELECT COALESCE(array_agg(DISTINCT e), '{}')
      FROM unnest(array_append(coalesced_comment_ids, trigger_comment_id)) AS e
      WHERE e IS NOT NULL AND e <> @comment_id::uuid),
    trigger_comment_id = @comment_id, force_fresh_session = false,
    trigger_summary = @summary,
    originator_user_id = @author_id, accountable_user_id = @author_id,
    originator_source = 'direct_human', delegated_from_task_id = NULL,
    rule_version_id = NULL, trigger_evidence_kind = 'comment', trigger_evidence_ref_id = @comment_id,
    runtime_mcp_overlay = sqlc.narg(runtime_mcp_overlay),
    runtime_connected_apps = sqlc.narg(runtime_connected_apps),
    context = COALESCE(context, '{}'::jsonb) || @correction_context::jsonb
WHERE issue_id = @issue_id AND agent_id = @agent_id AND status = 'queued'
RETURNING *;

-- name: GetClaimedIssueTaskForSteer :one
SELECT * FROM agent_task_queue
WHERE issue_id = @issue_id AND agent_id = @agent_id
  AND status IN ('dispatched', 'running', 'waiting_local_directory')
ORDER BY created_at DESC LIMIT 1;

-- name: DetachSteerCorrectionCallback :exec
-- This row is an input folded into another run, not a canceled execution.
UPDATE agent_task_queue SET context = context - 'completion_callback'
WHERE id=$1 AND status IN ('queued','deferred');

-- name: EnqueueSteerCallbackCompletion :exec
-- Merged accepted dispatches share an execution but retain their own callback.
-- Like synchronous dispatch receipts, they have no second physical root run.
INSERT INTO task_completion_outbox AS existing (
    callback_url,target_identity,request_id,agent_id,external_session_id,
    execution_status,result_message,execution_summary,error,failure_reason
) VALUES (@callback_url,@target_identity,@request_id,@agent_id,sqlc.narg(session_id),
    @execution_status,@result_message,@execution_summary,sqlc.narg(error),sqlc.narg(failure_reason))
ON CONFLICT (request_id) DO NOTHING;

-- name: GetSteerIssueReceiptTaskID :one
SELECT id FROM agent_task_queue
WHERE issue_id = @issue_id AND agent_id = @agent_id
  AND context->'steer_requests' ? @request_key::text
ORDER BY created_at DESC LIMIT 1;
