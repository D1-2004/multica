package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/fdebootstrap"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/workspaceprovision"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fdeBootstrapLinkResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

type fdeBootstrapStatusResponse struct {
	Status string `json:"status"`
}

type dingtalkBootstrapBindingConfig struct {
	SenderStaffID string `json:"sender_staff_id"`
}

// CreateFDEBootstrapIntent is deliberately agent-only. The task-token auth
// middleware stamps every source identifier below from the token row, so none
// of the DingTalk chat or installation context is accepted from request JSON.
func (h *Handler) CreateFDEBootstrapIntent(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "this endpoint is only available to an active agent task")
		return
	}
	if h.DingTalkInstallations == nil || h.DingTalkBootstrapDirectory == nil {
		writeError(w, http.StatusServiceUnavailable, "DingTalk bootstrap is not configured")
		return
	}
	base := strings.TrimRight(h.cfg.AppURL, "/")
	if base == "" {
		writeError(w, http.StatusServiceUnavailable, "FDE workspace links are not configured")
		return
	}

	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Agent-ID"), "agent id")
	if !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Workspace-ID"), "workspace id")
	if !ok {
		return
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskID)
	if err != nil || task.AgentID != agentID || !task.ChatSessionID.Valid {
		writeError(w, http.StatusForbidden, "the current task is not a DingTalk chat task")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), agentID)
	if err != nil || agent.WorkspaceID != workspaceID {
		writeError(w, http.StatusForbidden, "the current task workspace is invalid")
		return
	}
	binding, err := h.Queries.GetChannelChatSessionBindingBySession(r.Context(), db.GetChannelChatSessionBindingBySessionParams{
		ChatSessionID: task.ChatSessionID,
		ChannelType:   "dingtalk",
	})
	if err != nil || binding.ChatType != "p2p" {
		writeError(w, http.StatusForbidden, "FDE workspace creation is only available in a DingTalk single chat")
		return
	}

	inst, err := h.DingTalkInstallations.GetInWorkspace(r.Context(), binding.InstallationID, workspaceID)
	if err != nil || inst.Status != string(dingtalk.InstallationActive) || inst.AgentID != agentID {
		writeError(w, http.StatusForbidden, "the DingTalk installation for this chat is invalid")
		return
	}
	var bindingCfg dingtalkBootstrapBindingConfig
	if err := json.Unmarshal(binding.Config, &bindingCfg); err != nil || strings.TrimSpace(bindingCfg.SenderStaffID) == "" {
		writeError(w, http.StatusConflict, "the DingTalk sender identity is unavailable")
		return
	}
	clientSecret, err := h.DingTalkInstallations.DecryptClientSecret(inst)
	if err != nil {
		slog.Error("fde bootstrap: decrypt DingTalk installation failed", "installation_id", uuidToString(inst.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace link")
		return
	}
	unionID, err := h.DingTalkBootstrapDirectory.LookupInstallationUserUnionID(r.Context(), inst.ClientID, clientSecret, bindingCfg.SenderStaffID)
	if err != nil {
		slog.Warn("fde bootstrap: DingTalk identity lookup failed", "installation_id", uuidToString(inst.ID), "error", err)
		writeError(w, http.StatusBadGateway, "failed to verify the DingTalk sender")
		return
	}
	expectedIdentity, err := fdebootstrap.IdentityHMAC(unionID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "the DingTalk sender has no stable identity")
		return
	}
	token, err := fdebootstrap.GenerateToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace link")
		return
	}
	expiresAt := time.Now().Add(fdebootstrap.IntentTTL)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace link")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	// Directory lookup stays outside the transaction. Before persisting the
	// identity-bound link, re-read every mutable routing edge so an install
	// move/revoke or session retirement during the network call fails closed.
	currentTask, taskErr := qtx.GetAgentTask(r.Context(), taskID)
	currentBinding, bindingErr := qtx.GetChannelChatSessionBindingBySession(r.Context(), db.GetChannelChatSessionBindingBySessionParams{
		ChatSessionID: task.ChatSessionID,
		ChannelType:   "dingtalk",
	})
	currentInstallation, installationErr := qtx.GetChannelInstallationInWorkspace(r.Context(), db.GetChannelInstallationInWorkspaceParams{
		ID: binding.InstallationID, WorkspaceID: workspaceID, ChannelType: "dingtalk",
	})
	var currentBindingCfg dingtalkBootstrapBindingConfig
	currentBindingConfigErr := json.Unmarshal(currentBinding.Config, &currentBindingCfg)
	if taskErr != nil || bindingErr != nil || installationErr != nil ||
		currentBindingConfigErr != nil || strings.TrimSpace(currentBindingCfg.SenderStaffID) != strings.TrimSpace(bindingCfg.SenderStaffID) ||
		currentTask.AgentID != agentID || currentTask.ChatSessionID != task.ChatSessionID ||
		currentBinding.ID != binding.ID || currentBinding.InstallationID != inst.ID || currentBinding.ChatType != "p2p" ||
		currentInstallation.AgentID != agentID || currentInstallation.Status != "active" {
		writeError(w, http.StatusConflict, "the DingTalk chat changed; please request a new link")
		return
	}
	if err := qtx.ExpirePendingFDEBootstrapIntentsBySourceTask(r.Context(), taskID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace link")
		return
	}
	if _, err := qtx.CreateFDEBootstrapIntent(r.Context(), db.CreateFDEBootstrapIntentParams{
		TokenHash:            fdebootstrap.HashToken(token),
		ExpectedIdentityHmac: expectedIdentity,
		SourceWorkspaceID:    workspaceID,
		SourceAgentID:        agentID,
		SourceTaskID:         taskID,
		SourceChatSessionID:  task.ChatSessionID,
		SourceInstallationID: inst.ID,
		ExpiresAt:            pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace link")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace link")
		return
	}

	writeJSON(w, http.StatusCreated, fdeBootstrapLinkResponse{
		URL:       base + "/fde/bootstrap/" + url.PathEscape(token),
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
}

// GetFDEBootstrapIntentStatus intentionally reveals no workspace or user data.
// Unknown, malformed, and expired tokens share the same terminal status.
func (h *Handler) GetFDEBootstrapIntentStatus(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if !strings.HasPrefix(token, fdebootstrap.TokenPrefix) {
		writeJSON(w, http.StatusOK, fdeBootstrapStatusResponse{Status: "expired"})
		return
	}
	hash := fdebootstrap.HashToken(token)
	_ = h.Queries.ExpireFDEBootstrapIntentIfNeeded(r.Context(), hash)
	intent, err := h.Queries.GetFDEBootstrapIntentByTokenHash(r.Context(), hash)
	if err != nil || intent.Status == "expired" || !intent.ExpiresAt.Valid || !time.Now().Before(intent.ExpiresAt.Time) {
		writeJSON(w, http.StatusOK, fdeBootstrapStatusResponse{Status: "expired"})
		return
	}
	writeJSON(w, http.StatusOK, fdeBootstrapStatusResponse{Status: "authentication_required"})
}

// CompleteFDEBootstrapIntent consumes the DingTalk OAuth identity assertion
// and creates (or reuses) the one fde-agent product workspace for this user.
func (h *Handler) CompleteFDEBootstrapIntent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID := parseUUID(userID)
	proofCookie, err := r.Cookie(auth.FDEBootstrapIdentityCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "DingTalk authentication is required")
		return
	}
	proof, err := fdebootstrap.ParseIdentityAssertion(proofCookie.Value, time.Now())
	if err != nil || proof.UserID != userID {
		auth.ClearFDEBootstrapIdentityCookie(w)
		writeError(w, http.StatusUnauthorized, "DingTalk authentication is required")
		return
	}
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if !strings.HasPrefix(token, fdebootstrap.TokenPrefix) {
		writeError(w, http.StatusGone, "this FDE workspace link has expired")
		return
	}
	tokenHash := fdebootstrap.HashToken(token)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(r.Context())
		}
	}()
	qtx := h.Queries.WithTx(tx)
	intent, err := qtx.LockFDEBootstrapIntentByTokenHash(r.Context(), tokenHash)
	if err != nil || !intent.ExpiresAt.Valid || !time.Now().Before(intent.ExpiresAt.Time) || intent.Status == "expired" {
		writeError(w, http.StatusGone, "this FDE workspace link has expired")
		return
	}
	if !fdebootstrap.IdentityMatches(intent.ExpectedIdentityHmac, proof.IdentityHMAC) {
		writeError(w, http.StatusForbidden, "this link belongs to a different DingTalk user")
		return
	}
	if intent.Status == "ready" {
		if !intent.UserID.Valid || intent.UserID != userUUID {
			writeError(w, http.StatusForbidden, "this link belongs to a different DingTalk user")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to confirm FDE workspace")
			return
		}
		committed = true
		auth.ClearFDEBootstrapIdentityCookie(w)
		writeJSON(w, http.StatusOK, fdeBootstrapStatusResponse{Status: "ready"})
		return
	}
	if intent.UserID.Valid && intent.UserID != userUUID {
		writeError(w, http.StatusForbidden, "this link belongs to a different Multica user")
		return
	}
	if _, err := qtx.MarkFDEBootstrapIntentProvisioning(r.Context(), db.MarkFDEBootstrapIntentProvisioningParams{ID: intent.ID, UserID: userUUID}); err != nil {
		writeError(w, http.StatusConflict, "this FDE workspace link cannot be used")
		return
	}
	if err := qtx.InsertProductWorkspaceProvisioning(r.Context(), db.InsertProductWorkspaceProvisioningParams{
		UserID: userUUID, ProductKey: fdebootstrap.ProductKey,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace")
		return
	}
	product, err := qtx.LockProductWorkspaceProvisioning(r.Context(), db.LockProductWorkspaceProvisioningParams{
		UserID: userUUID, ProductKey: fdebootstrap.ProductKey,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace")
		return
	}

	workspaceID := product.WorkspaceID
	created := false
	if product.Status != "ready" || !workspaceID.Valid {
		workspace, provisionErr := workspaceprovision.Create(r.Context(), qtx, workspaceprovision.Params{
			Name:        "FDE Workspace",
			Slug:        "fde-" + randomID(),
			Description: pgtype.Text{String: "Workspace created by the Multica FDE bootstrap flow.", Valid: true},
			IssuePrefix: workspaceprovision.GenerateIssuePrefix("FDE Workspace"),
			OwnerID:     userUUID,
		})
		if provisionErr != nil {
			slog.Error("fde bootstrap: workspace provisioning failed", "intent_id", uuidToString(intent.ID), "user_id", userID, "error", provisionErr)
			writeError(w, http.StatusInternalServerError, "failed to create FDE workspace; please retry")
			return
		}
		workspaceID = workspace.ID
		created = true
		if _, err := qtx.MarkProductWorkspaceProvisioningReady(r.Context(), db.MarkProductWorkspaceProvisioningReadyParams{
			UserID: userUUID, ProductKey: fdebootstrap.ProductKey, WorkspaceID: workspaceID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create FDE workspace; please retry")
			return
		}
	}
	if _, err := qtx.MarkFDEBootstrapIntentReady(r.Context(), db.MarkFDEBootstrapIntentReadyParams{
		ID: intent.ID, UserID: userUUID, WorkspaceID: workspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace; please retry")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create FDE workspace; please retry")
		return
	}
	committed = true
	if created {
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.WorkspaceCreated(userID, uuidToString(workspaceID)))
		h.notifyDaemonWorkspacesChanged(userID)
	}
	auth.ClearFDEBootstrapIdentityCookie(w)
	writeJSON(w, http.StatusOK, fdeBootstrapStatusResponse{Status: "ready"})
}
