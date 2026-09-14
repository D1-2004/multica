package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This callback runs again for each gateway operation. A grant cannot retain
// access after removal from the workspace, employee archival or an ineligible runtime binding.
func (h *Handler) checkDSHNativeManage(ctx context.Context, key dshhost.Key, userID uuid.UUID) error {
	if h.Queries == nil {
		return dshhost.ErrNativeAccessDenied
	}
	member, err := h.getWorkspaceMember(ctx, userID.String(), key.WorkspaceID.String())
	if err != nil || !roleAllowed(member.Role, "owner", "admin", "member") {
		return dshhost.ErrNativeAccessDenied
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parseUUID(key.AgentID.String()), WorkspaceID: parseUUID(key.WorkspaceID.String())})
	if err != nil || agent.ArchivedAt.Valid || !agent.RuntimeID.Valid || agent.RuntimeMode != "cloud" {
		return dshhost.ErrNativeAccessDenied
	}
	if !roleAllowed(member.Role, "owner", "admin") && (!agent.OwnerID.Valid || uuid.UUID(agent.OwnerID.Bytes) != userID) {
		return dshhost.ErrNativeAccessDenied
	}
	runtime, err := h.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil || runtime.WorkspaceID != agent.WorkspaceID || runtime.Provider != "dsh" || !service.IsFCE2BRuntime(runtime) {
		return dshhost.ErrNativeAccessDenied
	}
	return nil
}

func (h *Handler) dshNativeAccessManager() dshhost.NativeAccessManager {
	if h.DB == nil {
		return dshhost.NativeAccessManager{}
	}
	return dshhost.NativeAccessManager{Store: dshhost.PostgresStore{DB: h.DB}, CheckManage: h.checkDSHNativeManage}
}

func dshNativeResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// IssueDSHNativeAccess is human-only and accepts no client placement or redirect.
func (h *Handler) IssueDSHNativeAccess(w http.ResponseWriter, r *http.Request) {
	dshNativeResponseHeaders(w)
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	var empty struct{}
	err := decoder.Decode(&empty)
	if (err != nil && !errors.Is(err, io.EOF)) || (err == nil && !errors.Is(decoder.Decode(&empty), io.EOF)) {
		writeError(w, http.StatusBadRequest, "DSH native entry accepts no client configuration")
		return
	}
	key, ok := h.dshHomeKey(w, r)
	if !ok {
		return
	}
	if h.DB == nil || h.FCE2BLauncher == nil {
		writeError(w, http.StatusServiceUnavailable, "DSH native entry is unavailable")
		return
	}
	userID, ok := parseUUIDOrBadRequest(w, requestUserID(r), "user_id")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	host, err := (dshhost.PostgresStore{DB: h.DB}).Get(ctx, key)
	if err != nil || host.State != "running" {
		writeError(w, http.StatusConflict, "DSH employee Host is not running")
		return
	}
	origin, err := h.FCE2BLauncher.DSHNativeGatewayURL(ctx, host)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DSH native gateway is not ready")
		return
	}
	access, token, err := h.dshNativeAccessManager().Issue(ctx, host, uuid.UUID(userID.Bytes))
	if err != nil {
		writeError(w, http.StatusForbidden, "DSH native entry is unavailable or no longer authorized")
		return
	}
	// Fragments are not transmitted to the gateway in the navigation request.
	writeJSON(w, http.StatusCreated, map[string]any{"access_id": access.ID, "entry_url": origin + "/_multica/open#entry=" + url.QueryEscape(token), "expires_at": access.ExpiresAt})
}

func (h *Handler) RevokeDSHNativeAccess(w http.ResponseWriter, r *http.Request) {
	dshNativeResponseHeaders(w)
	key, ok := h.dshHomeKey(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "accessId"), "access_id")
	if !ok {
		return
	}
	userID, ok := parseUUIDOrBadRequest(w, requestUserID(r), "user_id")
	if !ok {
		return
	}
	if err := h.dshNativeAccessManager().Revoke(r.Context(), key, uuid.UUID(id.Bytes), uuid.UUID(userID.Bytes)); err != nil {
		writeError(w, http.StatusForbidden, "DSH native access cannot be revoked")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Gateway callbacks authenticate with the capability itself, outside JWT
// middleware. Body identity must match the persisted grant and running Host;
// it never selects the human whose current permission is checked.
func dshNativeCallback(w http.ResponseWriter, r *http.Request, manager dshhost.NativeAccessManager, exchange bool) {
	dshNativeResponseHeaders(w)
	var input struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Generation  int64  `json:"generation"`
		SandboxID   string `json:"sandbox_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid DSH native scope")
		return
	}
	workspaceID, ok := parseUUIDOrBadRequest(w, input.WorkspaceID, "workspace_id")
	if !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, input.AgentID, "agent_id")
	if !ok {
		return
	}
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.UUID(workspaceID.Bytes), AgentID: uuid.UUID(agentID.Bytes)}, Generation: input.Generation, SandboxID: input.SandboxID}
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") || r.URL.RawQuery != "" {
		writeError(w, http.StatusUnauthorized, "DSH native access denied")
		return
	}
	token := strings.TrimPrefix(headers[0], "Bearer ")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var access dshhost.NativeAccess
	var session string
	var err error
	if exchange {
		access, session, err = manager.Exchange(ctx, token, host)
	} else {
		access, err = manager.Authorize(ctx, token, host)
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "DSH native access denied")
		return
	}
	response := map[string]any{"access_id": access.ID, "user_id": access.UserID, "workspace_id": access.WorkspaceID, "agent_id": access.AgentID, "generation": access.Generation, "sandbox_id": access.SandboxID, "expires_at": access.ExpiresAt}
	if exchange {
		response["session_token"] = session
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) ExchangeDSHNativeAccess(w http.ResponseWriter, r *http.Request) {
	dshNativeCallback(w, r, h.dshNativeAccessManager(), true)
}
func (h *Handler) CheckDSHNativeAccess(w http.ResponseWriter, r *http.Request) {
	dshNativeCallback(w, r, h.dshNativeAccessManager(), false)
}
