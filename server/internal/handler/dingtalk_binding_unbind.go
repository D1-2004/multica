package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) enqueueDingTalkBindingUnbinds(
	ctx context.Context,
	queries *db.Queries,
	agentIDs []pgtype.UUID,
) error {
	if h == nil || queries == nil || len(agentIDs) == 0 {
		return nil
	}
	readiness, err := queries.CountDingTalkBindingUnbindReadinessByAgentIDs(ctx, agentIDs)
	if err != nil {
		return err
	}
	if readiness.ActiveCount == 0 {
		return nil
	}
	if h.TaskCompletionTargetIdentity == "" {
		return errors.New("active dingtalk account binding has no Router target")
	}
	if readiness.ReadyCount != readiness.ActiveCount {
		return errors.New("active dingtalk account binding requires account-key enrichment")
	}
	return queries.EnqueueDingTalkBindingUnbindsByAgentIDs(
		ctx,
		db.EnqueueDingTalkBindingUnbindsByAgentIDsParams{
			TargetIdentity: h.TaskCompletionTargetIdentity,
			AgentIds:       agentIDs,
		},
	)
}

func (h *Handler) notifyDingTalkBindingUnbinds() {
	if h != nil && h.DingTalkBindingUnbindWorker != nil {
		h.DingTalkBindingUnbindWorker.Notify()
	}
}
