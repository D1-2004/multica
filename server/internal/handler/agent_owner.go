package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func canTransferAgentOwner(agent db.Agent, member db.Member) bool {
	return canTransferOwnedBy(agent.OwnerID, member)
}

// TransferAgentOwner reassigns agent.owner_id to a current human workspace
// member. The current owner can hand the agent off; a workspace owner/admin
// can reassign any agent, including one whose previous owner has left.
//
// Personal OAuth (A2A/MCP delegated_by) is revoked in the same transaction so
// the previous owner's callers stop immediately. DingTalk identity, channel
// install, custom_env, runtime binding, and composio_toolkit_allowlist stay
// on the agent. The new owner re-opens A2A/MCP and uses their own Composio
// connections.
func (h *Handler) TransferAgentOwner(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	wsID := uuidToString(existing.WorkspaceID)
	member, ok := h.requireWorkspaceRole(w, r, wsID, "agent not found", "owner", "admin", "member")
	if !ok {
		return
	}
	if !canTransferAgentOwner(existing, member) {
		writeError(w, http.StatusForbidden, "only the agent owner or a workspace admin can transfer this agent")
		return
	}

	ownerUUID, ok := h.resolveTransferOwnerTarget(w, r, existing.WorkspaceID)
	if !ok {
		return
	}

	actorID := requestUserID(r)
	if existing.OwnerID.Valid && existing.OwnerID.Bytes == ownerUUID.Bytes {
		h.writeTransferredAgent(w, r, existing, actorID, wsID)
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to transfer agent owner")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	locked, err := qtx.GetAgentForUpdate(r.Context(), existing.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to transfer agent owner")
		return
	}
	if locked.Kind != "user" || uuidToString(locked.WorkspaceID) != wsID {
		writeError(w, http.StatusConflict, "agent state changed; please retry")
		return
	}
	if !canTransferAgentOwner(locked, member) {
		writeError(w, http.StatusForbidden, "only the agent owner or a workspace admin can transfer this agent")
		return
	}

	updated, err := qtx.UpdateAgentOwner(r.Context(), db.UpdateAgentOwnerParams{
		ID:          locked.ID,
		OwnerID:     ownerUUID,
		WorkspaceID: locked.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "agent state changed; please retry")
			return
		}
		slog.Warn("transfer agent owner failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to transfer agent owner")
		return
	}

	if err := revokeAgentA2AOnOwnerTransfer(r.Context(), qtx, locked.WorkspaceID, locked.ID, parseUUID(actorID)); err != nil {
		slog.Warn("revoke A2A on agent owner transfer failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to transfer agent owner")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to transfer agent owner")
		return
	}

	slog.Info("agent owner transferred", append(logger.RequestAttrs(r),
		"agent_id", id,
		"workspace_id", wsID,
		"from_owner_id", uuidToString(locked.OwnerID),
		"to_owner_id", uuidToString(ownerUUID),
	)...)
	h.writeTransferredAgent(w, r, updated, actorID, wsID)
}

func (h *Handler) writeTransferredAgent(w http.ResponseWriter, r *http.Request, agent db.Agent, actorUserID, wsID string) {
	resp := h.agentToResponse(agent)
	h.hydrateChatSessionResume(r.Context(), &resp, agent.ID)
	h.hydrateInboundCoordinator(r.Context(), &resp, agent.ID)
	h.hydrateAgentVoice(r.Context(), &resp, agent.ID)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, agent.ID); err != nil {
		slog.Warn("transfer agent owner: load invocation targets failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return
	}
	if err := h.attachAgentSkills(r.Context(), &resp, agent.ID); err != nil {
		slog.Warn("transfer agent owner: load skills failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	actorType, actorID := h.resolveActor(r, actorUserID, wsID)
	h.publish(protocol.EventAgentStatus, wsID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
	redactAgentResponseForActor(&resp, actorType)
	if !h.composioMCPAppsEnabled(r.Context()) {
		suppressComposioToolkitAllowlist(&resp)
	} else if uuidToString(agent.OwnerID) != actorUserID {
		redactComposioToolkitAllowlist(&resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

func revokeAgentA2AOnOwnerTransfer(ctx context.Context, qtx *db.Queries, workspaceID, agentID, revokedBy pgtype.UUID) error {
	endpointIDs, err := qtx.LockAgentA2AEndpointsForMemberRevocation(ctx, db.LockAgentA2AEndpointsForMemberRevocationParams{
		WorkspaceID: workspaceID,
		AgentIds:    []pgtype.UUID{agentID},
	})
	if err != nil {
		return err
	}
	clientIDs, err := qtx.LockA2AClientsForMemberRevocation(ctx, endpointIDs)
	if err != nil {
		return err
	}
	credentialIDs, err := qtx.LockA2ACredentialsForMemberRevocation(ctx, clientIDs)
	if err != nil {
		return err
	}
	if _, err := qtx.DisableAgentA2AEndpointsForMemberRevocation(ctx, endpointIDs); err != nil {
		return err
	}
	if _, err := qtx.RevokeA2AClientsForMemberRevocation(ctx, db.RevokeA2AClientsForMemberRevocationParams{
		ClientIds: clientIDs,
		RevokedBy: revokedBy,
	}); err != nil {
		return err
	}
	if _, err := qtx.RevokeA2ACredentialsForMemberRevocation(ctx, db.RevokeA2ACredentialsForMemberRevocationParams{
		CredentialIds: credentialIDs,
		RevokedBy:     revokedBy,
	}); err != nil {
		return err
	}
	return nil
}
