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
	for i, message := range history {
		if message.CreatedAt.Time.After(boundary.CreatedAt.Time) ||
			(message.CreatedAt.Time.Equal(boundary.CreatedAt.Time) && bytes.Compare(message.ID.Bytes[:], boundary.ID.Bytes[:]) >= 0) {
			return history[i:]
		}
	}
	return nil
}
