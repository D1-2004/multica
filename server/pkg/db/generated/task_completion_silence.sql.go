package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

const enqueueSynchronousSilenceTaskCompletion = `-- name: EnqueueSynchronousSilenceTaskCompletion :one
INSERT INTO task_completion_outbox AS existing (
    root_task_id,
    terminal_task_id,
    callback_url,
    target_identity,
    request_id,
    agent_id,
    execution_status,
    result_message,
    error,
    failure_reason,
    execution_summary
) VALUES (
    NULL, NULL, $1, $2, $3, $4, 'completed', '', NULL, NULL, $5
)
ON CONFLICT (request_id) DO UPDATE
SET updated_at = existing.updated_at
WHERE existing.root_task_id IS NULL
  AND existing.terminal_task_id IS NULL
  AND existing.callback_url = EXCLUDED.callback_url
  AND existing.target_identity = EXCLUDED.target_identity
  AND existing.agent_id = EXCLUDED.agent_id
  AND existing.execution_status = EXCLUDED.execution_status
  AND existing.result_message = EXCLUDED.result_message
  AND existing.error IS NOT DISTINCT FROM EXCLUDED.error
  AND existing.failure_reason IS NOT DISTINCT FROM EXCLUDED.failure_reason
RETURNING id, root_task_id, terminal_task_id, callback_url, target_identity, request_id, agent_id, external_session_id, execution_status, result_message, error, failure_reason, status, available_at, attempt_count, lease_token, lease_expires_at, last_error, delivered_at, created_at, updated_at, execution_summary`

type EnqueueSynchronousSilenceTaskCompletionParams struct {
	CallbackUrl      string      `json:"callback_url"`
	TargetIdentity   string      `json:"target_identity"`
	RequestID        string      `json:"request_id"`
	AgentID          pgtype.UUID `json:"agent_id"`
	ExecutionSummary []byte      `json:"execution_summary"`
}

func (q *Queries) EnqueueSynchronousSilenceTaskCompletion(ctx context.Context, arg EnqueueSynchronousSilenceTaskCompletionParams) (TaskCompletionOutbox, error) {
	row := q.db.QueryRow(ctx, enqueueSynchronousSilenceTaskCompletion,
		arg.CallbackUrl,
		arg.TargetIdentity,
		arg.RequestID,
		arg.AgentID,
		arg.ExecutionSummary,
	)
	var i TaskCompletionOutbox
	err := row.Scan(
		&i.ID,
		&i.RootTaskID,
		&i.TerminalTaskID,
		&i.CallbackUrl,
		&i.TargetIdentity,
		&i.RequestID,
		&i.AgentID,
		&i.ExternalSessionID,
		&i.ExecutionStatus,
		&i.ResultMessage,
		&i.Error,
		&i.FailureReason,
		&i.Status,
		&i.AvailableAt,
		&i.AttemptCount,
		&i.LeaseToken,
		&i.LeaseExpiresAt,
		&i.LastError,
		&i.DeliveredAt,
		&i.CreatedAt,
		&i.UpdatedAt,
		&i.ExecutionSummary,
	)
	return i, err
}
