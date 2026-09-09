-- name: GetLatestCoordinatorIssueExecution :one
-- Only execution metadata for the currently owning Agent; no result/context/error payloads.
SELECT task.id, task.status, task.created_at, task.started_at, task.completed_at
FROM agent_task_queue AS task
JOIN issue AS current_issue ON current_issue.id = task.issue_id
JOIN agent AS current_agent ON current_agent.id = task.agent_id
WHERE current_issue.workspace_id = sqlc.arg('workspace_id')
  AND current_agent.workspace_id = sqlc.arg('workspace_id')
  AND current_issue.id = sqlc.arg('issue_id')
  AND task.agent_id = sqlc.arg('agent_id')
  AND current_issue.assignee_type = 'agent'
  AND current_issue.assignee_id = sqlc.arg('agent_id')
ORDER BY task.created_at DESC, task.id DESC
LIMIT 1;
