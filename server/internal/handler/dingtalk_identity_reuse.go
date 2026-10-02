package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type dingTalkIdentityReuseStore interface {
	GetAgentInWorkspace(context.Context, db.GetAgentInWorkspaceParams) (db.Agent, error)
	ListReusableDingTalkIdentities(context.Context, db.ListReusableDingTalkIdentitiesParams) ([]db.ListReusableDingTalkIdentitiesRow, error)
	ReuseDingTalkIdentity(context.Context, db.ReuseDingTalkIdentityParams) (pgtype.UUID, error)
}

func (h *Handler) identityReuseStore() dingTalkIdentityReuseStore {
	if h.dingTalkIdentityReuse != nil {
		return h.dingTalkIdentityReuse
	}
	return h.Queries
}

// Reusing a personal authorization is owner-only, even for workspace admins.
func (h *Handler) identityReuseTarget(w http.ResponseWriter, r *http.Request, target string) (db.Agent, pgtype.UUID, bool) {
	ws, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace_id")
	if !ok {
		return db.Agent{}, pgtype.UUID{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, target, "agent_id")
	if !ok {
		return db.Agent{}, pgtype.UUID{}, false
	}
	member, ok := h.requireWorkspaceMember(w, r, uuidToString(ws), "workspace not found")
	if !ok {
		return db.Agent{}, pgtype.UUID{}, false
	}
	a, err := h.identityReuseStore().GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent not found")
		return db.Agent{}, pgtype.UUID{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent")
		return db.Agent{}, pgtype.UUID{}, false
	}
	if a.ArchivedAt.Valid || !a.OwnerID.Valid || a.OwnerID != member.UserID {
		writeError(w, http.StatusForbidden, "only the agent owner can reuse an execution identity")
		return db.Agent{}, pgtype.UUID{}, false
	}
	return a, member.UserID, true
}

func (h *Handler) ListReusableDingTalkIdentities(w http.ResponseWriter, r *http.Request) {
	a, user, ok := h.identityReuseTarget(w, r, r.URL.Query().Get("agent_id"))
	if !ok {
		return
	}
	rows, err := h.identityReuseStore().ListReusableDingTalkIdentities(r.Context(), db.ListReusableDingTalkIdentitiesParams{WorkspaceID: a.WorkspaceID, UserID: user, TargetAgentID: a.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list reusable execution identities")
		return
	}
	if rows == nil {
		rows = []db.ListReusableDingTalkIdentitiesRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"identities": rows})
}

func (h *Handler) ReuseDingTalkIdentity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID       string `json:"agent_id"`
		SourceAgentID string `json:"source_agent_id"`
	}
	if err := decodeLimitedJSON(w, r, 4<<10, &req, "invalid_request"); err != nil {
		return
	}
	source, ok := parseUUIDOrBadRequest(w, req.SourceAgentID, "source_agent_id")
	if !ok {
		return
	}
	a, user, ok := h.identityReuseTarget(w, r, req.AgentID)
	if !ok {
		return
	}
	if !h.rejectTagTemplateBinding(w, r, a.WorkspaceID, a.ID) {
		return
	}
	_, err := h.identityReuseStore().ReuseDingTalkIdentity(r.Context(), db.ReuseDingTalkIdentityParams{WorkspaceID: a.WorkspaceID, UserID: user, SourceAgentID: source, TargetAgentID: a.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "identity is no longer reusable or the agent already has a different identity; refresh and try again")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reuse execution identity")
		return
	}
	h.publish(protocol.EventDingTalkAccountBindingActivated, uuidToString(a.WorkspaceID), "member", uuidToString(user), map[string]any{"agent_id": uuidToString(a.ID)})
	w.WriteHeader(http.StatusNoContent)
}
