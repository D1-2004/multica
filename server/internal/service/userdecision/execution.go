package userdecision

import "context"

// refreshExecution retains the committed materialization and the actual task
// states. A queued follow-up without a task is still pending, never completed.
func (s *Service) refreshExecution(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `WITH materialized AS (
 SELECT d.id,j.status AS job_status,j.command->'_coordinator_plan' AS plan,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('task_id',t.id,'issue_id',t.issue_id,'state',t.status,'started_at',t.started_at,'completed_at',t.completed_at,'failure_reason',t.failure_reason)) FROM agent_task_queue t WHERE t.agent_id=d.agent_id AND EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(j.command#>'{_coordinator_plan,IssueResults}','[]'::jsonb)) item WHERE t.id::text=item->>'task_id' OR (item->>'comment_id'<>'' AND t.trigger_comment_id::text=item->>'comment_id'))),'[]'::jsonb) AS tasks
 FROM coordinator_user_decision d JOIN inbound_coordinator_job j ON j.id=d.job_id WHERE d.environment=$1 AND d.state='dispatched'
 ), results AS (
 SELECT id,jsonb_build_object('coordinator',plan,'job_state',job_status,'tasks',tasks,'state',CASE
 WHEN job_status='failed' THEN 'failed'
 WHEN job_status<>'completed' THEN 'pending'
 WHEN plan->>'Action'='reply' THEN 'completed'
 WHEN jsonb_array_length(tasks)=0 THEN 'pending'
 WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(tasks) t WHERE t->>'state' NOT IN ('completed','failed','cancelled')) THEN 'running'
 WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(tasks) t WHERE t->>'state' IN ('failed','cancelled')) THEN 'failed'
 ELSE 'completed' END) AS result FROM materialized)
 UPDATE coordinator_user_decision d SET execution_result=results.result,card_update_pending=true,available_at=now(),updated_at=now() FROM results WHERE d.id=results.id AND d.execution_result IS DISTINCT FROM results.result`, s.Store.Environment)
	return err
}
