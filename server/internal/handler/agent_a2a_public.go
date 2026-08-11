package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/featureflags"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const maxAgentA2AProtocolBody = 1 << 20

// GetAgentA2ACard publishes the standard, credential-free discovery document
// for one enabled hosted Agent. Its origin comes only from MULTICA_PUBLIC_URL;
// request Host and forwarding headers are never part of the disclosure URL.
func (h *Handler) GetAgentA2ACard(w http.ResponseWriter, r *http.Request) {
	if !featureflags.AgentA2AInboundEnabled(r.Context(), h.FeatureFlags) {
		http.NotFound(w, r)
		return
	}

	runtimeSafety := evaluateAgentA2ARequestSafety(h.currentConfig().PublicURL, r.RemoteAddr)
	if !runtimeSafety.Allowed {
		http.NotFound(w, r)
		return
	}
	baseURL := runtimeSafety.PublicBaseURL
	endpoint, err := h.Queries.GetPublishedAgentA2AEndpointByPublicID(
		r.Context(),
		db.GetPublishedAgentA2AEndpointByPublicIDParams{
			PublicAgentID: strings.TrimSpace(chi.URLParam(r, "publicAgentId")),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load A2A Agent Card")
		return
	}

	skills, _, err := normalizeAgentA2ACardSkills(endpoint.CardSkills)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	card, err := a2aintegration.BuildAgentCard(a2aintegration.CardConfig{
		BaseURL:       baseURL,
		PublicAgentID: endpoint.PublicAgentID,
		Name:          endpoint.CardName,
		Description:   endpoint.CardDescription,
		Version:       endpoint.CardVersion,
		Skills:        skills,
	})
	if err != nil {
		http.NotFound(w, r)
		return
	}
	encoded, err := json.Marshal(card)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode A2A Agent Card")
		return
	}

	digest := sha256.Sum256(encoded)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if requestETagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

// HandleAgentA2ARPC authenticates an external client before handing the
// bounded request to the official A2A JSON-RPC adapter. Authorization is
// removed after verification so the raw token cannot reach SDK logging or the
// application service context.
func (h *Handler) HandleAgentA2ARPC(w http.ResponseWriter, r *http.Request) {
	if !featureflags.AgentA2AInboundEnabled(r.Context(), h.FeatureFlags) {
		http.NotFound(w, r)
		return
	}
	runtimeSafety := evaluateAgentA2ARequestSafety(h.currentConfig().PublicURL, r.RemoteAddr)
	if !runtimeSafety.Allowed {
		http.NotFound(w, r)
		return
	}
	if h.A2AProtocol == nil {
		writeError(w, http.StatusServiceUnavailable, "A2A protocol service is unavailable")
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	rawToken, ok := parseAgentAccessToken(r, "")
	if !ok {
		writeAgentA2AUnauthorized(w)
		return
	}
	credential, err := h.Queries.GetAgentA2ACredentialByTokenHash(r.Context(), auth.HashToken(rawToken))
	if err != nil {
		writeAgentA2AUnauthorized(w)
		return
	}
	publicAgentID := strings.TrimSpace(chi.URLParam(r, "publicAgentId"))
	if credential.PublicAgentID != publicAgentID ||
		!credential.AgentOwnerID.Valid ||
		!credential.DelegatedByUserID.Valid ||
		credential.AgentOwnerID.Bytes != credential.DelegatedByUserID.Bytes {
		writeAgentA2AUnauthorized(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAgentA2AProtocolBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "A2A request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read A2A request body")
		return
	}
	// JSON-RPC carries A2A service parameters as HTTP headers. Accepting the
	// standard query spelling as a convenience keeps local clients simple while
	// preserving one canonical version check inside the SDK interceptor.
	if r.Header.Get("A2A-Version") == "" {
		if version := strings.TrimSpace(r.URL.Query().Get("A2A-Version")); version != "" {
			r.Header.Set("A2A-Version", version)
		}
	}
	r.Header.Del("Authorization")
	r.Header.Del("X-API-Key")
	if agentA2AHasTrailingJSONValue(body) {
		// The SDK decoder owns JSON-RPC errors, but it intentionally consumes one
		// value. Replace multi-value input with malformed JSON so the same SDK
		// returns the protocol-native parse error instead of accepting a prefix.
		body = []byte("{")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	ctx := a2aintegration.WithPrincipal(r.Context(), a2aintegration.Principal{
		WorkspaceID:     uuidToString(credential.WorkspaceID),
		AgentID:         uuidToString(credential.AgentID),
		EndpointID:      uuidToString(credential.EndpointID),
		PublicAgentID:   credential.PublicAgentID,
		ClientID:        uuidToString(credential.ClientID),
		CredentialID:    uuidToString(credential.CredentialID),
		OwnerID:         uuidToString(credential.DelegatedByUserID),
		Scopes:          credential.ClientScopes,
		EndpointEnabled: credential.EndpointEnabled,
	})
	_ = h.Queries.TouchAgentA2ACredentialLastUsed(ctx, credential.CredentialID)
	warnAgentA2AUnsafeRuntimeAccepted(
		runtimeSafety.Mode,
		"rpc",
		uuidToString(credential.WorkspaceID),
		uuidToString(credential.AgentID),
		credential.PublicAgentID,
		uuidToString(credential.ClientID),
	)
	h.A2AProtocol.ServeHTTP(w, r.WithContext(ctx))
}

func parseAgentA2ABearer(authorization string) (string, bool) {
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	token := parts[1]
	const prefix = "mca2a_"
	if len(token) != len(prefix)+40 || !strings.HasPrefix(token, prefix) {
		return "", false
	}
	if _, err := hex.DecodeString(token[len(prefix):]); err != nil {
		return "", false
	}
	return token, true
}

// parseAgentAccessToken accepts the same opaque Agent credential for both A2A
// and MCP. Authorization is canonical; X-API-Key keeps simple clients simple.
// A path token is accepted only by the explicit MCP convenience route.
func parseAgentAccessToken(r *http.Request, pathToken string) (string, bool) {
	candidates := make([]string, 0, 3)
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" {
		token, ok := parseAgentA2ABearer(authorization)
		if !ok {
			return "", false
		}
		candidates = append(candidates, token)
	}
	if apiKey := strings.TrimSpace(r.Header.Get("X-API-Key")); apiKey != "" {
		if !validAgentAccessToken(apiKey) {
			return "", false
		}
		candidates = append(candidates, apiKey)
	}
	if pathToken = strings.TrimSpace(pathToken); pathToken != "" {
		if !validAgentAccessToken(pathToken) {
			return "", false
		}
		candidates = append(candidates, pathToken)
	}
	if len(candidates) == 0 {
		return "", false
	}
	for _, candidate := range candidates[1:] {
		if candidate != candidates[0] {
			return "", false
		}
	}
	return candidates[0], true
}

func validAgentAccessToken(token string) bool {
	const prefix = "mca2a_"
	if len(token) != len(prefix)+40 || !strings.HasPrefix(token, prefix) {
		return false
	}
	_, err := hex.DecodeString(token[len(prefix):])
	return err == nil
}

func writeAgentA2AUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="multica-a2a"`)
	writeError(w, http.StatusUnauthorized, "invalid A2A credential")
}

func agentA2AHasTrailingJSONValue(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var first json.RawMessage
	if err := decoder.Decode(&first); err != nil {
		return false
	}
	var trailing json.RawMessage
	return !errors.Is(decoder.Decode(&trailing), io.EOF)
}

func requestETagMatches(header, target string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == target || candidate == "W/"+target {
			return true
		}
	}
	return false
}
