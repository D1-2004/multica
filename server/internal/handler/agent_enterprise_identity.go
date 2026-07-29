package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type agentEnterpriseIdentityStatusResponse struct {
	Configured bool                               `json:"configured"`
	CanManage  bool                               `json:"can_manage"`
	Identity   *agentEnterpriseIdentityConnection `json:"identity"`
}

type agentEnterpriseIdentityConnection struct {
	EmployeeID          string `json:"employee_id,omitempty"`
	DisplayName         string `json:"display_name,omitempty"`
	Status              string `json:"status"`
	AIPID               string `json:"aip_id,omitempty"`
	AgentSPIFFEID       string `json:"agent_spiffe_id,omitempty"`
	BUCStatus           string `json:"buc_status"`
	AgentIdentityStatus string `json:"agent_identity_status"`
	RefreshExpiresAt    int64  `json:"refresh_expires_at,omitempty"`
}

type startAgentEnterpriseIdentityRequest struct {
	AgentID      string `json:"agent_id"`
	EmployeeID   string `json:"employee_id"`
	RedirectPath string `json:"redirect_path"`
}

type startAgentEnterpriseIdentityResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	ExpiresAt        int64  `json:"expires_at"`
}

type testAgentEnterpriseIdentityResponse struct {
	OK bool `json:"ok"`
}

func (h *Handler) GetAgentEnterpriseIdentityStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID, agent, _, member, ok := h.loadAgentEnterpriseIdentityTarget(
		w,
		r,
		strings.TrimSpace(r.URL.Query().Get("agent_id")),
	)
	if !ok {
		return
	}
	canManage := canManageEnterpriseIdentity(r, agent, member)
	if h.EnterpriseIdentity == nil {
		writeJSON(w, http.StatusOK, agentEnterpriseIdentityStatusResponse{
			Configured: false,
			CanManage:  canManage,
			Identity:   nil,
		})
		return
	}
	identity, err := h.Queries.GetAgentEnterpriseIdentity(r.Context(), db.GetAgentEnterpriseIdentityParams{
		WorkspaceID: workspaceID,
		AgentID:     agent.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, agentEnterpriseIdentityStatusResponse{
			Configured: true,
			CanManage:  canManage,
			Identity:   nil,
		})
		return
	}
	if err != nil {
		writeEnterpriseIdentityError(w, r, "status", workspaceID, agent.ID, err)
		return
	}
	connection := &agentEnterpriseIdentityConnection{
		Status:              identity.Status,
		BUCStatus:           identity.Status,
		AgentIdentityStatus: identity.Status,
	}
	if canManage {
		connection.EmployeeID = maskEnterpriseEmployeeID(identity.RawEmpID)
		connection.DisplayName = identity.DisplayName
		connection.AIPID = identity.AipID
		connection.AgentSPIFFEID = identity.AgentSpiffeID
		if identity.AuthxRefreshExpiresAt.Valid {
			connection.RefreshExpiresAt = identity.AuthxRefreshExpiresAt.Time.Unix()
		}
	}
	writeJSON(w, http.StatusOK, agentEnterpriseIdentityStatusResponse{
		Configured: true,
		CanManage:  canManage,
		Identity:   connection,
	})
}

func (h *Handler) BeginAgentEnterpriseIdentityBinding(w http.ResponseWriter, r *http.Request) {
	if h.EnterpriseIdentity == nil {
		writeError(w, http.StatusServiceUnavailable, "enterprise sandbox identity is not configured")
		return
	}
	var request startAgentEnterpriseIdentityRequest
	if err := decodeLimitedJSON(w, r, 4<<10, &request, "invalid_request"); err != nil {
		return
	}
	workspaceID, agent, actorUserID, ok := h.authorizeAgentEnterpriseIdentity(
		w,
		r,
		strings.TrimSpace(request.AgentID),
	)
	if !ok {
		return
	}
	if strings.TrimSpace(request.RedirectPath) == "" {
		request.RedirectPath = "/"
	}
	result, err := h.EnterpriseIdentity.StartBinding(r.Context(), service.StartEnterpriseIdentityBindingInput{
		WorkspaceID:  workspaceID,
		AgentID:      agent.ID,
		ActorUserID:  actorUserID,
		EmployeeID:   request.EmployeeID,
		RedirectPath: request.RedirectPath,
	})
	if err != nil {
		writeEnterpriseIdentityError(w, r, "start", workspaceID, agent.ID, err)
		return
	}
	writeJSON(w, http.StatusOK, startAgentEnterpriseIdentityResponse{
		AuthorizationURL: result.AuthorizeURL,
		ExpiresAt:        result.ExpiresAt.Unix(),
	})
}

func (h *Handler) CompleteAgentEnterpriseIdentityBinding(w http.ResponseWriter, r *http.Request) {
	if h.EnterpriseIdentity == nil {
		writeError(w, http.StatusServiceUnavailable, "enterprise sandbox identity is not configured")
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	result, err := h.EnterpriseIdentity.CompleteBinding(r.Context(), state, code)
	if err != nil {
		slog.Warn("enterprise identity OAuth callback failed")
		writeError(w, http.StatusBadRequest, "enterprise identity authorization could not be completed")
		return
	}
	target, err := url.Parse(result.RedirectPath)
	if err != nil || target.IsAbs() || !strings.HasPrefix(target.Path, "/") {
		writeError(w, http.StatusInternalServerError, "enterprise identity redirect is invalid")
		return
	}
	query := target.Query()
	query.Set("enterprise_identity", "connected")
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (h *Handler) TestAgentEnterpriseIdentity(w http.ResponseWriter, r *http.Request) {
	if h.EnterpriseIdentity == nil {
		writeError(w, http.StatusServiceUnavailable, "enterprise sandbox identity is not configured")
		return
	}
	workspaceID, agent, _, ok := h.authorizeAgentEnterpriseIdentity(
		w,
		r,
		strings.TrimSpace(r.URL.Query().Get("agent_id")),
	)
	if !ok {
		return
	}
	if _, err := h.EnterpriseIdentity.ResolveASBTaskIdentity(r.Context(), workspaceID, agent.ID); err != nil {
		writeEnterpriseIdentityError(w, r, "test", workspaceID, agent.ID, err)
		return
	}
	writeJSON(w, http.StatusOK, testAgentEnterpriseIdentityResponse{OK: true})
}

func (h *Handler) RevokeAgentEnterpriseIdentity(w http.ResponseWriter, r *http.Request) {
	if h.EnterpriseIdentity == nil {
		writeError(w, http.StatusServiceUnavailable, "enterprise sandbox identity is not configured")
		return
	}
	workspaceID, agent, _, ok := h.authorizeAgentEnterpriseIdentity(
		w,
		r,
		strings.TrimSpace(r.URL.Query().Get("agent_id")),
	)
	if !ok {
		return
	}
	if err := h.EnterpriseIdentity.Revoke(r.Context(), workspaceID, agent.ID); err != nil {
		writeEnterpriseIdentityError(w, r, "revoke", workspaceID, agent.ID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) authorizeAgentEnterpriseIdentity(
	w http.ResponseWriter,
	r *http.Request,
	agentID string,
) (pgtype.UUID, db.Agent, pgtype.UUID, bool) {
	workspaceUUID, agent, actorUUID, member, ok := h.loadAgentEnterpriseIdentityTarget(w, r, agentID)
	if !ok {
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, false
	}
	if !canManageEnterpriseIdentity(r, agent, member) {
		writeError(w, http.StatusForbidden, "only the agent owner can manage this agent")
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, false
	}
	return workspaceUUID, agent, actorUUID, true
}

func (h *Handler) loadAgentEnterpriseIdentityTarget(
	w http.ResponseWriter,
	r *http.Request,
	agentID string,
) (pgtype.UUID, db.Agent, pgtype.UUID, db.Member, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, db.Member{}, false
	}
	workspaceID := workspaceIDFromURL(r, "id")
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, db.Member{}, false
	}
	if strings.TrimSpace(agentID) == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, db.Member{}, false
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, db.Member{}, false
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentID, "agent_id")
	if !ok {
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, db.Member{}, false
	}
	actorUUID := parseUUID(userID)
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil || agent.Kind != "user" {
		writeError(w, http.StatusNotFound, "agent not found")
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, db.Member{}, false
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is archived")
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, db.Member{}, false
	}
	member, ok := h.requireWorkspaceRole(
		w,
		r,
		workspaceID,
		"agent not found",
		"owner",
		"admin",
		"member",
	)
	if !ok {
		return pgtype.UUID{}, db.Agent{}, pgtype.UUID{}, db.Member{}, false
	}
	return workspaceUUID, agent, actorUUID, member, true
}

func canManageEnterpriseIdentity(r *http.Request, agent db.Agent, member db.Member) bool {
	return roleAllowed(member.Role, "owner", "admin") ||
		uuidToString(agent.OwnerID) == requestUserID(r)
}

func maskEnterpriseEmployeeID(employeeID string) string {
	const visibleDigits = 4
	if len(employeeID) <= visibleDigits {
		return strings.Repeat("*", len(employeeID))
	}
	return strings.Repeat("*", len(employeeID)-visibleDigits) +
		employeeID[len(employeeID)-visibleDigits:]
}

func writeEnterpriseIdentityError(
	w http.ResponseWriter,
	r *http.Request,
	operation string,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	err error,
) {
	_ = r
	slog.Warn("enterprise identity request failed",
		"operation", operation,
		"workspace_id", util.UUIDToString(workspaceID),
		"agent_id", util.UUIDToString(agentID),
	)
	switch {
	case errors.Is(err, service.ErrEnterpriseIdentityNeedsReauth):
		writeError(w, http.StatusConflict, "enterprise sandbox identity needs reauthorization")
	case errors.Is(err, service.ErrEnterpriseIdentityEmployeeConflict):
		writeError(w, http.StatusConflict, "revoke the existing enterprise identity before binding another employee")
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "enterprise sandbox identity not found")
	default:
		writeError(w, http.StatusBadGateway, "enterprise sandbox identity request failed")
	}
}
