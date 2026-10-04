package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeeloopconfig"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) hydrateAgentCoordination(ctx context.Context, response *AgentResponse) {
	if response == nil {
		return
	}
	responses := []AgentResponse{*response}
	h.hydrateAgentsCoordination(ctx, responses)
	*response = responses[0]
}

func (h *Handler) hydrateAgentsCoordination(ctx context.Context, responses []AgentResponse) {
	if len(responses) == 0 {
		return
	}
	workspaceIDs := make([]pgtype.UUID, 0, len(responses))
	agentIDs := make([]pgtype.UUID, 0, len(responses))
	indices := make(map[string]int, len(responses))
	for i := range responses {
		responses[i].CoordinationMode = "unknown"
		responses[i].EmployeeLoopReady = false
		workspaceIDs = append(workspaceIDs, parseUUID(responses[i].WorkspaceID))
		agentIDs = append(agentIDs, parseUUID(responses[i].ID))
		indices[responses[i].ID] = i
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("load coordination settings failed", "error", err)
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT a.id,a.coordination_mode FROM agent a JOIN unnest($1::uuid[],$2::uuid[]) AS requested(workspace_id,id) ON a.workspace_id=requested.workspace_id AND a.id=requested.id`, workspaceIDs, agentIDs)
	if err != nil {
		slog.Warn("load coordination settings failed", "error", err)
		return
	}
	for rows.Next() {
		var id pgtype.UUID
		var mode string
		if err := rows.Scan(&id, &mode); err != nil {
			rows.Close()
			slog.Warn("read coordination settings failed", "error", err)
			return
		}
		if i, ok := indices[uuidToString(id)]; ok && employeeloopconfig.Validate(mode) == nil {
			responses[i].CoordinationMode = mode
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("read coordination settings failed", "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("finish loading coordination settings failed", "error", err)
		return
	}
	for i := range responses {
		if responses[i].CoordinationMode != "unknown" && h.EmployeeLoopReady != nil {
			responses[i].EmployeeLoopReady = h.EmployeeLoopReady(ctx, workspaceIDs[i], agentIDs[i]) == nil
		}
	}
}

func normalizeCoordinationUpdate(req *UpdateAgentRequest) {
	if req.EventTriggerEnabled != nil && req.InboundCoordinator != nil && !*req.InboundCoordinator {
		disabled := false
		req.EventTriggerEnabled = &disabled
	}
	if req.EventTriggerEnabled != nil && *req.EventTriggerEnabled {
		enabled := true
		req.InboundCoordinator = &enabled
	}
}

func writeCoordinationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, employeeloopconfig.ErrInvalidMode):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, employeeloopconfig.ErrNotReady):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "agent not found")
	default:
		slog.Warn("update coordination settings failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update coordination settings")
	}
}

func (h *Handler) checkAgentCoordinationUpdate(ctx context.Context, agent db.Agent, req UpdateAgentRequest) error {
	if req.CoordinationMode == nil && req.InboundCoordinator == nil {
		return nil
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = employeeloopconfig.ResolveUpdate(ctx, tx, agent.WorkspaceID, agent.ID, req.CoordinationMode, req.InboundCoordinator, h.EmployeeLoopReady)
	return err
}

func (h *Handler) updateAgentCoordinationPolicy(ctx context.Context, agent db.Agent, actor pgtype.UUID, req UpdateAgentRequest) error {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	resolved, err := employeeloopconfig.ResolveUpdate(ctx, tx, agent.WorkspaceID, agent.ID, req.CoordinationMode, req.InboundCoordinator, h.EmployeeLoopReady)
	if err != nil {
		return err
	}
	if req.CoordinationMode != nil {
		if err = employeeloopconfig.SaveMode(ctx, tx, agent.WorkspaceID, agent.ID, resolved.Mode); err != nil {
			return err
		}
	}
	if req.EventTriggerEnabled != nil {
		if err = service.SetEventTriggerEnabledAndInboundInTx(ctx, tx, agent, actor, *req.EventTriggerEnabled, req.InboundCoordinator); err != nil {
			return err
		}
	}
	policy := db.UpdateAgentDingTalkResponsePolicyParams{ID: agent.ID}
	if req.InboundCoordinatorUserDecisionNames != nil {
		policy.UserDecisionNames = normalizeUserDecisionNames(*req.InboundCoordinatorUserDecisionNames)
	}
	if req.InboundCoordinatorUserDecision != nil {
		policy.UserDecision = pgtype.Bool{Bool: *req.InboundCoordinatorUserDecision, Valid: true}
	}
	applyUserDecisionMode(req, &policy)
	if req.InboundCoordinator != nil {
		policy.InboundCoordinator = pgtype.Bool{Bool: *req.InboundCoordinator, Valid: true}
	}
	if req.DingTalkShowAITag != nil {
		policy.ShowAiTag = pgtype.Bool{Bool: *req.DingTalkShowAITag, Valid: true}
	}
	if req.DingTalkResponseEnabled != nil {
		policy.ResponseEnabled = pgtype.Bool{Bool: *req.DingTalkResponseEnabled, Valid: true}
	}
	if _, err = db.New(tx).UpdateAgentDingTalkResponsePolicy(ctx, policy); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if req.EventTriggerEnabled != nil && h.EventTriggers != nil {
		h.EventTriggers.Notify()
	}
	if h.DingTalkResponsePolicyNotifier != nil {
		h.DingTalkResponsePolicyNotifier.NotifyResponsePolicyChanged()
	}
	return nil
}
