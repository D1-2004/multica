package handler

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) dshNativeInvoke(ctx context.Context, q *db.Queries, agent db.Agent, userID pgtype.UUID) error {
	if q == nil || !canInvokeAgentWithStore(ctx, q, agent, "member", uuidToString(userID), uuidToString(userID), uuidToString(agent.WorkspaceID)) {
		return service.ErrDSHAccessDenied
	}
	return nil
}
