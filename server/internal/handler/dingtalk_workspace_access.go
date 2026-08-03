package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type dingTalkAgentMetadataStore interface {
	GetAgentInWorkspace(context.Context, db.GetAgentInWorkspaceParams) (db.Agent, error)
}

func (h *Handler) dingTalkAgentMetadataStore() dingTalkAgentMetadataStore {
	if h.dingTalkAccountBindingMetadata != nil {
		return h.dingTalkAccountBindingMetadata
	}
	return h.Queries
}

func (h *Handler) loadDingTalkAgentForRequest(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) (db.Agent, bool) {
	store := h.dingTalkAgentMetadataStore()
	if store == nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent")
		return db.Agent{}, false
	}
	agent, err := store.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found in this workspace")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load agent")
		}
		return db.Agent{}, false
	}
	return agent, true
}

func (h *Handler) canManageDingTalkAgentForRequest(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) (db.Agent, bool) {
	agent, ok := h.loadDingTalkAgentForRequest(w, r, workspaceID, agentID)
	if !ok || !h.canManageAgent(w, r, agent) {
		return db.Agent{}, false
	}
	return agent, true
}

func (h *Handler) canReadDingTalkAccountBindingStatus(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) (db.Agent, bool) {
	agent, ok := h.loadDingTalkAgentForRequest(w, r, workspaceID, agentID)
	if !ok {
		return db.Agent{}, false
	}
	member, ok := h.workspaceMember(w, r, uuidToString(workspaceID))
	if !ok {
		return db.Agent{}, false
	}
	if roleAllowed(member.Role, "owner", "admin") || uuidToString(agent.OwnerID) == requestUserID(r) {
		return agent, true
	}
	writeError(w, http.StatusNotFound, "agent not found in this workspace")
	return db.Agent{}, false
}

func (h *Handler) canManageDingTalkInstallationForRequest(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) bool {
	store := h.dingTalkAgentMetadataStore()
	if store == nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent")
		return false
	}
	agent, err := store.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_, ok := h.requireWorkspaceRole(w, r, uuidToString(workspaceID), "dingtalk installation not found", "owner", "admin")
			return ok
		}
		writeError(w, http.StatusInternalServerError, "failed to load agent")
		return false
	}
	return h.canManageAgent(w, r, agent)
}
