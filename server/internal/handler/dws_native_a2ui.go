package handler

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/service/a2ui"
)

// HandleDWSNativeCardAction applies one user_card_action_triggered from the
// native subscription. The click is not an IM message, so it does not go
// through HandleDWSNativeEvent. finish updates the visible card after the
// answer is stored; the service logs a finish failure and still acks.
func (h *Handler) HandleDWSNativeCardAction(ctx context.Context, id dwsclient.Identity, line []byte, finish func(context.Context, string, string) error) error {
	if h == nil || h.A2UI == nil {
		return nil
	}
	agentID, err := uuid.Parse(id.AgentID)
	if err != nil {
		slog.Warn("DWS native card action has no agent", "event", "a2ui_native_actor_invalid")
		return nil
	}
	return h.A2UI.Accept(ctx, a2ui.Actor{AgentID: agentID, UID: id.UID, OrgID: id.OrgID}, line, finish)
}
