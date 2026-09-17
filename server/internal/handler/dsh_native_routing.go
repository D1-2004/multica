package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

// Native routing uses the durable session binding, never client-provided hosts.
// A second process may read the shared log, but cannot become its live owner.
func nativeSessionID(payload json.RawMessage) (string, error) {
	var p struct {
		Args struct {
			Request struct {
				SessionID       string `json:"sessionId"`
				ParentSessionID string `json:"parentSessionId"`
				Address         *struct {
					Kind            string `json:"kind"`
					SessionID       string `json:"sessionId"`
					ParentSessionID string `json:"parentSessionId"`
				} `json:"address"`
			} `json:"request"`
		} `json:"args"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}
	r := p.Args.Request
	ids := []string{r.SessionID, r.ParentSessionID}
	if r.Address != nil {
		switch r.Address.Kind {
		case "session":
			ids = append(ids, r.Address.SessionID)
		case "subagent":
			ids = append(ids, r.Address.ParentSessionID)
		default:
			return "", errors.New("invalid session address")
		}
	}
	id := ""
	for _, candidate := range ids {
		if candidate == "" {
			continue
		}
		if !dshhost.ValidSessionID(candidate) || (id != "" && id != candidate) {
			return "", errors.New("ambiguous session identity")
		}
		id = candidate
	}
	return id, nil
}

func (h *Handler) nativeSessionHost(ctx context.Context, access dshhost.NativeAccess, sid string) (dshhost.Host, bool, error) {
	host := dshhost.Host{Key: access.Key, Generation: access.Generation, SandboxID: access.SandboxID, State: "running"}
	if sid == "" {
		return host, false, nil
	}
	var scope uuid.UUID
	err := h.DB.QueryRow(ctx, `SELECT sandbox_scope_id FROM dsh_employee_session WHERE workspace_id=$1 AND agent_id=$2 AND session_id=$3`, access.WorkspaceID, access.AgentID, sid).Scan(&scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return host, false, nil
	}
	if err != nil {
		return host, false, err
	}
	var owner dshhost.Host
	owner.Key = access.Key
	owner.ScopeID = scope
	err = h.DB.QueryRow(ctx, `SELECT sandbox_id,generation,state FROM employee_filesystem_host WHERE workspace_id=$1 AND agent_id=$2 AND scope_id=$3 AND state='running'`, access.WorkspaceID, access.AgentID, scope).Scan(&owner.SandboxID, &owner.Generation, &owner.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return host, true, nil
	}
	return owner, false, err
}

// History can be observed from the entry Host without acquiring a writer.
func (h *Handler) nativeReadTarget(ctx context.Context, access dshhost.NativeAccess, token string, host dshhost.Host) (string, string, func(), error) {
	upstream, child, cleanup, err := h.nativeTarget(ctx, access, token, host)
	if err == nil || host.SandboxID == access.SandboxID {
		return upstream, child, cleanup, err
	}
	base := dshhost.Host{Key: access.Key, SandboxID: access.SandboxID, Generation: access.Generation, State: "running"}
	return h.nativeTarget(ctx, access, token, base)
}

// A routed grant is private to this request/stream and revoked on completion.
// The original grant remains the authority and bounds the routed lifetime.
func (h *Handler) nativeTarget(ctx context.Context, parent dshhost.NativeAccess, token string, host dshhost.Host) (string, string, func(), error) {
	upstream, _, err := h.FCE2BLauncher.DSHNativeProxyAddress(host)
	if err != nil {
		return "", "", nil, err
	}
	noop := func() {}
	if host.SandboxID == parent.SandboxID && host.Generation == parent.Generation {
		return upstream, token, noop, nil
	}
	if _, err = h.FCE2BLauncher.EnsureDSHNativeAuthority(ctx, host, h.dshNativeAccessManager(), h.submitDSHNativePrompt); err != nil {
		return "", "", nil, err
	}
	manager := h.dshNativeAccessManager()
	grant, entry, err := manager.IssueRouted(ctx, host, parent)
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = (dshhost.PostgresStore{DB: h.DB}).RevokeNativeAccess(c, parent.Key, grant.ID)
	}
	_, child, err := manager.Exchange(ctx, entry, host)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	return upstream, child, cleanup, nil
}

func (h *Handler) nativeProxyAuthorize(r *http.Request, expected dshhost.NativeAccess) (string, error) {
	cookies := r.Cookies()
	token := ""
	for _, c := range cookies {
		if c.Name == dshNativeProxyCookie {
			if token != "" {
				return "", dshhost.ErrNativeAccessDenied
			}
			token = c.Value
		}
	}
	host := dshhost.Host{Key: expected.Key, SandboxID: expected.SandboxID, Generation: expected.Generation, State: "running"}
	actual, err := h.dshNativeAccessManager().Authorize(r.Context(), token, host)
	if err != nil || actual.ID != expected.ID {
		return "", dshhost.ErrNativeAccessDenied
	}
	return token, nil
}

func nativeRequestOrigin(r *http.Request, origin string) bool {
	return r.Header.Get("Authorization") == "" && r.Header.Get("Sec-Fetch-Site") != "cross-site" && r.Header.Get("Origin") == origin
}

// Returns true when the request was served (including fail-closed responses).
func (h *Handler) routeDSHNativeRequest(w http.ResponseWriter, r *http.Request, access dshhost.NativeAccess, origin, prefix string) bool {
	path := "/" + strings.TrimPrefix(r.URL.Path, prefix)
	mux := path == "/api/remote.mux" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if !mux && (r.Method != "POST" || !strings.HasPrefix(path, "/api/")) {
		return false
	}
	// Leave plugin RPC bodies (including uploads) and their gateway handling
	// untouched. Only known session routes need inspection by this layer.
	if !mux && path != "/api/session/list" && !nativeSessionMethod(strings.TrimPrefix(path, "/api/")) {
		return false
	}
	if !nativeRequestOrigin(r, origin) {
		writeError(w, 403, "invalid DSH native request origin")
		return true
	}
	token, err := h.nativeProxyAuthorize(r, access)
	if err != nil {
		writeError(w, 401, "open DSH again from the employee workbench")
		return true
	}
	ctx, cancel := context.WithDeadline(r.Context(), access.ExpiresAt)
	defer cancel()
	r = r.WithContext(ctx)
	if mux {
		h.serveNativeSessionMux(w, r, access, token)
		return true
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeError(w, 400, "invalid native request")
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var envelope struct {
		Type    string          `json:"type"`
		Method  string          `json:"method"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Type != "client-request" || "/api/"+envelope.Method != path {
		return false
	}
	if envelope.Method == "session/list" {
		h.serveNativeSessionList(w, r, access, token, body)
		return true
	}
	// Plugin RPCs remain opaque. A plugin's own sessionId need not identify
	// a DSH Session, and must not select a different Host.
	if !nativeSessionMethod(envelope.Method) {
		return false
	}
	sid, err := nativeSessionID(envelope.Payload)
	if err != nil {
		writeError(w, 400, "invalid native session identity")
		return true
	}
	if sid == "" {
		return false
	}
	host, unavailable, err := h.nativeSessionHost(r.Context(), access, sid)
	if err != nil || (unavailable && !nativeReadMethod(envelope.Method)) {
		writeError(w, 503, "session owner is unavailable; retry from the workbench")
		return true
	}
	target := h.nativeTarget
	if nativeReadMethod(envelope.Method) {
		target = h.nativeReadTarget
	}
	upstream, child, cleanup, err := target(r.Context(), access, token, host)
	if err != nil {
		writeError(w, 503, "session owner is unavailable")
		return true
	}
	defer cleanup()
	routed := r.Clone(r.Context())
	routed.Header = r.Header.Clone()
	routed.Header.Del("Cookie")
	routed.AddCookie(&http.Cookie{Name: dshNativeProxyCookie, Value: child})
	serveDSHNativeProxy(w, routed, upstream, origin, prefix)
	return true
}

func nativeReadMethod(method string) bool {
	switch method {
	case "session/page", "session/follow", "session/attachment", "session/list", "session/search":
		return true
	}
	return false
}

func nativeSessionMethod(method string) bool {
	switch method {
	case "session/rename", "session/page", "session/attachment", "session/selectModel",
		"session/cancel", "session/fork", "session/prompt", "session/updateQueue", "subagents/prompt":
		return true
	default:
		return false
	}
}
