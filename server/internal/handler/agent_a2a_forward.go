package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// When a pre-release Agent registered for a digital employee identity (see
// agent_a2a_forward_registration.go), the production Agent bound to the same
// identity forwards its inbound A2A JSON-RPC there, so DEAP keeps calling the
// production Card while the turn runs on pre-release. The caller still
// authenticates here with this environment's credential; only then is the
// unchanged body replayed to the target with the registration key. The target
// re-authenticates and re-validates every identity header itself.

const (
	// agentA2AForwardedHeader marks a forwarded request. An Agent whose own
	// client is forwarded rejects a marked request instead of serving or
	// forwarding it, which breaks loops and stops callers from forcing local
	// execution by setting the header.
	agentA2AForwardedHeader    = "X-Multica-A2A-Forwarded"
	maxAgentA2AForwardInflight = 64
	agentA2AForwardCopyBuffer  = 32 << 10
)

// Headers that describe this hop or this environment's session and must not
// reach the target. Hop-by-hop headers are removed separately.
var agentA2AForwardDroppedHeaders = []string{
	"Authorization",
	"X-API-Key",
	"Cookie",
	"Host",
	"Forwarded",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
	"X-Real-IP",
	"X-Actor-Source",
	"X-Auth-Method",
	"X-Request-ID",
	"Content-Length",
	"Accept-Encoding",
	agentA2AForwardedHeader,
	protocol.SandboxRelayTokenHeader,
}

var agentA2AHopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

var agentA2AForwardSlots = make(chan struct{}, maxAgentA2AForwardInflight)

var defaultAgentA2AForwardTransport http.RoundTripper = &http.Transport{
	Proxy: nil,
	DialContext: (&net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout: 5 * time.Second,
	// Blocking SendMessage answers only when the turn completes, so there is
	// no response-header deadline; the caller's request context bounds it.
	IdleConnTimeout:     90 * time.Second,
	MaxIdleConns:        32,
	MaxIdleConnsPerHost: 16,
	DisableCompression:  true,
	ForceAttemptHTTP2:   true,
}

type agentA2AForwardTarget struct {
	DWSUID         string
	OrgID          string
	RPCURL         string
	TargetClientID pgtype.UUID
	Token          string
	TokenSHA256    string
}

// maybeForwardAgentA2ARPC forwards an authenticated request when a
// pre-release Agent registered for this Agent's digital employee identity. It
// returns true when it has written the response.
func (h *Handler) maybeForwardAgentA2ARPC(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	credential db.GetAgentA2ACredentialByTokenHashRow,
	publicAgentID string,
) bool {
	target, configured, err := h.resolveAgentA2AForward(r.Context(), credential, publicAgentID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "A2A forward is misconfigured")
		return true
	}
	if !configured {
		return false
	}
	if r.Header.Get(agentA2AForwardedHeader) != "" {
		// This hop forwards the Agent itself, so a marked request is either a
		// forwarding loop or a caller trying to force local execution.
		// Neither may run here.
		writeError(w, http.StatusLoopDetected, "A2A forward loop detected")
		return true
	}
	method := agentA2ARPCMethod(body)
	// An archived Agent admits no new turns, exactly like an unpublished
	// endpoint; the local publication lookup already refuses both.
	admitsNewTurns := credential.EndpointEnabled && !credential.AgentArchivedAt.Valid
	if method == "" || !a2aintegration.MethodPermitted(method, credential.ClientScopes, admitsNewTurns) {
		// The local SDK answers with the protocol-native parse, method or
		// authorization error, so nothing this credential may not do reaches
		// the target. Reads and cancels of forwarded tasks keep flowing after
		// the endpoint is unpublished or the Agent archived; new turns do not.
		return false
	}
	for attempt := 0; ; attempt++ {
		// The target sees every forwarded call as its one registration
		// client, so that client serves exactly one source client for good.
		// A second client (or a second Agent bound to the same identity)
		// runs locally instead of sharing the first client's tasks there.
		owner, err := h.Queries.ClaimA2AForwardTokenBinding(r.Context(), db.ClaimA2AForwardTokenBindingParams{
			TokenSha256:    agentA2AForwardBindingKey(target.RPCURL, target.TargetClientID),
			SourceClientID: credential.ClientID,
			WorkspaceID:    credential.WorkspaceID,
			AgentID:        credential.AgentID,
			CreatedBy:      credential.DelegatedByUserID,
		})
		if err != nil {
			writeError(w, http.StatusBadGateway, "A2A forward is misconfigured")
			return true
		}
		if owner.Bytes != credential.ClientID.Bytes {
			slog.Warn("A2A forward target belongs to another source client; serving locally",
				"event", "a2a_forward_client_mismatch",
				"agent_id", uuidToString(credential.AgentID),
				"client_id", uuidToString(credential.ClientID),
			)
			return false
		}
		if h.forwardAgentA2ARPC(w, r, body, target, publicAgentID) {
			return true
		}
		// Pre-release no longer honors this key (withdrawn, or its employee
		// changed there) and ran nothing. Retire exactly this registration; a
		// newer one with another key is left alone.
		if err := h.Queries.RejectA2AForwardRegistration(r.Context(), db.RejectA2AForwardRegistrationParams{
			DwsUid:      target.DWSUID,
			OrgID:       target.OrgID,
			TokenSha256: target.TokenSHA256,
		}); err != nil {
			slog.Warn("retire rejected A2A forward registration failed", "agent_id", uuidToString(credential.AgentID), "error", err)
		}
		slog.Warn("A2A forward target rejected the registration key",
			"event", "a2a_forward_registration_rejected",
			"agent_id", uuidToString(credential.AgentID),
			"target_origin", agentA2AForwardOrigin(target.RPCURL),
			"attempt", attempt,
		)
		if attempt > 0 {
			return false
		}
		// A key rotation may have raced this call; follow the registration
		// that replaced it once, otherwise serve the call here.
		target, configured, err = h.resolveAgentA2AForward(r.Context(), credential, publicAgentID)
		if err != nil || !configured {
			return false
		}
	}
}

// resolveAgentA2AForward returns the pre-release target registered for the
// Agent's digital employee identity. configured=false means "serve locally":
// this deployment is not a registry, the operator did not enable the identity
// for A2A, nothing is registered, the registration points back at this Agent,
// its origin left the allow-list, or the settings could not be read. A
// registration that cannot be used returns an error so the request fails
// instead of silently running here.
func (h *Handler) resolveAgentA2AForward(
	ctx context.Context,
	credential db.GetAgentA2ACredentialByTokenHashRow,
	publicAgentID string,
) (agentA2AForwardTarget, bool, error) {
	if !h.agentA2AForwardRegistry() {
		return agentA2AForwardTarget{}, false, nil
	}
	uid, orgID, ok := service.A2AOperatorIdentity(ctx, h.Queries, credential.WorkspaceID, credential.AgentID)
	if !ok {
		return agentA2AForwardTarget{}, false, nil
	}
	registration, err := h.Queries.GetA2AForwardRegistration(ctx, db.GetA2AForwardRegistrationParams{
		DwsUid: uid,
		OrgID:  orgID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return agentA2AForwardTarget{}, false, nil
	}
	if err != nil {
		slog.Warn("load A2A forward registration failed; serving locally",
			"agent_id", uuidToString(credential.AgentID),
			"error", err,
		)
		return agentA2AForwardTarget{}, false, nil
	}
	if baseURL, err := normalizeAgentA2APublicBaseURL(h.currentConfig().PublicURL); err == nil {
		if own, err := a2aintegration.AgentRPCURL(baseURL, publicAgentID); err == nil && strings.EqualFold(own, registration.RpcUrl) {
			return agentA2AForwardTarget{}, false, nil
		}
	}
	allowed := normalizedAgentA2AForwardOrigins(h.currentConfig().A2AForwardAllowedOrigins)
	if _, err := agentA2AForwardOriginAllowed(registration.RpcUrl, allowed); err != nil {
		return agentA2AForwardTarget{}, false, nil
	}
	token, err := h.A2AService.PushSecrets.Open(registration.TokenEncrypted)
	if err != nil || !validAgentAccessToken(string(token)) {
		return agentA2AForwardTarget{}, false, errors.New("forward credential cannot be opened")
	}
	return agentA2AForwardTarget{
		DWSUID:         uid,
		OrgID:          orgID,
		RPCURL:         registration.RpcUrl,
		TargetClientID: registration.TargetClientID,
		Token:          string(token),
		TokenSHA256:    registration.TokenSha256,
	}, true, nil
}

// agentA2ARPCMethod returns the JSON-RPC method of a single request object,
// or "" for anything else (batches, malformed JSON).
func agentA2ARPCMethod(body []byte) string {
	var envelope struct {
		Method string `json:"method"`
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &envelope) != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Method)
}

// forwardAgentA2ARPC relays the call and returns true once it has written the
// response. It returns false, writing nothing, only when pre-release rejected
// the registration key itself (401 with agentA2AForwardKeyRejectedHeader),
// which it decides before doing anything; every other answer, including other
// 401s, passes through.
func (h *Handler) forwardAgentA2ARPC(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	target agentA2AForwardTarget,
	publicAgentID string,
) bool {
	select {
	case agentA2AForwardSlots <- struct{}{}:
		defer func() { <-agentA2AForwardSlots }()
	default:
		writeError(w, http.StatusServiceUnavailable, "A2A forward capacity exhausted")
		return true
	}
	started := time.Now()
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.RPCURL, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, "A2A forward target is invalid")
		return true
	}
	copyAgentA2AForwardRequestHeaders(request.Header, r.Header)
	request.Header.Set("Authorization", "Bearer "+target.Token)
	request.Header.Set(agentA2AForwardedHeader, publicAgentID)
	request.ContentLength = int64(len(body))

	transport := h.a2aForwardTransport
	if transport == nil {
		transport = defaultAgentA2AForwardTransport
	}
	response, err := (&http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}).Do(request)
	if err != nil {
		slog.Warn("A2A forward failed", append(logger.RequestAttrs(r),
			"event", "a2a_forward_failed",
			"public_agent_id", publicAgentID,
			"target_origin", agentA2AForwardOrigin(target.RPCURL),
			"duration_ms", time.Since(started).Milliseconds(),
			"error", err,
		)...)
		if r.Context().Err() == nil {
			writeError(w, http.StatusBadGateway, "A2A forward target is unavailable")
		}
		return true
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized && response.Header.Get(agentA2AForwardKeyRejectedHeader) != "" {
		return false
	}

	for name, values := range response.Header {
		if agentA2AIsHopByHop(name) || strings.EqualFold(name, "Set-Cookie") || strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	copied, copyErr := copyAgentA2AForwardBody(w, response.Body)
	slog.Info("A2A request forwarded", append(logger.RequestAttrs(r),
		"event", "a2a_forwarded",
		"public_agent_id", publicAgentID,
		"target_origin", agentA2AForwardOrigin(target.RPCURL),
		"status", response.StatusCode,
		"response_bytes", copied,
		"duration_ms", time.Since(started).Milliseconds(),
		"stream_error", copyErr != nil,
	)...)
	return true
}

func copyAgentA2AForwardRequestHeaders(destination, source http.Header) {
	for name, values := range source {
		if agentA2AForwardDropped(name) || agentA2AIsHopByHop(name) {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func agentA2AForwardDropped(name string) bool {
	if strings.HasPrefix(strings.ToLower(name), "x-user-") {
		return true
	}
	for _, dropped := range agentA2AForwardDroppedHeaders {
		if strings.EqualFold(name, dropped) {
			return true
		}
	}
	return false
}

func agentA2AIsHopByHop(name string) bool {
	for _, hop := range agentA2AHopByHopHeaders {
		if strings.EqualFold(name, hop) {
			return true
		}
	}
	return false
}

// copyAgentA2AForwardBody streams the target response and flushes after every
// chunk so SSE events reach the caller as the target emits them.
func copyAgentA2AForwardBody(w http.ResponseWriter, body io.Reader) (int64, error) {
	controller := http.NewResponseController(w)
	buffer := make([]byte, agentA2AForwardCopyBuffer)
	var total int64
	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
			written, writeErr := w.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			_ = controller.Flush()
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func agentA2AForwardOrigin(rpcURL string) string {
	if index := strings.Index(rpcURL, "/api/"); index > 0 {
		return rpcURL[:index]
	}
	return strconv.Quote(rpcURL)
}
