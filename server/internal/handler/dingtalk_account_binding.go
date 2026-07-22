package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	maxDingTalkAccountCallbackBodyBytes = 1 << 20
)

type dingTalkAccountBindingService interface {
	Begin(context.Context, agentmessagerouter.BeginParams) (agentmessagerouter.BeginResult, error)
	List(context.Context, pgtype.UUID) ([]agentmessagerouter.PublicDingTalkAccountBinding, error)
	CompleteBinding(context.Context, agentmessagerouter.CompleteBindingParams) (agentmessagerouter.CompleteBindingResult, error)
	UpdateSurface(context.Context, agentmessagerouter.UpdateSurfaceParams) (agentmessagerouter.PublicDingTalkAccountBinding, error)
	Unbind(context.Context, agentmessagerouter.UnbindParams) (agentmessagerouter.PublicDingTalkAccountBinding, error)
}

type beginDingTalkAccountBindingRequest struct {
	AgentID     string                          `json:"agent_id"`
	BindingMode agentmessagerouter.BindingMode `json:"binding_mode"`
}

type dingTalkAccountBindingCallbackRequest struct {
	BindingMode     agentmessagerouter.BindingMode          `json:"binding_mode"`
	Status          string                                   `json:"status"`
	IdentityBinding agentmessagerouter.IdentityBindingResult `json:"identity_binding"`
	MessageBinding  agentmessagerouter.MessageBindingResult  `json:"message_binding"`
}

type updateDingTalkAccountBindingSurfaceRequest struct {
	SurfaceType string `json:"surface_type"`
}

func (h *Handler) ListDingTalkAccountBindings(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkAccountBindings == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"bindings":   []agentmessagerouter.PublicDingTalkAccountBinding{},
			"configured": false,
		})
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	bindings, err := h.DingTalkAccountBindings.List(r.Context(), workspaceID)
	if err != nil {
		writeDingTalkAccountBindingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bindings":   bindings,
		"configured": true,
	})
}

func (h *Handler) BeginDingTalkAccountBinding(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkAccountBindings == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk account binding is not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	var request beginDingTalkAccountBindingRequest
	if err := decodeLimitedJSON(w, r, 4<<10, &request, "invalid_request"); err != nil {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(request.AgentID), "agent_id")
	if !ok {
		return
	}
	if !request.BindingMode.Valid() {
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "invalid_binding_mode", "binding_mode must be message or identity")
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "agent not found in this workspace")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load agent")
		}
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is archived")
		return
	}
	initiatorID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	result, err := h.DingTalkAccountBindings.Begin(r.Context(), agentmessagerouter.BeginParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		InitiatorID: initiatorID,
		BindingMode: request.BindingMode,
	})
	if err != nil {
		writeDingTalkAccountBindingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) CompleteDingTalkAccountBindingCallback(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkAccountBindings == nil || strings.TrimSpace(h.DingTalkAccountBindingOrigin) == "" {
		writeDingTalkAccountBindingAPIError(w, http.StatusServiceUnavailable, "binding_not_configured", "dingtalk account binding is not configured")
		return
	}
	if r.Header.Get("Origin") != h.DingTalkAccountBindingOrigin {
		writeDingTalkAccountBindingAPIError(w, http.StatusForbidden, "callback_origin_forbidden", "callback origin is not allowed")
		return
	}
	callbackToken, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeDingTalkAccountBindingAPIError(w, http.StatusUnauthorized, "callback_auth_required", "callback authorization required")
		return
	}
	bindingID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "bindingId"), "binding id")
	if !ok {
		return
	}
	var request dingTalkAccountBindingCallbackRequest
	if err := decodeLimitedJSON(w, r, maxDingTalkAccountCallbackBodyBytes, &request, "invalid_binding_result"); err != nil {
		return
	}
	result, err := h.DingTalkAccountBindings.CompleteBinding(r.Context(), agentmessagerouter.CompleteBindingParams{
		BindingID:     bindingID,
		BindingMode:   request.BindingMode,
		CallbackToken: callbackToken,
		Status:        request.Status,
		Identity:      request.IdentityBinding,
		Message:       request.MessageBinding,
	})
	if err != nil {
		writeDingTalkAccountBindingError(w, err)
		return
	}
	if result.Binding.DWSIdentity.Status == "active" || result.Binding.MessageRoute.Status == "active" {
		h.publish(
			protocol.EventDingTalkAccountBindingActivated,
			result.Binding.WorkspaceID,
			"system",
			"dingtalk_"+string(request.BindingMode)+"_binding",
			map[string]any{"id": result.Binding.ID},
		)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) UnbindDingTalkAccountBinding(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkAccountBindings == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk account binding is not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent id")
	if !ok {
		return
	}
	bindingMode := agentmessagerouter.BindingMode(strings.TrimSpace(r.URL.Query().Get("binding_mode")))
	if !bindingMode.Valid() {
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "invalid_binding_mode", "binding_mode must be message or identity")
		return
	}
	result, err := h.DingTalkAccountBindings.Unbind(r.Context(), agentmessagerouter.UnbindParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		BindingMode: bindingMode,
	})
	if err != nil {
		writeDingTalkAccountBindingError(w, err)
		return
	}
	h.publish(
		protocol.EventDingTalkAccountBindingRevoked,
		result.WorkspaceID,
		"user",
		userID,
		map[string]any{"id": result.ID},
	)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) UpdateDingTalkAccountBindingSurface(w http.ResponseWriter, r *http.Request) {
	if h.DingTalkAccountBindings == nil {
		writeError(w, http.StatusServiceUnavailable, "dingtalk account binding is not configured")
		return
	}
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent id")
	if !ok {
		return
	}
	var request updateDingTalkAccountBindingSurfaceRequest
	if err := decodeLimitedJSON(w, r, 4<<10, &request, "invalid_surface_type"); err != nil {
		return
	}
	request.SurfaceType = strings.TrimSpace(request.SurfaceType)
	if request.SurfaceType != agentmessagerouter.DingTalkSurfaceIssue &&
		request.SurfaceType != agentmessagerouter.DingTalkSurfaceChat {
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "invalid_surface_type", "surface_type must be issue or chat")
		return
	}
	_, err := h.DingTalkAccountBindings.UpdateSurface(r.Context(), agentmessagerouter.UpdateSurfaceParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		SurfaceType: request.SurfaceType,
	})
	if err != nil {
		writeDingTalkAccountBindingError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeLimitedJSON(w http.ResponseWriter, r *http.Request, limit int64, target any, code string) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeDingTalkAccountBindingAPIError(w, http.StatusRequestEntityTooLarge, code, "request body too large")
		} else {
			writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, code, "invalid request body")
		}
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, code, "invalid request body")
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func bearerToken(authorization string) (string, bool) {
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func writeDingTalkAccountBindingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentmessagerouter.ErrNotConfigured):
		writeDingTalkAccountBindingAPIError(w, http.StatusServiceUnavailable, "binding_not_configured", "dingtalk account binding is not configured")
	case errors.Is(err, agentmessagerouter.ErrAlreadyActive):
		writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "binding_already_active", "dingtalk account is already bound")
	case errors.Is(err, agentmessagerouter.ErrNotFound):
		writeDingTalkAccountBindingAPIError(w, http.StatusNotFound, "binding_attempt_not_found", "dingtalk account binding not found")
	case errors.Is(err, agentmessagerouter.ErrInvalidResult):
		writeDingTalkAccountBindingAPIError(w, http.StatusBadRequest, "invalid_binding_result", "invalid binding result")
	case errors.Is(err, agentmessagerouter.ErrCallbackExpired):
		writeDingTalkAccountBindingAPIError(w, http.StatusGone, "binding_callback_expired", "binding callback expired")
	case errors.Is(err, agentmessagerouter.ErrBindingConflict):
		writeDingTalkAccountBindingAPIError(w, http.StatusConflict, "binding_result_conflict", "binding result conflict")
	case errors.Is(err, agentmessagerouter.ErrRouterUnavailable):
		writeDingTalkAccountBindingAPIError(w, http.StatusBadGateway, "subscription_verify_failed", "subscription verification failed")
	default:
		writeDingTalkAccountBindingAPIError(w, http.StatusInternalServerError, "binding_internal_error", "dingtalk account binding failed")
	}
}

func writeDingTalkAccountBindingAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"code":  code,
		"error": message,
	})
}

func NormalizeDingTalkAccountBindingOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("DBase binding origin must be an HTTPS origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("DBase binding origin port is invalid")
		}
	}
	if port == "443" {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return (&url.URL{Scheme: "https", Host: host}).String(), nil
}
