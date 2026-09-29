package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The A2A operator surface binds a DEAP digital employee identity to an Agent
// and proxies an Agent's inbound A2A traffic to another environment. Both let
// the caller act through someone else's credential, so neither is an ordinary
// Agent-management permission: the deployment names the operators by email
// (MULTICA_A2A_OPERATOR_EMAILS) and everyone else sees only operator=false.

var (
	agentA2AOperatorDecimalID  = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	agentA2AForwardRPCPath     = regexp.MustCompile(`^/api/a2a/agents/([A-Za-z0-9_-]{16,128})/v1$`)
	agentA2AOperatorDEAPUUIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

type AgentA2AOperatorIdentityResponse struct {
	UID           string  `json:"uid"`
	OrgID         string  `json:"org_id"`
	DEAPAgentUUID *string `json:"deap_agent_uuid"`
	UpdatedBy     string  `json:"updated_by"`
	UpdatedAt     string  `json:"updated_at"`
}

type AgentA2AOperatorForwardResponse struct {
	RPCURL         string `json:"rpc_url"`
	SourceClientID string `json:"source_client_id"`
	Active         bool   `json:"active"`
	UpdatedBy      string `json:"updated_by"`
	UpdatedAt      string `json:"updated_at"`
}

type AgentA2AOperatorResponse struct {
	Operator              bool                              `json:"operator"`
	DWSIdentity           *AgentA2AOperatorIdentityResponse `json:"dws_identity,omitempty"`
	Forward               *AgentA2AOperatorForwardResponse  `json:"forward,omitempty"`
	ForwardAllowedOrigins []string                          `json:"forward_allowed_origins,omitempty"`
}

type updateAgentA2AOperatorIdentityRequest struct {
	UID           string `json:"uid"`
	OrgID         string `json:"org_id"`
	DEAPAgentUUID string `json:"deap_agent_uuid"`
}

type updateAgentA2AOperatorForwardRequest struct {
	RPCURL string `json:"rpc_url"`
	Token  string `json:"token"`
	// SourceClientID selects the one A2A client whose calls are forwarded.
	// Optional when the endpoint has exactly one active client.
	SourceClientID string `json:"source_client_id"`
}

// isAgentA2AOperator reports whether the human actor's account email is listed
// in the deployment operator allow-list. An empty list admits nobody.
func (h *Handler) isAgentA2AOperator(r *http.Request, actorID pgtype.UUID) bool {
	operators := h.currentConfig().A2AOperatorEmails
	if len(operators) == 0 || !actorID.Valid {
		return false
	}
	user, err := h.Queries.GetUser(r.Context(), actorID)
	if err != nil {
		return false
	}
	email := strings.ToLower(strings.TrimSpace(user.Email))
	if email == "" {
		return false
	}
	for _, operator := range operators {
		if strings.ToLower(strings.TrimSpace(operator)) == email {
			return true
		}
	}
	return false
}

func (h *Handler) requireAgentA2AOperator(w http.ResponseWriter, r *http.Request) (agentA2AManagementScope, bool) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return agentA2AManagementScope{}, false
	}
	if !h.isAgentA2AOperator(r, scope.ActorUserID) {
		writeError(w, http.StatusForbidden, "only deployment A2A operators can change this setting")
		return agentA2AManagementScope{}, false
	}
	return scope, true
}

func (h *Handler) GetAgentA2AOperatorConfig(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AManager(w, r)
	if !ok {
		return
	}
	if !h.isAgentA2AOperator(r, scope.ActorUserID) {
		writeJSON(w, http.StatusOK, AgentA2AOperatorResponse{Operator: false})
		return
	}
	response, err := h.loadAgentA2AOperatorResponse(r, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load A2A operator configuration")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) UpdateAgentA2AOperatorIdentity(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AOperator(w, r)
	if !ok {
		return
	}
	var req updateAgentA2AOperatorIdentityRequest
	if err := decodeAgentA2AJSON(w, r, &req, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	uid := strings.TrimSpace(req.UID)
	orgID := strings.TrimSpace(req.OrgID)
	if !agentA2AOperatorDecimalID.MatchString(uid) || !agentA2AOperatorDecimalID.MatchString(orgID) {
		writeError(w, http.StatusBadRequest, "uid and org_id must be positive decimal identifiers")
		return
	}
	deapAgentUUID := pgtype.Text{}
	if value := strings.TrimSpace(req.DEAPAgentUUID); value != "" {
		if !agentA2AOperatorDEAPUUIDRe.MatchString(value) {
			writeError(w, http.StatusBadRequest, "deap_agent_uuid is invalid")
			return
		}
		deapAgentUUID = pgtype.Text{String: value, Valid: true}
	}
	if err := h.Queries.UpsertAgentA2AOperatorIdentity(r.Context(), db.UpsertAgentA2AOperatorIdentityParams{
		AgentID:           scope.Agent.ID,
		WorkspaceID:       scope.WorkspaceID,
		DwsUid:            pgtype.Text{String: uid, Valid: true},
		DwsOrgID:          pgtype.Text{String: orgID, Valid: true},
		DeapAgentUuid:     deapAgentUUID,
		IdentityUpdatedBy: scope.ActorUserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save A2A DWS identity")
		return
	}
	h.auditAgentA2AOperatorChange(r, scope, "dws_identity_set")
	h.writeAgentA2AOperatorResponse(w, r, scope)
}

func (h *Handler) DeleteAgentA2AOperatorIdentity(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AOperator(w, r)
	if !ok {
		return
	}
	if err := h.Queries.ClearAgentA2AOperatorIdentity(r.Context(), db.ClearAgentA2AOperatorIdentityParams{
		WorkspaceID:       scope.WorkspaceID,
		AgentID:           scope.Agent.ID,
		IdentityUpdatedBy: scope.ActorUserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear A2A DWS identity")
		return
	}
	h.auditAgentA2AOperatorChange(r, scope, "dws_identity_cleared")
	h.writeAgentA2AOperatorResponse(w, r, scope)
}

func (h *Handler) UpdateAgentA2AOperatorForward(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AOperator(w, r)
	if !ok {
		return
	}
	if h.A2AService == nil || h.A2AService.PushSecrets == nil {
		writeError(w, http.StatusServiceUnavailable, "A2A forwarding requires MULTICA_A2A_PUSH_SECRET_KEY")
		return
	}
	var req updateAgentA2AOperatorForwardRequest
	if err := decodeAgentA2AJSON(w, r, &req, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	endpoint, err := h.Queries.GetAgentA2AEndpointByAgent(r.Context(), scope.Agent.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load A2A endpoint")
		return
	}
	target, err := normalizeAgentA2AForwardTarget(
		req.RPCURL,
		h.currentConfig().A2AForwardAllowedOrigins,
		h.currentConfig().PublicURL,
		endpoint.PublicAgentID,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token := strings.TrimSpace(req.Token)
	if !validAgentAccessToken(token) {
		writeError(w, http.StatusBadRequest, "token must be the target Agent's A2A access key")
		return
	}
	sourceClientID, status, err := h.resolveAgentA2AForwardSourceClient(r, scope, req.SourceClientID)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	sealed, err := h.A2AService.PushSecrets.Seal([]byte(token))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to protect the forward credential")
		return
	}
	// The key claim and the forward row commit together, so a failed save
	// never leaves a target key pinned to a client that did not get it.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := h.Queries.WithTx(tx)
	// A target key serves one source client for its whole life, across
	// rebinding, clearing and other Agents; see a2a_forward_token_binding.
	digest := sha256.Sum256([]byte(token))
	owner, err := queries.ClaimA2AForwardTokenBinding(r.Context(), db.ClaimA2AForwardTokenBindingParams{
		TokenSha256:    hex.EncodeToString(digest[:]),
		SourceClientID: sourceClientID,
		WorkspaceID:    scope.WorkspaceID,
		AgentID:        scope.Agent.ID,
		CreatedBy:      scope.ActorUserID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record the forward credential binding")
		return
	}
	if owner.Bytes != sourceClientID.Bytes {
		writeError(w, http.StatusConflict, "this target key already forwarded another source client; mint a new A2A key on the target Agent")
		return
	}
	if err := queries.UpsertAgentA2AOperatorForward(r.Context(), db.UpsertAgentA2AOperatorForwardParams{
		AgentID:               scope.Agent.ID,
		WorkspaceID:           scope.WorkspaceID,
		ForwardRpcUrl:         pgtype.Text{String: target, Valid: true},
		ForwardTokenEncrypted: sealed,
		ForwardSourceClientID: sourceClientID,
		ForwardUpdatedBy:      scope.ActorUserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save A2A forward")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save A2A forward")
		return
	}
	h.auditAgentA2AOperatorChange(r, scope, "forward_set",
		"forward_target", target,
		"forward_source_client_id", uuidToString(sourceClientID),
	)
	h.writeAgentA2AOperatorResponse(w, r, scope)
}

// resolveAgentA2AForwardSourceClient picks the single active A2A client whose
// calls are forwarded. The target sees every forwarded call as its own one
// client, so forwarding more than one source client would let them read and
// continue each other's tasks there.
func (h *Handler) resolveAgentA2AForwardSourceClient(
	r *http.Request,
	scope agentA2AManagementScope,
	requested string,
) (pgtype.UUID, int, error) {
	clients, err := h.Queries.ListAgentA2AClientsForOwner(r.Context(), db.ListAgentA2AClientsForOwnerParams{
		OwnerUserID: scope.OwnerUserID,
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if err != nil {
		return pgtype.UUID{}, http.StatusInternalServerError, errors.New("failed to load A2A clients")
	}
	active := make([]db.A2aClient, 0, len(clients))
	for _, client := range clients {
		if client.Status == "active" {
			active = append(active, client)
		}
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		if len(active) != 1 {
			return pgtype.UUID{}, http.StatusBadRequest, errors.New("source_client_id is required unless the Agent has exactly one active A2A client")
		}
		return active[0].ID, 0, nil
	}
	for _, client := range active {
		if uuidToString(client.ID) == requested {
			return client.ID, 0, nil
		}
	}
	return pgtype.UUID{}, http.StatusBadRequest, errors.New("source_client_id is not an active A2A client of this Agent")
}

func (h *Handler) DeleteAgentA2AOperatorForward(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.requireAgentA2AOperator(w, r)
	if !ok {
		return
	}
	if err := h.Queries.ClearAgentA2AOperatorForward(r.Context(), db.ClearAgentA2AOperatorForwardParams{
		WorkspaceID:      scope.WorkspaceID,
		AgentID:          scope.Agent.ID,
		ForwardUpdatedBy: scope.ActorUserID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear A2A forward")
		return
	}
	h.auditAgentA2AOperatorChange(r, scope, "forward_cleared")
	h.writeAgentA2AOperatorResponse(w, r, scope)
}

func (h *Handler) writeAgentA2AOperatorResponse(w http.ResponseWriter, r *http.Request, scope agentA2AManagementScope) {
	response, err := h.loadAgentA2AOperatorResponse(r, scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load A2A operator configuration")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) loadAgentA2AOperatorResponse(r *http.Request, scope agentA2AManagementScope) (AgentA2AOperatorResponse, error) {
	allowed := normalizedAgentA2AForwardOrigins(h.currentConfig().A2AForwardAllowedOrigins)
	response := AgentA2AOperatorResponse{Operator: true, ForwardAllowedOrigins: allowed}
	config, err := h.Queries.GetAgentA2AOperatorConfig(r.Context(), db.GetAgentA2AOperatorConfigParams{
		WorkspaceID: scope.WorkspaceID,
		AgentID:     scope.Agent.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return response, nil
	}
	if err != nil {
		return AgentA2AOperatorResponse{}, err
	}
	if config.DwsUid.Valid && config.DwsOrgID.Valid {
		identity := &AgentA2AOperatorIdentityResponse{
			UID:       config.DwsUid.String,
			OrgID:     config.DwsOrgID.String,
			UpdatedBy: uuidToString(config.IdentityUpdatedBy),
			UpdatedAt: timestampToString(config.IdentityUpdatedAt),
		}
		if config.DeapAgentUuid.Valid {
			value := config.DeapAgentUuid.String
			identity.DEAPAgentUUID = &value
		}
		response.DWSIdentity = identity
	}
	if config.ForwardRpcUrl.Valid && len(config.ForwardTokenEncrypted) > 0 {
		_, originErr := agentA2AForwardOriginAllowed(config.ForwardRpcUrl.String, allowed)
		response.Forward = &AgentA2AOperatorForwardResponse{
			RPCURL:         config.ForwardRpcUrl.String,
			SourceClientID: uuidToString(config.ForwardSourceClientID),
			Active:         originErr == nil && h.A2AService != nil && h.A2AService.PushSecrets != nil,
			UpdatedBy:      uuidToString(config.ForwardUpdatedBy),
			UpdatedAt:      timestampToString(config.ForwardUpdatedAt),
		}
	}
	return response, nil
}

func (h *Handler) auditAgentA2AOperatorChange(r *http.Request, scope agentA2AManagementScope, action string, attrs ...any) {
	fields := append(logger.RequestAttrs(r),
		"event", "a2a_operator_config_changed",
		"action", action,
		"workspace_id", uuidToString(scope.WorkspaceID),
		"agent_id", uuidToString(scope.Agent.ID),
		"actor_user_id", uuidToString(scope.ActorUserID),
	)
	slog.Info("A2A operator configuration changed", append(fields, attrs...)...)
}

// normalizeAgentA2AForwardTarget accepts only the canonical JSON-RPC URL of
// another hosted Agent on an allow-listed HTTPS origin. Forwarding to this
// deployment's own copy of the same Agent would loop, so it is rejected.
func normalizeAgentA2AForwardTarget(raw string, allowedOrigins []string, publicURL, ownPublicAgentID string) (string, error) {
	allowed := normalizedAgentA2AForwardOrigins(allowedOrigins)
	if len(allowed) == 0 {
		return "", errors.New("A2A forwarding is not enabled on this deployment")
	}
	parsed, err := agentA2AForwardOriginAllowed(strings.TrimSpace(raw), allowed)
	if err != nil {
		return "", err
	}
	match := agentA2AForwardRPCPath.FindStringSubmatch(parsed.Path)
	if match == nil {
		return "", errors.New("rpc_url must be an A2A JSON-RPC URL like https://host/api/a2a/agents/{id}/v1")
	}
	if own, ownErr := url.Parse(strings.TrimSpace(publicURL)); ownErr == nil &&
		strings.EqualFold(own.Scheme+"://"+own.Host, parsed.Scheme+"://"+parsed.Host) &&
		ownPublicAgentID != "" && match[1] == ownPublicAgentID {
		return "", errors.New("rpc_url must not point back to this Agent")
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.Path, nil
}

func agentA2AForwardOriginAllowed(raw string, allowed []string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("rpc_url must be an https URL without credentials, query or fragment")
	}
	origin := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
	for _, candidate := range allowed {
		if candidate == origin {
			return parsed, nil
		}
	}
	return nil, errors.New("rpc_url origin is not in MULTICA_A2A_FORWARD_ALLOWED_ORIGINS")
}

func normalizedAgentA2AForwardOrigins(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			continue
		}
		out = append(out, strings.ToLower(parsed.Scheme+"://"+parsed.Host))
	}
	return out
}
