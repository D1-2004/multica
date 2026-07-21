package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// DingTalk bot installation endpoints — the scan-to-create device flow
// ("一键创建钉钉应用扫码接入"). Mirrors the Lark install surface in
// lark.go: member-visible listing, admin-gated begin/status/revoke.

// DingTalkInstallationResponse is the wire shape for an installation
// row. The encrypted client_secret is INTENTIONALLY absent — the only
// consumer that needs the plaintext is the (future) inbound transport,
// which decrypts server-side.
type DingTalkInstallationResponse struct {
	ID                 string `json:"id"`
	WorkspaceID        string `json:"workspace_id"`
	AgentID            string `json:"agent_id"`
	ClientID           string `json:"client_id"`
	RobotCode          string `json:"robot_code"`
	InstallerUserID    string `json:"installer_user_id"`
	Status             string `json:"status"`
	InstalledAt        string `json:"installed_at"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
	TransportMode      string `json:"transport_mode"`
	ConnectionManaged  bool   `json:"connection_managed"`
	RouterStatus       string `json:"router_status,omitempty"`
	RouterLastError    string `json:"router_last_error,omitempty"`
	RegistrationStatus string `json:"registration_status,omitempty"`
}

func dingTalkInstallationToResponse(row dingtalk.Installation) DingTalkInstallationResponse {
	return DingTalkInstallationResponse{
		ID:                 uuidToString(row.ID),
		WorkspaceID:        uuidToString(row.WorkspaceID),
		AgentID:            uuidToString(row.AgentID),
		ClientID:           row.ClientID,
		RobotCode:          row.RobotCode,
		InstallerUserID:    uuidToString(row.InstallerUserID),
		Status:             row.Status,
		InstalledAt:        row.InstalledAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:          row.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:          row.UpdatedAt.Time.UTC().Format(time.RFC3339),
		TransportMode:      string(row.TransportMode),
		ConnectionManaged:  row.ConnectionManaged,
		RouterStatus:       row.RouterStatus,
		RouterLastError:    row.RouterLastError,
		RegistrationStatus: row.RegistrationStatus,
	}
}

func dingTalkInstallCapabilities(registration *dingtalk.RegistrationService) map[string]any {
	if registration == nil {
		return map[string]any{
			"http_callback": map[string]any{"available": false, "reason": "install_not_configured"},
		}
	}
	caps := registration.Capabilities()
	return map[string]any{
		"http_callback": map[string]any{
			"available": caps.HTTPCallbackAvailable,
			"reason":    caps.HTTPCallbackReason,
		},
	}
}

// ListDingTalkInstallations (GET /api/workspaces/{id}/dingtalk/installations)
// is member-visible — the Integrations tab should not render blank for
// non-admins.
//
// Response fields:
//   - configured: at-rest encryption key is set
//     (`DingTalkInstallations != nil`). When false, no install flow can
//     succeed at all; the UI hides the panel's actions.
//   - install_supported: the device-flow install path is wired
//     end-to-end (a RegistrationService exists). When false, the
//     agent-detail "Bind" button stays hidden and the Settings tab
//     surfaces a "coming soon" notice; already-installed bots still
//     appear and remain manageable.
func (h *Handler) ListDingTalkInstallations(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkInstallations == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []DingTalkInstallationResponse{},
			"configured":        false,
			"install_supported": false,
			"capabilities":      dingTalkInstallCapabilities(nil),
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.DingTalkInstallations.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list dingtalk installations")
		return
	}
	out := make([]DingTalkInstallationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, dingTalkInstallationToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": h.DingTalkRegistration != nil,
		"capabilities":      dingTalkInstallCapabilities(h.DingTalkRegistration),
	})
}

// RevokeDingTalkInstallation (DELETE /api/workspaces/{id}/dingtalk/installations/{installationId})
// flips status to 'revoked'. The row itself is preserved for audit; a
// re-install via the device-flow path flips status back to 'active'.
func (h *Handler) RevokeDingTalkInstallation(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkInstallations == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	instUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return
	}
	// Workspace-scoped lookup ensures one workspace cannot revoke
	// another's installation by guessing the UUID.
	if _, err := h.DingTalkInstallations.GetInWorkspace(r.Context(), instUUID, wsUUID); err != nil {
		if errors.Is(err, dingtalk.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "dingtalk installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	if err := h.DingTalkInstallations.Revoke(r.Context(), instUUID); err != nil {
		if errors.Is(err, dingtalk.ErrRouterUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "agent message router is temporarily unavailable")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventDingTalkInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RetryDingTalkRouterRegistration retries only the Multica -> Router source
// registration for an existing HTTP callback installation. It never starts a
// new DingTalk scan and never changes robot credentials or transport.
func (h *Handler) RetryDingTalkRouterRegistration(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkRegistration == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk install not configured")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	instUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return
	}
	inst, err := h.DingTalkRegistration.RetryHTTPCallbackRouter(r.Context(), wsUUID, instUUID)
	if err != nil {
		if errors.Is(err, dingtalk.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "dingtalk installation not found")
		} else {
			writeError(w, http.StatusBadGateway, "failed to register dingtalk router source")
		}
		return
	}
	writeJSON(w, http.StatusOK, dingTalkInstallationToResponse(inst))
}

// BeginDingTalkInstallResponse is the payload the QR-code dialog
// consumes. The frontend renders `qr_code_url` as a QR image (and as a
// tap-to-open link fallback) and starts polling
// /dingtalk/install/{session_id}/status at the supplied cadence.
type BeginDingTalkInstallResponse struct {
	SessionID           string `json:"session_id"`
	QRCodeURL           string `json:"qr_code_url"`
	ExpiresInSeconds    int    `json:"expires_in_seconds"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

// BeginDingTalkInstall (POST /api/workspaces/{id}/dingtalk/install/begin)
// opens a new device-flow registration session against DingTalk.
// Admin-only at the router. The agent_id query param picks which
// Multica Agent the new app will be bound to; the agent must belong to
// this workspace (RegistrationService re-checks that defense-in-depth).
//
// Returns 503 when the integration is not wired; the UI hides the bind
// button in that case so this should not be reached through the normal
// flow.
func (h *Handler) BeginDingTalkInstall(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkRegistration == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk install not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentIDStr := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentIDStr == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}
	// Ownership pre-check at the HTTP boundary so a malformed agent_id
	// surfaces 404 here (not an opaque service error from inside the
	// service's own re-check).
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "agent not found in this workspace")
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	// allow_unbound opts the new bot into "connect to customers" mode: an
	// unbound / non-member sender is served as the installer rather than
	// prompted to bind. Absent / non-"true" = the default bind-first bot.
	allowUnbound := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("allow_unbound")), "true")
	transportMode := dingtalk.TransportMode(strings.TrimSpace(r.URL.Query().Get("transport_mode")))

	res, err := h.DingTalkRegistration.BeginInstall(r.Context(), dingtalk.BeginInstallParams{
		WorkspaceID:   wsUUID,
		AgentID:       agentUUID,
		InitiatorID:   initiatorUUID,
		AllowUnbound:  allowUnbound,
		TransportMode: transportMode,
	})
	if err != nil {
		slog.Warn("dingtalk install begin failed",
			"workspace_id", uuidToString(wsUUID),
			"agent_id", uuidToString(agentUUID),
			"transport_mode", string(transportMode),
			"allow_unbound", allowUnbound,
			"error", err,
		)
		var registrationErr *dingtalk.RegistrationError
		if errors.As(err, &registrationErr) {
			switch registrationErr.Code {
			case "invalid_transport_mode":
				writeError(w, http.StatusBadRequest, registrationErr.Error())
			case "http_callback_unavailable":
				writeError(w, http.StatusServiceUnavailable, registrationErr.Error())
			default:
				writeError(w, http.StatusBadGateway, "failed to start install: "+err.Error())
			}
			return
		}
		writeError(w, http.StatusBadGateway, "failed to start install: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, BeginDingTalkInstallResponse{
		SessionID:           res.SessionID,
		QRCodeURL:           res.QRCodeURL,
		ExpiresInSeconds:    res.ExpiresInSeconds,
		PollIntervalSeconds: res.PollIntervalSeconds,
	})
}

// DingTalkInstallStatusResponse is the polling payload. `status` is one
// of "pending" | "approving" | "success" | "error"; on success or
// approving `installation_id` is populated, on error `error_reason` is a stable code (see
// dingtalk.RegistrationReason*).
type DingTalkInstallStatusResponse struct {
	Status         string `json:"status"`
	InstallationID string `json:"installation_id,omitempty"`
	ErrorReason    string `json:"error_reason,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
}

func dingTalkInstallStatusToResponse(state dingtalk.RegistrationSessionState) DingTalkInstallStatusResponse {
	status := string(state.Status)
	if state.Status == dingtalk.RegistrationStatusSuccess && state.RegistrationStatus == "APPROVING" {
		status = "approving"
	}
	resp := DingTalkInstallStatusResponse{
		Status:       status,
		ErrorReason:  state.ErrorReason,
		ErrorMessage: state.ErrorMessage,
	}
	if state.InstallationID.Valid {
		resp.InstallationID = uuidToString(state.InstallationID)
	}
	return resp
}

// GetDingTalkInstallStatus (GET /api/workspaces/{id}/dingtalk/install/{sessionId}/status)
// returns the current state of an in-flight install session.
// Admin-only at the router. Unknown / cross-workspace / GC'd sessions
// return 404 — the frontend treats it as "session lost, please restart".
func (h *Handler) GetDingTalkInstallStatus(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkRegistration == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk install not configured")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(chi.URLParam(r, "sessionId"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id is required")
		return
	}
	state, err := h.DingTalkRegistration.GetSession(r.Context(), wsUUID, sessionID)
	if err != nil {
		if errors.Is(err, dingtalk.ErrRegistrationSessionNotFound) {
			writeError(w, http.StatusNotFound, "install session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load install session")
		return
	}
	// The dingtalk_installation:created event is published by the
	// RegistrationService at the row-commit point, not here.
	resp := dingTalkInstallStatusToResponse(state)
	writeJSON(w, http.StatusOK, resp)
}

// ManualInstallDingTalkRequest carries the operator-supplied app
// credentials for the manual install path — the fallback for when the
// scan-to-create device flow ("一键创建") is unavailable (the DingTalk
// registration endpoints are down, or the deployment never wired the
// device flow at all). The operator creates the app themselves in the
// DingTalk developer console and pastes its AppKey / AppSecret here.
type ManualInstallDingTalkRequest struct {
	// AgentID is the Multica agent the new app binds to; must belong to
	// this workspace.
	AgentID string `json:"agent_id"`
	// ClientID is the DingTalk AppKey.
	ClientID string `json:"client_id"`
	// ClientSecret is the DingTalk AppSecret; encrypted at rest inside
	// InstallationService.Upsert, so it never persists as plaintext.
	ClientSecret string `json:"client_secret"`
	// RobotCode is the distinct robot receiver identifier; it must not be
	// inferred from client_id.
	RobotCode string `json:"robot_code"`
	// AllowUnbound opts the bot into "serve unbound senders as the
	// installer" mode — same semantics as the device-flow toggle.
	AllowUnbound bool `json:"allow_unbound"`
}

// ManualInstallDingTalk (POST /api/workspaces/{id}/dingtalk/install/manual)
// creates an installation directly from operator-supplied credentials,
// bypassing the device flow. Admin-only at the router. It is available
// whenever DingTalk is configured (the at-rest key is set) regardless of
// whether the device-flow RegistrationService is wired — that is the
// point: it keeps a workspace unblocked when the scan flow is broken.
//
// When a credential verifier is wired it exchanges the pair for an app
// access token first, so a typo'd AppKey/AppSecret surfaces as a clean
// error here instead of a silently dead bot later.
func (h *Handler) ManualInstallDingTalk(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkInstallations == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	var req ManualInstallDingTalkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	agentIDStr := strings.TrimSpace(req.AgentID)
	clientID := strings.TrimSpace(req.ClientID)
	clientSecret := strings.TrimSpace(req.ClientSecret)
	robotCode := strings.TrimSpace(req.RobotCode)
	if agentIDStr == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if clientID == "" {
		writeError(w, http.StatusBadRequest, "client_id is required")
		return
	}
	if clientSecret == "" {
		writeError(w, http.StatusBadRequest, "client_secret is required")
		return
	}
	if robotCode == "" {
		writeError(w, http.StatusBadRequest, "robot_code is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}
	// Ownership check at the boundary: a workspace admin must not bind an
	// app to another workspace's agent by guessing its UUID.
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "agent not found in this workspace")
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	// Verify the operator-supplied credentials before persisting so a
	// wrong AppKey/AppSecret is caught here, not at the first inbound
	// message. Skipped when no verifier is wired (offline / test builds).
	if h.DingTalkCredentialVerifier != nil {
		if err := h.DingTalkCredentialVerifier.VerifyAppCredentials(r.Context(), clientID, clientSecret); err != nil {
			writeError(w, http.StatusBadGateway, "dingtalk credentials check failed: "+err.Error())
			return
		}
	}

	inst, err := h.DingTalkInstallations.Upsert(r.Context(), dingtalk.InstallationParams{
		WorkspaceID:     wsUUID,
		AgentID:         agentUUID,
		ClientID:        clientID,
		ClientSecret:    clientSecret,
		RobotCode:       robotCode,
		InstallerUserID: initiatorUUID,
		AllowUnbound:    req.AllowUnbound,
	})
	if err != nil {
		switch {
		case errors.Is(err, dingtalk.ErrAppOwnedByAnotherWorkspace):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, dingtalk.ErrAgentAlreadyConnected):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, dingtalk.ErrRouterUnavailable):
			writeError(w, http.StatusServiceUnavailable, "agent message router is temporarily unavailable")
		default:
			writeError(w, http.StatusInternalServerError, "failed to save dingtalk installation")
		}
		return
	}
	// Mirror the device-flow success broadcast so every workspace client
	// refreshes its connection badge the moment the row commits.
	h.publish(protocol.EventDingTalkInstallationCreated, uuidToString(wsUUID), "user", userID, map[string]any{
		"installation_id": uuidToString(inst.ID),
	})
	writeJSON(w, http.StatusOK, dingTalkInstallationToResponse(inst))
}

// RedeemDingTalkBindingTokenRequest carries the raw token the user clicked
// through from the bot's "link your account" prompt.
type RedeemDingTalkBindingTokenRequest struct {
	Token string `json:"token"`
}

// RedeemDingTalkBindingTokenResponse echoes the bound workspace/installation/
// user so the frontend can confirm without a second fetch.
type RedeemDingTalkBindingTokenResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	DingTalkUserID string `json:"dingtalk_user_id"`
	// AgentName is the bound bot's display name for the confirmation copy;
	// empty when unavailable.
	AgentName string `json:"agent_name,omitempty"`
}

// RedeemDingTalkBindingToken (POST /api/dingtalk/binding/redeem) binds the
// DingTalk user id carried by the token to the logged-in Multica user. The
// redeemer's identity comes from the session, not the token, so a stolen
// token cannot bind a DingTalk id to an attacker's account. Failure modes
// map to distinct status codes (mirrors the Slack/Lark redeem endpoints):
//   - 410 Gone:      token unknown / consumed / expired
//   - 409 Conflict:  this DingTalk id is already bound to a different user
//   - 403 Forbidden: redeemer is not a workspace member
func (h *Handler) RedeemDingTalkBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkBindingTokens == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemDingTalkBindingTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}

	redeemed, err := h.DingTalkBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, dingtalk.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, dingtalk.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this DingTalk account is already bound to a different Multica user")
		case errors.Is(err, dingtalk.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}
	writeJSON(w, http.StatusOK, RedeemDingTalkBindingTokenResponse{
		WorkspaceID:    uuidToString(redeemed.WorkspaceID),
		InstallationID: uuidToString(redeemed.InstallationID),
		DingTalkUserID: redeemed.DingTalkUserID,
		AgentName:      redeemed.AgentName,
	})
}
