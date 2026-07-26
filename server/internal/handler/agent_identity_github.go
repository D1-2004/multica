package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentitygithub"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type agentIdentityGithubStatusResponse struct {
	Configured bool                           `json:"configured"`
	Connection *agentIdentityGithubConnection `json:"connection"`
}

type agentIdentityGithubConnection struct {
	ConnectionID     string `json:"connection_id"`
	AccountLogin     string `json:"account_login"`
	AccountID        string `json:"account_id"`
	Status           string `json:"status"`
	GrantedScopes    string `json:"granted_scopes"`
	AccessExpiresAt  *int64 `json:"access_expires_at,omitempty"`
	RefreshExpiresAt *int64 `json:"refresh_expires_at,omitempty"`
	LastRefreshAt    *int64 `json:"last_refresh_at,omitempty"`
	LastTestAt       *int64 `json:"last_test_at,omitempty"`
}

type agentIdentityGithubOAuthStartRequest struct {
	AgentID   string `json:"agent_id"`
	ReturnURL string `json:"return_url"`
}

type agentIdentityGithubOAuthStartResponse struct {
	State            string `json:"state"`
	AuthorizationURL string `json:"authorization_url"`
}

type agentIdentityGithubTestResponse struct {
	OK            bool   `json:"ok"`
	Refreshed     bool   `json:"refreshed"`
	ConnectionID  string `json:"connection_id,omitempty"`
	AccountLogin  string `json:"account_login,omitempty"`
	AccountID     string `json:"account_id,omitempty"`
	GrantedScopes string `json:"granted_scopes,omitempty"`
}

func (h *Handler) GetAgentIdentityGitHubStatus(w http.ResponseWriter, r *http.Request) {
	client, ok := h.requireAgentIdentityGitHub(w)
	if !ok {
		return
	}
	workspaceID, agent, userID, ok := h.authorizeAgentIdentityGitHub(w, r, strings.TrimSpace(r.URL.Query().Get("agent_id")))
	if !ok {
		return
	}

	result, err := client.GetStatus(r.Context(), workspaceID, uuidToString(agent.ID), userID)
	if err != nil {
		var svcErr *agentidentitygithub.ServiceError
		if errors.As(err, &svcErr) && svcErr.StatusCode == http.StatusNotFound {
			writeJSON(w, http.StatusOK, agentIdentityGithubStatusResponse{
				Configured: true,
				Connection: nil,
			})
			return
		}
		logAgentIdentityGitHubError(r, "status", err, workspaceID, uuidToString(agent.ID))
		writeAgentIdentityGitHubError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agentIdentityGithubStatusResponse{
		Configured: true,
		Connection: githubConnectionFromAgentIdentity(result),
	})
}

func (h *Handler) BeginAgentIdentityGitHubOAuth(w http.ResponseWriter, r *http.Request) {
	client, ok := h.requireAgentIdentityGitHub(w)
	if !ok {
		return
	}
	var req agentIdentityGithubOAuthStartRequest
	if err := decodeLimitedJSON(w, r, 4<<10, &req, "invalid_request"); err != nil {
		return
	}
	workspaceID, agent, userID, ok := h.authorizeAgentIdentityGitHub(w, r, strings.TrimSpace(req.AgentID))
	if !ok {
		return
	}
	returnURL, ok := h.resolveAgentIdentityGitHubReturnURL(w, r, req.ReturnURL)
	if !ok {
		return
	}

	result, err := client.StartOAuth(r.Context(), agentidentitygithub.OAuthStartRequest{
		WorkspaceID: workspaceID,
		AgentID:     uuidToString(agent.ID),
		UserID:      userID,
		ReturnURL:   returnURL,
	})
	if err != nil {
		logAgentIdentityGitHubError(r, "oauth_start", err, workspaceID, uuidToString(agent.ID))
		writeAgentIdentityGitHubError(w, err)
		return
	}
	if !result.OK || strings.TrimSpace(result.AuthorizationURL) == "" {
		writeError(w, http.StatusBadGateway, nonEmpty(result.ErrorMessage, "agent identity did not return a GitHub authorization URL"))
		return
	}
	writeJSON(w, http.StatusOK, agentIdentityGithubOAuthStartResponse{
		State:            result.State,
		AuthorizationURL: result.AuthorizationURL,
	})
}

func (h *Handler) TestAgentIdentityGitHubConnection(w http.ResponseWriter, r *http.Request) {
	client, ok := h.requireAgentIdentityGitHub(w)
	if !ok {
		return
	}
	connectionID := strings.TrimSpace(chi.URLParam(r, "connectionId"))
	if connectionID == "" {
		writeError(w, http.StatusBadRequest, "connection_id is required")
		return
	}
	workspaceID, agent, userID, ok := h.authorizeAgentIdentityGitHub(w, r, strings.TrimSpace(r.URL.Query().Get("agent_id")))
	if !ok {
		return
	}
	status, err := client.GetStatus(r.Context(), workspaceID, uuidToString(agent.ID), userID)
	if err != nil {
		logAgentIdentityGitHubError(r, "test_status", err, workspaceID, uuidToString(agent.ID))
		writeAgentIdentityGitHubError(w, err)
		return
	}
	if status.ConnectionID != connectionID {
		writeError(w, http.StatusNotFound, "GitHub identity connection not found")
		return
	}
	result, err := client.TestConnection(r.Context(), connectionID)
	if err != nil {
		logAgentIdentityGitHubError(r, "test_connection", err, workspaceID, uuidToString(agent.ID))
		writeAgentIdentityGitHubError(w, err)
		return
	}
	if !result.OK {
		writeError(w, http.StatusBadGateway, nonEmpty(result.ErrorMessage, "agent identity GitHub connection test failed"))
		return
	}
	writeJSON(w, http.StatusOK, agentIdentityGithubTestResponse{
		OK:            result.OK,
		Refreshed:     result.Refreshed,
		ConnectionID:  result.ConnectionID,
		AccountLogin:  result.AccountLogin,
		AccountID:     result.AccountID,
		GrantedScopes: result.GrantedScopes,
	})
}

func (h *Handler) requireAgentIdentityGitHub(w http.ResponseWriter) (*agentidentitygithub.Client, bool) {
	if h.AgentIdentityGitHub == nil || !h.AgentIdentityGitHub.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "agent identity GitHub is not configured")
		return nil, false
	}
	return h.AgentIdentityGitHub, true
}

func (h *Handler) authorizeAgentIdentityGitHub(w http.ResponseWriter, r *http.Request, agentID string) (string, db.Agent, string, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return "", db.Agent{}, "", false
	}
	workspaceID := workspaceIDFromURL(r, "id")
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return "", db.Agent{}, "", false
	}
	if strings.TrimSpace(agentID) == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return "", db.Agent{}, "", false
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentID, "agent_id")
	if !ok {
		return "", db.Agent{}, "", false
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return "", db.Agent{}, "", false
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil || agent.Kind != "user" {
		writeError(w, http.StatusNotFound, "agent not found")
		return "", db.Agent{}, "", false
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is archived")
		return "", db.Agent{}, "", false
	}
	if !h.canManageAgent(w, r, agent) {
		return "", db.Agent{}, "", false
	}
	return util.UUIDToString(workspaceUUID), agent, userID, true
}

func (h *Handler) resolveAgentIdentityGitHubReturnURL(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "/"
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid return_url")
		return "", false
	}
	origin := requestOrigin(r)
	if parsed.IsAbs() {
		if origin == "" || !sameOrigin(parsed, origin) {
			writeError(w, http.StatusBadRequest, "return_url origin is not allowed")
			return "", false
		}
		return parsed.String(), true
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		writeError(w, http.StatusBadRequest, "return_url must be absolute or start with /")
		return "", false
	}
	if origin == "" {
		writeError(w, http.StatusBadRequest, "request origin is required")
		return "", false
	}
	base, _ := url.Parse(origin)
	return base.ResolveReference(parsed).String(), true
}

func requestOrigin(r *http.Request) string {
	if origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/"); origin != "" {
		return origin
	}
	scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func sameOrigin(parsed *url.URL, origin string) bool {
	allowed, err := url.Parse(origin)
	if err != nil || allowed.Scheme == "" || allowed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, allowed.Scheme) && strings.EqualFold(parsed.Host, allowed.Host)
}

func githubConnectionFromAgentIdentity(result agentidentitygithub.Connection) *agentIdentityGithubConnection {
	if !result.OK || result.ConnectionID == "" {
		return nil
	}
	return &agentIdentityGithubConnection{
		ConnectionID:     result.ConnectionID,
		AccountLogin:     result.AccountLogin,
		AccountID:        result.AccountID,
		Status:           result.Status,
		GrantedScopes:    result.GrantedScopes,
		AccessExpiresAt:  result.AccessExpiresAt,
		RefreshExpiresAt: result.RefreshExpiresAt,
		LastRefreshAt:    result.LastRefreshAt,
		LastTestAt:       result.LastTestAt,
	}
}

func writeAgentIdentityGitHubError(w http.ResponseWriter, err error) {
	var svcErr *agentidentitygithub.ServiceError
	if errors.As(err, &svcErr) {
		switch svcErr.StatusCode {
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, nonEmpty(svcErr.Message, "GitHub identity connection not found"))
		case http.StatusUnauthorized:
			writeError(w, http.StatusUnauthorized, nonEmpty(svcErr.Message, "GitHub identity needs reauthorization"))
		case http.StatusServiceUnavailable:
			writeError(w, http.StatusServiceUnavailable, nonEmpty(svcErr.Message, "agent identity GitHub is not configured"))
		default:
			writeError(w, http.StatusBadGateway, nonEmpty(svcErr.Message, "agent identity GitHub request failed"))
		}
		return
	}
	writeError(w, http.StatusBadGateway, "agent identity GitHub request failed")
}

func logAgentIdentityGitHubError(r *http.Request, operation string, err error, workspaceID string, agentID string) {
	attrs := append(logger.RequestAttrs(r),
		"operation", operation,
		"workspace_id", workspaceID,
		"agent_id", agentID,
		"error", err,
	)
	var svcErr *agentidentitygithub.ServiceError
	if errors.As(err, &svcErr) {
		attrs = append(attrs,
			"upstream_status", svcErr.StatusCode,
			"upstream_code", svcErr.Code,
			"upstream_message", svcErr.Message,
		)
	}
	slog.Warn("agent identity github request failed", attrs...)
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
