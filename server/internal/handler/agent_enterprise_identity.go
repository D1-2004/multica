package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
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
	RedirectPath string `json:"redirect_path"`
}

type startAgentEnterpriseIdentityResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	ExpiresAt        int64  `json:"expires_at"`
}

const (
	// The streamed callback includes ASB creation, synchronous WireGuard
	// binding, BUC/a1 probes, snapshot persistence, and pause. Keep its
	// server-side budget beyond the complete source-establishment budget.
	enterpriseIdentityCallbackTimeout   = 15 * time.Minute
	enterpriseIdentityCallbackHeartbeat = 2 * time.Second
)

type enterpriseIdentityCallbackResult struct {
	result service.CompleteEnterpriseIdentityBindingResult
	err    error
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
	if identity.Status == "revoked" {
		writeJSON(w, http.StatusOK, agentEnterpriseIdentityStatusResponse{
			Configured: true,
			CanManage:  canManage,
			Identity:   nil,
		})
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
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "enterprise identity authorization could not be completed")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "enterprise identity callback streaming is unavailable")
		return
	}
	nonce, err := enterpriseIdentityCallbackNonce()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "enterprise identity callback could not be initialized")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", fmt.Sprintf(
		"default-src 'none'; script-src 'nonce-%s'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'",
		nonce,
	))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	clientConnected := true
	if _, err := io.WriteString(w, enterpriseIdentityCallbackOpeningPage(nonce)); err != nil {
		clientConnected = false
	} else {
		flusher.Flush()
	}

	// BUC redirects are browser-facing, while token exchange, AuthX rotation,
	// and Idem registration are server-side operations. Do not let a mobile
	// browser navigation cancel an already consumed one-time OAuth callback.
	bindingCtx, cancel := context.WithTimeout(
		context.WithoutCancel(r.Context()),
		enterpriseIdentityCallbackTimeout,
	)
	defer cancel()
	resultCh := make(chan enterpriseIdentityCallbackResult, 1)
	go func() {
		result, completeErr := h.EnterpriseIdentity.CompleteBinding(bindingCtx, state, code)
		resultCh <- enterpriseIdentityCallbackResult{result: result, err: completeErr}
	}()

	heartbeat := time.NewTicker(enterpriseIdentityCallbackHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case outcome := <-resultCh:
			if outcome.err != nil {
				slog.Warn(
					"enterprise identity OAuth callback failed",
					append(
						logger.RequestAttrs(r),
						"error_type",
						fmt.Sprintf("%T", outcome.err),
					)...,
				)
				if clientConnected {
					_, _ = io.WriteString(w, enterpriseIdentityCallbackFailurePage(nonce))
					flusher.Flush()
				}
				return
			}
			target, targetErr := url.Parse(outcome.result.RedirectPath)
			if targetErr != nil || target.IsAbs() || !strings.HasPrefix(target.Path, "/") {
				slog.Warn(
					"enterprise identity OAuth callback produced invalid redirect",
					logger.RequestAttrs(r)...,
				)
				if clientConnected {
					_, _ = io.WriteString(w, enterpriseIdentityCallbackFailurePage(nonce))
					flusher.Flush()
				}
				return
			}
			query := target.Query()
			query.Set("enterprise_identity", "connected")
			target.RawQuery = query.Encode()
			if clientConnected {
				_, _ = io.WriteString(w, enterpriseIdentityCallbackSuccessPage(nonce, target.String()))
				flusher.Flush()
			}
			return
		case <-heartbeat.C:
			if !clientConnected {
				continue
			}
			if _, err := io.WriteString(w, "<!-- enterprise-identity-callback-heartbeat -->\n"); err != nil {
				clientConnected = false
				continue
			}
			flusher.Flush()
		case <-bindingCtx.Done():
			slog.Warn(
				"enterprise identity OAuth callback timed out",
				append(
					logger.RequestAttrs(r),
					"timeout_seconds",
					int64(enterpriseIdentityCallbackTimeout/time.Second),
				)...,
			)
			if clientConnected {
				_, _ = io.WriteString(w, enterpriseIdentityCallbackFailurePage(nonce))
				flusher.Flush()
			}
			return
		}
	}
}

func enterpriseIdentityCallbackNonce() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func enterpriseIdentityCallbackOpeningPage(nonce string) string {
	// The padding makes the first response chunk larger than common ingress
	// proxy buffers, so the browser receives the progress page immediately.
	padding := strings.Repeat(" ", 4096)
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>正在绑定员工身份</title>
<style>
body{margin:0;background:#f7f8fa;color:#171a1f;font:16px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
main{max-width:420px;margin:18vh auto;padding:32px 24px;text-align:center}
.spinner{width:36px;height:36px;margin:0 auto 22px;border:3px solid #e5e7eb;border-top-color:#ff6a00;border-radius:50%%;animation:spin .8s linear infinite}
h1{font-size:20px;margin:0 0 12px}p{color:#5f6672;margin:0}
@keyframes spin{to{transform:rotate(360deg)}}
</style>
</head>
<body>
<main>
<div class="spinner" aria-hidden="true"></div>
<h1 id="callback-title">正在绑定员工身份</h1>
<p id="callback-message">授权已接收，正在安全保存并校验员工身份凭证，请不要返回或重复点击。</p>
</main>
<script nonce="%s">window.history.replaceState(null,"",window.location.pathname);</script>
<!-- %s -->
`, nonce, padding)
}

func enterpriseIdentityCallbackFailurePage(nonce string) string {
	return fmt.Sprintf(`<script nonce="%s">
document.getElementById("callback-title").textContent="员工身份绑定未完成";
document.getElementById("callback-message").textContent="请返回 Multica 后重新发起绑定。";
</script>
</body></html>`, nonce)
}

func enterpriseIdentityCallbackSuccessPage(nonce string, target string) string {
	targetJSON, _ := json.Marshal(target)
	return fmt.Sprintf(`<script nonce="%s">
document.getElementById("callback-title").textContent="员工身份绑定成功";
document.getElementById("callback-message").textContent="正在返回 Multica…";
window.location.replace(%s);
</script>
<noscript><a href="%s">返回 Multica</a></noscript>
</body></html>`, nonce, targetJSON, html.EscapeString(target))
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
