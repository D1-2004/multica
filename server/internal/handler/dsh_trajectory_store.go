package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Delete before tasks while the caller holds the workspace deletion lock.
// Removing the per-object data keys also makes retained ciphertext unreadable.
const deleteWorkspaceDSHTrajectories = `DELETE FROM agent_task_dsh_trajectory
 WHERE task_id IN (SELECT id FROM agent_task_queue
 WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id=$1)
 OR issue_id IN (SELECT id FROM issue WHERE workspace_id=$1)
 OR runtime_id IN (SELECT id FROM agent_runtime WHERE workspace_id=$1))`

// Object upload happens before this short transaction. No network call holds
// these locks. The workspace and task must still exist when their index is
// committed; a foreign key is not the application authorization boundary.
func (h *Handler) persistDSHTrajectory(ctx context.Context, workspace pgtype.UUID, task db.AgentTaskQueue, input db.PutAgentTaskDSHTrajectoryParams) (db.AgentTaskDshTrajectory, error) {
	var empty db.AgentTaskDshTrajectory
	if h.TxStarter == nil || !workspace.Valid || !task.ID.Valid || input.TaskID != task.ID {
		return empty, errors.New("DSH trajectory transaction is unavailable")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	var locked pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE`, workspace).Scan(&locked); err != nil {
		return empty, err
	}
	// Runtime, issue and hidden-builder deletion can also remove task rows.
	// Lock each parent before reading/writing the trajectory index, matching
	// those deletion entry points instead of relying on task cascades.
	if err := tx.QueryRow(ctx, `SELECT id FROM agent_runtime WHERE id=$1 AND workspace_id=$2 FOR KEY SHARE`, task.RuntimeID, workspace).Scan(&locked); err != nil {
		return empty, err
	}
	if task.IssueID.Valid {
		if err := tx.QueryRow(ctx, `SELECT id FROM issue WHERE id=$1 AND workspace_id=$2 FOR KEY SHARE`, task.IssueID, workspace).Scan(&locked); err != nil {
			return empty, err
		}
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM agent WHERE id=$1 AND workspace_id=$2 FOR SHARE`, task.AgentID, workspace).Scan(&locked); err != nil {
		return empty, err
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE id=$1 AND agent_id=$2 AND runtime_id=$3 FOR SHARE`, task.ID, task.AgentID, task.RuntimeID).Scan(&locked); err != nil {
		return empty, err
	}
	row, err := db.New(tx).PutAgentTaskDSHTrajectory(ctx, input)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(ctx); err != nil {
		// A lost commit response is uncertain. The caller must retain the
		// ciphertext because the index may already reference it.
		return empty, err
	}
	return row, nil
}
