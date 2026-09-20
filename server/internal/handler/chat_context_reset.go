package handler

import (
	"bytes"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Reset affects model context, while the user-visible transcript remains intact.
// Query against this immutable input turn: a later queued reset must not change
// an earlier task, and subsequent tasks must not resurrect pre-reset history.
func (h *Handler) chatHistorySinceReset(ctx context.Context, sessionID pgtype.UUID, input db.ChatMessage, history []db.ChatMessage) ([]db.ChatMessage, error) {
	boundary, err := h.Queries.GetChatContextResetBoundary(ctx, db.GetChatContextResetBoundaryParams{
		ChatSessionID: sessionID, InputCreatedAt: input.CreatedAt, InputID: input.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return history, nil
	}
	if err != nil {
		return nil, err
	}
	return chatHistoryAfterBoundary(history, boundary), nil
}

func chatHistoryAfterBoundary(history []db.ChatMessage, boundary db.GetChatContextResetBoundaryRow) []db.ChatMessage {
	excluded := make(map[pgtype.UUID]bool, len(boundary.ExcludedTaskIds))
	for _, id := range boundary.ExcludedTaskIds {
		excluded[id] = true
	}
	result := make([]db.ChatMessage, 0, len(history))
	for _, message := range history {
		// A pre-reset task may finish after the reset input was queued. Its late
		// answer still belongs to the old context even though its timestamp is new.
		if excluded[message.TaskID] {
			continue
		}
		if message.CreatedAt.Time.After(boundary.CreatedAt.Time) ||
			(message.CreatedAt.Time.Equal(boundary.CreatedAt.Time) && bytes.Compare(message.ID.Bytes[:], boundary.ID.Bytes[:]) >= 0) {
			result = append(result, message)
		}
	}
	return result
}

// Old native sessions remain browsable and executable after a reset. Their
// subsequent answers belong to that native context, not the new platform epoch.
func (h *Handler) chatHistoryForDSHSession(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue, history []db.ChatMessage) ([]db.ChatMessage, error) {
	var sessionID string
	if err := h.DB.QueryRow(ctx, `SELECT session_id FROM dsh_task_binding WHERE agent_id=$1 AND task_id=$2 AND workspace_id=$3`, task.AgentID, task.ID, workspaceID).Scan(&sessionID); err != nil {
		return nil, err
	}
	ids := make([]pgtype.UUID, 0, len(history))
	for _, message := range history {
		if message.TaskID.Valid {
			ids = append(ids, message.TaskID)
		}
	}
	rows, err := h.DB.Query(ctx, `SELECT task_id FROM dsh_task_binding WHERE agent_id=$1 AND task_id=ANY($2::uuid[]) AND session_id<>$3 AND workspace_id=$4`, task.AgentID, ids, sessionID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	excluded := map[pgtype.UUID]bool{}
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		excluded[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]db.ChatMessage, 0, len(history))
	for _, message := range history {
		if !excluded[message.TaskID] {
			result = append(result, message)
		}
	}
	return result, nil
}
