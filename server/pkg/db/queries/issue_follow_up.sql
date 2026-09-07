-- External issue follow-ups use a transaction-scoped lock to serialize the
-- same accepted operation across replicas. The task context is the receipt.
-- name: LockExternalIssueFollowUp :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(lock_key)::text, 0));

-- name: GetExternalIssueFollowUpTaskID :one
SELECT task.id
FROM agent_task_queue task
JOIN issue ON issue.id = task.issue_id
WHERE issue.workspace_id = sqlc.arg(workspace_id)
  AND task.agent_id = sqlc.arg(agent_id)
  AND task.context ->> 'issue_follow_up_idempotency_key' = sqlc.arg(idempotency_key)::text
ORDER BY task.created_at, task.id
LIMIT 1;

-- Different operations on one issue also serialize the active-task check
-- with the comment/task write. All related writes remain in this transaction.
-- name: LockIssueForExternalFollowUp :one
SELECT id FROM issue
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
FOR UPDATE;
