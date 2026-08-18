package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"os"
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
	Configured     bool                                   `json:"configured"`
	CanManage      bool                                   `json:"can_manage"`
	BindingVersion int64                                  `json:"binding_version"`
	BindingAttempt *agentEnterpriseIdentityBindingAttempt `json:"binding_attempt,omitempty"`
	Identity       *agentEnterpriseIdentityConnection     `json:"identity"`
}

type agentEnterpriseIdentityBindingAttempt struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
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

type rotateAgentEnterpriseIdentitySourceRequest struct {
	AgentID                   string `json:"agent_id"`
	ExpectedPreviousSandboxID string `json:"expected_previous_sandbox_id"`
}

type rotateAgentEnterpriseIdentitySourceAcceptedResponse struct {
	Status                    string `json:"status"`
	ExpectedPreviousSandboxID string `json:"expected_previous_sandbox_id"`
}

const (
	enterpriseIdentityCallbackTimeout       = 15 * time.Minute
	enterpriseIdentityCallbackPollInterval  = 2 * time.Second
	enterpriseIdentitySourceRotationTimeout = 5 * time.Minute
)

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
	bindingAttempt, ok := h.loadAgentEnterpriseIdentityBindingAttempt(
		w,
		r,
		workspaceID,
		agent.ID,
		canManage,
	)
	if !ok {
		return
	}
	if h.EnterpriseIdentity == nil {
		writeJSON(w, http.StatusOK, agentEnterpriseIdentityStatusResponse{
			Configured:     false,
			CanManage:      canManage,
			BindingAttempt: bindingAttempt,
			Identity:       nil,
		})
		return
	}
	identity, err := h.Queries.GetAgentEnterpriseIdentity(r.Context(), db.GetAgentEnterpriseIdentityParams{
		WorkspaceID: workspaceID,
		AgentID:     agent.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, agentEnterpriseIdentityStatusResponse{
			Configured:     true,
			CanManage:      canManage,
			BindingAttempt: bindingAttempt,
			Identity:       nil,
		})
		return
	}
	if err != nil {
		writeEnterpriseIdentityError(w, r, "status", workspaceID, agent.ID, err)
		return
	}
	if identity.Status == "revoked" {
		writeJSON(w, http.StatusOK, agentEnterpriseIdentityStatusResponse{
			Configured:     true,
			CanManage:      canManage,
			BindingVersion: identity.TokenVersion,
			BindingAttempt: bindingAttempt,
			Identity:       nil,
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
		Configured:     true,
		CanManage:      canManage,
		BindingVersion: identity.TokenVersion,
		BindingAttempt: bindingAttempt,
		Identity:       connection,
	})
}

func (h *Handler) loadAgentEnterpriseIdentityBindingAttempt(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	canManage bool,
) (*agentEnterpriseIdentityBindingAttempt, bool) {
	attemptID := strings.TrimSpace(r.URL.Query().Get("attempt_id"))
	if attemptID == "" {
		return nil, true
	}
	if !canManage {
		writeError(w, http.StatusForbidden, "only the agent owner can view enterprise identity binding progress")
		return nil, false
	}
	attemptUUID, ok := parseUUIDOrBadRequest(w, attemptID, "attempt_id")
	if !ok {
		return nil, false
	}
	attempt, err := h.Queries.GetAgentEnterpriseIdentityAttemptStatus(
		r.Context(),
		db.GetAgentEnterpriseIdentityAttemptStatusParams{
			ID:          attemptUUID,
			WorkspaceID: workspaceID,
			AgentID:     agentID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "enterprise identity binding attempt not found")
		return nil, false
	}
	if err != nil {
		writeEnterpriseIdentityError(w, r, "binding_attempt_status", workspaceID, agentID, err)
		return nil, false
	}
	status := &agentEnterpriseIdentityBindingAttempt{
		ID:     util.UUIDToString(attempt.ID),
		Status: attempt.CompletionStatus,
	}
	if attempt.CompletionErrorCode.Valid {
		status.ErrorCode = attempt.CompletionErrorCode.String
	}
	return status, true
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

	nonce, err := enterpriseIdentityCallbackNonce()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "enterprise identity callback could not be initialized")
		return
	}
	prepared, err := h.EnterpriseIdentity.PrepareBindingCompletion(r.Context(), state)
	if err != nil {
		slog.Warn(
			"enterprise identity OAuth callback preparation failed",
			append(logger.RequestAttrs(r), "error_type", fmt.Sprintf("%T", err))...,
		)
		if errors.Is(err, service.ErrEnterpriseIdentityBindingInProgress) {
			writeError(w, http.StatusConflict, "enterprise identity binding is already in progress")
			return
		}
		writeError(w, http.StatusBadRequest, "enterprise identity authorization could not be completed")
		return
	}
	target, targetErr := url.Parse(prepared.RedirectPath)
	if targetErr != nil || target.IsAbs() || !strings.HasPrefix(target.Path, "/") {
		slog.Warn(
			"enterprise identity OAuth callback produced invalid redirect",
			logger.RequestAttrs(r)...,
		)
		writeError(w, http.StatusBadRequest, "enterprise identity authorization could not be completed")
		return
	}
	failureRedirectURL := target.String()
	query := target.Query()
	query.Set("enterprise_identity", "connected")
	target.RawQuery = query.Encode()

	previousBindingVersion := int64(0)
	identity, identityErr := h.Queries.GetAgentEnterpriseIdentity(
		r.Context(),
		db.GetAgentEnterpriseIdentityParams{
			WorkspaceID: prepared.WorkspaceID,
			AgentID:     prepared.AgentID,
		},
	)
	if identityErr == nil {
		previousBindingVersion = identity.TokenVersion
	} else if !errors.Is(identityErr, pgx.ErrNoRows) {
		writeEnterpriseIdentityError(
			w,
			r,
			"load_callback_binding_version",
			prepared.WorkspaceID,
			prepared.AgentID,
			identityErr,
		)
		return
	}
	expectedBindingVersion := previousBindingVersion + 1
	statusURL := fmt.Sprintf(
		"/api/workspaces/%s/agent-identity/enterprise/status?agent_id=%s&attempt_id=%s",
		url.PathEscape(util.UUIDToString(prepared.WorkspaceID)),
		url.QueryEscape(util.UUIDToString(prepared.AgentID)),
		url.QueryEscape(util.UUIDToString(prepared.AttemptID)),
	)

	requestAttrs := logger.RequestAttrs(r)
	bindingCtx, cancelBinding := context.WithTimeout(
		context.Background(),
		enterpriseIdentityCallbackTimeout,
	)
	go func() {
		defer cancelBinding()
		if _, completeErr := h.EnterpriseIdentity.CompletePreparedBinding(
			bindingCtx,
			prepared,
			code,
		); completeErr != nil {
			slog.Warn(
				"enterprise identity OAuth callback failed",
				append(requestAttrs, "error_type", fmt.Sprintf("%T", completeErr))...,
			)
		}
	}()

	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("Content-Security-Policy", fmt.Sprintf(
		"default-src 'none'; connect-src 'self'; script-src 'nonce-%s'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'",
		nonce,
	))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(enterpriseIdentityCallbackProgressPage(
		nonce,
		statusURL,
		target.String(),
		failureRedirectURL,
		expectedBindingVersion,
	)))
}

func enterpriseIdentityCallbackNonce() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func enterpriseIdentityCallbackProgressPage(
	nonce string,
	statusURL string,
	redirectURL string,
	failureRedirectURL string,
	expectedBindingVersion int64,
) string {
	statusURLJSON, _ := json.Marshal(statusURL)
	redirectURLJSON, _ := json.Marshal(redirectURL)
	failureRedirectURLJSON, _ := json.Marshal(failureRedirectURL)
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>正在绑定员工身份</title>
<style>
body{margin:0;background:#f7f8fa;color:#171a1f;font:16px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
main{max-width:520px;margin:14vh auto;padding:32px 24px;text-align:center}
.spinner{width:36px;height:36px;margin:0 auto 22px;border:3px solid #e5e7eb;border-top-color:#ff6a00;border-radius:50%%;animation:spin .8s linear infinite}
h1{font-size:20px;margin:0 0 12px}p{color:#5f6672;margin:0}.notice{margin-top:16px;padding:14px 16px;border-radius:10px;background:#fff4e8;color:#7c3f00;text-align:left;font-size:14px}.return-link{display:inline-block;margin-top:20px;color:#c65300;text-decoration:none;font-weight:600}.return-link:hover{text-decoration:underline}
[hidden]{display:none!important}
@keyframes spin{to{transform:rotate(360deg)}}
</style>
</head>
<body>
<main>
<div id="callback-spinner" class="spinner" aria-hidden="true"></div>
<h1 id="callback-title">正在绑定员工身份</h1>
<p id="callback-message">授权请求已提交，正在创建企业沙箱身份，通常需要 1–2 分钟。完成前请保持本页打开，不要重复发起授权。</p>
<p id="callback-notice" class="notice">如阿里钉的“集团账号权限助手”发来本次请求，请完成所有“前往授权”。本页会自动显示最终结果。</p>
<a id="callback-return" class="return-link" href="%s" hidden>返回 Multica</a>
</main>
<script nonce="%s">
window.history.replaceState(null,"",window.location.pathname);
const statusURL=%s;
const redirectURL=%s;
const failureRedirectURL=%s;
const expectedBindingVersion=%d;
const startedAt=Date.now();
function bindingFailureMessage(errorCode){
  switch(errorCode){
    case "needs_reauthorization":
      return "企业沙箱未获得期望的员工身份。请完成“集团账号权限助手”中的所有授权后再重试。";
    case "employee_conflict":
      return "当前员工与智能体已有绑定不一致。请返回 Multica 先解除原绑定。";
    case "binding_in_progress":
      return "此智能体已有一笔员工身份绑定正在处理，请等待原请求完成。";
    case "timed_out":
      return "企业沙箱身份创建超时。请返回 Multica 查看状态后再重试。";
    default:
      return "企业沙箱身份创建失败。请返回 Multica 查看状态后再重试。";
  }
}
function showBindingFailure(errorCode){
  document.getElementById("callback-spinner").hidden=true;
  document.getElementById("callback-title").textContent="员工身份绑定未完成";
  document.getElementById("callback-message").textContent=bindingFailureMessage(errorCode);
  document.getElementById("callback-notice").textContent="本次绑定已经停止，不会继续创建沙箱。";
  const returnLink=document.getElementById("callback-return");
  returnLink.href=failureRedirectURL;
  returnLink.hidden=false;
}
async function pollBinding(){
  try {
    const response=await fetch(statusURL,{credentials:"same-origin",cache:"no-store"});
    if(response.ok){
      const payload=await response.json();
      if(payload.binding_attempt?.status==="failed"){
        showBindingFailure(payload.binding_attempt.error_code);
        return;
      }
      if(payload.identity?.status==="active" && Number(payload.binding_version)>=expectedBindingVersion){
        document.getElementById("callback-title").textContent="员工身份绑定成功";
        document.getElementById("callback-message").textContent="正在返回 Multica…";
        window.location.replace(redirectURL);
        return;
      }
    }
  } catch (_) {}
  if(Date.now()-startedAt>=%d){
    showBindingFailure("timed_out");
    return;
  }
  window.setTimeout(pollBinding,%d);
}
pollBinding();
</script>
<noscript><a href="%s">返回 Multica</a></noscript>
</body></html>`,
		html.EscapeString(failureRedirectURL),
		nonce,
		statusURLJSON,
		redirectURLJSON,
		failureRedirectURLJSON,
		expectedBindingVersion,
		enterpriseIdentityCallbackTimeout.Milliseconds(),
		enterpriseIdentityCallbackPollInterval.Milliseconds(),
		html.EscapeString(failureRedirectURL),
	)
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

func (h *Handler) RotateAgentEnterpriseIdentitySource(w http.ResponseWriter, r *http.Request) {
	if !enterpriseIdentitySourceRotationEnabled() {
		http.NotFound(w, r)
		return
	}
	if h.EnterpriseIdentity == nil {
		writeError(w, http.StatusServiceUnavailable, "enterprise sandbox identity is not configured")
		return
	}
	var request rotateAgentEnterpriseIdentitySourceRequest
	if err := decodeLimitedJSON(w, r, 4<<10, &request, "invalid_request"); err != nil {
		return
	}
	workspaceID, agent, ok := h.authorizeAgentEnterpriseIdentitySourceRotation(
		w,
		r,
		strings.TrimSpace(request.AgentID),
	)
	if !ok {
		return
	}
	expectedPreviousSandboxID := strings.TrimSpace(request.ExpectedPreviousSandboxID)
	if expectedPreviousSandboxID == "" {
		writeError(w, http.StatusBadRequest, "expected_previous_sandbox_id is required")
		return
	}

	requestAttrs := logger.RequestAttrs(r)
	rotationContext, cancelRotation := context.WithTimeout(
		context.WithoutCancel(r.Context()),
		enterpriseIdentitySourceRotationTimeout,
	)
	go func() {
		defer cancelRotation()
		result, rotateErr := h.EnterpriseIdentity.ForceRotateIdentitySource(
			rotationContext,
			workspaceID,
			agent.ID,
			expectedPreviousSandboxID,
		)
		if rotateErr != nil {
			status := "failed"
			switch {
			case errors.Is(rotateErr, service.ErrEnterpriseIdentitySourceChanged):
				status = "stale_predecessor"
			case errors.Is(rotateErr, service.ErrEnterpriseIdentityNeedsReauth):
				status = "needs_reauthorization"
			case errors.Is(rotateErr, context.DeadlineExceeded):
				status = "timed_out"
			}
			slog.Warn(
				"ASB enterprise identity source force rotation failed",
				append(requestAttrs,
					"workspace_id", util.UUIDToString(workspaceID),
					"agent_id", util.UUIDToString(agent.ID),
					"expected_predecessor_sandbox_id", expectedPreviousSandboxID,
					"status", status,
				)...,
			)
			return
		}
		slog.Info(
			"ASB enterprise identity source force rotation completed",
			append(requestAttrs,
				"workspace_id", util.UUIDToString(workspaceID),
				"agent_id", util.UUIDToString(agent.ID),
				"previous_runtime_id", util.UUIDToString(result.PreviousRuntimeID),
				"previous_sandbox_id", result.PreviousSandboxID,
				"runtime_id", util.UUIDToString(result.RuntimeID),
				"sandbox_id", result.SandboxID,
				"affected_references", result.AffectedReferences,
			)...,
		)
	}()

	slog.Info(
		"ASB enterprise identity source force rotation accepted",
		append(requestAttrs,
			"workspace_id", util.UUIDToString(workspaceID),
			"agent_id", util.UUIDToString(agent.ID),
			"expected_predecessor_sandbox_id", expectedPreviousSandboxID,
		)...,
	)
	writeJSON(w, http.StatusAccepted, rotateAgentEnterpriseIdentitySourceAcceptedResponse{
		Status:                    "accepted",
		ExpectedPreviousSandboxID: expectedPreviousSandboxID,
	})
}

func enterpriseIdentitySourceRotationEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AONE_ENV_TYPE"))) {
	case "pre", "prepub", "staging":
		return true
	default:
		return false
	}
}

func (h *Handler) authorizeAgentEnterpriseIdentitySourceRotation(
	w http.ResponseWriter,
	r *http.Request,
	agentID string,
) (pgtype.UUID, db.Agent, bool) {
	workspaceID, agent, _, member, ok := h.loadAgentEnterpriseIdentityTarget(w, r, agentID)
	if !ok {
		return pgtype.UUID{}, db.Agent{}, false
	}
	if !roleAllowed(member.Role, "owner", "admin") {
		writeError(w, http.StatusForbidden, "only workspace owners or admins can rotate enterprise identity sources")
		return pgtype.UUID{}, db.Agent{}, false
	}
	return workspaceID, agent, true
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
	case errors.Is(err, service.ErrEnterpriseIdentityBindingInProgress):
		writeError(w, http.StatusConflict, "enterprise identity binding is already in progress")
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
