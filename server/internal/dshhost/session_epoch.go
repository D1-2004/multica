package dshhost

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// NativeScope resolves the exact historical conversation, including its reset
// epoch. Native browsing and scheduled work must never jump to the latest epoch.
func (s PostgresStore) NativeScope(ctx context.Context, key Key, sessionID string) (SessionScope, error) {
	scope := SessionScope{Key: key}
	err := s.DB.QueryRow(ctx, `SELECT scope_kind,scope_id,epoch_id FROM dsh_employee_session
 WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3`, key.WorkspaceID, key.AgentID, sessionID).Scan(&scope.Kind, &scope.ID, &scope.Epoch)
	return scope, err
}

// TaskScope preserves an admitted task's exact epoch on retry. New platform
// turns inherit the most recent reset at their immutable queue position, so a
// delayed old task cannot reset the active conversation backwards.
func (s PostgresStore) TaskScope(ctx context.Context, scope SessionScope, taskID uuid.UUID) (SessionScope, error) {
	var bound SessionScope
	bound.Key = scope.Key
	err := s.DB.QueryRow(ctx, `SELECT s.scope_kind,s.scope_id,s.epoch_id FROM dsh_task_binding b
 JOIN dsh_employee_session s USING(workspace_id,agent_id,session_id)
 WHERE b.workspace_id=$1 AND b.agent_id=$2 AND b.task_id=$3`, scope.WorkspaceID, scope.AgentID, taskID).Scan(&bound.Kind, &bound.ID, &bound.Epoch)
	if err == nil {
		if bound.Kind != scope.Kind || bound.ID != scope.ID {
			return SessionScope{}, ErrChanged
		}
		return bound, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SessionScope{}, err
	}
	if scope.Kind == "task" {
		return scope, nil
	}
	err = s.DB.QueryRow(ctx, `SELECT reset.id FROM agent_task_queue current_task
 JOIN agent a ON a.id=current_task.agent_id AND a.workspace_id=$1
 JOIN agent_task_queue reset ON reset.agent_id=current_task.agent_id
 AND reset.force_fresh_session=TRUE AND (reset.created_at,reset.id)<=(current_task.created_at,current_task.id)
 WHERE current_task.agent_id=$2 AND current_task.id=$3
 AND (($4='chat' AND reset.chat_session_id=$5 AND current_task.chat_session_id=$5)
 OR ($4='issue' AND reset.issue_id=$5 AND current_task.issue_id=$5))
 ORDER BY reset.created_at DESC,reset.id DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, taskID, scope.Kind, scope.ID).Scan(&scope.Epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return scope, nil
	}
	return scope, err
}
